package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

func (r *Runner) LongPollStagedUpdateRuns(ctx context.Context) {
	wg := sync.WaitGroup{}

	// Run the polling workers.
	for i := 0; i < r.longPollingWorkerCount; i++ {
		wg.Add(1)

		go func(workerIdx int) {
			defer wg.Done()

			for {
				// Read from the channel.
				var resIdx int
				var readOk bool
				select {
				case resIdx, readOk = <-r.toLongPollStagedUpdateRunsChan:
					if !readOk {
						println(fmt.Sprintf("long polling worker %d exits", workerIdx))
						return
					}
				case <-ctx.Done():
					return
				default:
					if r.longPollingStagedUpdateRunsCount.Load() == r.completedStagedUpdateRunCount.Load() {
						return
					}
					continue
				}

				// Read the staged update run to check if it's completed.
				var stagedUpdateRun placementv1beta1.ClusterStagedUpdateRun
				if err := r.hubClient.Get(ctx, client.ObjectKey{Name: fmt.Sprintf(stagedUpdateRunNameFmt, resIdx)}, &stagedUpdateRun); err != nil {
					println(fmt.Sprintf("long polling worker %d: failed to get staged update run run-%d: %v", workerIdx, resIdx, err))
					// Requeue the run; no need to retry here.
					r.toLongPollStagedUpdateRunsChan <- resIdx
					time.Sleep(r.longPollingCoolDownPeriod)
					continue
				}

				// Check if the staged update run is completed.
				runSucceededCond := meta.FindStatusCondition(stagedUpdateRun.Status.Conditions, string(placementv1beta1.StageUpdatingConditionSucceeded))
				if runSucceededCond == nil || runSucceededCond.Status != metav1.ConditionTrue || runSucceededCond.ObservedGeneration != stagedUpdateRun.Generation {
					println(fmt.Sprintf("long polling worker %d: staged update run run-%d is not completed yet, requeue it", workerIdx, resIdx))
					// Requeue the run.
					r.toLongPollStagedUpdateRunsChan <- resIdx
					time.Sleep(r.longPollingCoolDownPeriod)
					continue
				} else {
					// The staged update run has been completed.
					newCount := r.completedStagedUpdateRunCount.Add(1)
					println(fmt.Sprintf("long polling worker %d: staged update run run-%d is completed, total completed count: %d", workerIdx, resIdx, newCount))

					// Track its latency.
					creationTimestamp := stagedUpdateRun.CreationTimestamp.Time
					completionLatency := runSucceededCond.LastTransitionTime.Sub(creationTimestamp)
					r.toTrackLatencyChan <- latencyTrackAttempt{
						latency: completionLatency,
						resIdx:  resIdx,
					}
				}
			}
		}(i)
	}

	wg.Wait()

	// Do a sanity check report.
	println(fmt.Sprintf("long polling %d staged update runs, with %d completed", r.longPollingStagedUpdateRunsCount.Load(), r.completedStagedUpdateRunCount.Load()))

	close(r.toTrackLatencyChan)
}
