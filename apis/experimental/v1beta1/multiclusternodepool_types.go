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

// MultiClusterNodePool is the representation of node resources across multiple member clusters.
// It specifies the types and capacities (capacity limits) of nodes in a region, and KubeFleet
// will provision member clusters with the nodes as needed to run workloads.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Cluster",shortName=mcnodepool,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MultiClusterNodePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The specification of this multi-cluster node pool.
	Spec MultiClusterNodePoolSpec `json:"spec,omitempty"`

	// The observed status of this multi-cluster node pool.
	Status MultiClusterNodePoolStatus `json:"status,omitempty"`
}

type MultiClusterNodePoolSpec struct {
	// The class of nodes in this multi-cluster node pool. It features common configurations for nodes
	// in this pool.
	//
	// The field is not in use at the moment.
	//
	// +kubebuilder:validation:Optional
	NodeClassRef *string `json:"nodeClassRef"`

	// The class of clusters that host the nodes in this multi-cluster node pool. It features common
	// configurations for the member clusters in this pool.
	//
	// The field is not in use at the moment.
	//
	// +kubebuilder:validation:Optional
	ClusterClassRef *string `json:"clusterClassRef"`

	// The minimum and maximum number of nodes in this multi-cluster node pool.
	// +kubebuilder:validation:Optional
	NodeCount *NodeCount `json:"nodeCount,omitempty"`

	// The total resource limits (CPU, memory, GPU, etc.) for this multi-cluster node pool.
	// KubeFleet will make sure that the total capacity of nodes across different resource types
	// do not exceed these limits.
	//
	// The field is not in use at the moment.
	//
	// +kubebuilder:validation:Optional
	ResourceLimits corev1.ResourceList `json:"resourceLimits,omitempty"`

	// The types of nodes that can be requested in this multi-cluster node pool. When an application
	// requests nodes of specific types, KubeFleet will try to find a multi-cluster node pool that has the requested
	// node types.
	// +kubebuilder:validation:Required
	NodeRequests NodeRequests `json:"nodeRequests,omitempty"`

	// More features to add: taints, auto-rotation, graceful termination, de-scheduling/balancing, priority, etc.
}

// +kubebuilder:validation:XValidation:rule="!has(self.min) || !has(self.max) || self.min <= self.max",message="min must be less than or equal to max"
type NodeCount struct {
	// The minimum number of nodes in this multi-cluster node pool. If set to a non-zero value,
	// KubeFleet may provision new member clusters to host the nodes as soon as the multi-cluster
	// node pool is created.
	//
	// The default value is 0.
	//
	// Demo-only note: the field is not in use at the moment. The min count is always treated as 0.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Required
	// +kubebuilder:default=0
	Min int32 `json:"min,omitempty"`

	// The maximum number of nodes in this multi-cluster node pool.
	//
	// The default value is 5.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Required
	// +kubebuilder:default=5
	Max int32 `json:"max,omitempty"`
}

type NodeRequests struct {
	// The region of the nodes in this multi-cluster node pool, e.g., westu2.
	//
	// The field is not in use at the moment.
	//
	// +kubebuilder:validation:Required
	Region string `json:"region,omitempty"`

	// A list of node sizes available in this multi-cluster node pool.
	//
	// The field is not in use at the moment.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:UniqueItems=true
	// +kubebuilder:validation:MaxItems=20
	NodeSizes []string `json:"instanceTypes,omitempty"`
}

// MultiClusterNodePoolStatus is the status of a multi-cluster node pool.
type MultiClusterNodePoolStatus struct {
	// A list of observed conditions about this multi-cluster node pool.
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The current number of nodes across the member clusters in this multi-cluster node pool.
	// +kubebuilder:validation:Optional
	Nodes int32 `json:"nodes,omitempty"`

	// The total resource capacity available in this multi-cluster node pool.
	// +kubebuilder:validation:Optional
	ResourceCapacities corev1.ResourceList `json:"resourceCapacities,omitempty"`

	// The total resource requests in this multi-cluster node pool.
	// +kubebuilder:validation:Optional
	ResourceRequests corev1.ResourceList `json:"resourceRequests,omitempty"`
}

// MultiClusterNodePoolList is a list of MultiClusterNodePool objects.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Cluster"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MultiClusterNodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MultiClusterNodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiClusterNodePool{}, &MultiClusterNodePoolList{})
}
