/*
Copyright 2025 The KubeFleet Authors.

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

package workapplier

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/metrics"
)

// Note (chenyu1):
// the metrics for the work applier are added the assumptions that:
// a) the applier should provide low-cardinality metrics only to keep the memory footprint low,
//    as there might be a high number (up to 10K) of works/manifests to process in a member cluster; and
// b) keep the overhead of metrics collection low, esp. considering that the applier runs in
//    parallel (5 concurrent reconciliations by default) and is on the critical path of Fleet workflows.
//
// TO-DO (chenyu1): evaluate if there is any need for high-cardinality options and/or stateful
// metric collection for better observability.

// The label values for the work processing request counter metric.
const (
	// The values for the apply status label.
	workApplyStatusApplied = "applied"
	workApplyStatusFailed  = "failed"
	workApplyStatusUnknown = "unknown"
	workApplyStatusSkipped = "skipped"

	// The values for the availability status label.
	workAvailabilityStatusAvailable   = "available"
	workAvailabilityStatusUnavailable = "unavailable"
	workAvailabilityStatusUnknown     = "unknown"
	workAvailabilityStatusSkipped     = "skipped"

	// The values for the diff reported status label.
	workDiffReportedStatusReported    = "reported"
	workDiffReportedStatusNotReported = "failed"
	workDiffReportedStatusUnknown     = "unknown"
	workDiffReportedStatusSkipped     = "skipped"
)

// Some of the label values for the manifest processing request counter metric.
const (
	// The values for the manifest apply status label.
	manifestApplyStatusUnknown = "unknown"
	manifestApplyStatusSkipped = "skipped"

	// The values for the manifest availability status label.
	manifestAvailabilityStatusSkipped = "skipped"
	manifestAvailabilityStatusUnknown = "unknown"

	// The values for the manifest diff reported status label.
	manifestDiffReportedStatusSkipped = "skipped"
	manifestDiffReportedStatusUnknown = "unknown"

	// The values for the manifest drift detection status label.
	manifestDriftDetectionStatusFound    = "found"
	manifestDriftDetectionStatusNotFound = "not_found"

	// The values for the manifest diff detection status label.
	manifestDiffDetectionStatusFound    = "found"
	manifestDiffDetectionStatusNotFound = "not_found"
)

func trackWorkAndManifestProcessingRequestMetrics(work *fleetv1beta1.Work) {
	// Increment the work processing request counter.

	var workApplyStatus string
	workAppliedCond := meta.FindStatusCondition(work.Status.Conditions, fleetv1beta1.WorkConditionTypeApplied)
	switch {
	case workAppliedCond == nil:
		workApplyStatus = workApplyStatusSkipped
	case workAppliedCond.ObservedGeneration != work.Generation:
		// Normally this should never occur; as the method is called right after the status
		// is refreshed.
		workApplyStatus = workApplyStatusUnknown
	case workAppliedCond.Status == metav1.ConditionTrue:
		workApplyStatus = workApplyStatusApplied
	case workAppliedCond.Status == metav1.ConditionFalse:
		workApplyStatus = workApplyStatusFailed
	default:
		// Normally this should never occur; this branch is added just for completeness reasons.
		workApplyStatus = workApplyStatusUnknown
	}

	var workAvailabilityStatus string
	workAvailableCond := meta.FindStatusCondition(work.Status.Conditions, fleetv1beta1.WorkConditionTypeAvailable)
	switch {
	case workAvailableCond == nil:
		workAvailabilityStatus = workAvailabilityStatusSkipped
	case workAvailableCond.ObservedGeneration != work.Generation:
		// Normally this should never occur; as the method is called right after the status
		// is refreshed.
		workAvailabilityStatus = workAvailabilityStatusUnknown
	case workAvailableCond.Status == metav1.ConditionTrue:
		workAvailabilityStatus = workAvailabilityStatusAvailable
	case workAvailableCond.Status == metav1.ConditionFalse:
		workAvailabilityStatus = workAvailabilityStatusUnavailable
	default:
		// Normally this should never occur; this branch is added just for completeness reasons.
		workAvailabilityStatus = workAvailabilityStatusUnknown
	}

	var workDiffReportedStatus string
	workDiffReportedCond := meta.FindStatusCondition(work.Status.Conditions, fleetv1beta1.WorkConditionTypeDiffReported)
	switch {
	case workDiffReportedCond == nil:
		workDiffReportedStatus = workDiffReportedStatusSkipped
	case workDiffReportedCond.ObservedGeneration != work.Generation:
		// Normally this should never occur; as the method is called right after the status
		// is refreshed.
		workDiffReportedStatus = workDiffReportedStatusUnknown
	case workDiffReportedCond.Status == metav1.ConditionTrue:
		workDiffReportedStatus = workDiffReportedStatusReported
	case workDiffReportedCond.Status == metav1.ConditionFalse:
		workDiffReportedStatus = workDiffReportedStatusNotReported
	default:
		// Normally this should never occur; this branch is added just for completeness reasons.
		workDiffReportedStatus = workDiffReportedStatusUnknown
	}

	metrics.FleetWorkProcessingRequestsTotal.WithLabelValues(
		workApplyStatus,
		workAvailabilityStatus,
		workDiffReportedStatus,
	).Inc()

	// Increment the manifest processing request counter.
	for idx := range work.Status.ManifestConditions {
		manifestCond := &work.Status.ManifestConditions[idx]

		// No generation checks are added here, as the observed generation reported
		// on manifest conditions is the generation of the corresponding object in the
		// member cluster, not the generation of the work object. Plus, as this
		// method is called right after the status is refreshed, all conditions are
		// expected to be up-to-date.

		var manifestApplyStatus string
		manifestAppliedCond := meta.FindStatusCondition(manifestCond.Conditions, fleetv1beta1.WorkConditionTypeApplied)
		switch {
		case manifestAppliedCond == nil:
			manifestApplyStatus = manifestApplyStatusSkipped
		case len(manifestAppliedCond.Reason) == 0:
			// Normally this should never occur; this branch is added just for completeness reasons.
			manifestApplyStatus = manifestApplyStatusUnknown
		default:
			manifestApplyStatus = strings.ToLower(manifestAppliedCond.Reason)
		}

		var manifestAvailabilityStatus string
		manifestAvailableCond := meta.FindStatusCondition(manifestCond.Conditions, fleetv1beta1.WorkConditionTypeAvailable)
		switch {
		case manifestAvailableCond == nil:
			manifestAvailabilityStatus = manifestAvailabilityStatusSkipped
		case len(manifestAvailableCond.Reason) == 0:
			// Normally this should never occur; this branch is added just for completeness reasons.
			manifestAvailabilityStatus = manifestAvailabilityStatusUnknown
		default:
			manifestAvailabilityStatus = strings.ToLower(manifestAvailableCond.Reason)
		}

		var manifestDiffReportedStatus string
		manifestDiffReportedCond := meta.FindStatusCondition(manifestCond.Conditions, fleetv1beta1.WorkConditionTypeDiffReported)
		switch {
		case manifestDiffReportedCond == nil:
			manifestDiffReportedStatus = manifestDiffReportedStatusSkipped
		case len(manifestDiffReportedCond.Reason) == 0:
			// Normally this should never occur; this branch is added just for completeness reasons.
			manifestDiffReportedStatus = manifestDiffReportedStatusUnknown
		default:
			manifestDiffReportedStatus = strings.ToLower(manifestDiffReportedCond.Reason)
		}

		manifestDriftDetectionStatus := manifestDriftDetectionStatusNotFound
		if manifestCond.DriftDetails != nil {
			manifestDriftDetectionStatus = manifestDriftDetectionStatusFound
		}

		manifestDiffDetectionStatus := manifestDiffDetectionStatusNotFound
		if manifestCond.DiffDetails != nil {
			manifestDiffDetectionStatus = manifestDiffDetectionStatusFound
		}

		metrics.FleetManifestProcessingRequestsTotal.WithLabelValues(
			manifestApplyStatus,
			manifestAvailabilityStatus,
			manifestDiffReportedStatus,
			manifestDriftDetectionStatus,
			manifestDiffDetectionStatus,
		).Inc()
	}
}
