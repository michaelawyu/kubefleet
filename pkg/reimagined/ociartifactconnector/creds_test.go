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
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
)

const (
	testSecretNamespace = "test-namespace"
	testSecretName      = "test-secret"
	testRegistry        = "localhost:9000"
	testArtifactURL     = "localhost:9000/testdata/manifests"
	testUsername        = "admin"
	testPassword        = "testonly"
)

// buildDockerConfigSecret builds a secret of type kubernetes.io/dockerconfigjson whose data holds the
// provided registry credentials.
func buildDockerConfigSecret(t *testing.T, secretType corev1.SecretType, cfg *dockerCfg) *corev1.Secret {
	t.Helper()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testSecretNamespace,
			Name:      testSecretName,
		},
		Type: secretType,
	}
	if cfg != nil {
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("failed to marshal docker config: %v", err)
		}
		secret.Data = map[string][]byte{
			corev1.DockerConfigJsonKey: data,
		}
	}
	return secret
}

func TestRetrieveStaticCredsFromSecretRef(t *testing.T) {
	validSecretRef := &experimentalv1beta1.CrossNamespaceObjectReference{
		Namespace: testSecretNamespace,
		Name:      testSecretName,
	}

	tests := []struct {
		name      string
		secretRef *experimentalv1beta1.CrossNamespaceObjectReference
		secret    *corev1.Secret
		url       string
		wantCreds RemoteOCIRepositoryCredential
		wantErr   bool
	}{
		{
			name:      "nil secret reference",
			secretRef: nil,
			url:       testArtifactURL,
			wantErr:   true,
		},
		{
			name: "secret reference with empty name",
			secretRef: &experimentalv1beta1.CrossNamespaceObjectReference{
				Namespace: testSecretNamespace,
			},
			url:     testArtifactURL,
			wantErr: true,
		},
		{
			name:      "secret not found",
			secretRef: validSecretRef,
			url:       testArtifactURL,
			wantErr:   true,
		},
		{
			name:      "secret of unexpected type",
			secretRef: validSecretRef,
			secret: buildDockerConfigSecret(t, corev1.SecretTypeOpaque, &dockerCfg{
				Auths: map[string]dockerCfgAuth{
					testRegistry: {Username: testUsername, Password: testPassword},
				},
			}),
			url:     testArtifactURL,
			wantErr: true,
		},
		{
			name:      "secret missing the .dockerconfigjson key",
			secretRef: validSecretRef,
			secret:    buildDockerConfigSecret(t, corev1.SecretTypeDockerConfigJson, nil),
			url:       testArtifactURL,
			wantErr:   true,
		},
		{
			name:      "docker config with no auths",
			secretRef: validSecretRef,
			secret: buildDockerConfigSecret(t, corev1.SecretTypeDockerConfigJson, &dockerCfg{
				Auths: map[string]dockerCfgAuth{},
			}),
			url:     testArtifactURL,
			wantErr: true,
		},
		{
			name:      "no matching registry for the artifact URL",
			secretRef: validSecretRef,
			secret: buildDockerConfigSecret(t, corev1.SecretTypeDockerConfigJson, &dockerCfg{
				Auths: map[string]dockerCfgAuth{
					"example.com": {Username: testUsername, Password: testPassword},
				},
			}),
			url:     testArtifactURL,
			wantErr: true,
		},
		{
			name:      "credentials retrieved successfully",
			secretRef: validSecretRef,
			secret: buildDockerConfigSecret(t, corev1.SecretTypeDockerConfigJson, &dockerCfg{
				Auths: map[string]dockerCfgAuth{
					testRegistry: {Username: testUsername, Password: testPassword},
				},
			}),
			url:       testArtifactURL,
			wantCreds: NewStaticRemoteOCIRepositoryCredential(testRegistry, testUsername, testPassword),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder()
			if tc.secret != nil {
				builder = builder.WithObjects(tc.secret)
			}
			fakeClient := builder.Build()

			gotCreds, err := retrieveStaticCredsFromSecretRef(context.Background(), fakeClient, tc.secretRef, tc.url)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("retrieveStaticCredsFromSecretRef() error = %v, wantErr %t", err, tc.wantErr)
			}

			if diff := cmp.Diff(gotCreds, tc.wantCreds, cmp.AllowUnexported(StaticRemoteOCIRepositoryCredential{})); diff != "" {
				t.Errorf("retrieveStaticCredsFromSecretRef() credential mismatch (-got, +want):\n%s", diff)
			}
		})
	}
}
