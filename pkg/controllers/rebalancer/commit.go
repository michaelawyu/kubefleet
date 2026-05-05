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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
)

func (r *Reconciler) commitChanges(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(rebalancingReq, clusterRebalancingRequestCleanUpFinalizer) {
		klog.V(2).InfoS("No finalizer is found; the changes have been committed already or there is no need to commit the changes", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, nil
	}

	// Nothing to commit if the rebalancing request has not been initialized yet.
	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, string(placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized))
	if !condition.IsConditionStatusTrue(initCond, rebalancingReq.Generation) {
		klog.V(2).InfoS("The rebalancing request has not been initialized yet; no need to commit the changes", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		if err := r.relinquishRolloutManagerRoleAndDropFinalizer(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to relinquish rollout manager role and remove finalizer", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// The rebalancing process might have started.

	// Commit each migration attempt as it is.
	migrations := rebalancingReq.Status.Migrations
	for idx := range migrations {
		migration := &migrations[idx]

		rolledBackCond := meta.FindStatusCondition(migration.Conditions, string(placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack))
		completedCond := meta.FindStatusCondition(migration.Conditions, string(placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted))

		switch {
		case condition.IsConditionStatusTrue(rolledBackCond, rebalancingReq.Generation):
			// The migration attempt has been rolled back. Commit the rollback by
			// a) applying the current set of changes to the from binding;
			// b) deleting the to binding.
			if err := r.applyChangesTo(ctx, migration.FromClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to apply changes",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.FromClusterBindingReference)
				return ctrl.Result{}, err
			}
			if err := r.deleteBinding(ctx, migration.ToClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to delete binding",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.ToClusterBindingReference)
				return ctrl.Result{}, err
			}
		case !rebalancingReq.Spec.Rollback && condition.IsConditionStatusTrue(completedCond, rebalancingReq.Generation):
			// The migration attempt has been completed successfully. Commit the migration by
			// a) applying the current set of changes to the to binding;
			// b) deleting the from binding.
			if err := r.applyChangesTo(ctx, migration.ToClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to apply changes",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.ToClusterBindingReference)
				return ctrl.Result{}, err
			}
			if err := r.deleteBinding(ctx, migration.FromClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to delete binding",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.FromClusterBindingReference)
				return ctrl.Result{}, err
			}
		default:
			// The migration attempt (or its rollback) is still in progress or has not started yet.
			// Leave the bindings as they are.
			if err := r.applyChangesTo(ctx, migration.FromClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to apply changes to from binding",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.FromClusterBindingReference)
				return ctrl.Result{}, err
			}
			if err := r.applyChangesTo(ctx, migration.ToClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to apply changes to to binding",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.ToClusterBindingReference)
				return ctrl.Result{}, err
			}
		}
	}

	// Relinquish rollout manager role and drop finalizer.
	if err := r.relinquishRolloutManagerRoleAndDropFinalizer(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to relinquish rollout manager role and remove finalizer", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, err
	}

	klog.V(2).InfoS("Committed the changes for the rebalancing request", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
	return ctrl.Result{}, nil
}

func (r *Reconciler) relinquishRolloutManagerRoleAndDropFinalizer(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) error {
	migrations := rebalancingReq.Status.Migrations
	for idx := range migrations {
		migration := &migrations[idx]
		placementRef := &migration.PlacementReference

		// Retrieve the placement.
		var placementObj placementv1beta1.PlacementObj
		if placementRef.Namespace == "" {
			// The placement is cluster-scoped (CRP).
			placementObj = &placementv1beta1.ClusterResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: placementRef.Name}, placementObj); err != nil {
				return err
			}
		} else {
			// The placement is namespace-scoped (RP).
			placementObj = &placementv1beta1.ResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Namespace: placementRef.Namespace, Name: placementRef.Name}, placementObj); err != nil {
				return err
			}
		}

		rolloutManagers := placementObj.GetPlacementStatus().RolloutManagedBy
		updatedRolloutManagers := make([]placementv1beta1.RolloutManagerReference, 0, len(rolloutManagers))
		for _, manager := range rolloutManagers {
			if manager.UID != rebalancingReq.UID {
				updatedRolloutManagers = append(updatedRolloutManagers, manager)
			}
		}
		if len(updatedRolloutManagers) != len(rolloutManagers) {
			placementObj.GetPlacementStatus().RolloutManagedBy = updatedRolloutManagers
			if err := r.Client.Status().Update(ctx, placementObj); err != nil {
				return err
			}
			klog.V(2).InfoS(
				"Relinquished rollout manager role for the placement",
				"placement", klog.KObj(placementObj),
				"clusterRebalancingRequest", klog.KObj(rebalancingReq))
		}
	}

	if err := r.dropFinalizerFrom(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to drop finalizer from ClusterRebalancingRequest", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return err
	}
	return nil
}

func (r *Reconciler) dropFinalizerFrom(ctx context.Context, rebalancingReq *placementv1beta1.ClusterRebalancingRequest) error {
	if controllerutil.ContainsFinalizer(rebalancingReq, clusterRebalancingRequestCleanUpFinalizer) {
		controllerutil.RemoveFinalizer(rebalancingReq, clusterRebalancingRequestCleanUpFinalizer)

		if err := r.Client.Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to remove cleanup finalizer from ClusterRebalancingRequest", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return err
		}
		klog.V(2).InfoS("Removed cleanup finalizer from ClusterRebalancingRequest", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
	}
	return nil
}

func (r *Reconciler) applyChangesTo(ctx context.Context, bindingRef *corev1.ObjectReference) error {
	// Retrieve the binding.
	if bindingRef == nil {
		// No binding to apply changes to. In the commit stage this is not considered as an error.
		return nil
	}

	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The to binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The to binding cannot be found; fail the attempt now.
				klog.V(2).InfoS(
					"The to binding cannot be found; the reference might be incorrect, or the binding has been deleted unexpectedly",
					"binding", klog.KObj(binding),
					"toCluster", bindingRef.Name)
				return nil
			}
			return fmt.Errorf("failed to retrieve the to binding: %w", err)
		}
	} else {
		// The to binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The to binding cannot be found; fail the attempt now.
				klog.V(2).InfoS(
					"The to binding cannot be found; the reference might be incorrect, or the binding has been deleted unexpectedly",
					"binding", klog.KObj(binding),
					"toCluster", fmt.Sprintf("%s/%s", bindingRef.Namespace, bindingRef.Name))
				return nil
			}
			return fmt.Errorf("failed to retrieve the to binding: %w", err)
		}
	}

	_, isWithdrawn := binding.GetAnnotations()[placementv1beta1.WithdrawnBindingAnnotationKey]
	if isWithdrawn {
		// The binding has been withdrawn. Delete it.
		if err := r.deleteBinding(ctx, bindingRef); err != nil {
			return fmt.Errorf("failed to delete the withdrawn binding: %w", err)
		}
		return nil
	}

	_, isProvisional := binding.GetAnnotations()[placementv1beta1.ProvisionalBindingAnnotationKey]
	rolloutStartedCond := meta.FindStatusCondition(binding.GetBindingStatus().Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
	switch {
	case isProvisional && !condition.IsConditionStatusTrue(rolloutStartedCond, binding.GetGeneration()):
		// The binding is provisional and no resources are placed yet. Delete it.
		if err := r.deleteBinding(ctx, bindingRef); err != nil {
			return fmt.Errorf("failed to delete the provisional binding that has not started rollout: %w", err)
		}
		return nil
	case isProvisional && len(binding.GetBindingSpec().ResourceSnapshotName) == 0:
		// Catch a corner case: the binding is provisional and has no associated resource snapshot.
		// The migration is a no-op anyway, delete the provisional binding.
		if err := r.deleteBinding(ctx, bindingRef); err != nil {
			return fmt.Errorf("failed to delete the provisional binding with no resource snapshot: %w", err)
		}
		return nil
	case isProvisional:
		// The binding is provisional but resources have been placed.
		// Drop the provisional status and leave the binding be.
		delete(binding.GetAnnotations(), placementv1beta1.ProvisionalBindingAnnotationKey)
		delete(binding.GetAnnotations(), placementv1beta1.ReservedBindingAnnotationKey)
		if err := r.Client.Update(ctx, binding); err != nil {
			return fmt.Errorf("failed to update the provisional binding: %w", err)
		}
		return nil
	default:
		// The binding is not provisional and no changes have been made.
		delete(binding.GetAnnotations(), placementv1beta1.ReservedBindingAnnotationKey)
		if err := r.Client.Update(ctx, binding); err != nil {
			return fmt.Errorf("failed to update the binding: %w", err)
		}
	}
	return nil
}

func (r *Reconciler) deleteBinding(ctx context.Context, bindingRef *corev1.ObjectReference) error {
	if bindingRef == nil {
		// No binding to delete. In the commit stage this is not considered as an error.
		return nil
	}

	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: bindingRef.Name,
			},
		}
	} else {
		// The binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: bindingRef.Namespace,
				Name:      bindingRef.Name,
			},
		}
	}

	// Delete the binding if it exists.
	if err := r.Client.Delete(ctx, binding); err != nil {
		if apierrors.IsNotFound(err) {
			// The binding has been deleted already; no further processing is needed.
			return nil
		}
		return fmt.Errorf("failed to delete the binding: %w", err)
	}
	return nil
}
