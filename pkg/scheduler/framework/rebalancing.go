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

package framework

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

func (f *framework) lookupRebalancingRequest(
	ctx context.Context,
	placement placementv1beta1.PlacementObj,
) (*placementv1beta1.ClusterRebalancingRequest, error) {
	rolloutManagers := placement.GetPlacementStatus().RolloutManagedBy
	if len(rolloutManagers) == 0 || len(rolloutManagers) > 1 {
		// Rebalancing requests require exclusive control over a placement's rollout process.
		// If there are no rollout managers, or there are multiple ones, the placement is not (yet)
		// a rebalancing target. No further processing is needed.
		klog.V(2).InfoS("No rebalancing request is found for the placement", "placement", klog.KObj(placement))
		return nil, nil
	}

	rolloutManager := rolloutManagers[0]
	if rolloutManager.Kind != "ClusterRebalancingRequest" {
		// The current rollout manager is not a rebalancing request; no further processing is needed.
		klog.V(2).InfoS("No rebalancing request is found for the placement", "placement", klog.KObj(placement))
		return nil, nil
	}

	// Retrieve the ClusterRebalancingRequest object as referenced.
	rebalancingReqName := rolloutManager.Name
	rebalancingReq := &placementv1beta1.ClusterRebalancingRequest{}
	if err := f.client.Get(ctx, types.NamespacedName{Namespace: "", Name: rebalancingReqName}, rebalancingReq); err != nil {
		return nil, fmt.Errorf("failed to get rebalancing request: %w", err)
	}

	// Check if the request has been initialized.
	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
	if !condition.IsConditionStatusTrue(initCond, rebalancingReq.Generation) {
		// The request has not been initialized; no further processing is needed.
		klog.V(2).InfoS("Rebalancing request is not initialized yet", "rebalancingRequest", klog.KObj(rebalancingReq))
		return nil, nil
	}

	// Note that the scheduler will still process completed rebalancing requests.
	return rebalancingReq, nil
}

func (f *framework) acknowledgeRebalancingRequest(
	ctx context.Context,
	placement placementv1beta1.PlacementObj,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) error {
	placementAnnotations := placement.GetAnnotations()
	if placementAnnotations == nil {
		placementAnnotations = map[string]string{}
	}
	placementAnnotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey] = string(rebalancingReq.GetUID())
	placement.SetAnnotations(placementAnnotations)
	if err := f.client.Update(ctx, placement); err != nil {
		return fmt.Errorf("failed to add rebalancing managed-by annotation to placement: %w", err)
	}
	return nil
}

func (f *framework) withdrawRebalancingRequestAcknowledgement(ctx context.Context, placement placementv1beta1.PlacementObj) error {
	placementAnnotations := placement.GetAnnotations()
	if _, found := placementAnnotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey]; !found {
		return nil
	}
	delete(placementAnnotations, placementv1beta1.PlacementRebalancingManagedByAnnotationKey)
	placement.SetAnnotations(placementAnnotations)
	if err := f.client.Update(ctx, placement); err != nil {
		return fmt.Errorf("failed to withdraw rebalancing request acknowledgement from placement: %w", err)
	}
	return nil
}

func (f *framework) manipulateBindingsForRebalancingRequest(
	ctx context.Context,
	placement placementv1beta1.PlacementObj,
	policy placementv1beta1.PolicySnapshotObj,
	bindings []placementv1beta1.BindingObj,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) error {
	var fromBinding placementv1beta1.BindingObj
	var fromBindingCnt int
	var toBinding placementv1beta1.BindingObj
	var toBindingCnt int

	for idx := range bindings {
		binding := bindings[idx]

		switch binding.GetBindingSpec().TargetCluster {
		case rebalancingReq.Spec.From:
			fromBinding = binding
			fromBindingCnt++
		case rebalancingReq.Spec.To:
			toBinding = binding
			toBindingCnt++
		}
	}

	switch {
	case fromBindingCnt > 1:
		// Retry with backoff.
		return fmt.Errorf("multiple bindings are found for the rebalancing request from cluster %s: %d", rebalancingReq.Spec.From, fromBindingCnt)
	case toBindingCnt > 1:
		// Retry with backoff.
		return fmt.Errorf("multiple bindings are found for the rebalancing request to cluster %s: %d", rebalancingReq.Spec.To, toBindingCnt)
	case fromBinding == nil || !fromBinding.GetDeletionTimestamp().IsZero():
		// There exists a corner case where a binding might exist when the rebalancing request is being
		// initialized, but disappears (or has been marked for deletion) before the scheduler
		// acknowledges the request. In this case, the scheduler would still acknowledge the request,
		// but would perform no action on any binding; it is up to the rebalancing controller to
		// handle this case properly.
		klog.V(2).InfoS(
			"No from binding is found for the rebalancing request; the request might have observed an inconsistent state during initialization",
			"rebalancingRequest", klog.KObj(rebalancingReq),
			"placement", klog.KObj(placement),
			"fromCluster", rebalancingReq.Spec.From)
		if err := f.acknowledgeRebalancingRequest(ctx, placement, rebalancingReq); err != nil {
			return fmt.Errorf("failed to acknowledge the rebalancing request: %w", err)
		}
		return nil
	case toBinding != nil && !toBinding.GetDeletionTimestamp().IsZero():
		// The to binding already exists but it has been marked for deletion. Wait until
		// the binding is fully deleted.
		klog.V(2).InfoS(
			"The to binding for the rebalancing request already exists but has been marked for deletion; waiting until it is fully deleted",
			"rebalancingRequest", klog.KObj(rebalancingReq),
			"placement", klog.KObj(placement),
			"toCluster", rebalancingReq.Spec.To)
		return fmt.Errorf("the to binding exists and has been marked for deletion")
	}

	// Process the from binding.
	fromBindingAnnotations := fromBinding.GetAnnotations()
	if fromBindingAnnotations == nil {
		fromBindingAnnotations = map[string]string{}
	}
	fromBindingAnnotations[placementv1beta1.ReservedBindingAnnotationKey] = strconv.FormatBool(true)
	fromBinding.SetAnnotations(fromBindingAnnotations)
	if err := f.client.Update(ctx, fromBinding); err != nil {
		return fmt.Errorf("failed to mark the from binding as bound: %w", err)
	}

	if toBinding != nil {
		// Process the to binding if it exists.

		// Mark the binding as bound.
		toBindingAnnotations := toBinding.GetAnnotations()
		if toBindingAnnotations == nil {
			toBindingAnnotations = map[string]string{}
		}
		toBindingAnnotations[placementv1beta1.ReservedBindingAnnotationKey] = strconv.FormatBool(true)
		toBinding.SetAnnotations(toBindingAnnotations)

		if err := f.client.Update(ctx, toBinding); err != nil {
			return fmt.Errorf("failed to mark the to binding as bound: %w", err)
		}
	} else {
		// The to binding does not exist. Create a new, provisional binding with the same spec as
		// the from binding, and set it to the bound state.
		if placement.GetNamespace() == "" {
			toBinding = &placementv1beta1.ClusterResourceBinding{}
		} else {
			toBinding = &placementv1beta1.ResourceBinding{}
		}
		// Set object metadata.
		toBinding.SetNamespace(placement.GetNamespace())
		// TO-DO (chenyu1): handle name generation properly.
		toBinding.SetName(fmt.Sprintf("%s-%s-provisional", placement.GetName(), rebalancingReq.Spec.To))
		// Reset the labels.
		toBinding.SetLabels(map[string]string{
			placementv1beta1.PlacementTrackingLabel: placement.GetName(),
		})
		// Reset the annotations.
		toBinding.SetAnnotations(map[string]string{
			placementv1beta1.ProvisionalBindingAnnotationKey: strconv.FormatBool(true),
			placementv1beta1.ReservedBindingAnnotationKey:    strconv.FormatBool(true),
		})
		toBinding.SetFinalizers([]string{placementv1beta1.SchedulerBindingCleanupFinalizer})

		// Set the binding spec.
		toBinding.GetBindingSpec().TargetCluster = rebalancingReq.Spec.To
		toBinding.GetBindingSpec().State = placementv1beta1.BindingStateBound
		toBinding.GetBindingSpec().ApplyStrategy = fromBinding.GetBindingSpec().ApplyStrategy.DeepCopy()
		toBinding.GetBindingSpec().SchedulingPolicySnapshotName = fromBinding.GetBindingSpec().SchedulingPolicySnapshotName
		// Note that the resource snapshot might be an empty value on both bindings.
		toBinding.GetBindingSpec().ResourceSnapshotName = fromBinding.GetBindingSpec().ResourceSnapshotName
		// TO-DO (chenyu1): support overrides.
		toBinding.GetBindingSpec().ClusterDecision = placementv1beta1.ClusterDecision{
			ClusterName: rebalancingReq.Spec.To,
			Selected:    true,
			Reason:      fmt.Sprintf("picked by rebalancing request (from cluster %s to %s)", rebalancingReq.Spec.From, rebalancingReq.Spec.To),
		}

		if err := f.client.Create(ctx, toBinding); err != nil {
			return fmt.Errorf("failed to create the to binding: %w", err)
		}
	}

	return nil
}
