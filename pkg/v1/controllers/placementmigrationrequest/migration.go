/*
Copyright 2026 The KubeFleet Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package placementmigrationrequest

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
)

func (r *Reconciler) migrateWorkloads(
	ctx context.Context,
	req *placementv1alpha1.PlacementMigrationRequest,
) (res migrationRunResult, err error) {
	if isMigrationCompleted(req) {
		return migrationRunResultSucceeded, nil
	}

	// Prepare the migration attempt bundles.
	migrationAttempts := req.Status.PlacementsToMigrate
	bundles := make([]migrationAttemptBundle, len(migrationAttempts))
	for idx := range migrationAttempts {
		migrationAttempt := &migrationAttempts[idx]
		bundles[idx] = migrationAttemptBundle{
			attempt: migrationAttempt,
		}
	}

	// Run the migration attempts in parallel.
	workerCnt := 1
	if req.Spec.MaxConcurrency != nil {
		workerCnt = int(*req.Spec.MaxConcurrency)
	}
	maxFailureCnt := req.Spec.FailurePolicy.MaxFailureCount
	failureCnt := &atomic.Int32{}
	failurePolicyTriggered := &atomic.Bool{}
	p := parallelizer.NewParallelizer(workerCnt)

	deadline := time.Now().Add(r.MaxWaitTimePerRun)
	childCtx, childCancel := context.WithDeadline(ctx, deadline)
	defer childCancel()

	innerClient := r.HubClient
	doWork := func(idx int) {
		bundle := &bundles[idx]
		bundle.assignedWorkerIdx = idx

		// Return the processing result if the bundle has been processed.
		shouldSkip := shouldSkipIfCompletedFor(bundle)
		if shouldSkip {
			klog.V(2).InfoS("A bundle has been processed", "workerIdx", idx, "result", bundle.res)
			return
		}

		migrateUntil(childCtx, innerClient, req, bundle, false)

		if bundle.res == migrationAttemptResultFailed {
			updatedFailureCnt := failureCnt.Add(1)
			if updatedFailureCnt >= int32(maxFailureCnt) {
				childCancel()
				failurePolicyTriggered.Store(true)

				klog.V(2).InfoS(
					"Too many failures have been observed while processing bundles; cancelling the whole process as the failure policy is triggered",
					"observedFailureCount", updatedFailureCnt,
					"maxFailureCount", maxFailureCnt,
					"workerIdx", idx,
					"placementMigrationAttempt", klog.KObj(req),
				)
				return
			}
		}

		klog.V(2).InfoS("Finished processing a bundle", "workerIdx", idx, "result", bundle.res, "placementMigrationAttempt", klog.KObj(req))
	}
	p.ParallelizeUntil(childCtx, len(bundles), doWork, "Migrating")

	return r.refreshStatusUponMigrationRunCompletion(ctx, req, bundles, failurePolicyTriggered.Load())
}

func isMigrationCompleted(req *placementv1alpha1.PlacementMigrationRequest) bool {
	completedCond := meta.FindStatusCondition(req.Status.Conditions, placementv1alpha1.PlacementMigrationRequestCondTypeCompleted)
	switch {
	case completedCond == nil:
		return false
	case completedCond.Status == metav1.ConditionTrue:
		return true
	case completedCond.Status == metav1.ConditionFalse && completedCond.Reason == placementv1alpha1.PlacementMigrationRequestCompletedCondReasonFailed:
		return true
	}
	return false
}

func shouldSkipIfCompletedFor(bundle *migrationAttemptBundle) bool {
	attempt := bundle.attempt
	completedCond := meta.FindStatusCondition(attempt.Conditions, placementv1alpha1.PlacementMigrationAttemptCondTypeCompleted)
	switch {
	case completedCond == nil:
		return false
	case completedCond.Status == metav1.ConditionTrue && completedCond.Reason == placementv1alpha1.PlacementMigrationAttemptCompletedCondReasonSucceeded:
		bundle.res = migrationAttemptResultSucceeded
		return true
	case completedCond.Status == metav1.ConditionFalse && completedCond.Reason == placementv1alpha1.PlacementMigrationAttemptCompletedCondReasonFailed:
		bundle.res = migrationAttemptResultFailed
		return true
	case completedCond.Status == metav1.ConditionTrue && completedCond.Reason == placementv1alpha1.PlacementMigrationAttemptCompletedCondReasonSkipped:
		bundle.res = migrationAttemptResultSkipped
		return true
	}
	return false
}

func migrateUntil(
	ctx context.Context,
	hubClient client.Client,
	req *placementv1alpha1.PlacementMigrationRequest,
	bundle *migrationAttemptBundle,
	isRollingBack bool,
) {
	for {
		select {
		case <-ctx.Done():
			// The context has been cancelled; return immediately.
			bundle.res = migrationAttemptResultInProgress
			return
		default:
		}

		// Do the migration. Re-enter the loop if the attempt is still in progress or a transient error
		// occurs.
		fromBinding := &placementv1alpha1.PlacementBinding{}
		fromBindingNamespacedName := client.ObjectKey{
			Name:      bundle.attempt.PlacementBindingRef.Name,
			Namespace: bundle.attempt.PlacementBindingRef.Namespace,
		}
		if err := hubClient.Get(ctx, fromBindingNamespacedName, fromBinding); err != nil {
			if apierrors.IsNotFound(err) {
				// Normally this should not happen; an inconsistent state might have been observed. Skip the attempt for now.
				bundle.res = migrationAttemptResultSkipped
				bundle.lastKnownErr = errors.Wraps(nil, "the placement is no longer present on the from cluster")
				return
			}
			// An error occurred when trying to retrieve the binding; retry later.
			wrappedErr := errors.NewAPIServerError(err, "", true, "placementBinding", fromBindingNamespacedName)
			klog.ErrorS(wrappedErr, "Failed to retrieve the placement binding on the from cluster", errors.Args(wrappedErr)...)
			continue

		}

		switch {
		case req.Spec.Mode == "SurgeFirst":
			// First surge, then drain.
			if err, isRetriable := surge(ctx, hubClient, req, fromBinding, bundle, isRollingBack); err != nil {
				if isRetriable {
					// A retriable error occurred during surge; retry the whole process later.
					klog.V(2).ErrorS(err, "Surge attempt failed with a retriable error; retry after a short wait", errors.Args(err)...)
					time.Sleep(3 * time.Second)
					continue
				}

				// A non-retriable error occurred during surge; mark the attempt as failed.
				bundle.res = migrationAttemptResultFailed
				bundle.lastKnownErr = errors.Wraps(err, "surge attempt failed with non-retriable error")
				klog.ErrorS(err, "Surge attempt failed with non-retriable error", errors.Args(err)...)
				return
			}

			if err, isRetriable := drain(ctx, hubClient, req, fromBinding, bundle, isRollingBack); err != nil {
				if isRetriable {
					// A retriable error occurred during drain; retry the whole process later.
					klog.V(2).ErrorS(err, "Drain attempt failed with a retriable error; retry after a short wait", errors.Args(err)...)
					time.Sleep(3 * time.Second)
					continue
				}

				// A non-retriable error occurred during drain; mark the attempt as failed.
				bundle.res = migrationAttemptResultFailed
				bundle.lastKnownErr = errors.Wraps(err, "drain attempt failed with non-retriable error")
				klog.ErrorS(err, "Drain attempt failed with non-retriable error", errors.Args(err)...)
				return
			}

			// The migration attempt has been completed successfully.
			bundle.res = migrationAttemptResultSucceeded
			return
		case req.Spec.Mode == "DrainFirst":
			// First drain, then surge.
			if err, isRetriable := drain(ctx, hubClient, req, fromBinding, bundle, isRollingBack); err != nil {
				if isRetriable {
					// A retriable error occurred during drain; retry the whole process later.
					klog.V(2).ErrorS(err, "Drain attempt failed with a retriable error; retry after a short wait", errors.Args(err)...)
					time.Sleep(3 * time.Second)
					continue
				}

				// A non-retriable error occurred during drain; mark the attempt as failed.
				bundle.res = migrationAttemptResultFailed
				bundle.lastKnownErr = errors.Wraps(err, "drain attempt failed with non-retriable error")
				klog.ErrorS(err, "Drain attempt failed with non-retriable error", errors.Args(err)...)
				return
			}

			if err, isRetriable := surge(ctx, hubClient, req, fromBinding, bundle, isRollingBack); err != nil {
				if isRetriable {
					// A retriable error occurred during surge; retry the whole process later.
					klog.V(2).ErrorS(err, "Surge attempt failed with a retriable error; retry after a short wait", errors.Args(err)...)
					time.Sleep(3 * time.Second)
					continue
				}

				// A non-retriable error occurred during surge; mark the attempt as failed.
				bundle.res = migrationAttemptResultFailed
				bundle.lastKnownErr = errors.Wraps(err, "surge attempt failed with non-retriable error")
				klog.ErrorS(err, "Surge attempt failed with non-retriable error", errors.Args(err)...)
				return
			}

			// The migration attempt has been completed successfully.
			bundle.res = migrationAttemptResultSucceeded
			return
		default:
			// Unsupported migration mode; mark the attempt as failed.
			bundle.res = migrationAttemptResultFailed
			wrappedErr := errors.NewUserError(nil, "an unsupported migration mode is provided", "mode", req.Spec.Mode)
			bundle.lastKnownErr = wrappedErr
			klog.ErrorS(wrappedErr, "An unsupported migration mode is provided", errors.Args(wrappedErr)...)
			return
		}
	}
}

func surge(
	ctx context.Context,
	hubClient client.Client,
	req *placementv1alpha1.PlacementMigrationRequest,
	fromBinding *placementv1alpha1.PlacementBinding,
	bundle *migrationAttemptBundle,
	isRollingBack bool,
) (err error, isRetriable bool) {
	attempt := bundle.attempt

	if isRollingBack {
		return unsuspendFromBindinAndWaitForItToBeAvailable(ctx, hubClient, fromBinding)
	}

	var toClusterName string
	switch {
	case attempt.ToClusterRequestName != nil:
		toClusterName, err, isRetriable = waitForClusterClaimToResolve(ctx, hubClient, req, fromBinding, *attempt.ToClusterRequestName)
		if err != nil {
			return err, isRetriable
		}
	case attempt.ToClusterName != nil:
		// The to cluster has been determined in the initialization stage.
		toClusterName = *attempt.ToClusterName
	default:
		// This should never happen; mark the attempt as failed.
		wrappedErr := errors.NewUnexpectedError(nil,
			"invalid migration attempt: the to cluster has not been determined and neither is a cluster claim provided")
		return wrappedErr, false
	}

	return createToBindingAndWaitForItToBeAvailable(ctx, hubClient, fromBinding, toClusterName)
}

func waitForClusterClaimToResolve(
	ctx context.Context,
	hubClient client.Client,
	migrationReq *placementv1alpha1.PlacementMigrationRequest,
	fromBinding *placementv1alpha1.PlacementBinding,
	clusterClaimName string,
) (clusterName string, err error, isRetriable bool) {
	// Retrieve the cluster claim. ClusterClaim is a cluster-scoped API object.
	clusterClaim := &placementv1alpha1.ClusterClaim{}
	if err := hubClient.Get(ctx, client.ObjectKey{Name: clusterClaimName}, clusterClaim); err != nil {
		if apierrors.IsNotFound(err) {
			clusterClaim = nil
		} else {
			return "", errors.NewAPIServerError(err, "failed to retrieve cluster claim", true, "clusterClaim", clusterClaimName), true
		}
	}

	if clusterClaim == nil {
		// The cluster claim has not been created yet; create it.
		clusterClaim = &placementv1alpha1.ClusterClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName,
			},
			Spec: placementv1alpha1.ClusterClaimSpec{
				PlacementPolicyRef: &placementv1alpha1.ObjectReference{
					Name:       fromBinding.Spec.PlacementPolicyName,
					Namespace:  fromBinding.Namespace,
					APIGroup:   placementv1alpha1.GroupVersion.Group,
					APIVersion: placementv1alpha1.GroupVersion.Version,
					Kind:       "PlacementPolicy",
				},
				ClusterSelectorTerms: migrationReq.Spec.ToClusterSelector.Terms,
			},
		}
		if err := hubClient.Create(ctx, clusterClaim); err != nil {
			return "", errors.NewAPIServerError(err, "failed to create cluster claim", false, "clusterClaim", clusterClaimName), true
		}
	}

	// Wait to see if the cluster claim has been fulfilled.
	clusterClaimCompletedCond := meta.FindStatusCondition(clusterClaim.Status.Conditions, placementv1alpha1.ClusterClaimCondTypeCompleted)
	if !condition.IsConditionStatusTrue(clusterClaimCompletedCond, clusterClaim.Generation) {
		// The cluster claim is still not fulfilled; retry later.
		return "", errors.NewTransientError(nil, "the cluster claim has not been completed yet", "clusterClaim", clusterClaimName), true
	}
	if clusterClaim.Status.ProvisionedClusterName == nil {
		return "", errors.NewTransientError(nil, "the cluster claim has not been completed yet", "clusterClaim", clusterClaimName), true
	}

	// TO-DO: check if the cluster is ready for running the worklods. For the demo,
	// assume that the presence of the Completed condition indicates that the cluster is ready.
	return *clusterClaim.Status.ProvisionedClusterName, nil, false
}

func createToBindingAndWaitForItToBeAvailable(
	ctx context.Context,
	hubClient client.Client,
	fromBinding *placementv1alpha1.PlacementBinding,
	toClusterName string,
) (err error, isRetriable bool) {
	// Retrieve the cluster binding.
	toBinding := &placementv1alpha1.PlacementBinding{}
	toBindingName := fmt.Sprintf(toClusterBindingNameFmt, fromBinding.Spec.PlacementPolicyName, toClusterName)
	toBindingNamespace := fromBinding.Namespace
	if err := hubClient.Get(ctx, client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}, toBinding); err != nil {
		if apierrors.IsNotFound(err) {
			// The to cluster binding has not been created yet.
			toBinding = nil
		} else {
			return errors.NewAPIServerError(err, "failed to retrieve the placement binding on the to cluster", true, "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
		}
	}

	if toBinding == nil {
		// Create the to cluster binding.
		toBinding = &placementv1alpha1.PlacementBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toBindingName,
				Namespace: toBindingNamespace,
				Labels: map[string]string{
					placementBindingOwnedByLabelKey:      fromBinding.Spec.PlacementPolicyName,
					placementBindingMigratedFromLabelKey: fromBinding.Name,
				},
				Annotations: map[string]string{
					clusterSelectorHashAnnotationKey: fromBinding.Annotations[clusterSelectorHashAnnotationKey],
				},
			},
			Spec: placementv1alpha1.PlacementBindingSpec{
				PlacementPolicyName:  fromBinding.Spec.PlacementPolicyName,
				ClusterSelectors:     fromBinding.Spec.ClusterSelectors,
				ClusterName:          toClusterName,
				ResourceSnapshotName: fromBinding.Spec.ResourceSnapshotName,
			},
		}
		if err := hubClient.Create(ctx, toBinding); err != nil {
			return errors.NewAPIServerError(err, "failed to create the placement binding on the to cluster", false, "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
		}
	}

	availableCond := meta.FindStatusCondition(toBinding.Status.Conditions, placementv1alpha1.PlacementBindingCondTypeAvailable)
	if !condition.IsConditionStatusTrue(availableCond, toBinding.Generation) {
		// The to cluster binding is still not available; retry later.
		return errors.NewTransientError(nil, "the placement binding on the to cluster is not available yet", "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
	}
	return nil, false
}

func unsuspendFromBindinAndWaitForItToBeAvailable(
	ctx context.Context,
	hubClient client.Client,
	fromBinding *placementv1alpha1.PlacementBinding,
) (err error, isRetriable bool) {
	if fromBinding.Spec.Suspended {
		fromBinding.Spec.Suspended = false
		if err := hubClient.Update(ctx, fromBinding); err != nil {
			return errors.NewAPIServerError(err, "failed to unsuspend the placement binding on the from cluster", false, "placementBinding", client.ObjectKey{Name: fromBinding.Name, Namespace: fromBinding.Namespace}), true
		}
	}

	availableCond := meta.FindStatusCondition(fromBinding.Status.Conditions, placementv1alpha1.PlacementBindingCondTypeAvailable)
	if !condition.IsConditionStatusTrue(availableCond, fromBinding.Generation) {
		// The from cluster binding is still not available; retry later.
		return errors.NewTransientError(nil, "the placement binding on the from cluster is not available yet", "placementBinding", client.ObjectKey{Name: fromBinding.Name, Namespace: fromBinding.Namespace}), true
	}
	return nil, false
}

func drain(
	ctx context.Context,
	hubClient client.Client,
	req *placementv1alpha1.PlacementMigrationRequest,
	fromBinding *placementv1alpha1.PlacementBinding,
	bundle *migrationAttemptBundle,
	isRollingBack bool,
) (error, bool) {
	attempt := bundle.attempt

	if isRollingBack {
		var toClusterName string
		var err error
		var isRetriable bool
		switch {
		case attempt.ToClusterRequestName != nil:
			toClusterName, err, isRetriable = checkIfClusterClaimHasBeenResolved(ctx, hubClient, *attempt.ToClusterRequestName)
			if err != nil {
				return err, isRetriable
			}
		case attempt.ToClusterName != nil:
			// The to cluster has been determined in the initialization stage.
			toClusterName = *attempt.ToClusterName
		default:
			// This should never happen; mark the attempt as failed.
			wrappedErr := errors.NewUnexpectedError(nil,
				"invalid migration attempt: the to cluster has not been determined and neither is a cluster claim provided")
			return wrappedErr, false
		}
		return waitForToBindingToBeSuspended(ctx, hubClient, fromBinding, toClusterName)
	}
	return waitForFromBindingToBeSuspended(ctx, hubClient, fromBinding)
}

func checkIfClusterClaimHasBeenResolved(
	ctx context.Context,
	hubClient client.Client,
	clusterClaimName string,
) (string, error, bool) {
	clusterClaim := &placementv1alpha1.ClusterClaim{}
	if err := hubClient.Get(ctx, client.ObjectKey{Name: clusterClaimName}, clusterClaim); err != nil {
		if apierrors.IsNotFound(err) {
			clusterClaim = nil
		} else {
			return "", errors.NewAPIServerError(err, "failed to retrieve cluster claim", true, "clusterClaim", clusterClaimName), true
		}
	}

	if clusterClaim == nil || clusterClaim.Status.ProvisionedClusterName == nil {
		// The cluster claim has not been fulfilled yet; delete it.
		//
		// It is up to the cluster claim fulfiller to decide how to handle the cancellation of the cluster claim.
		//
		// Here the system will attempt to delete a cluster claim even if the cache returns a nil, as the cache
		// might be stale.
		if err := hubClient.Delete(ctx, &placementv1alpha1.ClusterClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName,
			},
		}); err != nil && !apierrors.IsNotFound(err) {
			return "", errors.NewAPIServerError(err, "failed to delete the unresolved cluster claim", true, "clusterClaim", clusterClaimName), true
		}
		return "", errors.NewTransientError(nil, "the cluster claim has not been fulfilled yet", "clusterClaim", clusterClaimName), true
	}

	return *clusterClaim.Status.ProvisionedClusterName, nil, false
}

func waitForToBindingToBeSuspended(
	ctx context.Context,
	hubClient client.Client,
	fromBinding *placementv1alpha1.PlacementBinding,
	toClusterName string,
) (err error, isRetriable bool) {
	// Retrieve the cluster binding.
	toBinding := &placementv1alpha1.PlacementBinding{}
	toBindingName := fmt.Sprintf(toClusterBindingNameFmt, fromBinding.Spec.PlacementPolicyName, toClusterName)
	toBindingNamespace := fromBinding.Namespace
	if err := hubClient.Get(ctx, client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}, toBinding); err != nil {
		if apierrors.IsNotFound(err) {
			// The to cluster binding has not been created yet. No further action is needed.
			return nil, false
		}
		return errors.NewAPIServerError(err, "failed to retrieve the placement binding on the to cluster", true, "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
	}

	if !toBinding.Spec.Suspended {
		toBinding.Spec.Suspended = true
		if err := hubClient.Update(ctx, toBinding); err != nil {
			return errors.NewAPIServerError(err, "failed to suspend the placement binding on the to cluster", false, "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
		}
	}

	if len(toBinding.Finalizers) > 0 {
		// The to cluster binding has been suspended but the cleanup finalizer is still present.
		// Wait for the cleanup to finalize.
		return errors.NewTransientError(nil, "the placement binding on the to cluster is still being cleaned up", "placementBinding", client.ObjectKey{Name: toBindingName, Namespace: toBindingNamespace}), true
	}
	return nil, false
}

func waitForFromBindingToBeSuspended(
	ctx context.Context,
	hubClient client.Client,
	fromBinding *placementv1alpha1.PlacementBinding,
) (err error, isRetriable bool) {
	if fromBinding.Spec.Suspended {
		// The from cluster binding has been suspended. Wait for it to be cleaned up.
		if len(fromBinding.Finalizers) > 0 {
			return errors.NewTransientError(nil, "the placement binding on the from cluster is still being cleaned up", "placementBinding", client.ObjectKey{Name: fromBinding.Name, Namespace: fromBinding.Namespace}), true
		}
		return nil, false
	}

	fromBinding.Spec.Suspended = true
	if err := hubClient.Update(ctx, fromBinding); err != nil {
		return errors.NewAPIServerError(err, "failed to suspend the placement binding on the from cluster", false, "placementBinding", client.ObjectKey{Name: fromBinding.Name, Namespace: fromBinding.Namespace}), true
	}
	return errors.NewTransientError(nil, "the placement binding on the from cluster is being suspended", "placementBinding", client.ObjectKey{Name: fromBinding.Name, Namespace: fromBinding.Namespace}), true
}
