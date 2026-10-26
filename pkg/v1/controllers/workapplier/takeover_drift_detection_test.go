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

package workapplier

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	kfioplacementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

// TestIsOwnedByOlderKubeFleetAPIObjects tests the isOwnedByOlderKubeFleetAPIObjects function.
func TestIsOwnedByOlderKubeFleetAPIObjects(t *testing.T) {
	olderAPIAppliedWorkRef := metav1.OwnerReference{
		APIVersion:         kfioplacementv1beta1.GroupVersion.String(),
		Kind:               kfioplacementv1beta1.AppliedWorkKind,
		Name:               "applied-work-old",
		UID:                "uid-applied-work-old",
		BlockOwnerDeletion: ptr.To(true),
	}
	olderAPIWorkRef := metav1.OwnerReference{
		APIVersion: kfioplacementv1beta1.GroupVersion.String(),
		Kind:       "Work",
		Name:       "work-old",
		UID:        "uid-work-old",
	}
	newerAPIAppliedWorkRef := metav1.OwnerReference{
		APIVersion:         placementv1alpha1.GroupVersion.String(),
		Kind:               placementv1alpha1.AppliedWorkKind,
		Name:               "applied-work-new",
		UID:                "uid-applied-work-new",
		BlockOwnerDeletion: ptr.To(true),
	}
	nonKubeFleetRef := metav1.OwnerReference{
		APIVersion: "example.com/v1",
		Kind:       "AppliedWork",
		Name:       "non-kubefleet-owner",
		UID:        "uid-non-kubefleet-owner",
	}

	configMapWithOwners := func(ownerRefs ...metav1.OwnerReference) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetNamespace("ns-1")
		obj.SetName("configmap-1")
		obj.SetOwnerReferences(ownerRefs)
		return obj
	}

	testCases := []struct {
		name               string
		inMemberClusterObj *unstructured.Unstructured
		want               bool
	}{
		{
			name: "no object",
		},
		{
			name:               "no owners",
			inMemberClusterObj: configMapWithOwners(),
		},
		{
			name:               "owned by a non-KubeFleet owner of the AppliedWork kind",
			inMemberClusterObj: configMapWithOwners(nonKubeFleetRef),
		},
		{
			name:               "owned by an AppliedWork object from the newer KubeFleet APIs",
			inMemberClusterObj: configMapWithOwners(newerAPIAppliedWorkRef),
		},
		{
			name:               "owned by a non-AppliedWork object from the older KubeFleet APIs",
			inMemberClusterObj: configMapWithOwners(olderAPIWorkRef),
		},
		{
			name:               "owned by an AppliedWork object from the older KubeFleet APIs",
			inMemberClusterObj: configMapWithOwners(olderAPIAppliedWorkRef),
			want:               true,
		},
		{
			name:               "co-owned by AppliedWork objects from both the newer and the older KubeFleet APIs",
			inMemberClusterObj: configMapWithOwners(newerAPIAppliedWorkRef, nonKubeFleetRef, olderAPIAppliedWorkRef),
			want:               true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var ownerRefs []metav1.OwnerReference
			if tc.inMemberClusterObj != nil {
				ownerRefs = tc.inMemberClusterObj.GetOwnerReferences()
			}
			if got := isOwnedByOlderKubeFleetAPIObjects(tc.inMemberClusterObj); got != tc.want {
				t.Errorf("isOwnedByOlderKubeFleetAPIObjects(object with owners %v) = %t, want %t", ownerRefs, got, tc.want)
			}
		})
	}
}
