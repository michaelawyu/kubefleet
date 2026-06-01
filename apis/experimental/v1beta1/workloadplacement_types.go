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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadPlacement is a KubeFleet API that allows users to place workloads across
// multi-cluster node pools as needed.
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=wp,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
type WorkloadPlacement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The specification of the workload placement.
	// +kubebuilder:validation:Required
	Spec WorkloadPlacementSpec `json:"spec,omitempty"`

	// The observed status of the workload placement.
	// +kubebuilder:validation:Optional
	Status WorkloadPlacementStatus `json:"status,omitempty"`
}

type WorkloadPlacementSpec struct {
	// A list of label matchers that specifies the multi-cluster node pools where KubeFleet should place
	// the workloads.
	//
	// Currently, KubeFleet will place the workloads on one member cluster per multi-cluster node pool.
	// KubeFleet may scale up an existing member cluster or provision a new member cluster under a
	// selected multi-cluster node pool as necessary.
	//
	// These matchers are AND'd.
	//
	// If not specified, all multi-cluster node pools will be selected.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	MultiClusterNodePoolSelecter []map[string]string `json:"multiClusterNodePoolSelecter,omitempty"`

	// An object reference that points to the workload that KubeFleet will place.
	//
	// Currently, only Deployment is supported.
	//
	// +kubebuilder:validation:Required
	WorkloadRef SameNamespacedObjectReference `json:"workloadRef"`

	// A list of object references that point to the additional resources that are depended upon
	// by the workload object.
	//
	// KubeFleet can auto-discover such resources by checking at the pod template from the workload
	// object. There is no need to explicitly list them there. KubeFleet will also create the
	// namespace needed for this application automatically.
	//
	// The auto-discovery is not supported at the moment.
	//
	// +kubebuilder:validation:Optional
	AdditionalResourceRefs []SameNamespacedObjectReference `json:"additionalResourceRefs,omitempty"`

	// The capacity request for each pod of this workload. KubeFleet will use this information
	// to request fitting nodes as needed from each selected multi-cluster node pool; it also
	// helps KubeFleet collect and report resource usage across multi-cluster node pools and
	// their member clusters.
	//
	// The information is not currently in use by KubeFleet.
	//
	// +kubebuilder:validation:Optional
	CapacityRequest *CapacityRequestPerPod `json:"capacityRequest,omitempty"`

	// The revision history limit for this application. Every change to the application, including
	// changes to the pod template and to the supplementary resources (e.g., ConfigMaps, Secrets)
	// will create a new revision.
	//
	// The default value is 1.
	//
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
}

// CapacityRequestPerPod is a request of nodes and resources for each pod of a workload.
type CapacityRequestPerPod struct {
	// A list of label matchers that specifies the nodes appropriate for running pods of this
	// application. These selectors are OR'd. If not specified, any node is considered appropriate.
	//
	// For example:
	// * use node.kubernetes.io/instance-type to specify some node types (VM sizes), e.g., Standard_DS3_v2 (on Azure)
	//   to ensure that all pods will run on the specified VM types only.
	// * use kubernetes.io/arch to specify the architecture needed, e.g., amd64 or arm64.
	// * use kubernetes.io/os to specify the OS needed, e.g., linux or windows.
	//
	// KubeFleet will request fitting nodes from the selected multi-cluster node pool(s) based on these selectors.
	// It may run a pod on an existing cluster, or scale up an existing cluster, or even provision a new cluster
	// if necessary.
	//
	// +kubebuilder:validation:Optional
	NodeInstanceRequests []map[string]string `json:"nodeInstanceRequests,omitempty"`

	// A list of resource requests (CPU, memory, GPU, etc.) for each pod of this application.
	//
	// If not specified, KubeFleet will use the resource requests specified in the pod template
	// here.
	//
	// +kubebuilder:validation:Optional
	ResourceRequests corev1.ResourceList `json:"resourceRequests,omitempty"`
}

type WorkloadPlacementStatus struct {
	// A list of conditions that describe the workload placement.
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The name of the latest revision of the resource snapshot created for this placement.
	// +kubebuilder:validation:Optional
	LatestResourceSnapshotRevisionName *string `json:"latestResourceSnapshotRevisionName,omitempty"`
}

// WorkloadPlacementList contains a list of WorkloadPlacement.
//
// +kubebuilder:resource:scope="Namespaced"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadPlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []WorkloadPlacement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadPlacement{}, &WorkloadPlacementList{})
}
