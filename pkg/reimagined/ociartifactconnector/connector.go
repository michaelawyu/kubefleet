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

	"k8s.io/klog/v2"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	OCIArtifactHelmChartMediaType                       = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	OCIArtifactGenericImageLayerTarballMediaType        = "application/vnd.oci.image.layer.v1.tar"
	OCIArtifactGenericImageLayerGZippedTarballMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

	OpenContainerImageTitleAnnotationKey = "org.opencontainers.image.title"

	DefaultChartFilename = "chart.tgz"
)

const (
	ConcurrencyLimit = 3
)

const (
	FileBundleSizeLimitBytes = 10 * 1024 * 1024 // 10 MB
	LayerCountLimit          = 20
)

type OCIArtifactManifestTab struct {
	MediaType    string
	ArtifactType string
	Digest       string
	Annotations  map[string]string

	Layers []*OCIArtifactLayerTab
}

type OCIArtifactLayerTab struct {
	MediaType   string
	Digest      string
	SizeBytes   int64
	Annotations map[string]string
	Path        string
}

type OCIArtifactConnector interface {
	Describe(ctx context.Context, ref experimentalv1beta1.OCIArtifactReference) (*OCIArtifactManifestTab, error)
	Pull(ctx context.Context, digest string, dest oras.Target) ([]byte, error)
}

type RemoteOCIArtifactConnector struct {
	repo *remote.Repository
}

func NewRemoteConnectorFromAuthProvider(
	ctx context.Context,
	url string,
	authProvider *experimentalv1beta1.OCIArtifactAuthProvider,
	k8sClient client.Client,
	useHTTP bool,
) (OCIArtifactConnector, error) {
	var err error
	var creds RemoteOCIRepositoryCredential
	switch {
	case authProvider == nil || authProvider.Type == experimentalv1beta1.AuthProviderTypeNone:
		// No authentication is needed.
	case authProvider.Type == experimentalv1beta1.AuthProviderTypeGeneric:
		creds, err = retrieveStaticCredsFromSecretRef(ctx, k8sClient, authProvider.SecretRef, url)
		if err != nil {
			return nil, errors.Wraps(err, "failed to retrieve credentials for accessing the OCI artifact",
				"authType", "Generic", "secretRef", authProvider.SecretRef)
		}
	default:
		return nil, errors.NewUserError(nil, "unsupported authentication provider type: %s", authProvider.Type)
	}
	return NewRemoteConnectorFromCredential(url, creds, useHTTP)
}

func NewRemoteConnectorFromCredential(url string, creds RemoteOCIRepositoryCredential, useHTTP bool) (OCIArtifactConnector, error) {
	c := &RemoteOCIArtifactConnector{}
	var err error
	c.repo, err = remote.NewRepository(url)
	if err != nil {
		return nil, errors.NewUserError(err, "failed to set up the artifact URL", "URL", url)
	}
	c.repo.PlainHTTP = useHTTP

	c.repo.HandleWarning = func(warning remote.Warning) {
		klog.Warningf("Received warning from remote OCI repository: %s (agent: %s, code: %d)", warning.Text, warning.Agent, warning.Code)
	}

	if creds != nil {
		c.repo.Client = creds.AuthClient()
	}
	return c, nil
}
