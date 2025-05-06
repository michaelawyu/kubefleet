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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestTrackWorkAndManifestProcessingRequestMetrics(t *testing.T) {
	workMetricMetadata := `
		# HELP fleet_work_processing_requests_total Total number of processing requests of work objects, including retries and periodic checks
		# TYPE fleet_work_processing_requests_total counter
	`

	manifestMetricMetadata := `
		# HELP fleet_manifest_processing_requests_total Total number of processing requests of manifest objects, including retries and periodic checks
		# TYPE fleet_manifest_processing_requests_total counter
	`

	testCases := []struct {
		name                    string
		work                    *placementv1beta1.Work
		wantWorkMetricCount     int
		wantManifestMetricCount int
		wantWorkCounter         string
		wantManifestCounter     string
	}{
		{
			name: "applied and available work, single manifest",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeApplied,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   placementv1beta1.WorkConditionTypeAvailable,
							Status: metav1.ConditionTrue,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Reason: string(ManifestProcessingApplyResultTypeApplied),
									Status: metav1.ConditionTrue,
								},
								{
									Type:   placementv1beta1.WorkConditionTypeAvailable,
									Reason: string(ManifestProcessingAvailabilityResultTypeAvailable),
									Status: metav1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 1,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
			`,
			wantManifestMetricCount: 1,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "work not applied, single manifest",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeApplied,
							Status: metav1.ConditionFalse,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Status: metav1.ConditionFalse,
									Reason: string(ManifestProcessingApplyResultTypeFailedToApply),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 2,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
				fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 1
			`,
			wantManifestMetricCount: 2,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
				fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "work applied but not available, single manifest",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeApplied,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   placementv1beta1.WorkConditionTypeAvailable,
							Status: metav1.ConditionFalse,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Reason: string(ManifestProcessingApplyResultTypeApplied),
									Status: metav1.ConditionTrue,
								},
								{
									Type:   placementv1beta1.WorkConditionTypeAvailable,
									Reason: string(ManifestProcessingAvailabilityResultTypeNotYetAvailable),
									Status: metav1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 3,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
				fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
				fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 1
			`,
			wantManifestMetricCount: 3,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
           		fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "work diff reported, single manifest",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeDiffReported,
							Status: metav1.ConditionTrue,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingReportDiffResultTypeNoDiffFound),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 4,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
           		fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 1
			`,
			wantManifestMetricCount: 4,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "work diff reporting failed, single manifest",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeDiffReported,
							Status: metav1.ConditionFalse,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionFalse,
									Reason: string(ManifestProcessingReportDiffResultTypeFailed),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 5,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="failed"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 1
			`,
			wantManifestMetricCount: 5,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="failed",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "applied failed, found drifts, multiple manifests",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeApplied,
							Status: metav1.ConditionFalse,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Status: metav1.ConditionFalse,
									Reason: string(ManifestProcessingApplyResultTypeFoundDrifts),
								},
							},
							DriftDetails: &placementv1beta1.DriftDetails{},
						},
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingApplyResultTypeApplied),
								},
								{
									Type:   placementv1beta1.WorkConditionTypeAvailable,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingAvailabilityResultTypeAvailable),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 5,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 2
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="failed"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 1
			`,
			wantManifestMetricCount: 6,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="founddrifts",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="failed",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "diff reported, found diffs, multiple manifests",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeDiffReported,
							Status: metav1.ConditionTrue,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingReportDiffResultTypeFoundDiff),
								},
							},
							DiffDetails: &placementv1beta1.DiffDetails{},
						},
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingReportDiffResultTypeNoDiffFound),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 5,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 2
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="failed"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 2
			`,
			wantManifestMetricCount: 7,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="founddrifts",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="found",diff_reporting_status="founddiff",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="failed",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 2
			`,
		},
		// The cases below normally would never occur.
		{
			name: "with unknown status",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:   placementv1beta1.WorkConditionTypeApplied,
							Status: metav1.ConditionUnknown,
						},
						{
							Type:   placementv1beta1.WorkConditionTypeAvailable,
							Status: metav1.ConditionUnknown,
						},
						{
							Type:   placementv1beta1.WorkConditionTypeDiffReported,
							Status: metav1.ConditionUnknown,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Status: metav1.ConditionUnknown,
								},
								{
									Type:   placementv1beta1.WorkConditionTypeAvailable,
									Status: metav1.ConditionUnknown,
								},
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionUnknown,
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 6,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 2
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="failed"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 2
            	fleet_work_processing_requests_total{apply_status="unknown",availability_status="unknown",diff_reporting_status="unknown"} 1
			`,
			wantManifestMetricCount: 8,
			wantManifestCounter: `
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="founddrifts",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="found",diff_reporting_status="founddiff",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="failed",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="unknown",availability_status="unknown",diff_detection_status="not_found",diff_reporting_status="unknown",drift_detection_status="not_found"} 1
			`,
		},
		{
			name: "with out-of-date conditions",
			work: &placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:       workName,
					Generation: 2,
				},
				Status: placementv1beta1.WorkStatus{
					Conditions: []metav1.Condition{
						{
							Type:               placementv1beta1.WorkConditionTypeApplied,
							Status:             metav1.ConditionTrue,
							ObservedGeneration: 1,
						},
						{
							Type:               placementv1beta1.WorkConditionTypeAvailable,
							Status:             metav1.ConditionTrue,
							ObservedGeneration: 1,
						},
						{
							Type:               placementv1beta1.WorkConditionTypeDiffReported,
							Status:             metav1.ConditionTrue,
							ObservedGeneration: 1,
						},
					},
					ManifestConditions: []placementv1beta1.ManifestCondition{
						{
							Conditions: []metav1.Condition{
								{
									Type:   placementv1beta1.WorkConditionTypeApplied,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingApplyResultTypeAppliedWithFailedDriftDetection),
								},
								{
									Type:   placementv1beta1.WorkConditionTypeAvailable,
									Status: metav1.ConditionFalse,
									Reason: string(ManifestProcessingAvailabilityResultTypeFailed),
								},
								{
									Type:   placementv1beta1.WorkConditionTypeDiffReported,
									Status: metav1.ConditionTrue,
									Reason: string(ManifestProcessingReportDiffResultTypeFoundDiff),
								},
							},
						},
					},
				},
			},
			wantWorkMetricCount: 6,
			wantWorkCounter: `
				fleet_work_processing_requests_total{apply_status="applied",availability_status="available",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="applied",availability_status="unavailable",diff_reporting_status="skipped"} 1
            	fleet_work_processing_requests_total{apply_status="failed",availability_status="skipped",diff_reporting_status="skipped"} 2
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="failed"} 1
            	fleet_work_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_reporting_status="reported"} 2
            	fleet_work_processing_requests_total{apply_status="unknown",availability_status="unknown",diff_reporting_status="unknown"} 2
			`,
			wantManifestMetricCount: 9,
			wantManifestCounter: `
				fleet_manifest_processing_requests_total{apply_status="applied",availability_status="available",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="applied",availability_status="manifestnotavailableyet",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="appliedwithfaileddriftdetection",availability_status="failed",diff_detection_status="not_found",diff_reporting_status="founddiff",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="founddrifts",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="found"} 1
            	fleet_manifest_processing_requests_total{apply_status="manifestapplyfailed",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="skipped",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="found",diff_reporting_status="founddiff",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="failed",drift_detection_status="not_found"} 1
            	fleet_manifest_processing_requests_total{apply_status="skipped",availability_status="skipped",diff_detection_status="not_found",diff_reporting_status="nodifffound",drift_detection_status="not_found"} 2
            	fleet_manifest_processing_requests_total{apply_status="unknown",availability_status="unknown",diff_detection_status="not_found",diff_reporting_status="unknown",drift_detection_status="not_found"} 1
			`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			trackWorkAndManifestProcessingRequestMetrics(tc.work)

			// Collect the metrics.
			if c := testutil.CollectAndCount(metrics.FleetWorkProcessingRequestsTotal); c != tc.wantWorkMetricCount {
				t.Fatalf("unexpected work metric count: got %d, want %d", c, tc.wantWorkMetricCount)
			}

			if err := testutil.CollectAndCompare(
				metrics.FleetWorkProcessingRequestsTotal,
				strings.NewReader(workMetricMetadata+tc.wantWorkCounter),
			); err != nil {
				t.Fatalf("unexpected work counter value:\n%v", err)
			}

			if c := testutil.CollectAndCount(metrics.FleetManifestProcessingRequestsTotal); c != tc.wantManifestMetricCount {
				t.Fatalf("unexpected manifest metric count: got %d, want %d", c, tc.wantManifestMetricCount)
			}

			if err := testutil.CollectAndCompare(
				metrics.FleetManifestProcessingRequestsTotal,
				strings.NewReader(manifestMetricMetadata+tc.wantManifestCounter),
			); err != nil {
				t.Fatalf("unexpected manifest counter value:\n%v", err)
			}
		})
	}
}
