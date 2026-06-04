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

// ClusterAPIObjectWrapper is the API type KubeFleet uses for wrapping an API object from a member cluster.
// The API is used to enable multi-cluster object browsing, and it is served as an aggregated API.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterAPIObjectWrapper struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The identifier of the wrapped object.
	// +required
	Identifier ObjectIdentifier `json:"identifier,omitempty"`

	// The wrapped object, in the YAML/JSON format.
	// +required
	RawData []byte `json:"rawData,omitempty"`
}

type ObjectIdentifier struct {
	OriginCluster string `json:"originCluster,omitempty"`
	Group         string `json:"group,omitempty"`
	Version       string `json:"version,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name,omitempty"`
	UID           string `json:"uid,omitempty"`
}

func (id ObjectIdentifier) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.ObjectIdentifier"
}

func (w ClusterAPIObjectWrapper) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.ClusterAPIObjectWrapper"
}

// ClusterAPIObjectWrapperList is a list of ClusterAPIObjectWrapper objects.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterAPIObjectWrapperList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// A list of ClusterAPIObjectWrapper objects.
	Items []ClusterAPIObjectWrapper `json:"items"`
}

func (w ClusterAPIObjectWrapperList) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.ClusterAPIObjectWrapperList"
}
