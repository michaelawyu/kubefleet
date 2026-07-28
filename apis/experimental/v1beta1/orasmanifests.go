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
	ORASManifestCondTypeResolved = "Resolved"
)

const (
	ResourceSnapshotAdditionalInfoKeyORASManifests = "ORASManifestsLayers"
)

// ORASManifests is the KubeFleet API that represents a collection of Kubernetes manifests, as read
// from an OCI artifact, for placement.
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={kubefleet, kubefleet-experimental}
// +kubebuilder:storageversion
type ORASManifests struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The specification of the ORAS manifests.
	Spec ORASManifestsSpec `json:"spec,omitempty"`
	// The observed status of the ORAS manifests.
	Status ORASManifestsStatus `json:"status,omitempty"`
}

type ORASManifestsSpec struct {
	// The OCI artifact that contains the Kubernetes manifests.
	//
	// +kubebuilder:Validation:Required
	OCIArtifact *OCIArtifact `json:"ociArtifactRef,omitempty"`

	// The path to a file or a directory within the OCI artifact (after extraction) that contains the manifest(s)
	// to be placed. If the path is a directory, all the manifests (YAML files) under the directory will be placed.
	//
	// The default is `.` (the root directory of the extracted OCI artifact).
	//
	// +kubebuilder:Validation:Required
	// +kubebuilder:Default:="."
	Path string `json:"path,omitempty"`

	/**
	// The options for processing manifests from the OCI artifact.
	//
	// +kubebuilder:Validation:Optional
	Options ORASManifestsProcessingOptions `json:"options,omitempty"`
	*/
}

type ORASManifestsProcessingOptions struct {
	// Whether to find and place manifests under the specified path recursively if the path is a directory.
	//
	// +kubebuilder:Validation:Optional
	// +kubebuilder:Default:=true
	Recursive bool `json:"recursive,omitempty"`

	// A list of paths to ignore when processing manifests from the OCI artifact. You may use single asterisk
	// (`*`) to match any sequence of characters in a path segment.
	//
	// For example, the setup [`foo/`, `bar/*-test.yaml`] will set KubeFleet to ignore all manifests under
	// the `foo/` directory and any manifest file that ends with `-test.yaml` under the `bar/` directory.
	//
	// +kubebuilder:Validation:Optional
	IgnorePaths []string `json:"ignorePaths,omitempty"`
}

type ORASManifestsStatus struct {
	// A list of observed conditions of the ORAS Kubernetes manifests.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	OCIArtifactDetails *OCIArtifactDetails `json:"ociArtifactDetails,omitempty"`
}

type ORASManifestsAdditionalInfoForSnapshots struct {
	OCIArtifactDigest string `json:"ociArtifactDigest,omitempty"`
}

type OCIArtifactLayerDigestAndPath struct {
	Digest    string `json:"digest,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Path      string `json:"path,omitempty"`
}

// ORASManifestsList contains a list of ORASManifests.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope="Namespaced"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ORASManifestsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ORASManifests `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ORASManifests{}, &ORASManifestsList{})
}
