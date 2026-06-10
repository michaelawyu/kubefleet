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

package workloadmigrationrequest

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	bindingmanagertools "github.com/kubefleet-dev/kubefleet/pkg/reimagined/utils/bindingmanager"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) initialize(ctx context.Context, req *experimentalv1beta1.WorkloadMigrationRequest) error {
	// Check if the workload migration request has already been initialized.
	if isWorkloadMigrationRequestInitialized(req) {
		return nil
	}

	// Prep the list of all member clusters.
	allClustersList := &clusterv1beta1.MemberClusterList{}
	if err := r.HubClient.List(ctx, allClustersList); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to list member clusters in all namespaces", true)
		return wrappedErr
	}
	allClusters := allClustersList.Items

	// Must write this information ahead; otherwise there is a risk of leaving the request
	// as the permanant binding manager for some of the affected workload placements.
	fromClusterNameSet, targetBindings, clusterNamesToIgnore, err := r.retrieveOrCalculateFromClusterNameSetAndTargetBindings(ctx, req, allClusters)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve or calculate the from cluster name set and target bindings for the workload migration request")
		return wrappedErr
	}

	wantBindingManager := &experimentalv1beta1.PlacementBindingManager{
		Mode: experimentalv1beta1.BindingManagerModeExclusive,
		ObjectRef: &experimentalv1beta1.SameNamespacedObjectReference{
			Name:       req.Name,
			APIGroup:   experimentalv1beta1.GroupVersion.Group,
			APIVersion: experimentalv1beta1.GroupVersion.Version,
			Kind:       "WorkloadMigrationRequest",
			Resource:   "workloadmigrationrequests",
		},
	}
	for idx := range targetBindings {
		binding := targetBindings[idx]
		placement := &experimentalv1beta1.WorkloadPlacement{}
		placementNamespacedName := client.ObjectKey{Name: binding.Spec.WorkloadPlacementName, Namespace: binding.Namespace}
		if err := r.HubClient.Get(ctx, placementNamespacedName, placement); err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to get the workload placement for a workload resource cluster binding as part of initializing the workload migration request", true,
				"workloadResourceClusterBinding", klog.KObj(binding), "workloadPlacement", placementNamespacedName)
			return wrappedErr
		}

		// Claim the binding manager role for the workload placement.
		claimed, err := bindingmanagertools.ClaimAsBindingManager(ctx, r.HubClient, placement, wantBindingManager)
		if err != nil {
			wrappedErr := errors.Wraps(err, "failed to claim the binding manager role for a workload placement as part of initializing the workload migration request")
			return wrappedErr
		}
		if !claimed {
			// The binding manager role has been claimed by another controller; in this case, give up the scheduling attempt and requeue after some time.
			return errors.NewTransientError(nil, "the binding manager role for the workload placement %s/%s has been claimed by another controller; will retry after some time", placement.Namespace, placement.Name)
		}

	}

	toClusters := findToClusters(req, allClusters, fromClusterNameSet, clusterNamesToIgnore)

	// In this demo, for simplicity reasons, the system will attempt a 1:1 mapping between
	// the from clusters and to clusters.
	toClusterNameByFromClusterNameMapping := map[string]string{}
	tIdx := 0
	for fromClusterName := range fromClusterNameSet {
		if tIdx < len(toClusters) {
			toCluster := toClusters[tIdx]
			toClusterNameByFromClusterNameMapping[fromClusterName] = toCluster.Name
		} else {
			toClusterNameByFromClusterNameMapping[fromClusterName] = ""
		}
		tIdx++
	}

	migrationAttempts := req.Status.MigrationAttempts
	for idx := range migrationAttempts {
		migrationAttempt := &migrationAttempts[idx]
		fromClusterName := migrationAttempt.FromClusterName

		if toClusterName := toClusterNameByFromClusterNameMapping[fromClusterName]; len(toClusterName) > 0 {
			migrationAttempt.ToClusterName = &toClusterName
		} else {
			migrationAttempt.ToClusterName = nil
			migrationAttempt.ToClusterRequestName = ptr.To(fmt.Sprintf(clusterRequestNameFmt, req.Name, fromClusterName))
		}
	}

	// Report the migration attempts in the status and mark the request as initialized.
	fromToDiff := len(fromClusterNameSet) - len(toClusters)
	if fromToDiff < 0 {
		fromToDiff = 0
	}
	meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
		Type:   experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized,
		Status: metav1.ConditionTrue,
		Reason: "CalculatedAllMigrationAttempts",
		Message: fmt.Sprintf("Migrating %d bindings across %d from clusters to %d to clusters (%d existing, %d to request) (%d migration attempts in total)",
			len(targetBindings), len(fromClusterNameSet), len(fromClusterNameSet), len(toClusters), fromToDiff, len(migrationAttempts)),
		ObservedGeneration: req.Generation,
	})
	req.Status.MigrationAttempts = migrationAttempts

	if err := r.HubClient.Status().Update(ctx, req); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to update the status of the workload migration request after initialization", true)
		return wrappedErr
	}
	return nil
}

func isWorkloadMigrationRequestInitialized(req *experimentalv1beta1.WorkloadMigrationRequest) bool {
	initCond := meta.FindStatusCondition(req.Status.Conditions, experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized)
	if initCond != nil && initCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

func (r *Reconciler) retrieveOrCalculateFromClusterNameSetAndTargetBindings(
	ctx context.Context,
	req *experimentalv1beta1.WorkloadMigrationRequest,
	allClusters []clusterv1beta1.MemberCluster,
) (sets.Set[string], []*experimentalv1beta1.WorkloadResourceClusterBinding, sets.Set[string], error) {
	if req.Status.MigrationAttempts != nil {
		// A previous attempt has been made to calculate the from clusters and target bindings;
		// reconstruct the info based on the recorded migration attempts.
		fromClusterNameSet := sets.Set[string]{}
		targetBindings := make([]*experimentalv1beta1.WorkloadResourceClusterBinding, 0, len(req.Status.MigrationAttempts))
		clusterNamesToIgnore := sets.Set[string]{}
		for idx := range req.Status.MigrationAttempts {
			migrationAttempt := &req.Status.MigrationAttempts[idx]
			fromClusterNameSet.Insert(migrationAttempt.FromClusterName)

			bindingRef := migrationAttempt.WorkloadResourceBindingRef
			binding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			if err := r.HubClient.Get(ctx, client.ObjectKey{Name: bindingRef.Name, Namespace: bindingRef.Namespace}, binding); err != nil {
				wrappedErr := errors.NewAPIServerError(err,
					"failed to get a workload resource cluster binding as referenced by a migration attempt", true,
					"workloadResourceClusterBindingRef", bindingRef)
				return nil, nil, nil, wrappedErr
			}
			targetBindings = append(targetBindings, binding)

			bindingCandidates, err := r.findMatchingWorkloadBindings(ctx, req)
			if err != nil {
				wrappedErr := errors.Wraps(err,
					"failed to find the workload resource cluster bindings matching with the workload migration request as part of reconstructing the from cluster name set and target bindings")
				return nil, nil, nil, wrappedErr
			}
			clusterNamesToIgnore := sets.Set[string]{}
			for idx := range bindingCandidates {
				binding := &bindingCandidates[idx]
				if binding.Spec.MemberClusterName != nil {
					clusterNamesToIgnore.Insert(*binding.Spec.MemberClusterName)
				}
			}
		}
		return fromClusterNameSet, targetBindings, clusterNamesToIgnore, nil
	}

	// No previous attempt has been made to calculate the from clusters and target bindings;
	// calculate the info based on the current view of the world, and persist the calculated info in status for future reference.
	// Find all the bindings that match with the workload placement selectors as specified
	// in the workload migration request.
	bindingCandidates, err := r.findMatchingWorkloadBindings(ctx, req)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to find the workload resource cluster bindings matching with the workload migration request")
		return nil, nil, nil, wrappedErr
	}

	fromClusters := findFromClusters(req, allClusters)
	fromClusterNameSet := sets.Set[string]{}
	for _, cluster := range fromClusters {
		fromClusterNameSet.Insert(cluster.Name)
	}

	targetBindings := findTargetBindings(bindingCandidates, fromClusterNameSet)

	clusterNamesToIgnore := sets.Set[string]{}
	for idx := range bindingCandidates {
		binding := &bindingCandidates[idx]
		if binding.Spec.MemberClusterName != nil {
			clusterNamesToIgnore.Insert(*binding.Spec.MemberClusterName)
		}
	}

	migrationAttempts := make([]experimentalv1beta1.WorkloadMigrationAttempt, 0, len(targetBindings))
	for idx := range targetBindings {
		binding := targetBindings[idx]
		fromClusterName := *binding.Spec.MemberClusterName

		migrationAttempt := experimentalv1beta1.WorkloadMigrationAttempt{
			WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
				Name:       binding.Name,
				Namespace:  binding.Namespace,
				APIGroup:   experimentalv1beta1.GroupVersion.Group,
				APIVersion: experimentalv1beta1.GroupVersion.Version,
				Kind:       "WorkloadResourceClusterBinding",
				Resource:   "workloadresourceclusterbindings",
			},
			WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
				Name:       binding.Spec.WorkloadPlacementName,
				Namespace:  binding.Namespace,
				APIGroup:   experimentalv1beta1.GroupVersion.Group,
				APIVersion: experimentalv1beta1.GroupVersion.Version,
				Kind:       "WorkloadPlacement",
				Resource:   "workloadplacements",
			},
			FromClusterName: fromClusterName,
		}
		migrationAttempts = append(migrationAttempts, migrationAttempt)
	}

	req.Status.MigrationAttempts = migrationAttempts
	if err := r.HubClient.Status().Update(ctx, req); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to update the status of the workload migration request after calculating the from cluster name set and target bindings", true)
		return nil, nil, nil, wrappedErr
	}
	return fromClusterNameSet, targetBindings, clusterNamesToIgnore, nil
}

func (r *Reconciler) findMatchingWorkloadBindings(ctx context.Context, req *experimentalv1beta1.WorkloadMigrationRequest,
) ([]experimentalv1beta1.WorkloadResourceClusterBinding, error) {
	allBindings := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
	if err := r.HubClient.List(ctx, allBindings); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to list workload resource bindings in all namespaces", true)
		return nil, wrappedErr
	}

	if len(req.Spec.WorkloadPlacementSelectors) == 0 {
		// No workload placement selectors are specified; return bindings from all workload placements.
		return allBindings.Items, nil
	}

	workloadPlacementSelectors := req.Spec.WorkloadPlacementSelectors
	matchingBindings := make([]experimentalv1beta1.WorkloadResourceClusterBinding, 0, 10)
	for idx := range allBindings.Items {
		binding := &allBindings.Items[idx]
		placementName := binding.Spec.WorkloadPlacementName

		placement := &experimentalv1beta1.WorkloadPlacement{}
		if err := r.HubClient.Get(ctx, client.ObjectKey{Name: placementName, Namespace: binding.Namespace}, placement); err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to get the workload placement for a workload resource cluster binding", true,
				"workloadResourceClusterBinding", klog.KObj(binding), "workloadPlacement", placementName)
			return nil, wrappedErr
		}

		// Check if the placement matches with any of the workload placement selectors.
		if anyOfWorkloadPlacementSelectorsMatches(workloadPlacementSelectors, placement) {
			matchingBindings = append(matchingBindings, *binding)
		}
	}
	return matchingBindings, nil
}

func anyOfWorkloadPlacementSelectorsMatches(selectors []map[string]string, placement *experimentalv1beta1.WorkloadPlacement) bool {
	for _, selector := range selectors {
		matched := true
		for k, v := range selector {
			if gotV, found := placement.Labels[k]; !found || gotV != v {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func findFromClusters(req *experimentalv1beta1.WorkloadMigrationRequest, allClusters []clusterv1beta1.MemberCluster,
) []*clusterv1beta1.MemberCluster {
	fromClusterSelector := req.Spec.FromClusterSelector
	matchingClusters := make([]*clusterv1beta1.MemberCluster, 0, 10)
	for idx := range allClusters {
		cluster := &allClusters[idx]
		if clusterSelectorMatches(cluster, fromClusterSelector) {
			matchingClusters = append(matchingClusters, cluster)
		}
	}
	return matchingClusters
}

func clusterSelectorMatches(cluster *clusterv1beta1.MemberCluster, selector map[string]string) bool {
	for k, v := range selector {
		if gotV, found := cluster.Labels[k]; !found || gotV != v {
			return false
		}
	}
	return true
}

func findToClusters(
	req *experimentalv1beta1.WorkloadMigrationRequest,
	allClusters []clusterv1beta1.MemberCluster,
	fromClusterNameSet sets.Set[string],
	clusterNamesToIgnore sets.Set[string],
) []*clusterv1beta1.MemberCluster {
	toClusterSelector := req.Spec.ToClusterSelector
	matchingClusters := make([]*clusterv1beta1.MemberCluster, 0, 10)
	for idx := range allClusters {
		cluster := &allClusters[idx]
		if clusterSelectorMatches(cluster, toClusterSelector) {
			// Exclude the from clusters and ignored clusters from the to clusters.
			if !fromClusterNameSet.Has(cluster.Name) && !clusterNamesToIgnore.Has(cluster.Name) {
				matchingClusters = append(matchingClusters, cluster)
			}
		}
	}
	return matchingClusters
}

func findTargetBindings(
	bindingCandidates []experimentalv1beta1.WorkloadResourceClusterBinding,
	fromClusterNameSet sets.Set[string],
) []*experimentalv1beta1.WorkloadResourceClusterBinding {
	matchingBindings := make([]*experimentalv1beta1.WorkloadResourceClusterBinding, 0, 10)
	for idx := range bindingCandidates {
		binding := &bindingCandidates[idx]
		if binding.Spec.MemberClusterName == nil {
			continue
		}
		if fromClusterNameSet.Has(*binding.Spec.MemberClusterName) {
			matchingBindings = append(matchingBindings, binding)
		}
	}
	return matchingBindings
}
