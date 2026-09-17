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

// resourceeligibility features utilities for enforcing resource-level placement constraints in KubeFleet.
package resourceeligibility

import (
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
	"k8s.io/apimachinery/pkg/util/sets"
)

type NamespaceEligibilityMode string

const (
	NamespaceEligibilityModeAllowList NamespaceEligibilityMode = "AllowList"
	NamespaceEligibilityModeDenyList  NamespaceEligibilityMode = "DenyList"
)

type GVKEligibilityMode string

const (
	GVKEligibilityModeAllowList GVKEligibilityMode = "AllowList"
	GVKEligibilityModeDenyList  GVKEligibilityMode = "DenyList"
)

type Checker interface{}

type checker struct {
	nsEligibilityMode NamespaceEligibilityMode

	customNSAllowList       sets.Set[string]
	customNSPrefixAllowList sets.Set[string]
	customNSDenyList        sets.Set[string]
	customNSPrefixDenyList  sets.Set[string]
	builtInNSDenyList       sets.Set[string]
	builtInNSPrefixDenyList sets.Set[string]

	gvkEligibilityMode GVKEligibilityMode

	customGVKAllowList gvkMatcherTrie
	customGVKDenyList  gvkMatcherTrie
	builtInGVKDenyList gvkMatcherTrie

	wrapped Checker
}

func New() Checker {
	builtInGVKDenyList := gvkMatcherTrie{}

	return &checker{
		builtInNSPrefixDenyList: sets.Set[string]{
			utils.KubeNSNamePrefix:  {},
			utils.FleetNSNamePrefix: {},
		},
		builtInGVKDenyList: builtInGVKDenyList,
	}
}
