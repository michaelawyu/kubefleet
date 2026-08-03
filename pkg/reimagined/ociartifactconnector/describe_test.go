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
	"fmt"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	ociimagespecv1 "github.com/opencontainers/image-spec/specs-go/v1"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

var (
	ctx context.Context

	connector OCIArtifactConnector
)

var (
	cred = NewStaticRemoteOCIRepositoryCredential(
		localregistry.RegistryURL, localregistry.RegistryUsername, localregistry.RegistryPassword)
)

func TestMain(m *testing.M) {
	ctx = context.Background()

	if err := localregistry.BootstrapLocalRegistry(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to bootstrap local registry: %v\n", err)

		if err := localregistry.TearDownLocalRegistry(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to tear down local registry: %v\n", err)
		}
		os.Exit(1)
	}

	var err error
	connector, err = NewRemoteConnectorFromCredential(localregistry.RawManifestsArtifactURL, cred, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create remote connector: %v\n", err)
		os.Exit(1)
	}

	exitCode := m.Run()

	if err := localregistry.TearDownLocalRegistry(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to tear down local registry: %v\n", err)
	}

	os.Exit(exitCode)
}

func TestDescribe(t *testing.T) {
	digest, err := localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
	if err != nil {
		t.Fatalf("failed to resolve artifact digest: %v", err)
	}

	ref := experimentalv1beta1.OCIArtifactReference{
		Tag: localregistry.RawManifestsArtifactTag,
	}
	manifestTab, err := connector.Describe(ctx, ref)
	if err != nil {
		t.Fatalf("Describe() = %v, want no error", err)
	}

	// The artifact is packaged from the top-level entries under test/oci/testdata/raw; each entry
	// becomes an individual layer, in manifest order, whose path is recorded in the title annotation.
	wantManifestTab := &OCIArtifactManifestTab{
		MediaType:    ociimagespecv1.MediaTypeImageManifest,
		ArtifactType: "",
		Digest:       digest,
		Layers: []*OCIArtifactLayerTab{
			{MediaType: OCIArtifactGenericImageLayerTarballMediaType, Path: ".xignore"},
			{MediaType: OCIArtifactGenericImageLayerTarballMediaType, Path: "README.md"},
			{MediaType: OCIArtifactGenericImageLayerGZippedTarballMediaType, Path: "app"},
			{MediaType: OCIArtifactGenericImageLayerTarballMediaType, Path: "ns.yml"},
		},
	}

	// The layer digests/sizes and the manifest-level annotations embed content hashes and push
	// timestamps that vary across pushes, so they are excluded from the comparison.
	ignoreOpts := cmp.Options{
		cmpopts.IgnoreFields(OCIArtifactManifestTab{}, "Annotations"),
		cmpopts.IgnoreFields(OCIArtifactLayerTab{}, "Digest", "SizeBytes", "Annotations"),
	}
	if diff := cmp.Diff(manifestTab, wantManifestTab, ignoreOpts); diff != "" {
		t.Errorf("Describe() manifest tab mismatch (-got, +want):\n%s", diff)
	}
}
