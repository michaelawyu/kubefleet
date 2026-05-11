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

package rebalancer

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
)

func (r *Reconciler) rollbackChanges(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (ctrl.Result, error) {
	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
	rolledBackCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack)
	switch {
	case condition.IsConditionStatusTrue(rolledBackCond, rebalancingReq.Generation):
		// The request has been rolled back. No action to take.
		klog.V(2).InfoS(
			"The rebalancing request has already been rolled back; no need to roll back again",
			"clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, nil
	case condition.IsConditionStatusFalse(rolledBackCond, rebalancingReq.Generation) &&
		rolledBackCond.Reason == placementv1beta1.ClusterRebalancingReqRolledBackCondReasonFailed:
		// The request has been rolled back, but the roll back has failed. No action to take.
		klog.V(2).InfoS(
			"The rebalancing request has already been rolled back but the roll back has failed; no need to roll back again",
			"clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, nil
	}

	// If the request has never been initialized, or the initialization has failed,
	// there is no need to perform a roll back as the migrations never started.
	switch {
	case condition.IsConditionStatusTrue(initCond, rebalancingReq.Generation):
		// The request has been initialized and the the initialization condition is up to date.
		// No action to take.
	case initCond == nil || initCond.Status == metav1.ConditionFalse:
		// The request has not been initialized yet, or the initialization failed.
		// No need to perform a rollback as the migrations never started.
		klog.V(2).InfoS(
			"The rebalancing request has not been initialized yet or the initialization has failed; no need to roll back",
			"clusterRebalancingRequest", klog.KObj(rebalancingReq))

		// Update the rolled back condition to indicate that the roll back is skipped.
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
			Status:             metav1.ConditionTrue,
			Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonSkipped,
			Message:            "The rebalancing request has not been initialized yet or the initialization has failed; no need to roll back",
			ObservedGeneration: rebalancingReq.Generation,
		})
		if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to update rebalancing request status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	default:
		// The request has been initialized but the condition is not up to date (as the rollback switch
		// was just turned on). Refresh the conditon.
		klog.V(2).InfoS("The rebalancing request has been initialized but the condition is not up to date; refresh the initialization condition", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		initCond.ObservedGeneration = rebalancingReq.Generation
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *initCond)
		if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to update rebalancing request status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
	}

	// Similarly, if the request has not been started, or the startup has failed,
	// there is no need to perform a roll back.
	startedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeStarted)
	switch {
	case condition.IsConditionStatusTrue(startedCond, rebalancingReq.Generation):
		// The request has been started and the the started condition is up to date.
		// No action to take.
	case startedCond == nil || startedCond.Status == metav1.ConditionFalse:
		// The request has not been started yet, or the startup failed.
		// No need to perform a rollback as the migrations never started.
		klog.V(2).InfoS(
			"The rebalancing request has not been started yet or the startup has failed; no need to roll back",
			"clusterRebalancingRequest", klog.KObj(rebalancingReq))

		// Update the rolled back condition to indicate that the roll back is skipped.
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
			Status:             metav1.ConditionTrue,
			Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonSkipped,
			Message:            "The rebalancing request has not been started yet or the startup has failed; no need to roll back",
			ObservedGeneration: rebalancingReq.Generation,
		})
		if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to update rebalancing request status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	default:
		// The request has been started but the condition is not up to date (as the rollback switch
		// was just turned on). Refresh the conditon.
		klog.V(2).InfoS("The rebalancing request has been started but the condition is not up to date; refresh the started condition", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		startedCond.ObservedGeneration = rebalancingReq.Generation
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *startedCond)
		if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to update rebalancing request status", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return ctrl.Result{}, err
		}
	}

	// Prepare the migrations to be performed.
	migrations := rebalancingReq.Status.Migrations
	bundles := make([]rebalancingProcessingBundle, len(migrations))
	for idx := range migrations {
		bundles[idx] = rebalancingProcessingBundle{
			migration: &migrations[idx],
		}
	}

	// Set up the work coordinator.
	workerCnt := int(*rebalancingReq.Spec.MaxConcurrency)
	parallelizer := parallelizer.NewParallelizer(workerCnt)

	childCtx, childCancel := context.WithCancel(ctx)
	// For the rollback process, the controller does not keep track of failures, and the process
	// will not be cancelled upon failures, regardless of their number.
	defer childCancel()

	// This variable is set here so that the doWork function can capture it and call the client.
	innerClient := r.Client
	doWork := func(idx int) {
		bundle := &bundles[idx]
		bundle.assignedWorkerIdx = idx

		// Return the last known result if the bundle has already been processed.
		rolledBackCond := meta.FindStatusCondition(bundle.migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack)
		if condition.IsConditionStatusTrue(rolledBackCond, rebalancingReq.Generation) {
			if rolledBackCond.Reason == placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSkipped {
				bundle.res = WorkerResultSkipped
				klog.V(2).InfoS(
					"A migration attempt has been skipped",
					"workerIdx", bundle.assignedWorkerIdx,
					"clusterRebalancingRequest", klog.KObj(rebalancingReq))
			} else {
				bundle.res = WorkerResultCompleted
				klog.V(2).InfoS(
					"A migration attempt has been rolled back successfully",
					"workerIdx", bundle.assignedWorkerIdx,
					"clusterRebalancingRequest", klog.KObj(rebalancingReq))
			}
			return
		}
		if condition.IsConditionStatusFalse(rolledBackCond, rebalancingReq.Generation) {
			bundle.res = WorkerResultFailed
			klog.V(2).InfoS(
				"A migration attempt has been rolled back but the rollback has failed",
				"workerIdx", bundle.assignedWorkerIdx,
				"clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return
		}

		migrateUntil(childCtx, innerClient, rebalancingReq, bundle, true)
		klog.V(2).InfoS(
			"A migration attempt has been rolled back",
			"result", bundle.res,
			"workerIdx", bundle.assignedWorkerIdx,
			"rebalancingRequest", klog.KObj(rebalancingReq))
	}

	parallelizer.ParallelizeUntil(childCtx, len(bundles), doWork, "RollingBack")

	// Based on the processing results, refresh the rebalacing request status.
	completionCnt := 0
	failureCnt := 0
	for idx := range bundles {
		bundle := &bundles[idx]
		rolledBackCond := meta.FindStatusCondition(bundle.migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack)

		switch bundle.res {
		case WorkerResultCompleted:
			completionCnt++
			if rolledBackCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptRolledBackCondReasonSucceeded,
					Message:            "The migration attempt has been rolled back successfully",
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		case WorkerResultFailed:
			failureCnt++
			if rolledBackCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack,
					Status:             metav1.ConditionFalse,
					Reason:             placementv1beta1.RebalancingMigrationAttemptRolledBackCondReasonExpired,
					Message:            fmt.Sprintf("The migration attempt has been rolled back but the roll back has failed: %v", bundle.lastKnownErr),
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		case WorkerResultSkipped:
			completionCnt++
			if rolledBackCond == nil {
				meta.SetStatusCondition(&bundle.migration.Conditions, metav1.Condition{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptRolledBackCondReasonSkipped,
					Message:            fmt.Sprintf("The migration attempt has been skipped during the roll back process: %v", bundle.lastKnownErr),
					ObservedGeneration: rebalancingReq.Generation,
				})
			}
		default:
			// The bundle might not have been processed yet. No condition to populate.
		}
	}
	rebalancingReq.Status.Migrations = migrations

	var res RebalacingAttemptResult
	switch {
	case completionCnt == len(bundles):
		// All bundles have been rolled back successfully.
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
			Status:             metav1.ConditionTrue,
			Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonSucceeded,
			Message:            "The rebalancing request has been rolled back successfully",
			ObservedGeneration: rebalancingReq.Generation,
		})
		res = RebalancingAttemptResultCompleted
	case completionCnt+failureCnt == len(bundles) && failureCnt > 0:
		// Some of the rollback attempts have failed.
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
			Status:             metav1.ConditionFalse,
			Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonFailed,
			Message:            fmt.Sprintf("The rebalancing request has been rolled back but some of the rollback attempts have failed; number of failed rollback attempts: %d", failureCnt),
			ObservedGeneration: rebalancingReq.Generation,
		})
		res = RebalancingAttemptResultFailed
	default:
		// Some of the rollback attempts are still in progress.
		//
		// Leave the request as it is and rely on the next reconciliation to refresh the status.
		// Normally this should not occur.
		meta.SetStatusCondition(&rebalancingReq.Status.Conditions, metav1.Condition{
			Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
			Status:             metav1.ConditionFalse,
			Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonInProgress,
			Message:            "Rollback is still in progress",
			ObservedGeneration: rebalancingReq.Generation,
		})
		res = RebalancingAttemptResultInProgress
	}

	// Write back the status update to the API server.
	if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to update rebalancing request status after processing the roll back", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
		return ctrl.Result{}, err
	}

	if res == RebalancingAttemptResultInProgress {
		klog.V(2).InfoS(
			"The rollback request is still in progress; requeuing for another round of processing",
			"clusterRebalancingRequest", klog.KObj(rebalancingReq),
			"currentRollBackCompletionCount", completionCnt,
			"currentRollBackFailureCount", failureCnt)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}
	return ctrl.Result{}, nil
}
