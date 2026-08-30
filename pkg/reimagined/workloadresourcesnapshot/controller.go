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

package workloadresourcesnapshot

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

type workloadResourceSnapshotReqReconciler struct {
	client                     client.Client
	workloadResSnapshotManager *Manager

	maxSnapshotCreationWaitTime time.Duration
}

func NewWorkloadResourceSnapshotReqReconciler(
	client client.Client,
	workloadResSnapshotManager *Manager,
	maxSnapshotCreationWaitTimeSeconds int,
) *workloadResourceSnapshotReqReconciler {
	return &workloadResourceSnapshotReqReconciler{
		client:                      client,
		workloadResSnapshotManager:  workloadResSnapshotManager,
		maxSnapshotCreationWaitTime: time.Duration(maxSnapshotCreationWaitTimeSeconds) * time.Second,
	}
}

func (r *workloadResourceSnapshotReqReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation started",
		"workloadResourceSnapshotRequest", req.NamespacedName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ended",
			"workloadResourceSnapshotRequest", req.NamespacedName,
			"latencyMilliseconds", latency)
	}()

	workloadResSnapshotReq := &experimentalv1beta1.WorkloadResourceSnapshotRequest{}
	err := r.client.Get(ctx, req.NamespacedName, workloadResSnapshotReq)
	switch {
	case apierrors.IsNotFound(err):
		// The request was not found. It might have been deleted after the reconcile request was queued.
		// In this case, the controller simply ignores the error and ends the reconciliation.
		klog.V(2).InfoS("The workload resource snapshot request object is not found; no further processing is needed",
			"workloadResourceSnapshotRequest", req.NamespacedName)
		return ctrl.Result{}, nil
	case err != nil:
		// An error occurred while trying to retrieve the request. We should requeue the request to try again.
		wrappedErr := errors.NewAPIServerError(err, "", true, "workloadResourceSnapshotRequest", req.NamespacedName)
		klog.ErrorS(wrappedErr, "Failed to retrieve the workload resource snapshot request object", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	default:
		completedCond := meta.FindStatusCondition(workloadResSnapshotReq.Status.Conditions, experimentalv1beta1.WorkloadResourceSnapshotRequestCondTypeCompleted)
		if completedCond != nil && completedCond.Status == metav1.ConditionTrue {
			// The request has already been completed. No further processing is needed.
			klog.V(2).InfoS("The workload resource snapshot request has already been completed; no further processing is needed",
				"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq))
			return ctrl.Result{}, nil
		}
	}

	workloadPlacement := &experimentalv1beta1.WorkloadPlacement{}
	workloadPlacementKey := client.ObjectKey{
		Name:      workloadResSnapshotReq.Spec.WorkloadPlacementRef.Name,
		Namespace: workloadResSnapshotReq.Namespace,
	}
	err = r.client.Get(ctx, workloadPlacementKey, workloadPlacement)
	switch {
	case apierrors.IsNotFound(err):
		// The workload placement is not found. Abandon the request and update its status condition to indicate the failure.
		klog.V(2).InfoS("No workload placement is found; abandon the new workload resource snapshot request",
			"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq),
			"workloadPlacement", workloadPlacementKey)

		meta.SetStatusCondition(&workloadResSnapshotReq.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadResourceSnapshotRequestCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workloadResSnapshotReq.GetGeneration(),
			Reason:             experimentalv1beta1.WorkloadResourceSnapshotRequestCompletedReasonWorkloadPlacementNotFound,
			Message:            fmt.Sprintf("No workload placement is found with name %q in namespace %q", workloadPlacementKey.Name, workloadPlacementKey.Namespace),
		})
		if err := r.client.Status().Update(ctx, workloadResSnapshotReq); err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to update the status of the workload resource snapshot request",
				false,
				"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq),
				"workloadPlacement", workloadPlacementKey)
			klog.ErrorS(wrappedErr, "Failed to update status for workload resource snapshot request after determining the referenced workload placement does not exist", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	case err != nil:
		wrappedErr := errors.NewAPIServerError(err,
			"failed to retrieve the workload placement object referenced by the workload resource snapshot request", true,
			"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq), "workloadPlacement", workloadPlacementKey)
		klog.ErrorS(wrappedErr, "Failed to retrieve the workload placement object referenced by the workload resource snapshot request",
			errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Ask the workload resource snapshot manager to add a new snapshot for the workload placement.
	newSnapshot, err := r.workloadResSnapshotManager.RequestAndWaitForNewSnapshot(ctx, workloadPlacement, r.maxSnapshotCreationWaitTime)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to request the workload resource snapshot manager to add a new snapshot for the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement),
			"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq))
		klog.ErrorS(wrappedErr, "Failed to request the workload resource snapshot manager to add a new snapshot for the workload placement", errors.Args(wrappedErr)...)

		// Abandon the request right away. To retry, create a new request object.
		meta.SetStatusCondition(&workloadResSnapshotReq.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadResourceSnapshotRequestCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workloadResSnapshotReq.GetGeneration(),
			Reason:             experimentalv1beta1.WorkloadResourceSnapshotRequestCompletedReasonErred,
			Message:            fmt.Sprintf("Failed to request the workload resource snapshot manager to add a new snapshot for the workload placement: %v", wrappedErr),
		})
		if err := r.client.Status().Update(ctx, workloadResSnapshotReq); err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to update the status of the workload resource snapshot request",
				false,
				"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq),
				"workloadPlacement", klog.KObj(workloadPlacement))
			klog.ErrorS(wrappedErr, "Failed to update status for workload resource snapshot request after failing to request the workload resource snapshot manager to add a new snapshot for the workload placement", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}

	// Successfully added the new snapshot for the workload. Update the request status to indicate the completion.
	meta.SetStatusCondition(&workloadResSnapshotReq.Status.Conditions, metav1.Condition{
		Type:               experimentalv1beta1.WorkloadResourceSnapshotRequestCondTypeCompleted,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: workloadResSnapshotReq.GetGeneration(),
		Reason:             experimentalv1beta1.WorkloadResourceSnapshotRequestCompletedReasonSuccess,
		Message:            "Successfully added new snapshot for the workload placement",
	})
	workloadResSnapshotReq.Status.WorkloadResourceSnapshotName = newSnapshot.Name

	if err := r.client.Status().Update(ctx, workloadResSnapshotReq); err != nil {
		wrappedErr := errors.NewAPIServerError(err,
			"failed to update status of WorkloadResourceSnapshotRequest object", false,
			"workloadResourceSnapshotRequest", klog.KObj(workloadResSnapshotReq))
		klog.ErrorS(wrappedErr, "Failed to update status for workload resource snapshot request", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	return ctrl.Result{}, nil
}

func (r *workloadResourceSnapshotReqReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&experimentalv1beta1.WorkloadResourceSnapshotRequest{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
