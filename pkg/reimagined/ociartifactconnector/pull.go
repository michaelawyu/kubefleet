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

	ociimagespecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"

	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (c *RemoteOCIArtifactConnector) Pull(
	ctx context.Context,
	digest string,
	dest oras.Target,
) ([]byte, error) {
	copyOpts := oras.DefaultCopyOptions
	copyOpts.Concurrency = ConcurrencyLimit

	// Inspect the root node; return a failure if it is not an image manifest.
	copyOpts.MapRoot = func(ctx context.Context, src content.ReadOnlyStorage, root ociimagespecv1.Descriptor) (desc ociimagespecv1.Descriptor, err error) {
		if root.MediaType != ociimagespecv1.MediaTypeImageManifest {
			return ociimagespecv1.Descriptor{}, errors.NewUserError(nil, "the OCI artifact must target an OCI image manifest", "observedMediaType", root.MediaType)
		}
		return root, nil
	}

	// Override the default child node search logic. Ignore config and subject nodes for now;
	// skip unnamed nodes and empty nodes.
	copyOpts.FindSuccessors = func(ctx context.Context, fetcher content.Fetcher, desc ociimagespecv1.Descriptor) ([]ociimagespecv1.Descriptor, error) {
		nodes, err := content.Successors(ctx, fetcher, desc)
		if err != nil {
			return nil, errors.Wraps(err, "failed to iterate child nodes of the OCI artifact")
		}

		var res []ociimagespecv1.Descriptor
		for idx := range nodes {
			n := nodes[idx]

			title := n.Annotations[ociimagespecv1.AnnotationTitle]
			if title == "" {
				if content.Equal(n, ociimagespecv1.DescriptorEmptyJSON) {
					// Skip empty nodes.
					continue
				}

				childN, err := content.Successors(ctx, fetcher, n)
				if err != nil {
					return nil, errors.Wraps(err, "failed to iterate child nodes of the OCI artifact", "digest", n.Digest.String())
				}
				if len(childN) == 0 {
					// Skip unnamed nodes that have no children.
					continue
				}
			}

			// The node is either named or unnamed but has children.
			res = append(res, n)
		}
		return res, nil
	}

	// Do the pulling.
	_, err := oras.Copy(ctx, c.repo, digest, dest, digest, copyOpts)
	if err != nil {
		return nil, errors.Wraps(err, "failed to copy the OCI artifact")
	}
	return nil, nil
}
