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

package workloadplacement

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) refreshStatus(
	ctx context.Context,
	placement *experimentalv1beta1.WorkloadPlacement,
	latestResourceSnapshot *experimentalv1beta1.WorkloadResourceSnapshot,
	isLatestResourceSnapshotPresentAndUpToDate bool,
	isSchedulingNeeded bool,
	selectors []map[string]string,
	allBindings []experimentalv1beta1.WorkloadResourceClusterBinding,
	danglingBindings []experimentalv1beta1.WorkloadResourceClusterBinding,
) error {
	updatedStatus := placement.Status.DeepCopy()
	updatedStatus.LatestResourceSnapshotRevisionName = ptr.To(latestResourceSnapshot.Name)

	// Set the Scheduled condition.
	scheduledCond := meta.FindStatusCondition(updatedStatus.Conditions, experimentalv1beta1.WorkloadPlacementCondTypeScheduled)
	switch {
	case scheduledCond == nil && len(allBindings) == 0:
		// There is no bindings and the Scheduled condition has never been set before; in this case, no further
		// action is needed. Keep the placement status empty.
		return nil
	case !isSchedulingNeeded:
		// All cluster selectors have found their matching clusters, and there is no dangling binding; the placement is considered as scheduled.
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeScheduled,
			Status:             metav1.ConditionTrue,
			Reason:             "FoundClustersForAllSelectors",
			Message:            fmt.Sprintf("%d of %d clusters selected for placement", len(allBindings), len(selectors)),
			ObservedGeneration: placement.Generation,
		})
	default:
		// The placement still needs further processing (or some processing has been done and the status
		// will be refreshed in the next reconciliation attempt).
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeScheduled,
			Status:             metav1.ConditionFalse,
			Reason:             "FailedToFindClustersForAllSelectors",
			Message:            fmt.Sprintf("%d of %d clusters selected (%d clusters no longer matches)", len(allBindings), len(selectors), len(danglingBindings)),
			ObservedGeneration: placement.Generation,
		})
	}

	// Set the Synchronized condition.
	allBindingsHaveUpToDateSnapshot := true
	bindingWithStaleSnapshotCnt := 0
	for idx := range allBindings {
		binding := allBindings[idx]

		if binding.Spec.ResourceSnapshotRevisionName != nil && *binding.Spec.ResourceSnapshotRevisionName != latestResourceSnapshot.Name {
			allBindingsHaveUpToDateSnapshot = false
			bindingWithStaleSnapshotCnt++
		}
	}

	allBindingsHaveResourcesSynchronized := true
	bindingsOutOfSyncCnt := 0
	for idx := range allBindings {
		binding := allBindings[idx]

		syncCond := meta.FindStatusCondition(binding.Status.Conditions, experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized)
		if !condition.IsConditionStatusTrue(syncCond, binding.Generation) {
			allBindingsHaveResourcesSynchronized = false
			bindingsOutOfSyncCnt++
		}
	}
	switch {
	case !isLatestResourceSnapshotPresentAndUpToDate:
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             "StaleLatestResourceSnapshot",
			Message:            "The latest resource snapshot does not match with the current state of resources on the hub cluster; snapshot the resources again",
			ObservedGeneration: placement.Generation,
		})
	case !allBindingsHaveUpToDateSnapshot:
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             "NotAllBindingsHaveUpToDateSnapshot",
			Message:            fmt.Sprintf("%d of %d clusters have a stale resource snapshot; %d of %d failed to have resources synchronized", bindingWithStaleSnapshotCnt, len(allBindings), bindingsOutOfSyncCnt, len(allBindings)),
			ObservedGeneration: placement.Generation,
		})
	case !allBindingsHaveResourcesSynchronized:
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             "NotAllBindingsHaveSynchronizedResources",
			Message:            fmt.Sprintf("%d of %d clusters failed to have resources synchronized", bindingsOutOfSyncCnt, len(allBindings)),
			ObservedGeneration: placement.Generation,
		})
	default:
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
			Status:             metav1.ConditionTrue,
			Reason:             "AllBindingsHaveUpToDateSnapshot",
			Message:            "All clusters have an up-to-date resource snapshot and have their resources synchronized",
			ObservedGeneration: placement.Generation,
		})
	}

	// Set the AllResourcesAvailable condition.
	allBindingsHaveResourcesAvailable := true
	bindingsWithUnavailableResourcesCnt := 0
	for idx := range allBindings {
		binding := allBindings[idx]

		availableCond := meta.FindStatusCondition(binding.Status.Conditions, experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable)
		if !condition.IsConditionStatusTrue(availableCond, binding.Generation) {
			allBindingsHaveResourcesAvailable = false
			bindingsWithUnavailableResourcesCnt++
		}
	}
	if !allBindingsHaveResourcesAvailable {
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeAllResourcesAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             "NotAllBindingsHaveResourcesAvailable",
			Message:            fmt.Sprintf("%d of %d clusters have unavailable resources", bindingsWithUnavailableResourcesCnt, len(allBindings)),
			ObservedGeneration: placement.Generation,
		})
	} else {
		meta.SetStatusCondition(&updatedStatus.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadPlacementCondTypeAllResourcesAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "AllBindingsHaveResourcesAvailable",
			Message:            "All clusters have their resources available",
			ObservedGeneration: placement.Generation,
		})
	}

	placement.Status = *updatedStatus
	// Write the placement status.
	if err := r.HubClient.Status().Update(ctx, placement); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", false,
			"workloadPlacement", klog.KObj(placement), "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to update workload placement status", errors.Args(wrappedErr)...)
		return wrappedErr
	}
	return nil
}
