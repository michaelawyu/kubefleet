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

package ociartifactconnector

import (
	"context"
	"encoding/json"

	ociimagespecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (c *RemoteOCIArtifactConnector) Describe(
	ctx context.Context, ref experimentalv1beta1.OCIArtifactReference) (*OCIArtifactManifestTab, error) {
	if c.repo == nil {
		return nil, errors.NewUnexpectedError(nil, "no repository has been set up")
	}

	digestOrTag := ref.Digest
	if len(digestOrTag) == 0 {
		digestOrTag = ref.Tag
	}
	if len(digestOrTag) == 0 {
		// Normally this should never occur.
		return nil, errors.NewUserError(nil, "neither digest nor tag is provided in the OCI artifact reference")
	}

	// Fetch the OCI artifact manifest from the remote repository using the provided digest or tag.
	desc, rc, err := c.repo.FetchReference(ctx, digestOrTag)
	if err != nil {
		return nil, errors.NewUserError(err, "failed to resolve the OCI artifact reference", "digestOrTag", digestOrTag)
	}
	defer rc.Close()

	if desc.MediaType != ociimagespecv1.MediaTypeImageManifest {
		// Return an error if the OCI artifact reference is not resolved to an OCI image manifest.
		return nil, errors.NewUserError(nil, "the OCI artifact reference does not point to an OCI image manifest",
			"observedMediaType", desc.MediaType, "ref", ref)
	}

	manifestData, err := content.ReadAll(rc, desc)
	if err != nil {
		return nil, errors.Wraps(err, "failed to read the OCI artifact manifest data",
			"mediaType", desc.MediaType, "digest", desc.Digest.String(),
			"annotations", desc.Annotations, "artifactType", desc.ArtifactType)
	}
	var manifest ociimagespecv1.Manifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, errors.Wraps(err, "failed to unmarshal the OCI artifact manifest data",
			"mediaType", desc.MediaType, "digest", desc.Digest.String(),
			"annotations", desc.Annotations, "artifactType", desc.ArtifactType)
	}

	// Inspect the manifest; find all the layers that need to be fetched.
	layerTabs, err := inspectManifest(&manifest)
	if err != nil {
		return nil, errors.Wraps(err, "failed to process the OCI artifact manifest",
			"mediaType", desc.MediaType, "digest", desc.Digest.String(),
			"annotations", desc.Annotations, "artifactType", desc.ArtifactType)
	}

	// Build the manifest tab to return.
	manifestTab := &OCIArtifactManifestTab{
		MediaType:    desc.MediaType,
		ArtifactType: desc.ArtifactType,
		Digest:       desc.Digest.String(),
		Annotations:  desc.Annotations,
		Layers:       layerTabs,
	}
	return manifestTab, nil
}

func inspectManifest(manifest *ociimagespecv1.Manifest) ([]*OCIArtifactLayerTab, error) {
	// Verify if the image manifest describes a valid OCI artifact (i.e., it describes an OCI artifact that
	// KubeFleet can process for placement).

	// Check if the artifact is a Helm chart.
	if len(manifest.Layers) == 1 && manifest.Layers[0].MediaType == OCIArtifactHelmChartMediaType {
		layer := manifest.Layers[0]

		// Verify if the chart is over the size limit.
		//
		// Note that in many cases this is only a sanity check, as Helm charts that are > 1.5 MB cannot usually
		// be installed in a Kubernetes cluster due to etcd object size limits.
		if layer.Size > FileBundleSizeLimitBytes {
			return nil, errors.NewUserError(nil, "the Helm chart exceeds the size limit for file bundles",
				"layerSize", layer.Size, "limit", FileBundleSizeLimitBytes)
		}

		// Return the tab for the first (and only) layer.
		chartName, found := layer.Annotations[OpenContainerImageTitleAnnotationKey]
		if !found {
			return nil, errors.NewUserError(nil, "the Helm chart layer does not have the required title annotation",
				"mediaType", layer.MediaType, "digest", layer.Digest.String(), "size", layer.Size, "annotations", layer.Annotations)
		}
		layerRefs := []*OCIArtifactLayerTab{
			{
				MediaType:   layer.MediaType,
				Digest:      layer.Digest.String(),
				SizeBytes:   layer.Size,
				Annotations: layer.Annotations,
				Path:        chartName,
			},
		}
		return layerRefs, nil
	}

	// TO-DO: traverse the DAG and iterate each node; verify that the artifact can be processed.

	layerRefs := buildOCIArtifactLayerTabsForFileBundle(manifest)
	return layerRefs, nil
}

func buildOCIArtifactLayerTabsForFileBundle(manifest *ociimagespecv1.Manifest) []*OCIArtifactLayerTab {
	layerRefs := make([]*OCIArtifactLayerTab, len(manifest.Layers))
	for i, layer := range manifest.Layers {
		path := layer.Annotations[OpenContainerImageTitleAnnotationKey]
		layerRefs[i] = &OCIArtifactLayerTab{
			MediaType:   layer.MediaType,
			Digest:      layer.Digest.String(),
			SizeBytes:   layer.Size,
			Annotations: layer.Annotations,
			Path:        path,
		}
	}
	return layerRefs
}
