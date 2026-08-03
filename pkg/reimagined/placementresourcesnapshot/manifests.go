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

package placementresourcesnapshot

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/resource"
)

func (m *Manager) retrieveResourceContentsFrom(
	ctx context.Context, placementPolicy *experimentalv1beta1.PlacementPolicy,
) ([]experimentalv1beta1.ResourceContent, string, error) {
	var resourceContents []experimentalv1beta1.ResourceContent
	for idx := range placementPolicy.Spec.ResourceSelectors {
		additionalResRef := placementPolicy.Spec.ResourceSelectors[idx]
		additionalResGVK := schema.GroupVersionKind{
			Group:   additionalResRef.APIGroup,
			Version: additionalResRef.APIVersion,
			Kind:    additionalResRef.Kind,
		}
		additionalResGVR, err := m.GVKToGVR(additionalResGVK)
		if err != nil {
			return nil, "", errors.Wraps(err, "failed to convert GVK to GVR", "sourceRef", additionalResRef)
		}

		additionalResObj, err := m.dynamicClient.
			Resource(additionalResGVR).
			Namespace(placementPolicy.Namespace).
			Get(ctx, additionalResRef.Name, metav1.GetOptions{})
		if err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to get additional resource object",
				true,
				"sourceRef", additionalResRef, "sourceNamespace", placementPolicy.Namespace)
			return nil, "", wrappedErr
		}

		// Save a copy of the resource object in case it needs additional processing.
		additionalResObjCopy := additionalResObj.DeepCopy()

		additionalResObj = sanitizeManifestObject(additionalResObj)
		additionalResObjJSON, err := additionalResObj.MarshalJSON()
		if err != nil {
			wrappedErr := errors.NewUnexpectedError(err,
				"failed to marshal additional resource object into JSON",
				"sourceRef", additionalResRef, "sourceNamespace", placementPolicy.Namespace)
			return nil, "", wrappedErr
		}

		var additionalInfo map[string][]byte
		switch {
		case additionalResRef.Kind == "ORASManifests":
			additionalInfo, err = extractAdditionalInfoFromORASManifests(additionalResObjCopy)
			if err != nil {
				wrappedErr := errors.NewUnexpectedError(err,
					"failed to extract additional info from ORASManifests object",
					"sourceRef", additionalResRef, "sourceNamespace", placementPolicy.Namespace)
				return nil, "", wrappedErr
			}
		}

		resourceContents = append(resourceContents, experimentalv1beta1.ResourceContent{
			Identifier: experimentalv1beta1.SameNamespacedObjectReference{
				APIGroup:   additionalResGVR.Group,
				APIVersion: additionalResGVR.Version,
				Kind:       additionalResRef.Kind,
				Name:       additionalResRef.Name,
			},
			Manifest:       runtime.RawExtension{Raw: additionalResObjJSON},
			AdditionalInfo: additionalInfo,
		})
	}

	// Calculate the hash of the resource contents.
	resourceContentsHash, err := resource.HashOf(resourceContents)
	if err != nil {
		return nil, "", errors.NewUnexpectedError(err, "failed to calculate hash of resource contents")
	}

	return resourceContents, resourceContentsHash, nil
}

func (m *Manager) GVKToGVR(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	GK := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := m.restMapper.RESTMapping(GK, gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, errors.Wraps(err, "failed to retrieve REST mapping")
	}
	return mapping.Resource, nil
}

func extractAdditionalInfoFromORASManifests(unstructuredORASManifests *unstructured.Unstructured) (map[string][]byte, error) {
	// Convert the unstructured object to an ORASManifests object.
	orasManifests := &experimentalv1beta1.ORASManifests{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredORASManifests.Object, orasManifests); err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to convert unstructured object to an ORASManifests object")
	}

	resolvedCond := meta.FindStatusCondition(orasManifests.Status.Conditions, experimentalv1beta1.ORASManifestCondTypeResolved)
	if !condition.IsConditionStatusTrue(resolvedCond, orasManifests.Generation) || orasManifests.Status.OCIArtifactDetails == nil {
		return nil, errors.Wraps(nil, "the ORASManifests object is not fully processed yet; no details are available in the status")
	}
	additionalInfo := make(map[string][]byte)
	orasManifestAdditionalInfo := &experimentalv1beta1.ORASManifestsAdditionalInfoForSnapshots{
		OCIArtifactDigest: orasManifests.Status.OCIArtifactDetails.Digest,
	}

	orasManifestAdditionalInfoBytes, err := json.Marshal(orasManifestAdditionalInfo)
	if err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to marshal ORAS manifests additional info for snapshots into JSON",
			"additionalInfo", orasManifestAdditionalInfo)
	}
	additionalInfo[experimentalv1beta1.ResourceSnapshotAdditionalInfoKeyORASManifests] = orasManifestAdditionalInfoBytes
	return additionalInfo, nil
}
