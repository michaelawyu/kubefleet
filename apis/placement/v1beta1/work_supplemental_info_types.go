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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// Below is a list of supported supplementary information categories.
	SupplementalInfoCategoryWrappedObjectStatus = "WrappedObjectStatus"
)

// InMemberClusterObjectResourceIdentifier identifies a resource in a member cluster. It is
// used to cross-reference resources between the hub cluster and a member cluster.
type InMemberClusterObjectResourceIdentifier struct {
	// Group is the API Group of the resource.
	// +kubebuilder:validation:Optional
	Group string `json:"group,omitempty"`

	// Version is the version of the resource.
	// +kubebuilder:validation:Required
	Version string `json:"version,omitempty"`

	// Kind is the kind of the resource.
	// +kubebuilder:validation:Required
	Kind string `json:"kind,omitempty"`

	// Resource is the resource type of the resource.
	// +kubebuilder:validation:Required
	Resource string `json:"resource,omitempty"`

	// Namespace is the namespace of the resource; this is left empty for cluster-scoped resources.
	// +kubebuilder:validation:Optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the name of the resource.
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
}

// WrappedObjectStatus is a piece of supplemental information that wraps the status of an object
// in a member cluster. It is used to backport the status of an object in a member cluster
// to the hub cluster.
type WrappedObjectStatus struct {
	// ResourceIdentifier identifies the object whose status is being wrapped.
	// +kubebuilder:validation:Required
	ResourceIdentifier InMemberClusterObjectResourceIdentifier `json:"resourceIdentifier"`

	// ObservationTime is the timestamp when the status is wrapped.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	ObservationTime metav1.Time `json:"observationTime"`

	// Status is the wrapped status of the object.
	// +kubebuilder:validation:Required
	Status runtime.RawExtension `json:"status"`
}
