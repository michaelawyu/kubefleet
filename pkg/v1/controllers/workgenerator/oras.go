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

package workgenerator

import (
	"context"
	"encoding/json"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/utils/ociartifactconnector"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func (r *Reconciler) extractORASManifestsFrom(
	ctx context.Context,
	key string,
	wrappedOrasManifest placementv1alpha1.SnapshottedResource,
) ([]runtime.RawExtension, error) {
	// Load the ORASManifests API object.
	unstructuredObj := &unstructured.Unstructured{}
	if err := unstructuredObj.UnmarshalJSON(wrappedOrasManifest.Manifest.Raw); err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to unmarshal raw data into a unstructured object")
	}

	var orasManifests placementv1alpha1.ORASManifests
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, &orasManifests); err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to convert unstructured object into an ORASManifests API object")
	}

	// Build the OCI artifact connector.
	url := orasManifests.Spec.OCIArtifact.URL
	if len(url) == 0 {
		return nil, errors.NewUserError(nil, "invalid OCI artifact URL: empty string")
	}
	connector, err := ociartifactconnector.NewRemoteConnectorFromAuthProvider(ctx, url, orasManifests.Spec.OCIArtifact.AuthProvider, r.hubClient, r.UseHTTPToConnectToOCIRegistry)
	if err != nil {
		return nil, errors.Wraps(err, "failed to connect to the remote OCI artifact", "url", url)
	}

	// Read the digest of the OCI artifact to retrieve from the sanpshot.
	orasManifestsAdditionalInfoBytes, found := wrappedOrasManifest.AdditionalInfo[placementv1alpha1.ResourceSnapshotAdditionalInfoKeyORASManifests]
	if !found {
		return nil, errors.NewUserError(nil, "no layer information is found")
	}
	var orasManifestsAdditionalInfo placementv1alpha1.ORASManifestsAdditionalInfoForSnapshots
	if err := json.Unmarshal(orasManifestsAdditionalInfoBytes, &orasManifestsAdditionalInfo); err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to unmarshal ORAS manifests additional info from JSON data")
	}

	// Retrieve all the manifests from the OCI artifact.
	artifactDigest := orasManifestsAdditionalInfo.OCIArtifactDigest
	path := orasManifests.Spec.Path
	return r.OCIArtifactCachedStoreManager.GetManifests(ctx, artifactDigest, &key, connector, path)
}
