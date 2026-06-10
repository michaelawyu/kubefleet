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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	ResourceSnapshotOwnedByLabelKey  = "experimental.kubefleet.dev/resource-snapshot-owned-by"
	ResourceSnapshotRevisionLabelKey = "experimental.kubefleet.dev/resource-snapshot-revision"
)

const (
	WorkloadResourceSnapshotRequestCondTypeCompleted = "Completed"

	WorkloadResourceSnapshotRequestCompletedReasonSuccess                   = "Success"
	WorkloadResourceSnapshotRequestCompletedReasonErred                     = "Erred"
	WorkloadResourceSnapshotRequestCompletedReasonWorkloadPlacementNotFound = "WorkloadPlacementNotFound"
)

// WorkloadResourceSnapshot is the KubeFleet API that captures the resources
// (e.g., pod templates, configmaps, secrets, etc.) of a workload as seen on the hub cluster
// at a specific point in time. It can be referenced by other KubeFleet APIs
// to enable consistent rollouts of resources across multiple member clusters in the fleet.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=wkrs,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The spec of a workload resource snapshot.
	Spec WorkloadResourceSnapshotSpec `json:"spec,omitempty"`
}

type WorkloadResourceSnapshotSpec struct {
	// The manifest of the workload object.
	// +kubebuilder:validation:Optional
	Workload *ManifestWithIdentifier `json:"workload,omitempty"`

	// The manifests of the additional resources for a workload, e.g., configmaps, secrets, etc.
	// +kubebuilder:validation:Optional
	AdditionalResources []ManifestWithIdentifier `json:"additionalResources,omitempty"`
}

type ManifestWithIdentifier struct {
	// The identifier of the resource represented by the manifest.
	// +kubebuilder:validation:Required
	Identifier SameNamespacedObjectReference `json:"identifier,omitempty"`

	// The manifest of the resource. It should be a Kubernetes object in YAML or JSON format.
	// +kubebuilder:validation:Required
	Manifest runtime.RawExtension `json:"manifest,omitempty"`
}

// WorkloadResourceSnapshotList is a list of WorkloadResourceSnapshot objects.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []WorkloadResourceSnapshot `json:"items"`
}

// WorkloadResourceSnapshotRequest is the KubeFleet API that represents a request to
// create a WorkloadResourceSnapshot, or in other words, a new snapshot of resources
// for a workload.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceSnapshotRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The spec of a workload resource snapshot request.
	Spec WorkloadResourceSnapshotRequestSpec `json:"spec,omitempty"`

	// The status of a workload resource snapshot request.
	Status WorkloadResourceSnapshotRequestStatus `json:"status,omitempty"`
}

type WorkloadResourceSnapshotRequestSpec struct {
	// The reference to the workload for which a resource snapshot is requested.
	// +kubebuilder:validation:Required
	WorkloadPlacementRef SameNamespacedObjectReference `json:"workloadPlacementRef,omitempty"`

	// The TTL (time to live) of the request. KubeFleet will GC old requests that have outlived their TTL.
	//
	// The default is 10 minutes (600 seconds).
	//
	// Demo-only note: the field is not in use at the moment. No requests will be GC'd in the demo.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=600
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=86400
	TTLSeconds *int32 `json:"ttl,omitempty"`
}

type WorkloadResourceSnapshotRequestStatus struct {
	// A list of observed conditions of this workload resource snapshot request.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The name of the WorkloadResourceSnapshot object created for this request.
	WorkloadResourceSnapshotName string `json:"workloadResourceSnapshotName,omitempty"`
}

// WorkloadResourceSnapshotRequestList is a list of WorkloadResourceSnapshotRequest objects.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceSnapshotRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// A list of workload resource snapshot requests.
	Items []WorkloadResourceSnapshotRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&WorkloadResourceSnapshot{}, &WorkloadResourceSnapshotList{},
		&WorkloadResourceSnapshotRequest{}, &WorkloadResourceSnapshotRequestList{},
	)
}
