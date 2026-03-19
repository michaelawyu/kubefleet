package main

import (
	"context"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubefleet-dev/kubefleet/hack/perftest/1000stagedupdateruns/utils"
)

const (
	concurrentWorkerCount      = 15
	longPollingWorkerCount     = 15
	betweenStageCoolDownPeriod = time.Second * 30
	longPollingCoolDownPeriod  = time.Second * 45

	maxConcurrencyPerStage = "50%"
	maxCRPToUpdateCount    = 1000
)

var (
	retryOpsBackoff = wait.Backoff{
		Steps:    4,
		Duration: 4 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}
)

func main() {
	ctx := context.Background()

	// Read the arguments.
	doCleanUp := false
	cleanUpFlag := os.Getenv("CLEANUP")
	if len(cleanUpFlag) != 0 {
		doCleanUp = true
	}

	runner := utils.New(
		concurrentWorkerCount,
		longPollingWorkerCount,
		betweenStageCoolDownPeriod,
		longPollingCoolDownPeriod,
		maxConcurrencyPerStage,
		maxCRPToUpdateCount,
		retryOpsBackoff,
	)

	if doCleanUp {
		runner.CleanUp(ctx)
		return
	}

	// Prepare the staged update run strategy.
	println("Preparing the staged update run strategy...")
	runner.CreateStagedUpdateRunStrategy(ctx)

	// Patch existing resources.
	println("Updating existing resources...")
	runner.UpdateResources(ctx)

	// Cool down.
	println("Cooling down...")
	runner.CoolDown()

	// Create the staged update runs.
	println("Creating staged update runs...")
	runner.CreateStagedUpdateRuns(ctx)

	// Cool down.
	println("Cooling down...")
	runner.CoolDown()

	// Long poll the staged update runs.
	println("Long polling staged update runs...")
	runner.LongPollStagedUpdateRuns(ctx)

	// Track the latency.
	println("Tracking latency...")
	runner.TrackLatency(ctx)

	// Tally the latency quantiles.
	println("Tallying latency quantiles...")
	runner.TallyLatencyQuantiles()

	println("All staged update runs have been completed, exiting.")
}
