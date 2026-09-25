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

package placementpolicy

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) createBindingsFor(
	ctx context.Context,
	placementPolicy *placementv1alpha1.PlacementPolicy,
	clusters []clusterv1beta1.MemberCluster,
	selectorForSelectedClusters []placementv1alpha1.ClusterSelector,
	selectorHashForSelectedClusters []string,
	resourceSnapshotName string,
) error {
	for idx := range clusters {
		cluster := clusters[idx]
		selector := selectorForSelectedClusters[idx]

		binding := &placementv1alpha1.PlacementBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf(placementBindingNameFmt, placementPolicy.Name, cluster.Name),
				Namespace: placementPolicy.Namespace,
				Annotations: map[string]string{
					clusterSelectorHashAnnotationKey: selectorHashForSelectedClusters[idx],
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: placementv1alpha1.GroupVersion.String(),
						Kind:       placementv1alpha1.PlacementPolicyKind,
						Name:       placementPolicy.Name,
						UID:        placementPolicy.UID,
						Controller: ptr.To(true),
					},
				},
			},
			Spec: placementv1alpha1.PlacementBindingSpec{
				PlacementPolicyName: placementPolicy.Name,
				ClusterSelectors: []placementv1alpha1.ClusterSelectorWithTermsOnly{
					{Terms: selector.Terms},
				},
				ClusterName:          cluster.Name,
				ResourceSnapshotName: resourceSnapshotName,
			},
		}
		if err := r.HubClient.Create(ctx, binding); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"placementBinding", klog.KObj(binding), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to create placement binding for the selected member cluster", errors.Args(wrappedErr)...)
			return wrappedErr
		}
	}

	return nil
}
