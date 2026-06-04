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
	MultiClusterAPIObjectQueryCondTypeExecuted = "Executed"

	PerClusterAPIObjectRawQueryCondTypeExecuted = "Executed"
)

// MultiClusterAPIObjectQuery is the API type KubeFleet exposes for querying API objects across member clusters.
// The API facilitates the KubeFleet multi-cluster object browsing user-facing experience, and it is served as
// an aggregated API.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MultiClusterAPIObjectQuery struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// A list of clusters where the query will run against. If empty, KubeFleet will run the query against all
	// member clusters.
	// +optional
	TargetClusterNames []string `json:"targetClusterNames,omitempty"`

	// A list of object selector terms. At this moment, one can set exactly one term in the list.
	//
	// KubeFleet will always serve the query from the object cache.
	// +optional
	CachedObjectSelectorTerms []CachedObjectSelectorTerm `json:"cachedObjectSelectorTerms,omitempty"`

	// The continue token for paginated query results. If set, KubeFleet will return more results from the previous
	// query assciated with the continue token. Other inputs, such as TargetClusterNames and CachedObjectSelectorTerms,
	// will be ignored if the continue token is set.
	Continue string `json:"continue,omitempty"`

	// The observed status of the query execution.
	Status MultiClusterAPIObjectQueryStatus `json:"status,omitempty"`
}

type CachedObjectSelectorTerm struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Namespace *string `json:"namespace,omitempty"`
	Name      *string `json:"name,omitempty"`
}

type MultiClusterAPIObjectQueryStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The objects as returned by the query, in the form of wrapped API objects.
	Results []ClusterAPIObjectWrapper `json:"results,omitempty"`

	// The continue token for more query results. It is set when the query yields paginated results and
	// there are more results to be fetched.
	Continue string `json:"continue,omitempty"`
}

func (t CachedObjectSelectorTerm) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.CachedObjectSelectorTerm"
}

func (s MultiClusterAPIObjectQueryStatus) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.MultiClusterAPIObjectQueryStatus"
}

func (q MultiClusterAPIObjectQuery) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.MultiClusterAPIObjectQuery"
}

// MultiClusterAPIObjectQueryList is a list of MultiClusterAPIObjectQuery objects.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MultiClusterAPIObjectQueryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// A list of multi-cluster API object queries.
	Items []MultiClusterAPIObjectQuery `json:"items"`
}

func (q MultiClusterAPIObjectQueryList) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.MultiClusterAPIObjectQueryList"
}

// PerClusterAPIObjectRawQuery is the API type KubeFleet exposes for querying API objects in a specific cluster
// with a raw query path.
// The API facilitates the KubeFleet multi-cluster object browsing user-facing experience, and it is served as
// an aggregated API.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PerClusterAPIObjectRawQuery struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The raw query path to execute on the target member cluster.
	// +required
	RawQueryPath string `json:"rawQueryPath,omitempty"`

	// The continue token for paginated query results. If set, KubeFleet will return more results from the previous
	// query assciated with the continue token. Other inputs, such as RawQueryPath, will be ignored if the continue
	// token is set.
	Continue string `json:"continue,omitempty"`

	// The observed status of the query execution.
	Status PerClusterAPIObjectRawQueryStatus `json:"status,omitempty"`
}

// PerClusterAPIObjectRawQueryStatus defines the observed status of a PerClusterAPIObjectRawQuery.
type PerClusterAPIObjectRawQueryStatus struct {
	// A list of conditions representing the status of the query execution.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The objects as returned by the query, in the form of wrapped API objects.
	Results []ClusterAPIObjectWrapper `json:"results,omitempty"`

	// The continue token for more query results. It is set when the query yields paginated results and
	// there are more results to be fetched.
	Continue string `json:"continue,omitempty"`
}

func (s PerClusterAPIObjectRawQueryStatus) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.PerClusterAPIObjectRawQueryStatus"
}

func (q PerClusterAPIObjectRawQuery) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.PerClusterAPIObjectRawQuery"
}

// PerClusterAPIObjectRawQueryList is a list of PerClusterAPIObjectRawQuery objects.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PerClusterAPIObjectRawQueryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// A list of per-cluster API object raw queries.
	Items []PerClusterAPIObjectRawQuery `json:"items"`
}

func (q PerClusterAPIObjectRawQueryList) OpenAPIModelName() string {
	return "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1.PerClusterAPIObjectRawQueryList"
}
