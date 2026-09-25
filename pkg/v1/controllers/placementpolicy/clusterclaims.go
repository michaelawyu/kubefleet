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
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/naming"
)

// clusterClaimName derives the name of the cluster claim submitted on behalf of a placement policy.
//
// Unlike the old (namespace-scoped) ClusterRequest API, ClusterClaim is cluster-scoped, so its name must be
// unique across the whole cluster rather than just within a namespace; the namespace is folded into the name
// via a hash to keep that uniqueness.
func clusterClaimName(placementPolicy *placementv1alpha1.PlacementPolicy) string {
	id := fmt.Sprintf("%s/%s", placementPolicy.Namespace, placementPolicy.Name)
	nameSegment := naming.Truncate(naming.Sanitize(placementPolicy.Name), validation.DNS1123SubdomainMaxLength-naming.HashLength-1)
	return fmt.Sprintf("%s-%s", nameSegment, naming.Hash(id))
}

func (r *Reconciler) submitClusterClaimIfNeeded(
	ctx context.Context,
	placementPolicy *placementv1alpha1.PlacementPolicy,
	clusters []clusterv1beta1.MemberCluster,
	unmatchedSelectors []placementv1alpha1.ClusterSelector,
) (bool, error) {
	claimName := clusterClaimName(placementPolicy)

	if len(unmatchedSelectors) == 0 {
		klog.V(2).InfoS("No unmatched cluster selector, no need to submit a cluster claim", "placementPolicy", klog.KObj(placementPolicy))
		staleClaim := &placementv1alpha1.ClusterClaim{}
		staleClaim.Name = claimName
		if err := r.HubClient.Delete(ctx, staleClaim); err != nil && !apierrors.IsNotFound(err) {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"clusterClaim", klog.KObj(staleClaim), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to delete stale cluster claim for the placement policy", errors.Args(wrappedErr)...)
			return false, wrappedErr
		}
		return false, nil
	}

	klog.V(2).InfoS("Submitting cluster claim for the unmatched cluster selectors", "placementPolicy", klog.KObj(placementPolicy), "unmatchedSelectors", unmatchedSelectors)
	curClusterClaim := &placementv1alpha1.ClusterClaim{}
	if err := r.HubClient.Get(ctx, client.ObjectKey{Name: claimName}, curClusterClaim); err != nil {
		if apierrors.IsNotFound(err) {
			// No cluster claim exists for the placement.
			klog.V(2).InfoS("No existing cluster claim found for the placement policy; need to create a new one", "placementPolicy", klog.KObj(placementPolicy))
			curClusterClaim = nil
		} else {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"clusterClaim", client.ObjectKey{Name: claimName}, "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to get cluster claim for the placement policy", errors.Args(wrappedErr)...)

			return false, wrappedErr
		}
	}

	latestObservedClusterCreationTimestamp := metav1.Time{}
	for cidx := range clusters {
		cluster := clusters[cidx]

		if cluster.CreationTimestamp.After(latestObservedClusterCreationTimestamp.Time) {
			latestObservedClusterCreationTimestamp = cluster.CreationTimestamp
		}
	}

	if curClusterClaim == nil {
		// No cluster claim exists for the placement; need to create a new one.
		//
		// Note that, unlike the old ClusterRequest API, ClusterClaim is cluster-scoped and therefore cannot
		// carry an owner reference back to a namespaced placement policy; the PlacementPolicyRef spec field
		// is used instead to track the association.
		placementPolicyRef := placementPolicyObjectRef(placementPolicy)
		newClusterClaim := &placementv1alpha1.ClusterClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: claimName,
			},
			Spec: placementv1alpha1.ClusterClaimSpec{
				PlacementPolicyRef:   &placementPolicyRef,
				ClusterSelectorTerms: unmatchedSelectors[0].Terms,
			},
		}

		if err := r.HubClient.Create(ctx, newClusterClaim); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"clusterClaim", klog.KObj(newClusterClaim), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to create cluster claim for the placement policy", errors.Args(wrappedErr)...)
			return false, wrappedErr
		}

		// Add the latest observed cluster creation timestamp to the claim.
		if !latestObservedClusterCreationTimestamp.IsZero() {
			newClusterClaim.Status.LastObservedMostRecentClusterCreationTimestamp = &latestObservedClusterCreationTimestamp
			if err := r.HubClient.Status().Update(ctx, newClusterClaim); err != nil {
				wrappedErr := errors.NewAPIServerError(err, "", false,
					"clusterClaim", klog.KObj(newClusterClaim), "placementPolicy", klog.KObj(placementPolicy))
				klog.ErrorS(wrappedErr, "Failed to update cluster claim status for the placement policy", errors.Args(wrappedErr)...)
				return false, wrappedErr
			}
		}

		return true, nil
	}

	// A cluster claim already exists for the placement; check to see if it still matches one of the
	// currently unmatched selectors (its own selector terms are immutable, so it must be recreated,
	// not updated, if the selector it was created for no longer applies), or if it should be deleted.
	stillNeeded := false
	for idx := range unmatchedSelectors {
		if reflect.DeepEqual(curClusterClaim.Spec.ClusterSelectorTerms, unmatchedSelectors[idx].Terms) {
			stillNeeded = true
			break
		}
	}
	if !stillNeeded {
		if err := r.HubClient.Delete(ctx, curClusterClaim); err != nil && !apierrors.IsNotFound(err) {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"clusterClaim", klog.KObj(curClusterClaim), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to delete cluster claim for the placement policy", errors.Args(wrappedErr)...)
			return false, wrappedErr
		}
		return true, nil
	}

	// Update the latest observed cluster creation timestamp on the claim (if applicable).
	if curClusterClaim.DeletionTimestamp.IsZero() &&
		(curClusterClaim.Status.LastObservedMostRecentClusterCreationTimestamp == nil ||
			latestObservedClusterCreationTimestamp.After(curClusterClaim.Status.LastObservedMostRecentClusterCreationTimestamp.Time)) {
		curClusterClaim.Status.LastObservedMostRecentClusterCreationTimestamp = &latestObservedClusterCreationTimestamp
		if err := r.HubClient.Status().Update(ctx, curClusterClaim); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false,
				"clusterClaim", klog.KObj(curClusterClaim), "placementPolicy", klog.KObj(placementPolicy))
			klog.ErrorS(wrappedErr, "Failed to update cluster claim status for the placement policy", errors.Args(wrappedErr)...)
			return false, wrappedErr
		}
	}
	return true, nil
}
