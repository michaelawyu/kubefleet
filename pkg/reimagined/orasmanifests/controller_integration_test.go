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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ociimagespecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/reimagined/ociartifactconnector"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

const (
	orasManifestsName = "web-app"
	ociSecretName     = "local-registry-access"

	eventuallyDuration = time.Second * 10
	eventuallyInterval = time.Millisecond * 750
)

var _ = Describe("ORAS manifests controller", Ordered, func() {
	var wantDigest string

	BeforeAll(func() {
		By("Resolving the artifact digest")
		var err error
		wantDigest, err = localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
		Expect(err).ToNot(HaveOccurred())

		By("Creating the image pull secret for the OCI registry")
		dockerConfigJSON := fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q}}}`,
			localregistry.RegistryURL, localregistry.RegistryUsername, localregistry.RegistryPassword)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ociSecretName,
				Namespace: workNSName,
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(dockerConfigJSON),
			},
		}
		Expect(hubClient.Create(ctx, secret)).To(Succeed())

		By("Creating the ORASManifests object")
		orasManifests := &experimentalv1beta1.ORASManifests{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orasManifestsName,
				Namespace: workNSName,
			},
			Spec: experimentalv1beta1.ORASManifestsSpec{
				OCIArtifact: &experimentalv1beta1.OCIArtifact{
					URL: localregistry.RawManifestsArtifactURL,
					Ref: &experimentalv1beta1.OCIArtifactReference{
						Tag: localregistry.RawManifestsArtifactTag,
					},
					AuthProvider: &experimentalv1beta1.OCIArtifactAuthProvider{
						Type: experimentalv1beta1.AuthProviderTypeGeneric,
						SecretRef: &experimentalv1beta1.CrossNamespaceObjectReference{
							APIGroup:   "",
							APIVersion: "v1",
							Kind:       "Secret",
							Namespace:  workNSName,
							Name:       ociSecretName,
						},
					},
				},
				Path: ".",
			},
		}
		Expect(hubClient.Create(ctx, orasManifests)).To(Succeed())
	})

	AfterAll(func() {
		By("Deleting the ORASManifests object and confirming its removal")
		orasManifests := &experimentalv1beta1.ORASManifests{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orasManifestsName,
				Namespace: workNSName,
			},
		}
		Eventually(func() error {
			if err := hubClient.Delete(ctx, orasManifests); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests); !apierrors.IsNotFound(err) {
				return fmt.Errorf("ORASManifests object still exists or unexpected error has occurred (%w)", err)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to delete the ORASManifests object")

		By("Deleting the image pull secret and confirming its removal")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ociSecretName,
				Namespace: workNSName,
			},
		}
		Eventually(func() error {
			if err := hubClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: ociSecretName}, secret); !apierrors.IsNotFound(err) {
				return fmt.Errorf("image pull secret still exists or unexpected error has occurred (%w)", err)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to delete the image pull secret")
	})

	It("should reconcile the object and populate its status", func() {
		// The layer digests/sizes and the annotations embed content hashes and push timestamps that
		// vary across pushes, so they are excluded from the comparison.
		ignoreOpts := cmp.Options{
			cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
			cmpopts.IgnoreFields(experimentalv1beta1.OCIArtifactDetails{}, "Annotations"),
			cmpopts.IgnoreFields(experimentalv1beta1.OCIArtifactLayerDetails{}, "Digest", "SizeBytes", "Annotations"),
		}

		Eventually(func() string {
			gotORASManifests := &experimentalv1beta1.ORASManifests{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, gotORASManifests); err != nil {
				return err.Error()
			}

			wantStatus := experimentalv1beta1.ORASManifestsStatus{
				Conditions: []metav1.Condition{
					{
						Type:               experimentalv1beta1.ORASManifestCondTypeResolved,
						Status:             metav1.ConditionTrue,
						Reason:             "Resolved",
						Message:            "The OCI artifact has been successfully resolved",
						ObservedGeneration: gotORASManifests.Generation,
					},
				},
				OCIArtifactDetails: &experimentalv1beta1.OCIArtifactDetails{
					URL:       localregistry.RawManifestsArtifactURL,
					Tag:       localregistry.RawManifestsArtifactTag,
					Digest:    wantDigest,
					MediaType: ociimagespecv1.MediaTypeImageManifest,
					// The artifact type is read from the fetched descriptor, which does not carry it.
					ArtifactType: "",
					Layers: []experimentalv1beta1.OCIArtifactLayerDetails{
						{MediaType: ociartifactconnector.OCIArtifactGenericImageLayerTarballMediaType, Path: ".xignore"},
						{MediaType: ociartifactconnector.OCIArtifactGenericImageLayerTarballMediaType, Path: "README.md"},
						{MediaType: ociartifactconnector.OCIArtifactGenericImageLayerGZippedTarballMediaType, Path: "app"},
						{MediaType: ociartifactconnector.OCIArtifactGenericImageLayerTarballMediaType, Path: "ns.yml"},
					},
				},
			}
			return cmp.Diff(gotORASManifests.Status, wantStatus, ignoreOpts)
		}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(), "ORASManifests status is not populated as expected")
	})
})
