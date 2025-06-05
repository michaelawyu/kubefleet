package aksoidc

import (
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken"
)

var _ identitytoken.Issuer = &AKSOIDCIssuer{}

// TO-DO (chenyu1): add support for manual retrieval.
type AKSOIDCIssuer struct{}

func (issuer *AKSOIDCIssuer) Name() string {
	return "aks-oidc-issuer"
}

func (issuer *AKSOIDCIssuer) GetIDToken(fedCPConfig *runtime.RawExtension) (string, error) {
	projectedSvcAccTokenPath, err := retrieveProjectedServiceAccountTokenPath(fedCPConfig)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve projected service account token path: %w", err)
	}

	data, err := os.ReadFile(projectedSvcAccTokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read projected service account token file at path %s: %w", projectedSvcAccTokenPath, err)
	}

	return string(data), nil
}

func retrieveProjectedServiceAccountTokenPath(fedCPConfig *runtime.RawExtension) (string, error) {
	var marshalled map[string]string
	if err := json.Unmarshal(fedCPConfig.Raw, &marshalled); err != nil {
		return "", fmt.Errorf("failed to unmarshal federated authentication credential provider config: %w", err)
	}

	projectSvcAccTokenPath := marshalled["projectedServiceAccountTokenPath"]
	if len(projectSvcAccTokenPath) == 0 {
		return "", fmt.Errorf("projectedServiceAccountTokenPath is not set in the federated authentication credential provider config")
	}
	return projectSvcAccTokenPath, nil
}
