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

package stagedupdate

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const resourceSnapshotRequestMaxWaitTime = 5 * time.Minute

func (r *Reconciler) initialize(
	ctx context.Context,
	placementPolicyAccessor placementv1alpha1.PlacementPolicyAccessor,
	stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor,
) (bool, error) {
	initCond := meta.FindStatusCondition(stagedUpdateRunAccessor.GetStatus().Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeInitialized)
	// Skip if a staged update run has been initialized.
	if initCond != nil && initCond.Status == metav1.ConditionTrue {
		// Note that for this check no observed generation check is needed.
		klog.V(2).InfoS("Staged update run has been initialized; skip the initialization step", "stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor))
		return false, nil
	}

	// End the reconciliation loop if the initialization has failed.
	//
	// This is a sanity check. Normally the branch will never run.
	if initCond != nil && initCond.Status == metav1.ConditionFalse {
		klog.V(2).InfoS("Staged update run has failed to initialize; no further processing is needed", "stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor))
		return true, nil
	}

	// Retrieve the staged update strategy.
	stagedUpdateStrategy, err := r.retrieveStagedUpdateStrategy(ctx, stagedUpdateRunAccessor)
	if err != nil {
		return false, errors.Wraps(err, "failed to find staged update strategy")
	}

	// List all the placement bindings from the target placement policy.
	//
	// As the staged update run has acquired the binding manager role, it is guaranteed that no binding will be created
	// or deleted when the staged update is still in progress. This is not to say that the read (list) op here is strongly
	// consistent though; if the cache is severely lagging, and the placement policy just has a scheduling decision
	// update, the binding list returned might be stale. Considering that the chances of this happening is low, no
	// prevention measure is taken here; it is recommended that the user re-attempt a staged update if the placement policy
	// still reports that some clusters are not yet synchronized.
	placementBindings, err := r.listAllPlacementBindings(ctx, placementPolicyAccessor)
	if err != nil {
		return false, errors.Wraps(err, "failed to find placement bindings")
	}

	// Retrieve all the clusters that are referenced by the placement bindings.
	//
	// Note that due to the pre-allocation, the order of the returned clusters is guaranteed to be the same as
	// the order of the placement bindings.
	referencedClusters, err := r.retrieveReferencedClusters(ctx, placementBindings)
	if err != nil {
		return false, errors.Wraps(err, "failed to retrieve referenced clusters")
	}

	// Group clusters into stages.
	perStageStatuses, err := r.groupClusters(stagedUpdateStrategy, placementBindings, referencedClusters, klog.KObj(stagedUpdateRunAccessor))
	if err != nil {
		return false, errors.Wraps(err, "failed to group clusters into stages")
	}

	// Identify the resource snapshot to roll out.
	//
	// If one has been explicitly specified, check if it exists; otherwise, retrieve the latest snapshot or
	// request a new one if the latest is stale.
	resourceSnapshot, err := r.identifyResourceSnapshotToRollout(ctx, placementPolicyAccessor, stagedUpdateRunAccessor)
	if err != nil {
		return false, errors.Wraps(err, "failed to identify resource snapshot to roll out")
	}

	// Write the per-stage status to the staged update run.
	//
	// Once written, all follow-up processing will be based on the per-stage status, and the staged update run
	// will be considered initialized.
	if err := r.syncStagedUpdateRunInitializedStatus(ctx, stagedUpdateRunAccessor, perStageStatuses, resourceSnapshot.GetName()); err != nil {
		return false, errors.Wraps(err, "failed to mark the staged update run as initialized")
	}
	return false, nil
}

func (r *Reconciler) retrieveStagedUpdateStrategy(
	ctx context.Context,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
) (rolloutv1alpha1.StagedUpdateStrategyAccessor, error) {
	strategyKey := types.NamespacedName{
		Namespace: stagedUpdateRun.GetNamespace(),
		Name:      stagedUpdateRun.GetSpec().StagedUpdateStrategyName,
	}

	var stagedUpdateStrategy rolloutv1alpha1.StagedUpdateStrategyAccessor
	if strategyKey.Namespace == "" {
		// The staged update run is cluster-scoped; retrieve the ClusterStagedUpdateStrategy.
		stagedUpdateStrategy = &rolloutv1alpha1.ClusterStagedUpdateStrategy{}
	} else {
		// The staged update run is namespaced; retrieve the StagedUpdateStrategy in its namespace.
		stagedUpdateStrategy = &rolloutv1alpha1.StagedUpdateStrategy{}
	}

	if err := r.HubClient.Get(ctx, strategyKey, stagedUpdateStrategy); err != nil {
		return nil, errors.NewAPIServerError(err, "failed to get staged update strategy object", true,
			"stagedUpdateStrategy", klog.KRef(strategyKey.Namespace, strategyKey.Name))
	}
	return stagedUpdateStrategy, nil
}

func (r *Reconciler) listAllPlacementBindings(
	ctx context.Context,
	placementPolicy placementv1alpha1.PlacementPolicyAccessor,
) ([]placementv1alpha1.PlacementBindingAccessor, error) {
	ownerLabelSelector := client.MatchingLabels{
		placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicy.GetName(),
	}

	var placementBindings []placementv1alpha1.PlacementBindingAccessor
	if placementPolicy.GetNamespace() == "" {
		// The placement policy is cluster-scope; list all cluster placement bindings owned by it.
		cpbs := &placementv1alpha1.ClusterPlacementBindingList{}
		if err := r.HubClient.List(ctx, cpbs, ownerLabelSelector); err != nil {
			return nil, errors.NewAPIServerError(err, "failed to list cluster resource bindings", true,
				"placementPolicy", klog.KRef(placementPolicy.GetNamespace(), placementPolicy.GetName()))
		}
		for i := range cpbs.Items {
			placementBindings = append(placementBindings, &cpbs.Items[i])
		}
	} else {
		// The placement policy is namespaced; list all placement bindings owned by it in its namespace.
		namespaceSelector := client.InNamespace(placementPolicy.GetNamespace())
		pbs := &placementv1alpha1.PlacementBindingList{}
		if err := r.HubClient.List(ctx, pbs, namespaceSelector, ownerLabelSelector); err != nil {
			return nil, errors.NewAPIServerError(err, "failed to list resource bindings", true,
				"placementPolicy", klog.KRef(placementPolicy.GetNamespace(), placementPolicy.GetName()))
		}
		for i := range pbs.Items {
			placementBindings = append(placementBindings, &pbs.Items[i])
		}
	}
	return placementBindings, nil
}

func (r *Reconciler) retrieveReferencedClusters(
	ctx context.Context,
	placementBindings []placementv1alpha1.PlacementBindingAccessor,
) ([]*clusterv1beta1.MemberCluster, error) {
	// Prepare a child context.
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()

	// Pre-allocate the slice to avoid contention.
	memberClusters := make([]*clusterv1beta1.MemberCluster, len(placementBindings))
	errs := make([]error, len(placementBindings))
	// Retrieve member cluster objects in parallel.
	doWork := func(pieces int) {
		clusterName := placementBindings[pieces].GetSpec().ClusterName
		memberCluster := &clusterv1beta1.MemberCluster{}
		if err := r.HubClient.Get(childCtx, types.NamespacedName{Name: clusterName}, memberCluster); err != nil {
			errs[pieces] = errors.NewAPIServerError(err, "failed to get member cluster object", true,
				"memberCluster", klog.KRef("", clusterName), "placementBinding", klog.KObj(placementBindings[pieces]))
			// Fail fast.
			childCancel()
			return
		}
		memberClusters[pieces] = memberCluster
	}
	r.Parallelizer.ParallelizeUntil(childCtx, len(placementBindings), doWork, "retrieveReferencedClusters")

	if err := utilerrors.NewAggregate(errs); err != nil {
		return nil, errors.Wraps(err, "aggregated errors occurred when retrieving referenced clusters")
	}
	return memberClusters, nil
}

func (r *Reconciler) groupClusters(
	stagedUpdateStrategy rolloutv1alpha1.StagedUpdateStrategyAccessor,
	placementBindings []placementv1alpha1.PlacementBindingAccessor,
	clusters []*clusterv1beta1.MemberCluster,
	stagedUpdateRunObjRef klog.ObjectRef,
) (
	perStageStatuses []rolloutv1alpha1.PerStageStatus,
	err error,
) {
	checked := make(map[string]bool)

	perStageStatuses = make([]rolloutv1alpha1.PerStageStatus, 0, len(stagedUpdateStrategy.GetSpec().Stages))

	stagesInStrategy := stagedUpdateStrategy.GetSpec().Stages
	for idx := range stagesInStrategy {
		stage := stagesInStrategy[idx]

		// Handle the special cases where the stage has nil or empty label selector.
		switch {
		case stage.LabelSelector == nil:
			// The stage has nil label selector; it includes no clusters at all.
			perStageStatus, err := buildPerStageStatus(stage, nil, nil)
			if err != nil {
				return nil, errors.Wraps(err, "failed to build per stage status", "stage", stage.Name)
			}
			perStageStatuses = append(perStageStatuses, perStageStatus)
			continue
		case stage.LabelSelector != nil && (len(stage.LabelSelector.MatchLabels) == 0 && len(stage.LabelSelector.MatchExpressions) == 0):
			// The stage has an empty label selector; it includes all clusters.
			perStageStatus, err := buildPerStageStatus(stage, placementBindings, clusters)
			if err != nil {
				return nil, errors.Wraps(err, "failed to build per stage status", "stage", stage.Name)
			}
			perStageStatuses = append(perStageStatuses, perStageStatus)
			continue
		}

		// The stage has a non-empty label selector; group and sort the clusters accordingly.
		labelSelector, err := metav1.LabelSelectorAsSelector(stage.LabelSelector)
		if err != nil {
			return nil, errors.Wraps(err, "failed to convert label selector to selector", "stage", stage.Name)
		}

		// Find all the clusters that match the label selector, and their corresponding placement bindings.
		type matchedCluster struct {
			cluster *clusterv1beta1.MemberCluster
			binding placementv1alpha1.PlacementBindingAccessor

			weight int
		}
		matched := make([]matchedCluster, 0, 10) // Pre-allocate with a reasonable capacity.
		for idx := range clusters {
			cluster := clusters[idx]
			binding := placementBindings[idx]

			if checked[cluster.GetName()] {
				klog.V(2).InfoS("Cluster has already been included in a previous stage; skip the current stage",
					"cluster", klog.KObj(cluster), "placementBinding", klog.KObj(binding),
					"stage", stage.Name, "stagedUpdateRun", stagedUpdateRunObjRef, "stagedUpdateStrategy", klog.KObj(stagedUpdateStrategy))
				continue
			}

			if labelSelector.Matches(labels.Set(cluster.GetLabels())) {
				matched = append(matched, matchedCluster{cluster: cluster, binding: binding})
				checked[cluster.GetName()] = true

				klog.V(2).InfoS("Cluster matches the label selector of the stage; include it in the current stage",
					"cluster", klog.KObj(cluster), "placementBinding", klog.KObj(binding),
					"stage", stage.Name, "stagedUpdateRun", stagedUpdateRunObjRef, "stagedUpdateStrategy", klog.KObj(stagedUpdateStrategy))
			}
		}

		// Sort the matched clusters if a sorting label key is specified.
		if stage.SortingLabelKey != nil && len(*stage.SortingLabelKey) > 0 {
			sortingLabelKey := *stage.SortingLabelKey
			for idx := range matched {
				labelValue, ok := matched[idx].cluster.GetLabels()[sortingLabelKey]
				if !ok {
					return nil, errors.NewUserError(nil, "a sorting label key is specified in staged update strategy, yet the label is missing on a cluster",
						"stage", stage.Name, "sortingLabelKey", sortingLabelKey,
						"cluster", klog.KObj(matched[idx].cluster), "placementBinding", klog.KObj(matched[idx].binding),
						"stagedUpdateRun", stagedUpdateRunObjRef, "stagedUpdateStrategy", klog.KObj(stagedUpdateStrategy))
				}

				sortValue, parseErr := strconv.Atoi(labelValue)
				if parseErr != nil {
					return nil, errors.NewUserError(parseErr, "failed to parse sorting label value as integer",
						"stage", stage.Name,
						"sortingLabelKey", sortingLabelKey, "sortingLabelValue", labelValue,
						"cluster", klog.KObj(matched[idx].cluster), "placementBinding", klog.KObj(matched[idx].binding),
						"stagedUpdateRun", stagedUpdateRunObjRef, "stagedUpdateStrategy", klog.KObj(stagedUpdateStrategy))
				}
				matched[idx].weight = sortValue
			}

			sort.SliceStable(matched, func(i, j int) bool {
				if matched[i].weight == matched[j].weight {
					return matched[i].cluster.GetName() < matched[j].cluster.GetName()
				}
				return matched[i].weight < matched[j].weight
			})
		} else {
			// Sort clusters by name in ascending order when no sorting label key is specified.
			sort.SliceStable(matched, func(i, j int) bool {
				return matched[i].cluster.GetName() < matched[j].cluster.GetName()
			})
		}

		matchedClusters := make([]*clusterv1beta1.MemberCluster, 0, len(matched))
		matchedBindings := make([]placementv1alpha1.PlacementBindingAccessor, 0, len(matched))
		for idx := range matched {
			matchedClusters = append(matchedClusters, matched[idx].cluster)
			matchedBindings = append(matchedBindings, matched[idx].binding)
		}

		// Build the stage information.
		perStageStatus, err := buildPerStageStatus(stage, matchedBindings, matchedClusters)
		if err != nil {
			return nil, errors.Wraps(err, "failed to build per stage status", "stage", stage.Name)
		}
		perStageStatuses = append(perStageStatuses, perStageStatus)
	}

	return perStageStatuses, nil
}

func buildPerStageStatus(
	stage rolloutv1alpha1.Stage,
	matchedBindings []placementv1alpha1.PlacementBindingAccessor,
	matchedCluster []*clusterv1beta1.MemberCluster,
) (rolloutv1alpha1.PerStageStatus, error) {
	// Record the clusters assigned to the stage, and their corresponding placement bindings.
	perClusterStatuses := make([]rolloutv1alpha1.PerClusterStatus, 0, len(matchedCluster))
	for idx := range matchedCluster {
		matchedCluster := matchedCluster[idx]
		matchedBinding := matchedBindings[idx]

		perClusterStatuses = append(perClusterStatuses, rolloutv1alpha1.PerClusterStatus{
			ClusterName:          matchedCluster.GetName(),
			PlacementBindingName: matchedBinding.GetName(),
		})
	}

	// Record the before-stage tasks and after-stage tasks.
	beforeStageTaskStatuses := make([]rolloutv1alpha1.PerStageTaskStatus, 0, len(stage.BeforeStageTasks))
	for idx := range stage.BeforeStageTasks {
		beforeStageTask := stage.BeforeStageTasks[idx]
		beforeStageTaskStatus := rolloutv1alpha1.PerStageTaskStatus{
			Type: beforeStageTask.Type,
		}
		if beforeStageTask.Type == rolloutv1alpha1.StageTaskTypeTimedWait {
			beforeStageTaskStatus.WaitTime = beforeStageTask.WaitTime
		}
		beforeStageTaskStatuses = append(beforeStageTaskStatuses, beforeStageTaskStatus)
	}

	afterStageTaskStatuses := make([]rolloutv1alpha1.PerStageTaskStatus, 0, len(stage.AfterStageTasks))
	for idx := range stage.AfterStageTasks {
		afterStageTask := stage.AfterStageTasks[idx]
		afterStageTaskStatus := rolloutv1alpha1.PerStageTaskStatus{
			Type: afterStageTask.Type,
		}
		if afterStageTask.Type == rolloutv1alpha1.StageTaskTypeTimedWait {
			afterStageTaskStatus.WaitTime = afterStageTask.WaitTime
		}
		afterStageTaskStatuses = append(afterStageTaskStatuses, afterStageTaskStatus)
	}

	// Resolve the max concurrency value for the stage.
	maxConcurrencyValue := int32(1)
	if stage.MaxConcurrency != nil {
		resolvedMaxConcurrency, err := intstr.GetScaledValueFromIntOrPercent(stage.MaxConcurrency, len(matchedCluster), false)
		if err != nil {
			return rolloutv1alpha1.PerStageStatus{}, errors.Wraps(err, "failed to resolve max concurrency",
				"stage", stage.Name, "totalClustersCnt", len(matchedCluster), "maxConcurrency", stage.MaxConcurrency)
		}
		if resolvedMaxConcurrency == 0 {
			resolvedMaxConcurrency = 1
		}
		maxConcurrencyValue = int32(resolvedMaxConcurrency)
	}

	return rolloutv1alpha1.PerStageStatus{
		StageName:        stage.Name,
		Clusters:         perClusterStatuses,
		BeforeStageTasks: beforeStageTaskStatuses,
		AfterStageTasks:  afterStageTaskStatuses,
		MaxConcurrency:   &maxConcurrencyValue,
	}, nil
}

func (r *Reconciler) identifyResourceSnapshotToRollout(
	ctx context.Context,
	placementPolicyAccessor placementv1alpha1.PlacementPolicyAccessor,
	stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor,
) (placementv1alpha1.PlacementResourceSnapshotAccessor, error) {
	resourceSnapshotName := stagedUpdateRunAccessor.GetSpec().ResourceSnapshotName
	if resourceSnapshotName == "" {
		// If no resource snapshot is explicitly specified, retrieve the latest snapshot or request a new one if the latest is stale.
		resourceSnapshots, _, err := r.PlacementResourceSnapshotManager.SnapshotResourcesIfStale(
			ctx, placementPolicyAccessor)
		if err != nil {
			return nil, errors.Wraps(err, "failed to retrieve or create the latest resource snapshot")
		}
		return resourceSnapshots[0], nil
	}

	// A resource snapshot has been been explicitly specified; check if it exists.
	var resourceSnapshot placementv1alpha1.PlacementResourceSnapshotAccessor
	snapshotKey := types.NamespacedName{
		Namespace: placementPolicyAccessor.GetNamespace(),
		Name:      resourceSnapshotName,
	}
	if snapshotKey.Namespace == "" {
		// The resource snapshot is cluster-scoped; retrieve the ClusterPlacementResourceSnapshot.
		resourceSnapshot = &placementv1alpha1.ClusterPlacementResourceSnapshot{}
	} else {
		// The resource snapshot is namespaced; retrieve the PlacementResourceSnapshot in its namespace.
		resourceSnapshot = &placementv1alpha1.PlacementResourceSnapshot{}
	}

	if err := r.HubClient.Get(ctx, snapshotKey, resourceSnapshot); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errors.NewUserError(err, "the explicitly specified resource snapshot does not exist",
				"resourceSnapshot", klog.KRef(snapshotKey.Namespace, snapshotKey.Name))
		}
		return nil, errors.NewAPIServerError(err, "failed to get the explicitly specified resource snapshot", true,
			"resourceSnapshot", klog.KRef(snapshotKey.Namespace, snapshotKey.Name))
	}

	// Check if the retrieved resource snapshot has been marked for deletion and if it is a primary resource snapshot.
	if !resourceSnapshot.GetDeletionTimestamp().IsZero() {
		// A resource snapshot marked for deletion is on its way out (most likely reclaimed by the
		// revision history limit); rolling out against it would race the deletion and is never
		// the user's intent, so this is reported back as a user error rather than retried.
		return nil, errors.NewUserError(nil, "the explicitly specified resource snapshot has been marked for deletion",
			"resourceSnapshot", klog.KRef(snapshotKey.Namespace, snapshotKey.Name))
	}
	if subIdx := resourceSnapshot.GetLabels()[placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey]; subIdx != "0" {
		// Only the snapshot of the sub-index 0 is the primary snapshot of its index; a staged update
		// run can only be rolled out against a primary snapshot, since a secondary one does not carry
		// the full picture of the resources selected at that point in time.
		return nil, errors.NewUserError(nil, "the explicitly specified resource snapshot is not a primary resource snapshot",
			"resourceSnapshot", klog.KRef(snapshotKey.Namespace, snapshotKey.Name), "subIndex", subIdx)
	}

	return resourceSnapshot, nil
}

func (r *Reconciler) syncStagedUpdateRunInitializedStatus(
	ctx context.Context,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	perStageStatuses []rolloutv1alpha1.PerStageStatus,
	resourceSnapshotName string,
) error {
	stagedUpdateRunStatus := stagedUpdateRun.GetStatus()

	// Set the resource snapshot name to roll out, the prepped stages, and the initialized condition.
	stagedUpdateRunStatus.ResourceSnapshotNameToRollout = resourceSnapshotName
	stagedUpdateRunStatus.Stages = perStageStatuses

	meta.SetStatusCondition(&stagedUpdateRunStatus.Conditions, metav1.Condition{
		Type:   rolloutv1alpha1.StagedUpdateRunCondTypeInitialized,
		Status: metav1.ConditionTrue,
		Reason: rolloutv1alpha1.StagedUpdateRunInitializedCondReasonPreppedResourceSnapshotAndAllStages,
		Message: fmt.Sprintf("the staged update run has been initialized (resource snapshot to roll out: %s; prepped %d stages)",
			resourceSnapshotName, len(perStageStatuses)),
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})

	if err := r.HubClient.Status().Update(ctx, stagedUpdateRun); err != nil {
		return errors.NewAPIServerError(err, "failed to update the staged update run status", false)
	}
	return nil
}
