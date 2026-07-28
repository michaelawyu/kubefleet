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

package orasmanifests

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/reimagined/ociartifactconnector"
)

func (r *Reconciler) reportErrorStatus(
	ociArtifact *experimentalv1beta1.ORASManifests,
	err error,
) {
	meta.SetStatusCondition(&ociArtifact.Status.Conditions, metav1.Condition{
		Type:               experimentalv1beta1.ORASManifestCondTypeResolved,
		Status:             metav1.ConditionFalse,
		Reason:             "Erred",
		Message:            fmt.Sprintf("Failed to resolve the OCI artifact: %v", err),
		ObservedGeneration: ociArtifact.Generation,
	})
}

func (r *Reconciler) reportResolvedStatus(
	orasManifests *experimentalv1beta1.ORASManifests,
	tab *ociartifactconnector.OCIArtifactManifestTab,
) {
	meta.SetStatusCondition(&orasManifests.Status.Conditions, metav1.Condition{
		Type:               experimentalv1beta1.ORASManifestCondTypeResolved,
		Status:             metav1.ConditionTrue,
		Reason:             "Resolved",
		Message:            "The OCI artifact has been successfully resolved",
		ObservedGeneration: orasManifests.Generation,
	})

	ociArtifactDetails := &experimentalv1beta1.OCIArtifactDetails{
		URL:          orasManifests.Spec.OCIArtifact.URL,
		Tag:          orasManifests.Spec.OCIArtifact.Ref.Tag,
		Digest:       tab.Digest,
		MediaType:    tab.MediaType,
		ArtifactType: tab.ArtifactType,
		Annotations:  tab.Annotations,
	}
	ociArtifactLayerDetails := make([]experimentalv1beta1.OCIArtifactLayerDetails, 0, len(tab.Layers))
	for _, layer := range tab.Layers {
		layerDetails := experimentalv1beta1.OCIArtifactLayerDetails{
			MediaType:   layer.MediaType,
			Digest:      layer.Digest,
			SizeBytes:   layer.SizeBytes,
			Annotations: layer.Annotations,
			Path:        layer.Path,
		}
		ociArtifactLayerDetails = append(ociArtifactLayerDetails, layerDetails)
	}
	ociArtifactDetails.Layers = ociArtifactLayerDetails
	orasManifests.Status.OCIArtifactDetails = ociArtifactDetails
}
