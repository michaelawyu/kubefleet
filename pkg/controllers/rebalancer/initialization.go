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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *Reconciler) initialize(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (bool, error) {
	// Check if the request has been initialized already.
	if isClusterRebalancingRequestInitialized(rebalancingReq) {
		return true, nil
	}

	placements, err := r.identifyPlacementsFor(ctx, rebalancingReq)
	if err != nil {
		return false, fmt.Errorf("failed to identify placements for the rebalancing request: %w", err)
	}

	// For each placement, attempt to claim its rollout management.
	for k := range placements {
		placement := placements[k]

		claimed, err := r.claimRolloutManagement(ctx, placement, rebalancingReq)
		if err != nil {
			return false, err
		}
		if !claimed {
			// Cannot make the rollout manager claim for the placement right now. Wait and retry later.

			// Add an Initialized condition with the false status.
			initCond := metav1.Condition{
				Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized,
				Status:             metav1.ConditionFalse,
				Reason:             "FailedToClaimRolloutManagement",
				Message:            fmt.Sprintf("Failed to claim rollout management for placement %s/%s; will retry later", placement.GetNamespace(), placement.GetName()),
				ObservedGeneration: rebalancingReq.Generation,
			}
			meta.SetStatusCondition(&rebalancingReq.Status.Conditions, initCond)
			if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
				klog.ErrorS(err, "Failed to update ClusterRebalancingRequest status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
				return false, err
			}
			return false, nil
		}
	}

	// All placements have been claims. Mark the request as initialized, and track the state.
	initCond := metav1.Condition{
		Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized,
		Status:             metav1.ConditionTrue,
		Reason:             "IdentifiedAndClaimedAllPlacements",
		Message:            fmt.Sprintf("Successfully identified and claimed %d placements", len(placements)),
		ObservedGeneration: rebalancingReq.Generation,
	}
	meta.SetStatusCondition(&rebalancingReq.Status.Conditions, initCond)
	// Refresh the status.
	if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to update ClusterRebalancingRequest status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return false, err
	}

	return true, nil
}

func isClusterRebalancingRequestInitialized(rebalancingReq *placementv1beta1.ClusterRebalancingRequest) bool {
	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
	if condition.IsConditionStatusTrue(initCond, rebalancingReq.Generation) {
		return true
	}
	return false
}

func (r *Reconciler) identifyPlacementsFor(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (map[string]placementv1beta1.PlacementObj, error) {
	if len(rebalancingReq.Status.Migrations) != 0 {
		// A previous identification attempt has been made. Use the existing list of placements.
		placements := make(map[string]placementv1beta1.PlacementObj, len(rebalancingReq.Status.Migrations))
		for idx := range rebalancingReq.Status.Migrations {
			migration := rebalancingReq.Status.Migrations[idx]

			var placement placementv1beta1.PlacementObj
			if migration.PlacementReference.Namespace != "" {
				// The placement is namespace-scoped (ResourcePlacement).
				placement = &placementv1beta1.ResourcePlacement{}
				if err := r.Client.Get(ctx, client.ObjectKey{Namespace: migration.PlacementReference.Namespace, Name: migration.PlacementReference.Name}, placement); err != nil {
					klog.ErrorS(err, "Failed to retrieve resource placement",
						"placement", fmt.Sprintf("%s/%s", migration.PlacementReference.Namespace, migration.PlacementReference.Name))
					// No need to abort the process; skip the placement instead.
					continue
				}
			} else {
				// The placement is cluster-scoped (ClusterResourcePlacement).
				placement = &placementv1beta1.ClusterResourcePlacement{}
				if err := r.Client.Get(ctx, client.ObjectKey{Name: migration.PlacementReference.Name}, placement); err != nil {
					klog.ErrorS(err, "Failed to retrieve cluster resource placement", "placement", migration.PlacementReference.Name)
					// No need to abort the process; skip the placement instead.
					continue
				}
			}
			placements[fmt.Sprintf("%s/%s", placement.GetNamespace(), placement.GetName())] = placement
		}
		return placements, nil
	}

	// No previous identification attempt has been made. Identify placements based on
	// the from cluster, and write ahead the list to the status for future reference.
	fromCluster := rebalancingReq.Spec.From

	// Identify first all placements that has the from cluster selected and write ahead
	// the list to the rebalancing request.

	// List all bindings (from cache).
	bindings := []placementv1beta1.BindingObj{}

	// List all the ClusterResourceBindings.
	clusterResBindings := &placementv1beta1.ClusterResourceBindingList{}
	if err := r.Client.List(ctx, clusterResBindings); err != nil {
		return nil, err
	}
	for idx := range clusterResBindings.Items {
		binding := &clusterResBindings.Items[idx]
		if !binding.GetDeletionTimestamp().IsZero() {
			// Skip bindings that have been marked for deletion.
			continue
		}

		if binding.Spec.TargetCluster == fromCluster {
			bindings = append(bindings, binding)
		}
	}

	// List all the ResourceBindings.
	resBindings := &placementv1beta1.ResourceBindingList{}
	if err := r.Client.List(ctx, resBindings); err != nil {
		return nil, err
	}
	for idx := range resBindings.Items {
		binding := &resBindings.Items[idx]
		if !binding.GetDeletionTimestamp().IsZero() {
			// Skip bindings that have been marked for deletion.
			continue
		}

		if binding.Spec.TargetCluster == fromCluster {
			bindings = append(bindings, binding)
		}
	}

	// Retrieve the corresponding placements.
	placements := make(map[string]placementv1beta1.PlacementObj, len(bindings))
	for idx := range bindings {
		binding := bindings[idx]

		// Find the name of the placement that produced this binding.
		parentPlacementName, ok := binding.GetLabels()[placementv1beta1.PlacementTrackingLabel]
		if !ok {
			// Normally this should never occur.
			continue
		}

		// Finding the namespace of the placement (if applicable).
		parentPlacementNamespace := binding.GetNamespace()

		// Retrieve the placement.
		var placement placementv1beta1.PlacementObj
		if parentPlacementNamespace != "" {
			// The placement is namespace-scoped (ResourcePlacement).
			placement = &placementv1beta1.ResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Namespace: parentPlacementNamespace, Name: parentPlacementName}, placement); err != nil {
				klog.ErrorS(err, "Failed to retrieve resource placement", "placement", fmt.Sprintf("%s/%s", parentPlacementNamespace, parentPlacementName), "binding", klog.KObj(binding))
				// No need to abort the initialization; skip the placement instead.
				continue
			}
		} else {
			// The placement is cluster-scoped (ClusterResourcePlacement).
			placement = &placementv1beta1.ClusterResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: parentPlacementName}, placement); err != nil {
				klog.ErrorS(err, "Failed to retrieve cluster resource placement", "placement", parentPlacementName, "binding", klog.KObj(binding))
				// No need to abort the initialization; skip the placement instead.
				continue
			}
		}
		placements[fmt.Sprintf("%s/%s", placement.GetNamespace(), placement.GetName())] = placement
	}

	migrations := make([]placementv1beta1.ClusterRebalancingRequestPerMigrationStatus, 0, len(placements))
	for k := range placements {
		placement := placements[k]
		migrations = append(migrations, placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{
			PlacementReference: corev1.ObjectReference{
				Kind:      placement.GetObjectKind().GroupVersionKind().Kind,
				Name:      placement.GetName(),
				Namespace: placement.GetNamespace(),
				UID:       placement.GetUID(),
			},
		})
	}
	rebalancingReq.Status.Migrations = migrations

	// Refresh the status.
	if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to update ClusterRebalancingRequest status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return nil, err
	}
	return placements, nil
}

func (r *Reconciler) claimRolloutManagement(
	ctx context.Context,
	placement placementv1beta1.PlacementObj,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (bool, error) {
	// Check if a claim has been made.
	rolloutManagers := placement.GetPlacementStatus().RolloutManagedBy
	switch {
	case len(rolloutManagers) == 0:
		// No claim has been made. Add the rebalancing request as the exclusive rollout manager.
		rolloutManagers = append(rolloutManagers, placementv1beta1.RolloutManagerReference{
			ObjectReference: corev1.ObjectReference{
				Kind: "ClusterRebalancingRequest",
				Name: rebalancingReq.Name,
				UID:  rebalancingReq.UID,
			},
			Mode: placementv1beta1.RolloutManagerModeExclusive,
		})
	case len(rolloutManagers) == 1 && rolloutManagers[0].Name == rebalancingReq.Name && rolloutManagers[0].UID == rebalancingReq.UID:
		// A claim has been made under this rebalancing request's name. No further action needed.
		return true, nil
	default:
		// A claim has been made under a different object, or there are multiple claims.
		//
		// Wait and retry later.
		return false, nil
	}

	// Update the placement with the new claim.
	placement.GetPlacementStatus().RolloutManagedBy = rolloutManagers
	if err := r.Client.Status().Update(ctx, placement); err != nil {
		klog.ErrorS(err, "Failed to add claim to the placement",
			"placement", klog.KObj(placement), "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return false, fmt.Errorf("Failed to add claim to the placement")
	}

	return true, nil
}
