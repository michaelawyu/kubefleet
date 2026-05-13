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
	"github.com/kubefleet-dev/kubefleet/pkg/utils/rolloutmanagerclaim"
)

func (r *Reconciler) commitChanges(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (ctrl.Result, error) {
	// Important: deletion itself will bump the rebalancing request generations. As a result,
	// this part of the code no longer verifies generation when checking for conditions.
	//
	// See also: `markAsDeleting` calls in the `apiserver` pkg.

	if !controllerutil.ContainsFinalizer(rebalancingReq, clusterRebalancingRequestCleanUpFinalizer) {
		klog.V(2).InfoS("No finalizer is found; the changes have been committed already or there is no need to commit the changes", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, nil
	}

	// Nothing to commit if the rebalancing request has not been initialized yet.
	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, string(placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized))
	// Ignore generation comparison in this check; as the initialized condition might get stale right after
	// a spec change.
	if initCond == nil || initCond.Status != metav1.ConditionTrue {
		klog.V(2).InfoS("The rebalancing request has not been initialized yet; no need to commit the changes", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		if err := r.relinquishRolloutManagerRoleAndDropFinalizer(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to relinquish rollout manager role and remove finalizer", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Commit each migration attempt as it is.
	migrations := rebalancingReq.Status.Migrations
	for idx := range migrations {
		migration := &migrations[idx]

		rolledBackCond := meta.FindStatusCondition(migration.Conditions, string(placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack))
		completedCond := meta.FindStatusCondition(migration.Conditions, string(placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted))
		isRolledBack := rolledBackCond != nil && rolledBackCond.Status == metav1.ConditionTrue
		isCompleted := completedCond != nil && completedCond.Status == metav1.ConditionTrue

		switch {
		case migration.FromClusterBindingReference == nil || migration.ToClusterBindingReference == nil:
			// The migration attempt has not been fully set up; no changes have been made. No need to commit.
			klog.V(2).InfoS(
				"The migration attempt has not been fully set up; no changes have been made. No need to commit.",
				"migrationIndex", idx,
				"fromCluster", migration.FromClusterBindingReference,
				"toCluster", migration.ToClusterBindingReference,
				"rebalancingRequest", klog.KObj(rebalancingReq))
			continue
		case isRolledBack:
			// The migration attempt has been rolled back. Commit the rollback by
			// deleting the to binding if it is provisional. No action is needed on the
			// from binding as it has been surged back in the rollback process.
			if err := r.deleteBinding(ctx, migration.ToClusterBindingReference, true); err != nil {
				klog.ErrorS(err,
					"Failed to delete binding if it is provisional",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.ToClusterBindingReference)
				return ctrl.Result{}, err
			}
		case !rebalancingReq.Spec.Rollback && isCompleted:
			// The migration attempt has been completed successfully. Commit the migration by
			// bringing the provisional binding (if any) to an active (non-provisional) state,
			// and deleting the from binding.
			if err := r.activateBindingIfProvisional(ctx, migration.ToClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to activate binding if it is provisional",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.ToClusterBindingReference)
				return ctrl.Result{}, err
			}
			if err := r.deleteBinding(ctx, migration.FromClusterBindingReference, false); err != nil {
				klog.ErrorS(err,
					"Failed to delete the from binding",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.FromClusterBindingReference)
				return ctrl.Result{}, err
			}
		default:
			// The migration attempt (or its rollback) is still in progress or has not started yet.
			// KubeFleet will commit the current state as it is, i.e.,
			// a) if the from binding has been withdrawn, delete it;
			// b) if the to binding is provisional and has resources applied, activate it;
			// c) if the to binding is provisional but does not have resources applied, delete it.
			if err := r.deleteBindingIfWithdrawn(ctx, migration.FromClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to delete the from binding if it is withdrawn",
					"clusterRebalancingRequest", klog.KObj(rebalancingReq),
					"bindingRef", migration.FromClusterBindingReference)
				return ctrl.Result{}, err
			}
			if err := r.activateOrDeleteProvisionalBinding(ctx, migration.ToClusterBindingReference); err != nil {
				klog.ErrorS(err,
					"Failed to activate or delete the provisional binding",
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

		isRefreshNeeded := rolloutmanagerclaim.RelinquishRolloutManagerRole(placementObj, &placementv1beta1.RolloutManagerReference{
			ObjectReference: corev1.ObjectReference{
				Kind:      "ClusterRebalancingRequest",
				Name:      rebalancingReq.GetName(),
				Namespace: rebalancingReq.GetNamespace(),
				UID:       rebalancingReq.GetUID(),
			},
			Mode: placementv1beta1.RolloutManagerModeExclusive,
		})
		if isRefreshNeeded {
			if err := r.Client.Status().Update(ctx, placementObj); err != nil {
				klog.ErrorS(err, "Failed to update the placement after relinquishing the rollout manager role", "placement", klog.KObj(placementObj))
				return err
			}
		}
		klog.V(2).InfoS(
			"Relinquished rollout manager role for the placement",
			"placement", klog.KObj(placementObj),
			"clusterRebalancingRequest", klog.KObj(rebalancingReq))
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

func (r *Reconciler) activateBindingIfProvisional(ctx context.Context, bindingRef *corev1.ObjectReference) error {
	// Retrieve the binding.
	if bindingRef == nil {
		// Just a sanity check; normally this should not occur. And nil reference is not considered
		// as an error in the commit stage.
		return nil
	}

	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The to binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			klog.ErrorS(err, "Failed to retrieve the binding to activate", "bindingRef", bindingRef)
			return fmt.Errorf("failed to retrieve the to binding: %w", err)
		}
	} else {
		// The to binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			klog.ErrorS(err, "Failed to retrieve the binding to activate", "bindingRef", bindingRef)
			return fmt.Errorf("failed to retrieve the to binding: %w", err)
		}
	}

	// Drop the provisional binding annotation.
	annotations := binding.GetAnnotations()
	lenBfr := len(annotations)
	_, isProvisional := annotations[placementv1beta1.ProvisionalBindingAnnotationKey]
	if !isProvisional {
		// The binding is not provisional; no need to activate.
		return nil
	}

	delete(annotations, placementv1beta1.ProvisionalBindingAnnotationKey)
	delete(annotations, placementv1beta1.ReservedBindingAnnotationKey)
	if len(annotations) != lenBfr {
		binding.SetAnnotations(annotations)
		if err := r.Client.Update(ctx, binding); err != nil {
			klog.ErrorS(err, "Failed to update the binding to activate", "bindingRef", bindingRef)
			return fmt.Errorf("failed to update the to binding: %w", err)
		}

		klog.V(2).InfoS("Activated the provisional binding by removing the provisional annotation", "bindingRef", bindingRef)
	}
	return nil
}

func (r *Reconciler) deleteBinding(
	ctx context.Context,
	bindingRef *corev1.ObjectReference,
	skipIfNotProvisional bool,
) error {
	if bindingRef == nil {
		// Just a sanity check; normally this should not occur. And nil reference is not considered
		// as an error in the commit stage.
		return nil
	}

	// Retrieve the binding.
	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	} else {
		// The binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	}

	if binding == nil {
		// A sanity check to satisfy the static code analyzer.
		return nil
	}

	_, isProvisional := binding.GetAnnotations()[placementv1beta1.ProvisionalBindingAnnotationKey]
	if skipIfNotProvisional && !isProvisional {
		// The binding is not provisional; skip the deletion.
		return nil
	}

	// Delete the binding.
	if err := r.Client.Delete(ctx, binding); err != nil {
		if apierrors.IsNotFound(err) {
			// The binding has been deleted already; no further processing is needed.
			return nil
		}
		return fmt.Errorf("failed to delete the binding: %w", err)
	}
	return nil
}

func (r *Reconciler) deleteBindingIfWithdrawn(ctx context.Context, bindingRef *corev1.ObjectReference) error {
	if bindingRef == nil {
		// Just a sanity check; normally this should not occur. And nil reference is not considered
		// as an error in the commit stage.
		return nil
	}

	// Retrieve the binding.
	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	} else {
		// The binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	}

	_, isWithdrawn := binding.GetAnnotations()[placementv1beta1.WithdrawnBindingAnnotationKey]
	if isWithdrawn {
		// The binding has been withdrawn; delete it.
		if err := r.Client.Delete(ctx, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to delete the withdrawn binding: %w", err)
		}
		klog.V(2).InfoS("Deleted the from binding as it has been withdrawn", "bindingRef", bindingRef)
	}
	// The binding has not been withdrawn; no need to delete it.
	return nil
}

func (r *Reconciler) activateOrDeleteProvisionalBinding(ctx context.Context, bindingRef *corev1.ObjectReference) error {
	if bindingRef == nil {
		// Just a sanity check; normally this should not occur. And nil reference is not considered
		// as an error in the commit stage.
		return nil
	}

	// Retrieve the binding.
	var binding placementv1beta1.BindingObj
	if len(bindingRef.Namespace) == 0 {
		// The binding is cluster-scoped (ClusterResourceBinding).
		binding = &placementv1beta1.ClusterResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	} else {
		// The binding is namespace-scoped (ResourceBinding).
		binding = &placementv1beta1.ResourceBinding{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bindingRef.Namespace, Name: bindingRef.Name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to retrieve the binding: %w", err)
		}
	}

	_, isProvisional := binding.GetAnnotations()[placementv1beta1.ProvisionalBindingAnnotationKey]
	if !isProvisional {
		// The binding is not provisional; no need to activate or delete.
		klog.V(2).InfoS("The binding is not provisional; no need to activate or delete", "bindingRef", bindingRef)
		return nil
	}

	// The binding is provisional. Check if it has resources applied.
	rolloutStartedCond := meta.FindStatusCondition(binding.GetBindingStatus().Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
	if condition.IsConditionStatusTrue(rolloutStartedCond, binding.GetGeneration()) {
		// The provisional binding has resources applied; activate it by removing the provisional annotation.
		annotations := binding.GetAnnotations()
		delete(annotations, placementv1beta1.ProvisionalBindingAnnotationKey)
		delete(annotations, placementv1beta1.ReservedBindingAnnotationKey)
		binding.SetAnnotations(annotations)
		if err := r.Client.Update(ctx, binding); err != nil {
			klog.ErrorS(err, "Failed to update the binding to activate", "bindingRef", bindingRef)
			return fmt.Errorf("failed to update the provisional binding: %w", err)
		}
		klog.V(2).InfoS("Activated the provisional binding by removing the provisional annotation", "bindingRef", bindingRef)
	} else {
		// The provisional binding does not have resources applied; delete it.
		if err := r.Client.Delete(ctx, binding); err != nil {
			if apierrors.IsNotFound(err) {
				// The binding has been deleted already; no further processing is needed.
				return nil
			}
			return fmt.Errorf("failed to delete the provisional binding: %w", err)
		}
		klog.V(2).InfoS("Deleted the provisional binding as it does not have resources applied", "bindingRef", bindingRef)
	}
	return nil
}
