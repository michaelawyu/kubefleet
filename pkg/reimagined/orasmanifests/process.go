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
	"context"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/reimagined/ociartifactconnector"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) process(
	ctx context.Context, orasManifests *experimentalv1beta1.ORASManifests) (*ociartifactconnector.OCIArtifactManifestTab, error) {
	// TO-DO (chenyu1): in the production code, add rate limiting support to avoid the thundering
	// herd effect.

	url := orasManifests.Spec.OCIArtifact.URL
	if len(url) == 0 {
		return nil, errors.NewUserError(nil, "invalid OCI artifact URL: empty string")
	}

	// Connect to the remote OCI artifact and resolve its manifest and layers.
	connector, err := ociartifactconnector.NewRemoteConnectorFromAuthProvider(ctx, url, orasManifests.Spec.OCIArtifact.AuthProvider, r.HubClient, r.UseHTTPToConnectToOCIRegistry)
	if err != nil {
		return nil, errors.Wraps(err, "failed to connect to the remote OCI artifact", "url", url)
	}

	tab, err := connector.Describe(ctx, *orasManifests.Spec.OCIArtifact.Ref)
	if err != nil {
		return nil, errors.Wraps(err, "failed to resolve the OCI artifact", "url", url, "ref", orasManifests.Spec.OCIArtifact.Ref)
	}
	return tab, nil
}
