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

// WorkloadResourceNodePoolBinding is the KubeFleet API that binds a workload placement to a
// specific multi-cluster node pool.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Namespaced",shortName=npbinding,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceNodePoolBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The specification of the binding.
	Spec WorkloadResourceNodePoolBindingSpec `json:"spec,omitempty"`

	// The observed status of the binding.
	Status WorkloadResourceNodePoolBindingStatus `json:"status,omitempty"`
}

type WorkloadResourceNodePoolBindingSpec struct {
	// The name of the workload placement that this binding is associated with.
	// +kubebuilder:validation:Required
	WorkloadPlacementName string `json:"workloadPlacementName"`

	// The name of the multi-cluster node pool that this binding is associated with.
	// +kubebuilder:validation:Required
	MultiClusterNodePoolName string `json:"multiClusterNodePoolName"`

	// The name of the resource snapshot revision that this binding is associated with.
	// +kubebuilder:validation:Required
	LatestResourceSnapshotRevisionName string `json:"latestResourceSnapshotRevisionName,omitempty"`
}

type WorkloadResourceNodePoolBindingStatus struct {
	// A list of observed conditions about the binding.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WorkloadResourceNodePoolBindingList contains a list of WorkloadResourceNodePoolBinding.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Namespaced"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadResourceNodePoolBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadResourceNodePoolBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadResourceNodePoolBinding{}, &WorkloadResourceNodePoolBindingList{})
}
