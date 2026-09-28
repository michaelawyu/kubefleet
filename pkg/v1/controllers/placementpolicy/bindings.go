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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

// listBindingsFor lists the placement bindings owned by the given placement policy.
//
// The current placement API has no label that tracks a binding's owner placement policy (unlike, say,
// placement resource snapshots), so bindings are matched by their spec field instead of a label selector.
// A namespace-scoped placement policy owns namespace-scoped placement bindings (in its own namespace),
// while a cluster-scoped placement policy owns cluster-scoped placement bindings.
func (r *Reconciler) listBindingsFor(ctx context.Context, placementPolicy placementv1alpha1.PlacementPolicyAccessor) ([]placementv1alpha1.PlacementBindingAccessor, error) {
	var allItemRefs []placementv1alpha1.PlacementBindingAccessor
	if placementPolicy.GetNamespace() == "" {
		bindingList := &placementv1alpha1.ClusterPlacementBindingList{}
		if err := r.HubClient.List(ctx, bindingList); err != nil {
			return nil, err
		}
		allItemRefs = make([]placementv1alpha1.PlacementBindingAccessor, 0, len(bindingList.Items))
		for idx := range bindingList.Items {
			if bindingList.Items[idx].Spec.PlacementPolicyName == placementPolicy.GetName() {
				allItemRefs = append(allItemRefs, &bindingList.Items[idx])
			}
		}
		return allItemRefs, nil
	}

	bindingList := &placementv1alpha1.PlacementBindingList{}
	if err := r.HubClient.List(ctx, bindingList, client.InNamespace(placementPolicy.GetNamespace())); err != nil {
		return nil, err
	}
	allItemRefs = make([]placementv1alpha1.PlacementBindingAccessor, 0, len(bindingList.Items))
	for idx := range bindingList.Items {
		if bindingList.Items[idx].Spec.PlacementPolicyName == placementPolicy.GetName() {
			allItemRefs = append(allItemRefs, &bindingList.Items[idx])
		}
	}
	return allItemRefs, nil
}

func (r *Reconciler) createBindingsFor(
	ctx context.Context,
	placementPolicy placementv1alpha1.PlacementPolicyAccessor,
	clusters []clusterv1beta1.MemberCluster,
	selectorForSelectedClusters []placementv1alpha1.ClusterSelector,
	selectorHashForSelectedClusters []string,
	resourceSnapshotName string,
) error {
	isClusterScoped := placementPolicy.GetNamespace() == ""

	for idx := range clusters {
		cluster := clusters[idx]
		selector := selectorForSelectedClusters[idx]

		spec := placementv1alpha1.PlacementBindingSpec{
			PlacementPolicyName: placementPolicy.GetName(),
			ClusterSelectors: []placementv1alpha1.ClusterSelectorWithTermsOnly{
				{Terms: selector.Terms},
			},
			ClusterName:          cluster.Name,
			ResourceSnapshotName: resourceSnapshotName,
		}
		objectMeta := metav1.ObjectMeta{
			Name: fmt.Sprintf(placementBindingNameFmt, placementPolicy.GetName(), cluster.Name),
			Labels: map[string]string{
				placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicy.GetName(),
			},
			Annotations: map[string]string{
				clusterSelectorHashAnnotationKey: selectorHashForSelectedClusters[idx],
			},
		}

		var binding placementv1alpha1.PlacementBindingAccessor
		if isClusterScoped {
			binding = &placementv1alpha1.ClusterPlacementBinding{ObjectMeta: objectMeta, Spec: spec}
		} else {
			objectMeta.Namespace = placementPolicy.GetNamespace()
			binding = &placementv1alpha1.PlacementBinding{ObjectMeta: objectMeta, Spec: spec}
		}

		if err := controllerutil.SetControllerReference(placementPolicy, binding, r.HubClient.Scheme()); err != nil {
			wrappedErr := errors.NewUnexpectedError(err, "failed to set controller reference on the placement binding",
				"placementBinding", klog.KObj(binding), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to set controller reference on the placement binding", errors.Args(wrappedErr)...)
			return wrappedErr
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
