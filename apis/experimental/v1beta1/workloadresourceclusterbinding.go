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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	WorkloadResourceClusterBindingOwnedByLabelKey               = "experimental.kubefleet.dev/cluster-binding-owned-by"
	WorkloadResourceClusterBindingCreatedForMigrationRequestKey = "experimental.kubefleet.dev/cluster-binding-created-for-migration-request"
	WorkloadResourceClusterBindingCreatedInPlaceForKey          = "experimental.kubefleet.dev/cluster-binding-created-in-place-for"

	WorkloadResourceClusterBindingSelectorHashAnnotationKey = "experimental.kubefleet.dev/cluster-selector-hash"
)

const (
	WorkOwnedByClusterBindingLabelKey = "experimental.kubefleet.dev/work-owned-by-cluster-binding"
	WorkOwnedByPlacementLabelKey      = "experimental.kubefleet.dev/work-owned-by-placement"
	WorkOwnerNamespaceLabelKey        = "experimental.kubefleet.dev/work-owner-namespace"

	WorkDerivedFromSnapshotRevisionAnnotationKey = "experimental.kubefleet.dev/derived-from-snapshot-revision"
)

const (
	WorkloadResourceClusterBindingCondTypeSynchronized          = "Synchronized"
	WorkloadResourceClusterBindingCondTypeAllResourcesAvailable = "AllResourcesAvailable"
)

// WorkloadResourceClusterBinding is the KubeFleet API that binds a workload placement to a
// specific member cluster.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Namespaced",shortName=wkbinding,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceClusterBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The specification of the binding.
	Spec WorkloadResourceClusterBindingSpec `json:"spec,omitempty"`

	// The observed status of the binding.
	Status WorkloadResourceClusterBindingStatus `json:"status,omitempty"`
}

type WorkloadResourceClusterBindingSpec struct {
	// The name of the workload placement that this binding is associated with.
	// +kubebuilder:validation:Required
	WorkloadPlacementName string `json:"workloadPlacementName"`

	// The hash of the cluster selector associated with this binding.
	// +kubebuilder:validation:Required
	ClusterSelectorHash string `json:"clusterSelectorHash"`

	// The name of the member cluster that this binding is associated with.
	// +kubebuilder:validation:Optional
	MemberClusterName *string `json:"memberClusterName"`

	// The name of the resource snapshot revision that this binding is associated with.
	// +kubebuilder:validation:Optional
	ResourceSnapshotRevisionName *string `json:"resourceSnapshotRevisionName,omitempty"`

	// Whether the binding is suspended. If true, KubeFleet will remove resources
	// from the associated member cluster.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

type WorkloadResourceClusterBindingStatus struct {
	// A list of observed conditions about the binding.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WorkloadResourceClusterBindingList contains a list of WorkloadResourceClusterBinding.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Namespaced"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceClusterBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadResourceClusterBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadResourceClusterBinding{}, &WorkloadResourceClusterBindingList{})
}
