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
	"iter"

	"golang.org/x/sync/errgroup"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/scheduler/queue"
)

func (f *framework) applyChanges(
	ctx context.Context,
	bundles iter.Seq[*BindingProcessingBundle],
) error {
	// Apply the updates in parallel.
	errs, childCtx := errgroup.WithContext(ctx)
	for bundle := range bundles {
		errs.Go(func() error {
			if bundle.Action == BindingActionTypeCreate {
				// Create a new binding.
				if err := f.client.Create(childCtx, bundle.PatchedBinding); err != nil {
					return fmt.Errorf("failed to create binding %s (cluster %s): %w", bundle.PatchedBinding.GetName(), bundle.ClusterName, err)
				}
				return nil
			}

			if bundle.PatchedBinding == nil {
				// No patch is needed for the binding.
				return nil
			}

			// Use patches without optimistic locking to update a binding, as the scheduler
			// only updates the fields that are managed by it exclusively.
			patch := client.MergeFrom(bundle.Binding)
			patchOpts := []client.PatchOption{
				client.FieldOwner(schedulerFrameworkFieldOwnerName),
			}
			if err := f.client.Patch(childCtx, bundle.PatchedBinding, patch, patchOpts...); err != nil {
				return fmt.Errorf("failed to patch binding %s (cluster %s): %w", bundle.Binding.GetName(), bundle.ClusterName, err)
			}
			return nil
		})
	}

	return errs.Wait()
}

func markAsUnscheduled(binding placementv1beta1.BindingObj) {
	// Track the binding's original state as an annotation, just in case the scheduler
	// needs to restore the binding to its original state.
	annotations := binding.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[placementv1beta1.PreviousBindingStateAnnotation] = string(binding.GetBindingSpec().State)
	binding.SetAnnotations(annotations)
	binding.GetBindingSpec().State = placementv1beta1.BindingStateUnscheduled
}

func refreshSchedulingDecisionAndPolicySnapshotAssociationFor(
	binding placementv1beta1.BindingObj,
	scored *ScoredCluster,
	policy placementv1beta1.PolicySnapshotObj,
) {
	bindingSpec := binding.GetBindingSpec()
	targetCluster := bindingSpec.TargetCluster
	bindingSpec.SchedulingPolicySnapshotName = policy.GetName()

	clusterDecision := placementv1beta1.ClusterDecision{
		ClusterName: targetCluster,
		Selected:    true,
		Reason:      fmt.Sprintf(resourceScheduleSucceededMessageFormat, targetCluster),
	}
	if scored != nil {
		clusterDecision.ClusterScore = &placementv1beta1.ClusterScore{
			AffinityScore:       ptr.To(scored.Score.AffinityScore),
			TopologySpreadScore: ptr.To(scored.Score.TopologySpreadScore),
		}
		clusterDecision.Reason = fmt.Sprintf(resourceScheduleSucceededWithScoreMessageFormat, targetCluster, scored.Score.AffinityScore, scored.Score.TopologySpreadScore)
	}
	bindingSpec.ClusterDecision = clusterDecision
}

func restoreToOriginalState(
	binding placementv1beta1.BindingObj,
) error {
	var originalState placementv1beta1.BindingState

	// Read the original state from the annotations.
	currentAnnotation := binding.GetAnnotations()
	if previousStateAnnotationVal, found := currentAnnotation[placementv1beta1.PreviousBindingStateAnnotation]; found {
		originalState = placementv1beta1.BindingState(previousStateAnnotationVal)
		// Remove the original state annotation, as it is no longer needed.
		delete(currentAnnotation, placementv1beta1.PreviousBindingStateAnnotation)
		binding.SetAnnotations(currentAnnotation)
	} else {
		return fmt.Errorf("failed to restore binding to its original state: no previous state was found from the binding annotations")
	}

	binding.GetBindingSpec().State = originalState
	return nil
}

func prepareNewBindingFor(
	clusterName string,
	scored *ScoredCluster,
	placementKey queue.PlacementKey,
	policy placementv1beta1.PolicySnapshotObj,
) (placementv1beta1.BindingObj, error) {
	clusterDecision := placementv1beta1.ClusterDecision{
		ClusterName: clusterName,
		Selected:    true,
		Reason:      fmt.Sprintf(resourceScheduleSucceededMessageFormat, clusterName),
	}
	if scored != nil {
		clusterDecision.ClusterScore = &placementv1beta1.ClusterScore{
			AffinityScore:       ptr.To(scored.Score.AffinityScore),
			TopologySpreadScore: ptr.To(scored.Score.TopologySpreadScore),
		}
		clusterDecision.Reason = fmt.Sprintf(resourceScheduleSucceededWithScoreMessageFormat, clusterName, scored.Score.AffinityScore, scored.Score.TopologySpreadScore)
	}

	bindingSpec := placementv1beta1.ResourceBindingSpec{
		State: placementv1beta1.BindingStateScheduled,
		// Leave the associated resource snapshot name empty; it is up to another controller
		// to fulfill this field.
		SchedulingPolicySnapshotName: policy.GetName(),
		TargetCluster:                clusterName,
		ClusterDecision:              clusterDecision,
	}
	binding, err := generateBinding(placementKey, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate binding for cluster %s: %w", clusterName, err)
	}
	binding.SetBindingSpec(bindingSpec)
	return binding, nil
}
