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

// Demo-only code.

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PlacementRebalancingManagedByAnnotationKey = "kubernetes-fleet.io/rebalancing-managed-by"
)

const (
	ClusterRebalancingRequestConditionTypeInitialized string = "Initialized"
	ClusterRebalancingRequestConditionTypeStarted     string = "Started"
	ClusterRebalancingRequestConditionTypeCompleted   string = "Completed"
	ClusterRebalancingRequestConditionTypeRolledBack  string = "RolledBack"
)

const (
	ClusterRebalancingReqCompletedCondReasonFailed     string = "Failed"
	ClusterRebalancingReqCompletedCondReasonSucceeded  string = "Succeeded"
	ClusterRebalancingReqCompletedCondReasonInProgress string = "InProgress"

	ClusterRebalancingReqRolledBackCondReasonFailed     string = "Failed"
	ClusterRebalancingReqRolledBackCondReasonSkipped    string = "Skipped"
	ClusterRebalancingReqRolledBackCondReasonSucceeded  string = "Succeeded"
	ClusterRebalancingReqRolledBackCondReasonInProgress string = "InProgress"
)

const (
	RebalancingMigrationAttemptConditionTypeCompleted  string = "Completed"
	RebalancingMigrationAttemptConditionTypeRolledBack string = "RolledBack"
)

const (
	RebalancingMigrationAttemptCompletedCondReasonExpired   string = "Expired"
	RebalancingMigrationAttemptCompletedCondReasonSucceeded string = "Succeeded"
	RebalancingMigrationAttemptCompletedCondReasonSkipped   string = "Skipped"

	RebalancingMigrationAttemptRolledBackCondReasonExpired   string = "Expired"
	RebalancingMigrationAttemptRolledBackCondReasonSucceeded string = "Succeeded"
	RebalancingMigrationAttemptRolledBackCondReasonSkipped   string = "Skipped"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories={fleet,fleet-placement}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
type ClusterRebalancingRequest struct {
	metav1.TypeMeta `json:",inline"`
	// Assumption: the name of a ClusterRebalancingRequest is the same as its target ClusterResourcePlacement.
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The desired state of the ClusterRebalancingRequest.
	// +required
	Spec ClusterRebalancingRequestSpec `json:"spec,omitempty"`

	// The observed status of the ClusterRebalancingRequest.
	// +optional
	Status ClusterRebalancingRequestStatus `json:"status,omitempty"`
}

// ClusterRebalancingRequestSpec defines the desired state of ClusterRebalancingRequest.
type ClusterRebalancingRequestSpec struct {
	// The name of the cluster from which placements would be migrated.
	//
	// This field is immutable after the request is created.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="from field cannot be changed once set"
	From string `json:"from,omitempty"`

	// The name of the cluster to which placements would be migrated.
	//
	// This field is immutable after the request is created.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="to field cannot be changed once set"
	To string `json:"to,omitempty"`

	// How KubeFleet should handle this rebalancing request.
	//
	// The available options are:
	// * SurgeFirst: for each placement to migrate, KubeFleet will first place
	//   its resources on the `to` cluster, wait until they become available, then
	//   remove the resources on the `from` cluster.
	// * DrainFirst: for each placement to migrate,KubeFleet will first remove
	//   resources on the `from` cluster, wait until all the resources are gone,
	//   then place resources on the `to` cluster.
	//
	// The default value is DrainFirst. This field is immutable after the request is created.
	// +optional
	// +kubebuilder:validation:Enum=SurgeFirst;DrainFirst
	// +kubebuilder:default=DrainFirst
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode cannot be changed once set"
	Mode string `json:"mode,omitempty"`

	// The maximum number of placements that can be migrated at the same time.
	//
	// The default value is 1. This field is immutable after the request is created.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="maxConcurrency cannot be changed once set"
	MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`

	// Whether to roll back this rebalancing request. If set to true, KubeFleet
	// will migrate placements back to the `from` cluster. Once the rollback starts, it cannot be stopped..
	//
	// When rolling back a rebalancing request, KubeFleet will use the same mode (SurgeFirst or DrainFirst) as specified in the request to
	// migrate placements back to the `from` cluster. The max concurrency setting will be respected as well. However, the failure
	// policy will cease to apply: KubeFleet will try to put placements back as much as possible within the limit of max concurrency,
	// even if some of the placements fail to function properly when they are put back to the `from` cluster. The rollback process might
	// get stuck and require manual intervention under adverse conditions.
	//
	// The default value is false.
	// +optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="!oldSelf || self",message="rollback field cannot be reset to false once set to true"
	Rollback bool `json:"rollback,omitempty"`

	// The failure policy of this rebalancing request. If not set, the rebalancing might continue despite that some of the affected
	// placements fail to be migrated.
	//
	// This field is immutable after the request is created.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="failurePolicy cannot be changed once set"
	FailurePolicy *ClusterRebalancingRequestFailurePolicy `json:"failurePolicy,omitempty"`
}

type ClusterRebalancingRequestFailurePolicy struct {
	// Trigger the action if the number of placements that fail to be migrated reaches this threshold.
	//
	// The default value is 1. This field is immutable after the request is created.
	// +optional
	// +kubebuilder:default="1"
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="onFailureCount cannot be changed once set"
	OnFailureCount *int32 `json:"onMigrationFailures,omitempty"`

	// Consider a placement migration attempt as failed if it does not complete within this duration.
	//
	// The default value is 300. This field is immutable after the request is created.
	// +optional
	// +kubebuilder:default=300
	// +kubebuilder:minimum=5
	// +kubebuilder:maximum=3600
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="maximumWaitDurationPerMigrationAttemptSeconds cannot be changed once set"
	MaximumWaitDurationPerMigrationAttemptSeconds *int32 `json:"maximumWaitDurationPerMigrationAttemptSeconds,omitempty"`
}

// ClusterRebalancingRequestStatus defines the observed state of ClusterRebalancingRequest.
type ClusterRebalancingRequestStatus struct {
	Migrations []ClusterRebalancingRequestPerMigrationStatus `json:"migrations,omitempty"`

	// An array of currently observed conditions of the ClusterRebalancingRequest.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterRebalancingRequestPerMigrationStatus defines the observed state of a single placement migration attempt of a ClusterRebalancingRequest.
type ClusterRebalancingRequestPerMigrationStatus struct {
	// The object reference of the placement being migrated.
	PlacementReference corev1.ObjectReference `json:"placementName,omitempty"`

	// The object references of the resource bindings for the `from` and `to` clusters respectively.
	FromClusterBindingReference *corev1.ObjectReference `json:"fromClusterBinding,omitempty"`
	ToClusterBindingReference   *corev1.ObjectReference `json:"toClusterBinding,omitempty"`

	// The current stage of the migration attempt.
	Stage string `json:"stage,omitempty"`

	// An array of currently observed conditions of this placement migration attempt.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:resource:scope="Cluster"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterRebalancingRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRebalancingRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterRebalancingRequest{}, &ClusterRebalancingRequestList{})
}
