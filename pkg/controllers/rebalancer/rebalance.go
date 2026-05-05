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

package rebalancer

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
)

type workerResult string

const (
	WorkerResultCompleted workerResult = "Completed"
	WorkerResultSkipped   workerResult = "Skipped"
	WorkerResultFailed    workerResult = "Failed"
)

type rebalancingProcessingBundle struct {
	migration         *placementv1beta1.ClusterRebalancingRequestPerMigrationStatus
	assignedWorkerIdx int

	res          workerResult
	lastKnownErr error
}

func surge(
	ctx context.Context,
	client client.Client,
	bindingRef *corev1.ObjectReference,
) (err error, isRetriable bool) {
	// Retrieve the to binding.
	if bindingRef == nil {
		// Normally this should never happen.
		return fmt.Errorf("the reference of the binding to surge is nil"), false
	}

	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The to binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The to binding cannot be found; fail the attempt now.
				klog.V(2).InfoS(
					"The to binding cannot be found; the reference might be incorrect, or the binding has been deleted unexpectedly",
					"binding", klog.KObj(binding),
					"toCluster", bindingRef.Name)
				return nil, false
			}
			return fmt.Errorf("failed to retrieve the to binding: %w", err), false
		}
	} else {
		// The to binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The to binding cannot be found; fail the attempt now.
				klog.V(2).InfoS(
					"The to binding cannot be found; the reference might be incorrect, or the binding has been deleted unexpectedly",
					"binding", klog.KObj(binding),
					"toCluster", fmt.Sprintf("%s/%s", bindingRef.Namespace, bindingRef.Name))
				return nil, false
			}
			return fmt.Errorf("failed to retrieve the to binding: %w", err), false
		}
	}

	if len(binding.GetBindingSpec().ResourceSnapshotName) == 0 {
		// The to binding has no resource snapshot. No action needed as it cannot be surged; consider
		// the attempt as successful.
		//
		// This can happen when:
		// a) the to binding exists before the rebalancing request is created/initialized, but it is of the scheduled state;
		//    KubeFleet should wait for a rollout attempt to assign the binding a resource snapshot, which is separate from
		//    the rebalancing process.
		// b) the to binding does not exist when the rebalancing request is created/initialized, but the from binding
		//    has no resource snapshot associated (it is of the scheduled state), as a result there is no resource to migrate
		//    at all.
		klog.V(2).InfoS("The to binding has no resource snapshot associated; no surge needed",
			"binding", klog.KObj(binding),
			"toCluster", bindingRef.Name)
		return nil, true
	}

	// Drop the withdrawn binding annotation (if applicable); add RolloutStarted condition so that resources
	// will be synchronized to the member cluster.
	annotations := binding.GetAnnotations()
	if _, found := annotations[placementv1beta1.WithdrawnBindingAnnotationKey]; found {
		delete(annotations, placementv1beta1.WithdrawnBindingAnnotationKey)
		binding.SetAnnotations(annotations)
		if err := client.Update(ctx, binding); err != nil {
			return fmt.Errorf("failed to update the to binding: %w", err), true
		}
	}

	rolloutStartedCond := meta.FindStatusCondition(binding.GetBindingStatus().Conditions, string(placementv1beta1.ClusterResourcePlacementRolloutStartedConditionType))
	if !condition.IsConditionStatusTrue(rolloutStartedCond, binding.GetGeneration()) {
		meta.SetStatusCondition(&binding.GetBindingStatus().Conditions, metav1.Condition{
			Type:               string(placementv1beta1.ResourceBindingRolloutStarted),
			Status:             metav1.ConditionTrue,
			Reason:             condition.RolloutStartedReason,
			Message:            "Synchronization of resources to the cluster starts as part of the rebalancing process",
			ObservedGeneration: binding.GetGeneration(),
		})
		if err := client.Status().Update(ctx, binding); err != nil {
			return fmt.Errorf("failed to update the to binding status: %w", err), true
		}
	}

	// Wait until the to binding becomes available.
	availableCond := meta.FindStatusCondition(binding.GetBindingStatus().Conditions, string(placementv1beta1.ResourceBindingAvailable))
	if !condition.IsConditionStatusTrue(availableCond, binding.GetGeneration()) {
		// The to binding is not available yet; consider this attempt as still in progress.
		return fmt.Errorf("the binding is not yet available"), true
	}

	return nil, true
}

func drain(
	ctx context.Context,
	client client.Client,
	bindingRef *corev1.ObjectReference,
) (err error, isRetriable bool) {
	// Retrieve the from binding.
	if bindingRef == nil {
		// Normally this should never happen.
		return fmt.Errorf("the reference of the binding to drain is nil"), false
	}

	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The from binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The from binding is already gone. No further action needed; consider the
				// attempt as successful.
				klog.V(2).InfoS("The from binding cannot be found; no further action needed",
					"binding", klog.KObj(binding),
					"fromCluster", bindingRef.Name)
				return nil, false
			}
			return fmt.Errorf("failed to retrieve the from binding: %w", err), true
		}
	} else {
		// The from binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The from binding is already gone. No further action needed; consider the
				// attempt as successful.
				klog.V(2).InfoS("The from binding cannot be found; no further action needed",
					"binding", klog.KObj(binding),
					"fromCluster", bindingRef.Name)
				return nil, false
			}
			return fmt.Errorf("failed to retrieve the from binding: %w", err), true
		}
	}

	if len(binding.GetBindingSpec().ResourceSnapshotName) == 0 {
		// The from binding has no resource snapshot. No action needed as there is no resource
		// to move; consider the attempt as successful.
		klog.V(2).InfoS("The from binding has no resource snapshot associated; no drain needed",
			"binding", klog.KObj(binding),
			"fromCluster", bindingRef.Name)
		return nil, true
	}

	// Add the withdrawn binding annotation.
	annotations := binding.GetAnnotations()
	if _, found := annotations[placementv1beta1.WithdrawnBindingAnnotationKey]; !found {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[placementv1beta1.WithdrawnBindingAnnotationKey] = "true"
		binding.SetAnnotations(annotations)
		if err := client.Update(ctx, binding); err != nil {
			return fmt.Errorf("failed to update the from binding: %w", err), true
		}
		return nil, true
	}

	// Wait until the from binding has the resources withdrawn (i.e., the work generator finalizer
	// has been removed).
	if controllerutil.ContainsFinalizer(binding, placementv1beta1.WorkFinalizer) {
		// The withdrawal is still being processed by the work generator; consider this attempt as still in progress.
		return fmt.Errorf("the from binding is still withdrawing resources"), true
	}

	return nil, true
}

// TO-DO: persist the timing information.
func migrate(
	ctx context.Context,
	client client.Client,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
	bundle *rebalancingProcessingBundle,
	isRollingBack bool,
	resCh chan<- *rebalancingProcessingBundle,
) {
	// Skip the bundle if there is no binding references populated. As mentioned in the starting phase, this
	// can happen when the rebalancer observes an inconsistent state.
	if bundle.migration.FromClusterBindingReference == nil || bundle.migration.ToClusterBindingReference == nil {
		bundle.res = WorkerResultSkipped
		wrappedErr := fmt.Errorf("the migration has incomplete binding references; this might be a sign of cache inconsistency; skip this migration attempt for now")
		klog.ErrorS(wrappedErr,
			"Skipping the migration attempt as it has incomplete binding references",
			"workerIdx", bundle.assignedWorkerIdx,
			"rebalancingRequest", klog.KObj(rebalancingReq))
		bundle.lastKnownErr = wrappedErr
		resCh <- bundle
		return
	}

	fromBindingRef := bundle.migration.FromClusterBindingReference
	toBindingRef := bundle.migration.ToClusterBindingReference
	if isRollingBack {
		// Swap the from and to references for roll back.
		fromBindingRef, toBindingRef = toBindingRef, fromBindingRef
	}

	shelfLife := time.Second * time.Duration(*rebalancingReq.Spec.FailurePolicy.MaximumWaitDurationPerMigrationAttemptSeconds)
	childCtx, childCancel := context.WithDeadline(ctx, time.Now().Add(shelfLife))
	defer childCancel()
	for {
		select {
		case <-childCtx.Done():
			// The work has been running for too long; consider it as failed.
			bundle.res = WorkerResultFailed
			wrappedErr := fmt.Errorf("the work has been running for too long (> %d seconds)", *rebalancingReq.Spec.FailurePolicy.MaximumWaitDurationPerMigrationAttemptSeconds)
			klog.ErrorS(wrappedErr,
				"The work has been running for too long; consider it as failed",
				"workerIdx", bundle.assignedWorkerIdx,
				"rebalancingRequest", klog.KObj(rebalancingReq))
			bundle.lastKnownErr = wrappedErr
			resCh <- bundle
			return
		default:
			// Perform the actual migration.
			switch {
			case rebalancingReq.Spec.Mode == "SurgeFirst":
				// First surge, then drain.
				if err, isRetriable := surge(ctx, client, toBindingRef); err != nil {
					if isRetriable {
						// The error is retriable; retry after a short wait.
						klog.V(2).ErrorS(err,
							"Surge attempt failed with retriable error; retrying after a short wait",
							"workerIdx", bundle.assignedWorkerIdx,
							"toBindingRef", toBindingRef)
						time.Sleep(time.Second * 10)
						continue
					}
					// The error is not retriable; consider the attempt as failed.
					klog.ErrorS(err,
						"Surge attempt failed with non-retriable error; consider the attempt as failed",
						"workerIdx", bundle.assignedWorkerIdx,
						"toBindingRef", toBindingRef)
					bundle.res = WorkerResultFailed
					bundle.lastKnownErr = fmt.Errorf("surge attempted failed with non-retriable error: %s", err)
					resCh <- bundle
					return
				}

				if err, isRetriable := drain(ctx, client, fromBindingRef); err != nil {
					if isRetriable {
						// The error is retriable; retry after a short wait.
						klog.ErrorS(err,
							"Drain attempt failed with retriable error; retrying after a short wait",
							"workerIdx", bundle.assignedWorkerIdx,
							"fromBindingRef", fromBindingRef)
						time.Sleep(time.Second * 10)
						continue
					}
					// The error is not retriable; consider the attempt as failed.
					klog.ErrorS(err,
						"Drain attempt failed with non-retriable error; consider the attempt as failed",
						"workerIdx", bundle.assignedWorkerIdx,
						"fromBindingRef", fromBindingRef)
					bundle.res = WorkerResultFailed
					bundle.lastKnownErr = fmt.Errorf("drain attempted failed with non-retriable error: %s", err)
					resCh <- bundle
					return
				}

				// The surge and drain attempts have both succeeded. Mark the migration attempt as completed.
				bundle.res = WorkerResultCompleted
				resCh <- bundle
				return
			case rebalancingReq.Spec.Mode == "DrainFirst":
				// First drain, then surge.
				if err, isRetriable := drain(ctx, client, fromBindingRef); err != nil {
					if isRetriable {
						// The error is retriable; retry after a short wait.
						klog.ErrorS(err,
							"Drain attempt failed with retriable error; retrying after a short wait",
							"workerIdx", bundle.assignedWorkerIdx,
							"fromBindingRef", fromBindingRef)
						time.Sleep(time.Second * 10)
						continue
					}
					// The error is not retriable; consider the attempt as failed.
					klog.ErrorS(err,
						"Drain attempt failed with non-retriable error; consider the attempt as failed",
						"workerIdx", bundle.assignedWorkerIdx,
						"fromBindingRef", fromBindingRef)
					bundle.res = WorkerResultFailed
					bundle.lastKnownErr = fmt.Errorf("drain attempted failed with non-retriable error: %s", err)
					resCh <- bundle
					return
				}

				if err, isRetriable := surge(ctx, client, toBindingRef); err != nil {
					if isRetriable {
						// The error is retriable; retry after a short wait.
						klog.ErrorS(err,
							"Surge attempt failed with retriable error; retrying after a short wait",
							"workerIdx", bundle.assignedWorkerIdx,
							"toBindingRef", toBindingRef)
						time.Sleep(time.Second * 10)
						continue
					}
					// The error is not retriable; consider the attempt as failed.
					klog.ErrorS(err,
						"Surge attempt failed with non-retriable error; consider the attempt as failed",
						"workerIdx", bundle.assignedWorkerIdx,
						"toBindingRef", toBindingRef)
					bundle.res = WorkerResultFailed
					bundle.lastKnownErr = fmt.Errorf("surge attempted failed with non-retriable error: %s", err)
					resCh <- bundle
					return
				}

				// The drain and surge attempts have both succeeded. Mark the migration attempt as completed.
				bundle.res = WorkerResultCompleted
				resCh <- bundle
				return
			default:
				// An unsupported mode has been specified; consider this as a failure.
				//
				// Normally this should never happen.
				bundle.res = WorkerResultFailed
				wrappedErr := fmt.Errorf("unsupported rebalancing mode: %s", rebalancingReq.Spec.Mode)
				klog.ErrorS(wrappedErr,
					"An unsupported rebalancing mode has been specified in the request; this might be a sign of data inconsistency",
					"workerIdx", bundle.assignedWorkerIdx,
					"rebalancingMode", rebalancingReq.Spec.Mode,
					"rebalancingRequest", klog.KObj(rebalancingReq))
				bundle.lastKnownErr = wrappedErr
				resCh <- bundle
				return
			}
		}
	}
}

func (r *Reconciler) rebalancePlacements(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (res RebalacingAttemptResult, err error) {
	// Check if the rebalancing request has been completed.
	completedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, string(placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted))
	switch {
	case condition.IsConditionStatusTrue(completedCond, rebalancingReq.Generation):
		return RebalancingAttemptResultCompleted, nil
	case condition.IsConditionStatusFalse(completedCond, rebalancingReq.Generation) && completedCond.Reason == placementv1beta1.ClusterRebalancingReqCompletedCondReasonFailed:
		return RebalancingAttemptResultFailed, nil
	}

	// Prepare the migrations to be performed.
	migrations := rebalancingReq.Status.Migrations
	bundles := make([]rebalancingProcessingBundle, len(migrations))
	for idx := range migrations {
		bundles[idx] = rebalancingProcessingBundle{
			migration: &migrations[idx],
		}
	}

	// Set up the work coordinator.
	workerCnt := int(*rebalancingReq.Spec.MaxConcurrency)
	tokens := make(chan int, workerCnt)
	// Pre-populate tokens so that workers can start immediately.
	for i := 0; i < workerCnt; i++ {
		tokens <- i
	}
	resCh := make(chan *rebalancingProcessingBundle, workerCnt)

	bundleIdx := 0
	childCtx, childCancel := context.WithCancel(ctx)
	failureCnt := 0
	failurePolicyTriggered := false
	completedWorkerCnt := 0
	defer childCancel()

	doWork := func(
		ctx context.Context,
		client client.Client,
		rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
		bundle *rebalancingProcessingBundle,
		resCh chan<- *rebalancingProcessingBundle,
	) {
		// Return the last known result if the bundle has already been processed.
		completedCond := meta.FindStatusCondition(bundle.migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted)
		if condition.IsConditionStatusTrue(completedCond, rebalancingReq.Generation) {
			if completedCond.Reason == placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSkipped {
				bundle.res = WorkerResultSkipped
			} else {
				bundle.res = WorkerResultCompleted
			}
			resCh <- bundle
			return
		}
		if condition.IsConditionStatusFalse(completedCond, rebalancingReq.Generation) {
			bundle.res = WorkerResultFailed
			resCh <- bundle
			return
		}

		migrate(ctx, client, rebalancingReq, bundle, false, resCh)
	}

WorkCoordinatorLoop:
	for {
		select {
		case <-childCtx.Done():
			// The main context or the child context has been cancelled; exit the loop immediately.
			break WorkCoordinatorLoop
		case token := <-tokens:
			// A worker is available; assign a bundle to it for processing.
			if bundleIdx >= len(bundles) {
				// A worker has become available, but there is no more bundle to process. Mark the
				// worker as completed.
				completedWorkerCnt++
			}
			if completedWorkerCnt >= workerCnt {
				// All workers have completed processing; exit the loop.
				break WorkCoordinatorLoop
			}
			if failureCnt >= int(*rebalancingReq.Spec.FailurePolicy.OnFailureCount) {
				// Too many failures have been observed; cancel the child context to stop all ongoing work, and exit the loop.
				childCancel()
				failurePolicyTriggered = true
				break WorkCoordinatorLoop
			}

			// Assign a worker to process the bundle.
			bundle := &bundles[bundleIdx]
			bundle.assignedWorkerIdx = token
			bundleIdx++

			go doWork(childCtx, r.Client, rebalancingReq, bundle, resCh)
		case bundle := <-resCh:
			// A worker has completed processing a bundle; inspect the result and decide whether to continue or not.
			switch bundle.res {
			case WorkerResultCompleted:
				// Return the token so that the worker can start processing another bundle.
				tokens <- bundle.assignedWorkerIdx

				klog.V(2).InfoS(
					"A bundle has been processed successfully",
					"workerIdx", bundle.assignedWorkerIdx,
					"rebalancingRequest", klog.KObj(rebalancingReq))
			case WorkerResultFailed:
				// A failure has been reported when processing the bundle.
				failureCnt++

				klog.V(2).InfoS(
					"A failure has been observed when processing a bundle; the failure is tolerable, but if too many failures are observed the whole process will be cancelled",
					"observedFailureCount", failureCnt,
					"failureThreshold", *rebalancingReq.Spec.FailurePolicy.OnFailureCount,
					"error", bundle.lastKnownErr)
				// Return the token so that the worker can start processing another bundle.
				tokens <- bundle.assignedWorkerIdx
			case WorkerResultSkipped:
				// The bundle has been skipped. This is considered as a successful attempt.
				// Return the token so that the worker can start processing another bundle.
				tokens <- bundle.assignedWorkerIdx

				klog.V(2).InfoS(
					"A bundle has been skipped",
					"workerIdx", bundle.assignedWorkerIdx,
					"rebalancingRequest", klog.KObj(rebalancingReq),
					"error", bundle.lastKnownErr)
			default:
				// An unexpected result has been observed; this should never happen. Consider this as a failure.
				failureCnt++

				klog.V(2).ErrorS(fmt.Errorf("an unexpected worker result has been observed: %s", bundle.res),
					"observedFailureCount", failureCnt,
					"failureThreshold", *rebalancingReq.Spec.FailurePolicy.OnFailureCount,
					"unexpectedWorkerResult", bundle.res)
				// Return the token so that the worker can start processing another bundle.
				tokens <- bundle.assignedWorkerIdx
			}
		}
	}

	// Based on the processing results, refresh the rebalancing request status.
	completionCnt := 0
	for idx := range bundles {
		bundle := &bundles[idx]
		completedCond := meta.FindStatusCondition(bundle.migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted)

		switch bundle.res {
		case WorkerResultCompleted:
			completionCnt++
			if completedCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					Message:            "Successfully migrated resources between clusters",
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		case WorkerResultFailed:
			if completedCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionFalse,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonExpired,
					Message:            fmt.Sprintf("Failed to migrate resources between clusters: %s", bundle.lastKnownErr),
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		case WorkerResultSkipped:
			// The bundle has been skipped. This is considered as a successful attempt.
			completionCnt++
			if completedCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSkipped,
					Message:            fmt.Sprintf("The migration attempt has been skipped: %s", bundle.lastKnownErr),
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		default:
			// The bundle might not have been processed yet. No condition to populate.
		}
	}
	rebalancingReq.Status.Migrations = migrations

	switch {
	case failurePolicyTriggered:
		// The failure policy has been triggered; handle the request per the policy action.
		completedCond = &metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
			Status:             metav1.ConditionFalse,
			Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonFailed,
			Message:            fmt.Sprintf("The rebalancing process has been cancelled as the number of failed migration attempts has reached the failure threshold (%d)", *rebalancingReq.Spec.FailurePolicy.OnFailureCount),
			ObservedGeneration: rebalancingReq.Generation,
		}
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *completedCond)

		res = RebalancingAttemptResultFailed
	case completionCnt == len(bundles):
		// All bundles have been processed successfully; mark the whole request as completed.
		completedCond = &metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonSucceeded,
			Message:            "Successfully completed the rebalancing process for all placements",
			ObservedGeneration: rebalancingReq.Generation,
		}
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *completedCond)

		res = RebalancingAttemptResultCompleted
	default:
		// Not all bundles have been processed successfully, but the failure policy has not been triggered;
		// leave the request as is, and rely on the periodic reconciliation to refresh the status later.
		//
		// Normally this should not occur.
		completedCond = &metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
			Status:             metav1.ConditionFalse,
			Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonInProgress,
			Message:            "The rebalancing process is still in progress; some migrations have not been completed yet",
			ObservedGeneration: rebalancingReq.Generation,
		}
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *completedCond)

		res = RebalancingAttemptResultInProgress
	}

	// Write back the status update to the API server.
	if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
		return RebalancingAttemptResultFailed, fmt.Errorf("failed to update the rebalancing request status: %w", err)
	}
	return res, nil
}
