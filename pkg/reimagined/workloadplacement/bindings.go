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

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func defaultResourceSnapshotRevisionName(
	workloadPlacement *experimentalv1beta1.WorkloadPlacement,
	latestResourceSnapshot *experimentalv1beta1.WorkloadResourceSnapshot,
) *string {
	res := (*string)(nil)

	scheduledCond := meta.FindStatusCondition(workloadPlacement.Status.Conditions, experimentalv1beta1.WorkloadPlacementCondTypeScheduled)
	if scheduledCond == nil {
		// The placement is just created; for the first round of scheduling, assign the current latest
		// resource snapshot revision to each binding.
		res = ptr.To(latestResourceSnapshot.Name)
	}
	return res
}

func (r *Reconciler) createBindingsFor(
	ctx context.Context,
	placement *experimentalv1beta1.WorkloadPlacement,
	clusters []clusterv1beta1.MemberCluster,
	selectorHashForSelectedClusters []string,
	resourceSnapshotRevisionNamePtr *string,
) error {
	for idx := range clusters {
		cluster := clusters[idx]

		binding := &experimentalv1beta1.WorkloadResourceClusterBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf(workloadResourceClusterBindingNameFmt, placement.Name, cluster.Name),
				Namespace: placement.Namespace,
				Labels: map[string]string{
					experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: placement.Name,
				},
				Annotations: map[string]string{
					experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: selectorHashForSelectedClusters[idx],
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: experimentalv1beta1.GroupVersion.String(),
						Kind:       "WorkloadPlacement",
						Name:       placement.Name,
						UID:        placement.UID,
						Controller: ptr.To(true),
					},
				},
			},
			Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
				WorkloadPlacementName:        placement.Name,
				ClusterSelectorHash:          selectorHashForSelectedClusters[idx],
				MemberClusterName:            ptr.To(cluster.Name),
				ResourceSnapshotRevisionName: resourceSnapshotRevisionNamePtr,
			},
		}
		if err := r.HubClient.Create(ctx, binding); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"workloadResourceClusterBinding", klog.KObj(binding), "workloadPlacement", klog.KObj(placement))
			klog.ErrorS(wrappedErr, "Failed to create workload resource cluster binding for the selected member cluster", errors.Args(wrappedErr)...)
			return wrappedErr
		}
	}

	return nil
}
