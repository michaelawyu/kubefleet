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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	WorkloadMigrationRequestCondTypeInitialized = "Initialized"

	WorkloadMigrationRequestCondTypeCompleted             = "Completed"
	WorkloadMigrationRequestCompletedCondReasonSucceeded  = "Succeeded"
	WorkloadMigrationRequestCompletedCondReasonFailed     = "Failed"
	WorkloadMigrationRequestCompletedCondReasonInProgress = "InProgress"

	WorkloadMigrationRequestCondTypeRolledBack             = "RolledBack"
	WorkloadMigrationRequestRolledBackCondReasonSucceeded  = "Succeeded"
	WorkloadMigrationRequestRolledBackCondReasonFailed     = "Failed"
	WorkloadMigrationRequestRolledBackCondReasonInProgress = "InProgress"
)

const (
	WorkloadMigrationAttemptCondTypeCompleted            = "Completed"
	WorkloadMigrationAttemptCompletedCondReasonSucceeded = "Succeeded"
	WorkloadMigrationAttemptCompletedCondReasonFailed    = "Failed"
	WorkloadMigrationAttemptCompletedCondReasonSkipped   = "Skipped"

	WorkloadMigrationAttemptCondTypeRolledBack            = "RolledBack"
	WorkloadMigrationAttemptRolledBackCondReasonSucceeded = "Succeeded"
	WorkloadMigrationAttemptRolledBackCondReasonFailed    = "Failed"
	WorkloadMigrationAttemptRolledBackCondReasonSkipped   = "Skipped"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=wkmigreq,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadMigrationRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The spec of a workload migration request.
	Spec WorkloadMigrationRequestSpec `json:"spec,omitempty"`

	// The status of a workload migration request.
	Status WorkloadMigrationRequestStatus `json:"status,omitempty"`
}

// WorkloadMigrationRequestSpec is the spec of a workload migration request.
type WorkloadMigrationRequestSpec struct {
	// A list of label matchers that select the workload placements to migrate; KubeFleet will migrate
	// them if they are placed on the from clusters as selected by the fromClusterSelectors field
	// below.
	//
	// These selectors are OR'd.
	//
	// If the list is empty, all workload placements on the from cluster are selected. This is best used to
	// migrate all workload placements from one cluster to another, so as to, for example, decommission a cluster.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the workloadPlacementSelectors field is immutable"
	WorkloadPlacementSelectors []map[string]string `json:"workloadPlacementSelectors,omitempty"`

	// A label matcher that select the clusters to migrate workloads from. For each selected from
	// cluster, KubeFleet will migrate the workloads, as selected by the workloadPlacementSelectors field above,
	// to a corresponding to cluster, as selected by the toClusterSelectors field below. KubeFleet
	// may request to create a new cluster if an appropriate target to cluster cannot be found.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the fromClusterSelector field is immutable"
	FromClusterSelector map[string]string `json:"fromClusterSelector,omitempty"`

	// A label matcher that select the clusters to which workloads are migrated. KubeFleet may
	// use this selector to request a new cluster if an appropriate target to cluster cannot be found.
	//
	// If the selector is unset, any cluster can be a target.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the toClusterSelector field is immutable"
	ToClusterSelector map[string]string `json:"toClusterSelector,omitempty"`

	// How KubeFleet should handle this migration.
	//
	// The available options are:
	// * SurgeFirst: for each workload to migrate, KubeFleet will add the workload first to a to cluster,
	//   wait until it becomes available, then remove it from the from cluster. If a to cluster cannot
	//   be found, KubeFleet will send a cluster request and wait until a to cluster becomes available before
	//   proceeding with the migration; during this period the workload will continue to run on the from cluster.
	// * DrainFirst: for each workload to migrate, KubeFleet will first remove the workload from the from
	//   cluster, then add it to a to cluster. If a to cluster cannot be found, KubeFleet will send a cluster
	//   request; during this period the from cluster will not be running the workload anymore.
	//
	// The default mode is SurgeFirst.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=SurgeFirst;DrainFirst
	// +kubebuilder:default=SurgeFirst
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the mode field is immutable"
	Mode string `json:"mode,omitempty"`

	// The maximum number of workloads to migrate in parallel.
	//
	// The default value is 1.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the maxConcurrency field is immutable"
	MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`

	// Whether to roll back the migration attempt. If set to true, KubeFleet will move workloads
	// back from the to clusters to the from clusters. Once the rollback starts, it cannot be
	// cancelled; to start the migration again, delete and re-create a workload migration request as
	// needed.
	//
	// During the rollback process, KbueFleet will use the same mode and maxConcurrency settings as
	// specified in this request; however, the failure policy will cease to apply. KubeFleet will
	// move back as many workloads as possible, even if the from clusters fail to run them
	// for some reason.
	//
	// The field always starts with false and can only be set to true once. After that it cannot be changed anymore.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="!oldSelf || self",message="the rollback field cannot be reset to false once set to true"
	Rollback bool `json:"rollback,omitempty"`

	// The failure policy of this migration request. It helps KubeFleet to stop the migration attempt
	// when there are too many failures, so as to keep the impact radius within control.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the failurePolicy field is immutable"
	FailurePolicy WorkloadMigrationFailurePolicy `json:"failurePolicy,omitempty"`
}

type WorkloadMigrationFailurePolicy struct {
	// The maximum number of failed migration attempts allowed before KubeFleet stops the migration.
	// A migration attempt is the replacement of one specific workload from one specific from cluster to one
	// specific to cluster.
	//
	// If this threshold is reached, KubeFleet will suspend the migration process. Delete the request
	// to accept the current state, or roll back the request to restore to the original state.
	//
	// The default value is 1.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the maxFailureCount field is immutable"
	MaxFailureCount int32 `json:"maxFailureCount,omitempty"`

	/**
	// The maximum time in minutes that KubeFleet will wait for a migration attempt to complete.
	// If this threshold is reached, KubeFleet will consider the migration attempt as failed.
	//
	// Setting this field to be of a value that is too low may lead to unexpected failures, especially in situations
	// where KubeFleet has to request a new cluster to fulfill a migration attempt, as cluster provisioning may
	// take some time.
	//
	// The default value is 30 minutes.
	//
	// This field is immutable after creation.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:default=30
	// +kubebuilder:validation:Maximum=1440
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the maxWaitTimePerWorkloadMinutes field is immutable"
	MaxWaitTimePerWorkloadMinutes int32 `json:"maxWaitTimePerWorkloadMinutes,omitempty"`
	*/
}

type WorkloadMigrationRequestStatus struct {
	// A list of migration attempts that KubeFleet will perform for this migration request.
	MigrationAttempts []WorkloadMigrationAttempt `json:"migrationAttempts,omitempty"`

	// A list of observed conditions of this workload migration request.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type WorkloadMigrationAttempt struct {
	// The reference to workload resource cluster binding involved in this migration attempt.
	WorkloadResourceBindingRef CrossNamespaceObjectReference `json:"workloadResourceBindingRef,omitempty"`

	// The reference to the workload placement involved in this migration attempt.
	WorkloadPlacementRef CrossNamespaceObjectReference `json:"workloadPlacementRef,omitempty"`

	// The from cluster of this migration attempt.
	FromClusterName string `json:"fromClusterName,omitempty"`

	// The to cluster of this migration attempt. If unset, the to cluster is not determined yet,
	// and KubeFleet may have to request a new cluster to fulfill the migration attempt.
	ToClusterName *string `json:"toClusterName,omitempty"`

	// The cluster request created for this migration attempt, if any.
	ToClusterRequestName *string `json:"toClusterRequestName,omitempty"`

	// A list of observed conditions of this migration attempt.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WorkloadMigrationRequestList contains a list of WorkloadMigrationRequest objects.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Cluster"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadMigrationRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []WorkloadMigrationRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadMigrationRequest{}, &WorkloadMigrationRequestList{})
}
