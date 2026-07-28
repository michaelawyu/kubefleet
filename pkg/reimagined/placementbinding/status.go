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

package placementbinding

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
)

func prepareSynchronizedCondition(work *placementv1beta1.Work, placementBinding *experimentalv1beta1.PlacementBinding) {
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

	if isWorkApplied {
		meta.SetStatusCondition(&placementBinding.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.PlacementBindingCondTypeSynchronized,
			Status:             metav1.ConditionTrue,
			Reason:             "AllResourcesApplied",
			Message:            "All resources in the snapshot have been applied on the member cluster",
			ObservedGeneration: placementBinding.Generation,
		})
	} else {
		meta.SetStatusCondition(&placementBinding.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.PlacementBindingCondTypeSynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             "NotAllResourcesApplied",
			Message:            fmt.Sprintf("%d of %d resources in the snapshot have been applied on the member cluster", len(work.Status.ManifestConditions)-failedToApplyResourceCnt, len(work.Status.ManifestConditions)),
			ObservedGeneration: placementBinding.Generation,
		})
	}
}

func prepareAllResourcesAvailableCondition(work *placementv1beta1.Work, placementBinding *experimentalv1beta1.PlacementBinding) {
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
		meta.SetStatusCondition(&placementBinding.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.PlacementBindingCondTypeAllResourcesAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "AllResourcesAvailable",
			Message:            "All resources in the snapshot are available on the member cluster",
			ObservedGeneration: placementBinding.Generation,
		})
	} else {
		meta.SetStatusCondition(&placementBinding.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.PlacementBindingCondTypeAllResourcesAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             "NotAllResourcesAvailable",
			Message:            fmt.Sprintf("%d of %d resources in the snapshot are available on the member cluster", len(work.Status.ManifestConditions)-failedToBeAvailableResourceCnt, len(work.Status.ManifestConditions)),
			ObservedGeneration: placementBinding.Generation,
		})
	}
}
