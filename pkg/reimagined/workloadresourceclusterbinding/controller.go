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

package workloadresourceclusterbinding

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "WorkloadPlacementClusterBindingController"

	workloadResourceClusterBindingCleanupFinalizer = "experimental.kubefleet.dev/workload-resource-cluster-binding-cleanup"

	workNameFmt                = "%s-0"
	memberClusterReservedNSFmt = "fleet-member-%s"
)

type Reconciler struct {
	HubClient client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the WorkloadPlacementClusterBinding object.
	clusterBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
	err := r.HubClient.Get(ctx, req.NamespacedName, clusterBinding)
	switch {
	case apierrors.IsNotFound(err):
		// The binding cannot be found; it may have been deleted already. No need for
		// further reconciliation.
		klog.V(2).InfoS("The binding object cannot be found", "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		// An error occurred when trying to retrieve the binding object; retry later.
		wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceClusterBinding", req.NamespacedName, "controllerName", controllerName)
		klog.ErrorS(wrappedErr, "Failed to get workload resource cluster binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	if !clusterBinding.DeletionTimestamp.IsZero() || clusterBinding.Spec.Suspended {
		// Perform some cleanup.
		if clusterBinding.Spec.MemberClusterName != nil {
			workNSName := fmt.Sprintf(memberClusterReservedNSFmt, *clusterBinding.Spec.MemberClusterName)

			labelSelector := client.MatchingLabels{
				experimentalv1beta1.WorkOwnedByClusterBindingLabelKey: clusterBinding.Name,
				experimentalv1beta1.WorkOwnerNamespaceLabelKey:        clusterBinding.Namespace,
			}
			workList := &placementv1beta1.WorkList{}
			if err := r.HubClient.List(ctx, workList, labelSelector, client.InNamespace(workNSName)); err != nil {
				wrappedErr := errors.NewAPIServerError(err, "", true, "work", "namespace", workNSName, "controllerName", controllerName)
				klog.ErrorS(wrappedErr, "Failed to list work objects associated with the binding for cleanup", errors.Args(wrappedErr)...)
				return ctrl.Result{}, wrappedErr
			}

			for idx := range workList.Items {
				work := &workList.Items[idx]

				if err := r.HubClient.Delete(ctx, work); err != nil && !apierrors.IsNotFound(err) {
					wrappedErr := errors.NewAPIServerError(err, "", true, "work", klog.KObj(work), "controllerName", controllerName)
					klog.ErrorS(wrappedErr, "Failed to delete work object associated with the binding for cleanup", errors.Args(wrappedErr)...)
					return ctrl.Result{}, wrappedErr
				}
				klog.V(2).InfoS("Deleted work object associated with the binding for cleanup", "work", klog.KObj(work), "controller", controllerName)
			}
		}

		// The binding has been marked for deletion; drop its cleanup finalizer.
		if controllerutil.ContainsFinalizer(clusterBinding, workloadResourceClusterBindingCleanupFinalizer) {
			controllerutil.RemoveFinalizer(clusterBinding, workloadResourceClusterBindingCleanupFinalizer)
			if err := r.HubClient.Update(ctx, clusterBinding); err != nil {
				wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceClusterBinding", req.NamespacedName, "controllerName", controllerName)
				klog.ErrorS(wrappedErr, "Failed to remove finalizer from workload resource cluster binding", errors.Args(wrappedErr)...)
				return ctrl.Result{}, wrappedErr
			}
		}
		return ctrl.Result{}, nil
	}

	// Add the cleanup finalizer if not already added.
	if !controllerutil.ContainsFinalizer(clusterBinding, workloadResourceClusterBindingCleanupFinalizer) {
		controllerutil.AddFinalizer(clusterBinding, workloadResourceClusterBindingCleanupFinalizer)
		if err := r.HubClient.Update(ctx, clusterBinding); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceClusterBinding", req.NamespacedName, "controllerName", controllerName)
			klog.ErrorS(wrappedErr, "Failed to add finalizer to workload resource cluster binding", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	}

	// Check if a member cluster has been selected.
	if clusterBinding.Spec.MemberClusterName == nil || len(*clusterBinding.Spec.MemberClusterName) == 0 {
		klog.V(2).InfoS("No member cluster has been selected yet; should wait for the scheduling to conclude",
			"workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	}

	// Check if a resource snapshot has been associated.
	if clusterBinding.Spec.ResourceSnapshotRevisionName == nil || len(*clusterBinding.Spec.ResourceSnapshotRevisionName) == 0 {
		klog.V(2).InfoS("No resource snapshot has been associated with the binding yet; a rollout might be needed",
			"workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	}

	// Retrieve the corresponding Work object.
	work := &placementv1beta1.Work{}
	workName := fmt.Sprintf(workNameFmt, clusterBinding.Name)
	workNSName := fmt.Sprintf(memberClusterReservedNSFmt, *clusterBinding.Spec.MemberClusterName)
	workFound := true
	if err := r.HubClient.Get(ctx, client.ObjectKey{Namespace: workNSName, Name: workName}, work); err != nil {
		if apierrors.IsNotFound(err) {
			workFound = false
		} else {
			wrappedErr := errors.NewAPIServerError(err, "", true, "work", client.ObjectKey{Namespace: workNSName, Name: workName}, "controllerName", controllerName)
			klog.ErrorS(wrappedErr, "Failed to get the corresponding Work object for the binding", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	}

	if workFound {
		curDerivedFromSnapshotRevision := work.Annotations[experimentalv1beta1.WorkDerivedFromSnapshotRevisionAnnotationKey]
		if curDerivedFromSnapshotRevision == *clusterBinding.Spec.ResourceSnapshotRevisionName {
			// The work object is found and up-to-date; no update is needed.
			klog.V(2).InfoS("The corresponding Work object for the binding is up-to-date; no update is needed", "work", klog.KObj(work), "controller", controllerName)

			// Refresh the status of the binding.
			workAppliedCond := meta.FindStatusCondition(work.Status.Conditions, placementv1beta1.WorkConditionTypeApplied)
			isWorkApplied := false
			failedToApplyResourceCnt := 0
			if condition.IsConditionStatusTrue(workAppliedCond, work.Generation) {
				isWorkApplied = true
			}
			for idx := range work.Status.ManifestConditions {
				manifestCond := &work.Status.ManifestConditions[idx]
				manifestAppliedCond := meta.FindStatusCondition(manifestCond.Conditions, placementv1beta1.WorkConditionTypeApplied)
				if !condition.IsConditionStatusTrue(manifestAppliedCond, work.Generation) {
					failedToApplyResourceCnt++
				}
			}

			updatedBinding := clusterBinding.DeepCopy()
			if isWorkApplied {
				meta.SetStatusCondition(&updatedBinding.Status.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesApplied",
					Message:            "All resources in the snapshot have been applied on the member cluster",
					ObservedGeneration: updatedBinding.Generation,
				})
			} else {
				meta.SetStatusCondition(&updatedBinding.Status.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionFalse,
					Reason:             "NotAllResourcesApplied",
					Message:            fmt.Sprintf("%d of %d resources in the snapshot have been applied on the member cluster", len(work.Status.ManifestConditions)-failedToApplyResourceCnt, len(work.Status.ManifestConditions)),
					ObservedGeneration: updatedBinding.Generation,
				})
			}

			workAvailableCond := meta.FindStatusCondition(work.Status.Conditions, placementv1beta1.WorkConditionTypeAvailable)
			isWorkAvailable := false
			if condition.IsConditionStatusTrue(workAvailableCond, work.Generation) {
				isWorkAvailable = true
			}
			failedToBeAvailableResourceCnt := 0
			for idx := range work.Status.ManifestConditions {
				manifestCond := &work.Status.ManifestConditions[idx]
				manifestAvailableCond := meta.FindStatusCondition(manifestCond.Conditions, placementv1beta1.WorkConditionTypeAvailable)
				if !condition.IsConditionStatusTrue(manifestAvailableCond, work.Generation) {
					failedToBeAvailableResourceCnt++
				}
			}

			if isWorkAvailable {
				meta.SetStatusCondition(&updatedBinding.Status.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesAvailable",
					Message:            "All resources in the snapshot are available on the member cluster",
					ObservedGeneration: updatedBinding.Generation,
				})
			} else {
				meta.SetStatusCondition(&updatedBinding.Status.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionFalse,
					Reason:             "NotAllResourcesAvailable",
					Message:            fmt.Sprintf("%d of %d resources in the snapshot are available on the member cluster", len(work.Status.ManifestConditions)-failedToBeAvailableResourceCnt, len(work.Status.ManifestConditions)),
					ObservedGeneration: updatedBinding.Generation,
				})
			}

			// Write the status.
			if !equality.Semantic.DeepEqual(clusterBinding.Status, updatedBinding.Status) {
				if err := r.HubClient.Status().Update(ctx, updatedBinding); err != nil {
					wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceClusterBinding", req.NamespacedName, "controllerName", controllerName)
					klog.ErrorS(wrappedErr, "Failed to update the status of the binding", errors.Args(wrappedErr)...)
					return ctrl.Result{}, wrappedErr
				}
				klog.V(2).InfoS("Updated the status of the binding", "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
			} else {
				klog.V(2).InfoS("The status of the binding is up-to-date; no update is needed", "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
			}

			return ctrl.Result{}, nil
		}
	}

	// Create or update the corresponding Work object for the binding.

	// Retrieve the associated resource snapshot.
	resourceSnapshot := &experimentalv1beta1.WorkloadResourceSnapshot{}
	if err := r.HubClient.Get(ctx, client.ObjectKey{Namespace: clusterBinding.Namespace, Name: *clusterBinding.Spec.ResourceSnapshotRevisionName}, resourceSnapshot); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceSnapshot", client.ObjectKey{Namespace: clusterBinding.Namespace, Name: *clusterBinding.Spec.ResourceSnapshotRevisionName}, "controllerName", controllerName)
		klog.ErrorS(wrappedErr, "Failed to get the associated resource snapshot for the binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	manifests := []placementv1beta1.Manifest{}
	// Add the workload manifest.
	manifests = append(manifests, placementv1beta1.Manifest{
		RawExtension: resourceSnapshot.Spec.Workload.Manifest,
	})
	// Add the additional resource manifests (if any).
	for _, additionalRes := range resourceSnapshot.Spec.AdditionalResources {
		manifests = append(manifests, placementv1beta1.Manifest{
			RawExtension: additionalRes.Manifest,
		})
	}

	workToCreateOrUpdate := &placementv1beta1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workName,
			Namespace: workNSName,
		},
	}
	resOp, err := controllerutil.CreateOrUpdate(ctx, r.HubClient, workToCreateOrUpdate, func() error {
		if workToCreateOrUpdate.Labels == nil {
			workToCreateOrUpdate.Labels = make(map[string]string)
		}
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnedByClusterBindingLabelKey] = clusterBinding.Name
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnerNamespaceLabelKey] = clusterBinding.Namespace
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnedByPlacementLabelKey] = clusterBinding.Spec.WorkloadPlacementName

		if workToCreateOrUpdate.Annotations == nil {
			workToCreateOrUpdate.Annotations = make(map[string]string)
		}
		workToCreateOrUpdate.Annotations[experimentalv1beta1.WorkDerivedFromSnapshotRevisionAnnotationKey] = *clusterBinding.Spec.ResourceSnapshotRevisionName

		workToCreateOrUpdate.Spec = placementv1beta1.WorkSpec{
			Workload: placementv1beta1.WorkloadTemplate{
				Manifests: manifests,
			},
		}
		return nil
	})
	if err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", true, "work", klog.KObj(workToCreateOrUpdate), "controllerName", controllerName, "op", resOp)
		klog.ErrorS(wrappedErr, "Failed to create or update the corresponding Work object for the binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	klog.V(2).InfoS("Created or updated the corresponding Work object for the binding",
		"work", klog.KObj(workToCreateOrUpdate), "controller", controllerName, "op", resOp)
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&experimentalv1beta1.WorkloadResourceClusterBinding{}).
		Watches(&placementv1beta1.Work{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			work, ok := obj.(*placementv1beta1.Work)
			if !ok {
				// The object is not a Work object; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The object to enqueue is not a Work object; ignoring it", "object", klog.KObj(obj), "controller", controllerName)
				klog.ErrorS(err, "Failed to enqueue an object for processing", errors.Args(err)...)
				return nil
			}

			// Enqueue the corresponding WorkloadResourceClusterBinding object for reconciliation.
			clusterBindingName, ok := work.Labels[experimentalv1beta1.WorkOwnedByClusterBindingLabelKey]
			if !ok {
				// The Work object is not associated with any WorkloadResourceClusterBinding; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The Work object is not associated with any WorkloadResourceClusterBinding as the label is missing; ignoring it", "work", klog.KObj(work), "controller", controllerName)
				klog.V(4).ErrorS(err, "Failed to enqueue a Work object for processing", errors.Args(err)...)
				return nil
			}
			clusterBindingNSName, ok := work.Labels[experimentalv1beta1.WorkOwnerNamespaceLabelKey]
			if !ok {
				// The Work object is not associated with any WorkloadResourceClusterBinding; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The Work object is not associated with any WorkloadResourceClusterBinding as the namespace label is missing; ignoring it", "work", klog.KObj(work), "controller", controllerName)
				klog.V(4).ErrorS(err, "Failed to enqueue a Work object for processing", errors.Args(err)...)
				return nil
			}
			req := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      clusterBindingName,
					Namespace: clusterBindingNSName,
				},
			}
			klog.V(2).InfoS("Found an updated work object; enqueue its owner binding for further processing", "work", klog.KObj(work), "workloadResourceClusterBinding", req.NamespacedName, "controller", controllerName)
			return []ctrl.Request{req}
		})).
		Complete(r)
}
