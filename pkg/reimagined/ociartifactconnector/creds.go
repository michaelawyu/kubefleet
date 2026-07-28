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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

type RemoteOCIRepositoryCredential interface {
	AuthClient() *auth.Client
}

type StaticRemoteOCIRepositoryCredential struct {
	registry string
	username string
	password string
}

func NewStaticRemoteOCIRepositoryCredential(registry, username, password string) RemoteOCIRepositoryCredential {
	return &StaticRemoteOCIRepositoryCredential{
		registry: registry,
		username: username,
		password: password,
	}
}

func (c *StaticRemoteOCIRepositoryCredential) AuthClient() *auth.Client {
	return &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.NewCache(),
		Credential: auth.StaticCredential(c.registry, auth.Credential{
			Username: c.username,
			Password: c.password,
		}),
	}
}

type dockerCfg struct {
	Auths map[string]dockerCfgAuth `json:"auths"`
}

type dockerCfgAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func retrieveStaticCredsFromSecretRef(
	ctx context.Context,
	k8sClient client.Client,
	secretRef *experimentalv1beta1.CrossNamespaceObjectReference,
	url string,
) (RemoteOCIRepositoryCredential, error) {
	if secretRef == nil || len(secretRef.Namespace) == 0 || len(secretRef.Name) == 0 {
		return nil, errors.NewUserError(nil, "invalid secret reference: namespace or name is empty")
	}
	secretNSName := secretRef.Namespace

	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: secretNSName, Name: secretRef.Name}, secret); err != nil {
		return nil, errors.NewAPIServerError(err, "failed to retrieve secret object", true,
			"secretNamespace", secretNSName, "secretName", secretRef.Name)
	}

	// Verify if the secret is of the expected type (kubernetes.io/dockerconfigjson).
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return nil, errors.NewUserError(nil, "invalid secret type; expected a secret of type kubernetes.io/dockerconfigjson",
			"observedSecretType", secret.Type)
	}
	dockerCfgJSON, ok := secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return nil, errors.NewUserError(nil, "invalid secret data; the .dockerconfigjson key is missing in the secret data")
	}

	var cfg dockerCfg
	if err := json.Unmarshal(dockerCfgJSON, &cfg); err != nil {
		return nil, errors.NewUserError(err, "failed to unmarshal docker config JSON data")
	}
	if len(cfg.Auths) == 0 {
		return nil, errors.NewUserError(nil, "invalid docker config JSON data; no auths field found")
	}
	var registry, username, password string
	for registryURL := range cfg.Auths {
		if strings.HasPrefix(url, registryURL) {
			registry = registryURL
			auth := cfg.Auths[registryURL]
			username = auth.Username
			password = auth.Password
			break
		}
	}
	if len(username) == 0 || len(password) == 0 {
		return nil, errors.NewUserError(nil, "no matching credentials found for the OCI artifact URL", "artifactURL", url)
	}

	return NewStaticRemoteOCIRepositoryCredential(registry, username, password), nil
}
