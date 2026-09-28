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
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

type allStagesProcessingResult string

const (
	allStagesProcessingResultSucceeded  allStagesProcessingResult = "Succeeded"
	allStagesProcessingResultFailed     allStagesProcessingResult = "Failed"
	allStagesProcessingResultInProgress allStagesProcessingResult = "InProgress"
	allStagesProcessingResultErred      allStagesProcessingResult = "Erred"
)

type stageProcessingResult string

const (
	stageProcessingResultSucceeded  stageProcessingResult = "Succeeded"
	stageProcessingResultFailed     stageProcessingResult = "Failed"
	stageProcessingResultInProgress stageProcessingResult = "InProgress"
	stageProcessingResultErred      stageProcessingResult = "Erred"
)

type clusterProcessingResult string

const (
	clusterProcessingResultSucceeded clusterProcessingResult = "Succeeded"
	clusterProcessingResultFailed    clusterProcessingResult = "Failed"
	clusterProcessingResultErred     clusterProcessingResult = "Erred"
)

// bindingAvailabilityPollInterval is how often refreshOneCluster re-checks a placement binding for the
// Synchronized and Available conditions after triggering a rollout on it.
const bindingAvailabilityPollInterval = 5 * time.Second

func (r *Reconciler) process(ctx context.Context, stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor) (
	allStagesProcessingRes allStagesProcessingResult, requeueAfter *time.Duration, err error) {
	// Do a sanity check.
	completedCond := meta.FindStatusCondition(stagedUpdateRun.GetStatus().Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeCompleted)
	switch {
	case completedCond == nil || completedCond.Status != metav1.ConditionTrue:
		// The staged update run has not yet completed; continue processing.
	case completedCond.Reason == rolloutv1alpha1.StagedUpdateRunCompletedCondReasonSucceeded:
		// The staged update run has completed successfully; no further processing is needed.
		return allStagesProcessingResultSucceeded, nil, nil
	case completedCond.Reason == rolloutv1alpha1.StagedUpdateRunCompletedCondReasonFailed:
		// The staged update run has completed with failure; no further processing is needed.
		return allStagesProcessingResultFailed, nil, nil
	default:
		// The staged update run has completed with an unknown reason; report an unexpected error. Normally this
		// should never occur.
		return allStagesProcessingResultErred, nil, errors.NewUnexpectedError(nil, "staged update run completed with unknown reason", "observedCompletionReason", completedCond.Reason)
	}

	// Check if the staged update run has been marked as started; if not, add the Started condition.
	if startedCond := meta.FindStatusCondition(stagedUpdateRun.GetStatus().Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeStarted); startedCond == nil {
		stagedUpdateRunStatus := stagedUpdateRun.GetStatus()
		meta.SetStatusCondition(&stagedUpdateRunStatus.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunCondTypeStarted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunStartedCondReasonUpdateStarted,
			Message:            "The staged update run has started",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
	}

	// Start a child context.
	//
	// This context is set to time out after a specific period of time, and terminates the processing of the staged update
	// run, in order to:
	//
	// a) avoid one staged update run monopolizing the controller's processing capacity for too long; and
	// b) allow users to see timely updates on the staged update run's progress.
	childCtx, childCancel := context.WithTimeout(ctx, r.RefreshPeriod)
	defer childCancel()

	// Prepare the failure policy.
	failurePolicy := &rolloutv1alpha1.StagedUpdateRunFailurePolicy{
		MaxAllowedClusterFailures:    1,
		MaxWaitTimePerClusterMinutes: 30,
	}
	stagedUpdateRunSpec := stagedUpdateRun.GetSpec()
	if stagedUpdateRunSpec.FailurePolicy != nil {
		failurePolicy.MaxAllowedClusterFailures = int32(stagedUpdateRunSpec.FailurePolicy.MaxAllowedClusterFailures)
		failurePolicy.MaxWaitTimePerClusterMinutes = stagedUpdateRunSpec.FailurePolicy.MaxWaitTimePerClusterMinutes
	}

	// Scan the stages and process them one by one.
	stages := stagedUpdateRun.GetStatus().Stages
	for idx := range stages {
		stage := &stages[idx]

		stageProcessingRes, requeueAfter, err := r.processOneStage(childCtx, stagedUpdateRun, stage, failurePolicy)
		if err != nil {
			if childCtx.Err() == context.DeadlineExceeded {
				// The per-run processing time budget has been exhausted; this is expected (not a real
				// failure), so retry in the next reconciliation loop instead of reporting an error.
				return allStagesProcessingResultInProgress, requeueAfter, nil
			}
			return allStagesProcessingResultErred, requeueAfter, err
		}

		switch stageProcessingRes {
		case stageProcessingResultSucceeded:
			// The stage has been successfully processed; move on to the next stage.
		case stageProcessingResultFailed:
			// The stage has failed; stop processing further stages.
			stagedUpdateRunStatus := stagedUpdateRun.GetStatus()
			meta.SetStatusCondition(&stagedUpdateRunStatus.Conditions, metav1.Condition{
				Type:               rolloutv1alpha1.StagedUpdateRunCondTypeCompleted,
				Status:             metav1.ConditionTrue,
				Reason:             rolloutv1alpha1.StagedUpdateRunCompletedCondReasonFailed,
				Message:            fmt.Sprintf("The staged update run has failed (stage %q has failed)", stage.StageName),
				ObservedGeneration: stagedUpdateRun.GetGeneration(),
			})
			return allStagesProcessingResultFailed, requeueAfter, nil
		case stageProcessingResultInProgress:
			// The stage is still being processed in progress; retry in the next reconciliation loop.
			return allStagesProcessingResultInProgress, requeueAfter, nil
		default:
			// The stage failed processing for an unknown reason; report an unexpected error. Normally this
			// should never occur.
			return allStagesProcessingResultErred, nil, errors.NewUnexpectedError(nil, "staged processing yields an unexpected result",
				"observedProcessingResult", stageProcessingRes)
		}
	}

	// All stages have completed successfully; mark the staged update run as completed successfully.
	stagedUpdateRunStatus := stagedUpdateRun.GetStatus()
	meta.SetStatusCondition(&stagedUpdateRunStatus.Conditions, metav1.Condition{
		Type:               rolloutv1alpha1.StagedUpdateRunCondTypeCompleted,
		Status:             metav1.ConditionTrue,
		Reason:             rolloutv1alpha1.StagedUpdateRunCompletedCondReasonSucceeded,
		Message:            "The staged update run has completed successfully",
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})
	return allStagesProcessingResultSucceeded, nil, nil
}

func (r *Reconciler) processOneStage(
	ctx context.Context,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	stage *rolloutv1alpha1.PerStageStatus,
	failurePolicy *rolloutv1alpha1.StagedUpdateRunFailurePolicy,
) (result stageProcessingResult, requeueAfter *time.Duration, err error) {
	// Skip the stage if it has already been completed.
	stageCompletedCond := meta.FindStatusCondition(stage.Conditions, rolloutv1alpha1.StagedUpdateRunPerStageCondTypeCompleted)
	switch {
	case stageCompletedCond == nil || stageCompletedCond.Status != metav1.ConditionTrue:
		// Move on to process this stage.
	case stageCompletedCond.Reason == rolloutv1alpha1.StagedUpdateRunPerStageCompletedCondReasonSucceeded:
		// The stage has already been completed successfully, so skip further processing.
		return stageProcessingResultSucceeded, nil, nil
	case stageCompletedCond.Reason == rolloutv1alpha1.StagedUpdateRunPerStageCompletedCondReasonFailed:
		// The stage has already been completed and failed, so skip further processing.
		return stageProcessingResultFailed, nil, nil
	}

	// Set the stage started timestamp.
	if stage.StartedTimestamp == nil {
		now := metav1.Now()
		stage.StartedTimestamp = &now
	}

	// Mark the stage as started.
	if startedCond := meta.FindStatusCondition(stage.Conditions, rolloutv1alpha1.StagedUpdateRunPerStageCondTypeStarted); startedCond == nil {
		meta.SetStatusCondition(&stage.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunPerStageCondTypeStarted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunPerStageStartedCondReasonUpdateStarted,
			Message:            "Updating has started in the stage",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
	}

	// Execute the before stage tasks.
	beforeStageTasksAllCleared := true
	beforeStageTasksRequeueAfter := time.Duration(0)
	for idx := range stage.BeforeStageTasks {
		task := &stage.BeforeStageTasks[idx]
		shouldContinue, requeueAfter, err := r.executeOneStageTask(ctx, stagedUpdateRun, stage, task)
		if err != nil {
			return stageProcessingResultErred, requeueAfter, errors.Wraps(err, "failed to complete before stage task", "task", task.Type)
		}
		if !shouldContinue {
			beforeStageTasksAllCleared = false
			if requeueAfter != nil && *requeueAfter > beforeStageTasksRequeueAfter {
				beforeStageTasksRequeueAfter = *requeueAfter
			}
		}
	}
	if !beforeStageTasksAllCleared {
		return stageProcessingResultInProgress, &beforeStageTasksRequeueAfter, nil
	}

	// Process updates to all clusters in the stage per given concurrency setting.
	allowedClusterProcessingConcurrency := 1
	if stage.MaxConcurrency != nil && *stage.MaxConcurrency > 0 {
		allowedClusterProcessingConcurrency = int(*stage.MaxConcurrency)
	}
	if allowedClusterProcessingConcurrency > maxAllowedClusterProcessingConcurrency {
		allowedClusterProcessingConcurrency = maxAllowedClusterProcessingConcurrency
	}

	maxAllowedClusterFailures := failurePolicy.MaxAllowedClusterFailures

	var succeededClusterCnt, failedClusterCnt, erredClusterCnt = atomic.Int32{}, atomic.Int32{}, atomic.Int32{}

	// Spin up a child context.
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()

	wg := &sync.WaitGroup{}
	nextClusterIdx := atomic.Int32{}
	clusterCnt := int32(len(stage.Clusters))
	errs := make([]error, clusterCnt)
	for workerIdx := 0; workerIdx < allowedClusterProcessingConcurrency; workerIdx++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			clusterIdx := nextClusterIdx.Add(1) - 1
			klog.V(2).InfoS("Processing cluster in the stage", "workerIdx", workerIdx,
				"clusterIdx", clusterIdx, "stage", stage.StageName, "stagedUpdateRun", klog.KObj(stagedUpdateRun))
			if clusterIdx >= clusterCnt {
				klog.V(2).InfoS("No more clusters to process in the stage", "workerIdx", workerIdx,
					"stage", stage.StageName, "stagedUpdateRun", klog.KObj(stagedUpdateRun))
				return
			}

			cluster := &stage.Clusters[clusterIdx]
			clusterProcessingRes, err := r.refreshOneCluster(childCtx, cluster, stagedUpdateRun, failurePolicy)
			if err != nil {
				klog.ErrorS(err, "Failed to update a cluster in the stage",
					append(errors.Args(err), "stagedUpdateRun", klog.KObj(stagedUpdateRun), "stage", stage.StageName, "cluster", cluster.ClusterName)...)
				errs[clusterIdx] = err
			}

			switch clusterProcessingRes {
			case clusterProcessingResultSucceeded:
				// The processing has completed successfully; the worker can move on to processing the next cluster.
				succeededClusterCnt.Add(1)
			case clusterProcessingResultFailed:
				updated := failedClusterCnt.Add(1)
				if updated > maxAllowedClusterFailures {
					// The failure threshold has been exceeded. Cancel all ongoing cluster processing attempts.
					childCancel()
				}
				// The worker will stop processing clusters.
				return
			case clusterProcessingResultErred:
				// An error occurred while processing the cluster. It does not count as a cluster failure, however,
				// the worker will stop processing clusters.
				erredClusterCnt.Add(1)
				return
			}
		}()
	}
	wg.Wait()

	// Tally the results.
	klog.InfoS("Processed all clusters in the stage",
		"stagedUpdateRun", klog.KObj(stagedUpdateRun),
		"stage", stage.StageName,
		"succeededClusterCnt", succeededClusterCnt.Load(),
		"failedClusterCnt", failedClusterCnt.Load(),
		"erredClusterCnt", erredClusterCnt.Load(),
	)
	switch {
	case succeededClusterCnt.Load() == int32(len(stage.Clusters)):
		// All clusters in the stage have succeeded. Move on to the execution of the after stage tasks.
	case failedClusterCnt.Load() > maxAllowedClusterFailures:
		// The failure threshold has been exceeded. The stage has failed.
		meta.SetStatusCondition(&stage.Conditions, metav1.Condition{
			Type:   rolloutv1alpha1.StagedUpdateRunPerStageCondTypeCompleted,
			Status: metav1.ConditionTrue,
			Reason: rolloutv1alpha1.StagedUpdateRunPerStageCompletedCondReasonFailed,
			Message: fmt.Sprintf("The stage has failed (%d out of %d clusters failed to update, exceeding the maximum allowed failures of %d)",
				failedClusterCnt.Load(), len(stage.Clusters), maxAllowedClusterFailures),
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
		return stageProcessingResultFailed, nil, nil
	default:
		// Not all clusters have been successfully updated, but the failure threshold has not been exceeded either.
		//
		// Requeue and continue processing in the next reconciliation loop.
		if aggregatedErrs := utilerrors.NewAggregate(errs); aggregatedErrs != nil {
			return stageProcessingResultErred, nil, errors.NewTransientError(nil, "failed to update some clusters", "errs", aggregatedErrs)
		}
		return stageProcessingResultInProgress, nil, nil
	}

	// Execute the after stage tasks.
	afterStageTasksAllCleared := true
	afterStageTasksRequeueAfter := time.Duration(0)
	for idx := range stage.AfterStageTasks {
		task := &stage.AfterStageTasks[idx]
		shouldContinue, requeueAfter, err := r.executeOneStageTask(ctx, stagedUpdateRun, stage, task)
		if err != nil {
			return stageProcessingResultErred, requeueAfter, errors.Wraps(err, "failed to complete after stage task", "task", task.Type)
		}
		if !shouldContinue {
			afterStageTasksAllCleared = false
			if requeueAfter != nil && *requeueAfter > afterStageTasksRequeueAfter {
				afterStageTasksRequeueAfter = *requeueAfter
			}
		}
	}
	if !afterStageTasksAllCleared {
		return stageProcessingResultInProgress, &afterStageTasksRequeueAfter, nil
	}

	// Mark the stage as completed successfully.
	meta.SetStatusCondition(&stage.Conditions, metav1.Condition{
		Type:               rolloutv1alpha1.StagedUpdateRunPerStageCondTypeCompleted,
		Status:             metav1.ConditionTrue,
		Reason:             rolloutv1alpha1.StagedUpdateRunPerStageCompletedCondReasonSucceeded,
		Message:            "The stage has completed successfully",
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})
	return stageProcessingResultSucceeded, nil, nil
}

func (r *Reconciler) executeOneStageTask(
	ctx context.Context,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	stage *rolloutv1alpha1.PerStageStatus,
	task *rolloutv1alpha1.PerStageTaskStatus,
) (shouldContinue bool, requeueAfter *time.Duration, err error) {
	switch task.Type {
	case rolloutv1alpha1.StageTaskTypeTimedWait:
		return r.executeTimedWaitStageTask(stagedUpdateRun, task)
	case rolloutv1alpha1.StageTaskTypeApproval:
		return r.executeApprovalStageTask(ctx, stagedUpdateRun, stage, task)
	default:
		// Normally this will never happen.
		return false, nil, errors.NewUnexpectedError(nil, "an unknown stage task type was found", "taskType", task.Type)
	}
}

func (r *Reconciler) executeApprovalStageTask(
	ctx context.Context,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	stage *rolloutv1alpha1.PerStageStatus,
	task *rolloutv1alpha1.PerStageTaskStatus,
) (shouldContinue bool, requeueAfter *time.Duration, err error) {
	// Skip the task if it has already been completed (the approval has been completed).
	approvedCond := meta.FindStatusCondition(task.Conditions, rolloutv1alpha1.StagedUpdateRunTaskCondTypeApprovalRequestApproved)
	if approvedCond != nil && approvedCond.Status == metav1.ConditionTrue {
		return true, nil, nil
	}

	// Check if an approval request has been created. If not, create one, and add the ApprovalRequestCreated condition.
	requestCreatedCond := meta.FindStatusCondition(task.Conditions, rolloutv1alpha1.StagedUpdateRunTaskCondTypeApprovalRequestCreated)
	if requestCreatedCond == nil {
		// Create an approval request.
		approvalRequestName := formatBeforeStageApprovalRequestName(stagedUpdateRun.GetName(), stage.StageName)
		// Set the approval request name in the task status.
		task.ApprovalRequestName = approvalRequestName

		var approvalRequest rolloutv1alpha1.ApprovalRequestAccessor
		ownerGVK := schema.GroupVersionKind{}
		if stagedUpdateRun.GetNamespace() == "" {
			ownerGVK = rolloutv1alpha1.GroupVersion.WithKind("ClusterStagedUpdateRun")
			approvalRequest = &rolloutv1alpha1.ClusterApprovalRequest{}
		} else {
			ownerGVK = rolloutv1alpha1.GroupVersion.WithKind("StagedUpdateRun")
			approvalRequest = &rolloutv1alpha1.ApprovalRequest{}
		}

		approvalRequest.SetName(approvalRequestName)
		approvalRequest.SetNamespace(stagedUpdateRun.GetNamespace())
		approvalRequest.SetOwnerReferences([]metav1.OwnerReference{
			*metav1.NewControllerRef(stagedUpdateRun, ownerGVK),
		})
		approvalRequest.SetSpec(rolloutv1alpha1.ApprovalRequestSpec{
			StagedUpdateRunName: stagedUpdateRun.GetName(),
			StageName:           stage.StageName,
		})

		if err := r.HubClient.Create(ctx, approvalRequest); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, nil, errors.NewAPIServerError(err, "failed to create approval request", false,
				"approvalRequest", klog.KObj(approvalRequest))
		}

		meta.SetStatusCondition(&task.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunTaskCondTypeApprovalRequestCreated,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunTaskApprovalRequestCreatedCondReasonCreated,
			Message:            "The approval request has been created",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})

		// No need to wait for the approval to be granted; the reconciliation loop will be triggered again
		// when the approval request is approved.
		return false, nil, nil
	}

	// The approval request has been created. Check if it has been approved. If not, wait for the next reconciliation attempt.
	approvalRequestName := task.ApprovalRequestName
	if approvalRequestName == "" {
		// Do a sanity check; normally this branch will never run.
		return false, nil, errors.NewUnexpectedError(nil, "approval request name is empty in the task status")
	}

	var approvalRequest rolloutv1alpha1.ApprovalRequestAccessor
	if stagedUpdateRun.GetNamespace() == "" {
		approvalRequest = &rolloutv1alpha1.ClusterApprovalRequest{}
		if err := r.HubClient.Get(ctx, types.NamespacedName{Name: approvalRequestName}, approvalRequest); err != nil {
			return false, nil, errors.NewAPIServerError(err, "failed to get cluster approval request", true,
				"approvalRequest", klog.KRef("", approvalRequestName))
		}
	} else {
		approvalRequest = &rolloutv1alpha1.ApprovalRequest{}
		if err := r.HubClient.Get(ctx, types.NamespacedName{Namespace: stagedUpdateRun.GetNamespace(), Name: approvalRequestName}, approvalRequest); err != nil {
			return false, nil, errors.NewAPIServerError(err, "failed to get approval request", true,
				"approvalRequest", klog.KRef(stagedUpdateRun.GetNamespace(), approvalRequestName))
		}
	}

	approvalRequestApprovedCond := meta.FindStatusCondition(approvalRequest.GetStatus().Conditions, rolloutv1alpha1.ApprovalRequestCondTypeApproved)
	if approvalRequestApprovedCond == nil || approvalRequestApprovedCond.Status != metav1.ConditionTrue {
		// The approval request has not been approved yet.
		return false, nil, nil
	}

	meta.SetStatusCondition(&task.Conditions, metav1.Condition{
		Type:               rolloutv1alpha1.StagedUpdateRunTaskCondTypeApprovalRequestApproved,
		Status:             metav1.ConditionTrue,
		Reason:             rolloutv1alpha1.StagedUpdateRunTaskApprovalRequestApprovedCondReasonApproved,
		Message:            "The approval request has been approved",
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})

	// The approval request has been approved.
	return true, nil, nil
}

func (r *Reconciler) executeTimedWaitStageTask(
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	task *rolloutv1alpha1.PerStageTaskStatus,
) (shouldContinue bool, requeueAfter *time.Duration, err error) {
	// Skip the task if it has already been completed (the wait time has elapsed).
	waitTimeElapsedCond := meta.FindStatusCondition(task.Conditions, rolloutv1alpha1.StagedUpdateRunTaskCondTypeWaitTimeElapsed)
	if waitTimeElapsedCond != nil && waitTimeElapsedCond.Status == metav1.ConditionTrue {
		return true, nil, nil
	}

	// Execute a TimedWait task.
	timedWaitStartedCond := meta.FindStatusCondition(task.Conditions, rolloutv1alpha1.StagedUpdateRunTaskCondTypeTimedWaitStarted)
	if timedWaitStartedCond == nil {
		// The task is being run for the first time. Add the TimedWaitStarted condition to track the start time.
		timedWaitStartedCond = &metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunTaskCondTypeTimedWaitStarted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunTaskTimedWaitStartedCondReasonTimerStarted,
			Message:            "The TimedWait task has started",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		}
		meta.SetStatusCondition(&task.Conditions, *timedWaitStartedCond)
	}

	// Calculate how long the task has been waiting based on the last transition time of the TimedWaitStarted condition.
	elapsed := time.Since(timedWaitStartedCond.LastTransitionTime.Time)
	remaining := task.WaitTime.Duration - elapsed
	if remaining > 0 {
		// The wait time has not yet elapsed. Return the remaining wait time.
		return false, &remaining, nil
	}

	// The wait time has elapsed. Mark the task as completed.
	meta.SetStatusCondition(&task.Conditions, metav1.Condition{
		Type:               rolloutv1alpha1.StagedUpdateRunTaskCondTypeWaitTimeElapsed,
		Status:             metav1.ConditionTrue,
		Reason:             rolloutv1alpha1.StagedUpdateRunTaskWaitTimeElapsedCondReasonTimerElapsed,
		Message:            "The TimedWait task is completed (the wait time has elapsed)",
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})
	return true, nil, nil
}

func (r *Reconciler) refreshOneCluster(
	ctx context.Context,
	cluster *rolloutv1alpha1.PerClusterStatus,
	stagedUpdateRun rolloutv1alpha1.StagedUpdateRunAccessor,
	failurePolicy *rolloutv1alpha1.StagedUpdateRunFailurePolicy,
) (clusterProcessingResult, error) {
	// Check if an update attempt has been completed before for the cluster.
	completedCond := meta.FindStatusCondition(cluster.Conditions, rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeCompleted)
	switch {
	case completedCond == nil || completedCond.Status != metav1.ConditionTrue:
		// There is no update attempt before or it has not been completed yet. Continue with the update process.
	case completedCond.Reason == rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonSucceeded:
		// The update attempt has been completed successfully before.
		return clusterProcessingResultSucceeded, nil
	case completedCond.Reason == rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonFailed:
		// The update attempt has been failed before.
		return clusterProcessingResultFailed, nil
	default:
		// Found an unexpected completion reason. Consider this as an unexpected error.
		return clusterProcessingResultErred, errors.NewUnexpectedError(nil, "failed to determine the update status for a cluster", "unexpectedCompletionReason", completedCond.Reason)
	}

	// Check if an update attempt has started already for the cluster.
	maxWaitTime := time.Duration(failurePolicy.MaxWaitTimePerClusterMinutes) * time.Minute
	markClusterFailedDueToTimeout := func() {
		meta.SetStatusCondition(&cluster.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonFailed,
			Message:            fmt.Sprintf("The update on the cluster has failed (it has not completed within the wait time limit of %s)", maxWaitTime),
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
	}
	startedCond := meta.FindStatusCondition(cluster.Conditions, rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeStarted)
	if startedCond != nil {
		// Check if the cluster has been updating for too long; if so, fail it rather than waiting forever.
		if elapsed := time.Since(startedCond.LastTransitionTime.Time); elapsed > maxWaitTime {
			markClusterFailedDueToTimeout()
			return clusterProcessingResultFailed, nil
		}
	}

	// Retrieve the placement binding.
	var binding placementv1alpha1.PlacementBindingAccessor
	bindingKey := types.NamespacedName{
		Namespace: stagedUpdateRun.GetNamespace(),
		Name:      cluster.PlacementBindingName,
	}
	if bindingKey.Namespace == "" {
		binding = &placementv1alpha1.ClusterPlacementBinding{}
	} else {
		binding = &placementv1alpha1.PlacementBinding{}
	}
	if err := r.HubClient.Get(ctx, bindingKey, binding); err != nil {
		return clusterProcessingResultErred, errors.NewAPIServerError(err, "failed to get the placement binding", true,
			"placementBinding", klog.KRef(bindingKey.Namespace, bindingKey.Name))
	}

	// Mark the cluster as being updated.
	if startedCond == nil {
		meta.SetStatusCondition(&cluster.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeStarted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunPerClusterStartedCondReasonUpdateStarted,
			Message:            "The update on the cluster has started",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
		// Read the condition back so that its (server-assigned) LastTransitionTime is available for the
		// wait-time check in the polling loop below.
		startedCond = meta.FindStatusCondition(cluster.Conditions, rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeStarted)
	}

	// Verify that the binding has not yet been marked for deletion and the cluster name matches, as a sanity check.
	//
	// Normally these branches should never run: the binding is only removed, and its target cluster only
	// changed, by re-running initialization, which also refreshes this very status.
	if !binding.GetDeletionTimestamp().IsZero() || binding.GetSpec().ClusterName != cluster.ClusterName {
		// Mark the cluster as failed to update.
		meta.SetStatusCondition(&cluster.Conditions, metav1.Condition{
			Type:               rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonFailed,
			Message:            "The update on the cluster has failed",
			ObservedGeneration: stagedUpdateRun.GetGeneration(),
		})
		return clusterProcessingResultFailed, errors.NewUnexpectedError(nil, "the placement binding has been marked for deletion or its cluster name does not match the tracked cluster name",
			"placementBinding", klog.KObj(binding),
			"placementBindingDeletionTimestamp", binding.GetDeletionTimestamp(),
			"expectedClusterName", cluster.ClusterName, "observedClusterName", binding.GetSpec().ClusterName)
	}

	// Update the binding to use the expected resource snapshot name.
	//
	// Note (chenyu1): for simplicity reasons, here the control loop does not implement the rollback support
	// properly (i.e., no forward only constraint), and concurrent staged update runs are implemented in a
	// first writer wins manner.
	wantResourceSnapshotName := stagedUpdateRun.GetStatus().ResourceSnapshotNameToRollout
	if binding.GetSpec().ResourceSnapshotName != wantResourceSnapshotName {
		binding.GetSpec().ResourceSnapshotName = wantResourceSnapshotName
		if err := r.HubClient.Update(ctx, binding); err != nil {
			return clusterProcessingResultErred, errors.NewAPIServerError(err, "failed to update the placement binding with the resource snapshot to roll out", false,
				"placementBinding", klog.KObj(binding), "resourceSnapshotName", wantResourceSnapshotName)
		}
	}

	// Wait for the placement binding to report that the rollout has been synchronized to, and is available
	// on, the target cluster, polling periodically until either that happens or the context is cancelled
	// (e.g., the per-run processing time budget has been exhausted, in which case the caller will pick up
	// where this left off on the next reconciliation pass).
	ticker := time.NewTicker(bindingAvailabilityPollInterval)
	defer ticker.Stop()
	for {
		// Check also on every loop run that the max wait time is still respected; the cluster might take
		// too long to become synchronized and available even though it did not time out earlier.
		if elapsed := time.Since(startedCond.LastTransitionTime.Time); elapsed > maxWaitTime {
			markClusterFailedDueToTimeout()
			return clusterProcessingResultFailed, nil
		}

		// Re-fetch the binding at the start of every iteration so that the check below always runs
		// against the latest observed state; without this, the loop would keep re-checking the same
		// (initial) snapshot of the binding and never notice it becoming synchronized and available.
		if err := r.HubClient.Get(ctx, bindingKey, binding); err != nil {
			return clusterProcessingResultErred, errors.NewAPIServerError(err, "failed to get the placement binding", true,
				"placementBinding", klog.KRef(bindingKey.Namespace, bindingKey.Name))
		}

		bindingStatus := binding.GetStatus()
		synchronizedCond := meta.FindStatusCondition(bindingStatus.Conditions, placementv1alpha1.PlacementBindingCondTypeSynchronized)
		availableCond := meta.FindStatusCondition(bindingStatus.Conditions, placementv1alpha1.PlacementBindingCondTypeAvailable)
		if synchronizedCond != nil && synchronizedCond.Status == metav1.ConditionTrue &&
			availableCond != nil && availableCond.Status == metav1.ConditionTrue {
			// The rollout has been synchronized to, and is available on, the target cluster.
			break
		}

		select {
		case <-ctx.Done():
			return clusterProcessingResultErred, errors.NewTransientError(ctx.Err(),
				"the placement binding has not yet become synchronized and available", "placementBinding", klog.KObj(binding))
		case <-ticker.C:
			// Time to check again; loop back to the top.
		}
	}

	// Mark the cluster as updated successfully.
	meta.SetStatusCondition(&cluster.Conditions, metav1.Condition{
		Type:               rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeCompleted,
		Status:             metav1.ConditionTrue,
		Reason:             rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonSucceeded,
		Message:            "The update on the cluster has completed successfully",
		ObservedGeneration: stagedUpdateRun.GetGeneration(),
	})
	return clusterProcessingResultSucceeded, nil
}
