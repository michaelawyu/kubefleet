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

const (
	WorkloadPlacementCondTypeScheduled             = "Scheduled"
	WorkloadPlacementCondTypeSynchronized          = "Synchronized"
	WorkloadPlacementCondTypeAllResourcesAvailable = "AllResourcesAvailable"
)

// WorkloadPlacement is a KubeFleet API that allows users to place workloads across
// member clusters.
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=wkp,categories={kubefleet, kubefleet-experimental}
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
	// A list of label matchers that specifies the member clusters where KubeFleet should place
	// the workloads. For **each** label matcher, KubeFleet will find all matching member cluster and
	// pick one of them to place the workload, as appropriate; if none can be found for a specific
	// label matcher, KubeFleet will request a member cluster to be provisioned.
	//
	// If not specified, KubeFleet will pick one member cluster from all member clusters.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	ClusterSelectors []map[string]string `json:"clusterSelectors,omitempty"`

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

	// The capacity request for each pod of this workload. KubeFleet uses this information to help
	// find the most appropriate member cluster(s) for placing the workload, taking information
	// such as node availability into consideration.
	//
	// The information is not currently in use by KubeFleet.
	//
	// +kubebuilder:validation:Optional
	CapacityRequest *CapacityRequest `json:"capacityRequest,omitempty"`

	// The revision history limit for this application. Every change to the application, including
	// changes to the pod template and to the supplementary resources (e.g., ConfigMaps, Secrets)
	// will create a new revision.
	//
	// The default value is 3.
	//
	// This field is not currently in use by KubeFleet.
	//
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=3
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
}

// CapacityRequest is a request of nodes and resources for each pod of a workload.
type CapacityRequest struct {
	// A list of label matchers that specifies the nodes appropriate for running pods of this
	// application. These selectors are OR'd. If not specified, any node is considered appropriate.
	//
	// For example:
	// * use node.kubernetes.io/instance-type to specify some node types (VM sizes), e.g., Standard_DS3_v2 (on Azure)
	//   to ensure that all pods will run on the specified VM types only.
	// * use kubernetes.io/arch to specify the architecture needed, e.g., amd64 or arm64.
	// * use kubernetes.io/os to specify the OS needed, e.g., linux or windows.
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

	// A list of binding managers that are currently managing the bindings for this placement.
	// +kubebuilder:validation:Optional
	BindingManagers []PlacementBindingManager `json:"bindingManagers,omitempty"`
}

type BindingManagerMode string

const (
	// The exclusive mode implies that the bindings for a placement is currently solely managed by one
	// object or one controller; no other object or controller should add itself as a binding manager
	// and should wait until the current manager removes itself from the binding managers list.
	BindingManagerModeExclusive BindingManagerMode = "Exclusive"

	// The shared among same-kind objects mode implies that the bindings for a placement can be managed
	// by multiple objects of the same kind if needed; such objects can add themselves as binding managers by
	// inserting to the list of binding managers.
	BindingManagerModeSharedAmongSameKindObjects BindingManagerMode = "SharedAmongSameKindObjects"
)

// +kubebuilder:validation:XValidation:rule="has(self.objectRef) != has(self.controllerName)",message="exactly one of objectRef or controllerName must be set"
type PlacementBindingManager struct {
	// The mode of this binding manager. See the comments for each mode for more information.
	//
	// The default value is "Exclusive".
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=Exclusive
	// +kubebuilder:validation:Enum=Exclusive;SharedAmongSameKindObjects
	Mode BindingManagerMode `json:"mode"`

	// A reference that points to the binding manager object.
	//
	// This field is mutually exclusive with the field, `ControllerName`. Exactly one of them
	// should be set.
	//
	// +kubebuilder:validation:Optional
	ObjectRef *SameNamespacedObjectReference `json:"objectRef,omitempty"`

	// A name of the controller that manages the bindings for this placement.
	//
	// This field is mutually exclusive with the field, `ObjectRef`. Exactly one of them
	// should be set.
	//
	// +kubebuilder:validation:Optional
	ControllerName *string `json:"controllerName,omitempty"`
}

// WorkloadPlacementList contains a list of WorkloadPlacement.
//
// +kubebuilder:object:root=true
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
