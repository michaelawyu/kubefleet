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

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) refreshStatusUponMigrationRunCompletion(
	ctx context.Context,
	req *experimentalv1beta1.WorkloadMigrationRequest,
	bundles []migrationAttemptBundle,
	failurePolicyTiggered bool,
) (migrationRunResult, error) {
	completionCnt := 0
	for idx := range bundles {
		bundle := &bundles[idx]
		migrationAttemptCompletedCond := meta.FindStatusCondition(bundle.attempt.Conditions, experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted)

		switch bundle.res {
		case migrationAttemptResultSucceeded:
			completionCnt++
			if migrationAttemptCompletedCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonSucceeded,
					Message:            "The migration attempt has completed successfully",
					ObservedGeneration: req.Generation,
				})
			}
		case migrationAttemptResultFailed:
			if migrationAttemptCompletedCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
					Status:             metav1.ConditionFalse,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonFailed,
					Message:            fmt.Sprintf("The migration attempt has failed: %v", bundle.lastKnownErr),
					ObservedGeneration: req.Generation,
				})
			}
		case migrationAttemptResultSkipped:
			completionCnt++
			if migrationAttemptCompletedCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonSkipped,
					Message:            "The migration attempt has been skipped",
					ObservedGeneration: req.Generation,
				})
			}
		default:
			// Do nothing.
		}
	}

	var res migrationRunResult
	switch {
	case failurePolicyTiggered:
		// The failure policy has been triggered. Mark the migration request as failed.
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestCompletedCondReasonFailed,
			Message:            "The migration request has failed as the failure policy has been triggered",
			ObservedGeneration: req.Generation,
		})
		res = migrationRunResultFailed
	case completionCnt == len(bundles):
		// All attempts have completed. Mark the migration request as succeeded.
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestCompletedCondReasonSucceeded,
			Message:            "The migration request has completed successfully",
			ObservedGeneration: req.Generation,
		})
		res = migrationRunResultSucceeded
	default:
		// The migration request is still in progress.
		res = migrationRunResultInProgress
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestCompletedCondReasonInProgress,
			Message:            "The migration request is still in progress",
			ObservedGeneration: req.Generation,
		})
	}

	// Write the status update.
	if err := r.HubClient.Status().Update(ctx, req); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to update the status of the workload migration request", true)
		return res, wrappedErr
	}
	return res, nil
}

func (r *Reconciler) refreshStatusUponRollbackRunCompletion(
	ctx context.Context,
	req *experimentalv1beta1.WorkloadMigrationRequest,
	bundles []migrationAttemptBundle,
) (migrationRunResult, error) {
	rolledBackCnt := 0
	failureCnt := 0

	for idx := range bundles {
		bundle := &bundles[idx]
		migrationAttemptRolledBackCond := meta.FindStatusCondition(bundle.attempt.Conditions, experimentalv1beta1.WorkloadMigrationAttemptCondTypeRolledBack)

		switch bundle.res {
		case migrationAttemptResultSucceeded:
			rolledBackCnt++
			if migrationAttemptRolledBackCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeRolledBack,
					Status:             metav1.ConditionTrue,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptRolledBackCondReasonSucceeded,
					Message:            "The rollback attempt has completed successfully",
					ObservedGeneration: req.Generation,
				})
			}
		case migrationAttemptResultFailed:
			failureCnt++
			if migrationAttemptRolledBackCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeRolledBack,
					Status:             metav1.ConditionFalse,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptRolledBackCondReasonFailed,
					Message:            fmt.Sprintf("The rollback attempt has failed: %v", bundle.lastKnownErr),
					ObservedGeneration: req.Generation,
				})
			}
		case migrationAttemptResultSkipped:
			rolledBackCnt++
			if migrationAttemptRolledBackCond == nil {
				meta.SetStatusCondition(&bundle.attempt.Conditions, metav1.Condition{
					Type:               experimentalv1beta1.WorkloadMigrationAttemptCondTypeRolledBack,
					Status:             metav1.ConditionTrue,
					Reason:             experimentalv1beta1.WorkloadMigrationAttemptRolledBackCondReasonSkipped,
					Message:            "The rollback attempt has been skipped",
					ObservedGeneration: req.Generation,
				})
			}
		default:
			// Do nothing.
		}
	}

	var res migrationRunResult
	switch {
	case rolledBackCnt == len(bundles):
		// All attempts have completed the rollback. Mark the migration request as rolled back successfully.
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeRolledBack,
			Status:             metav1.ConditionTrue,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestRolledBackCondReasonSucceeded,
			Message:            "The rollback process has completed successfully",
			ObservedGeneration: req.Generation,
		})
		res = migrationRunResultSucceeded
	case rolledBackCnt+failureCnt == len(bundles) && failureCnt > 0:
		// Some attempts have failed in the rollback. Mark the migration request as rolled back with failures.
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeRolledBack,
			Status:             metav1.ConditionFalse,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestRolledBackCondReasonFailed,
			Message:            "The rollback process has completed with some failures",
			ObservedGeneration: req.Generation,
		})
		res = migrationRunResultFailed
	default:
		// The rollback process is still in progress.
		res = migrationRunResultInProgress
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               experimentalv1beta1.WorkloadMigrationRequestCondTypeRolledBack,
			Status:             metav1.ConditionFalse,
			Reason:             experimentalv1beta1.WorkloadMigrationRequestRolledBackCondReasonInProgress,
			Message:            "The rollback process is still in progress",
			ObservedGeneration: req.Generation,
		})
	}

	// Write the status update.
	if err := r.HubClient.Status().Update(ctx, req); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to update the status of the workload migration request for the rollback process", true)
		return res, wrappedErr
	}
	return res, nil
}
