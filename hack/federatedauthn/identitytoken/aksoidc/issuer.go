package aksoidc

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken"
)

var _ identitytoken.Issuer = &AKSOIDCIssuer{}

// TO-DO (chenyu1): add support for manual retrieval.
type AKSOIDCIssuer struct{}

func (issuer *AKSOIDCIssuer) Name() string {
	return "aks-oidc-issuer"
}

func (issuer *AKSOIDCIssuer) GetIDToken(fedCPConfig *runtime.RawExtension, appConfig map[string]string) (string, time.Time, error) {
	projectedSvcAccTokenPath, projectedSvcAccTokenExpiryTime, err := retrieveProjectedServiceAccountTokenPathAndExpiryTimeFromAppConfig(appConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve projected service account token path and expiry time: %w", err)
	}

	data, err := os.ReadFile(projectedSvcAccTokenPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to read projected service account token file at path %s: %w", projectedSvcAccTokenPath, err)
	}

	return string(data), projectedSvcAccTokenExpiryTime, nil
}

func retrieveProjectedServiceAccountTokenPathAndExpiryTimeFromAppConfig(appConfig map[string]string) (string, time.Time, error) {
	projectSvcAccTokenPath := appConfig["aksOIDCIssuerProjectedServiceAccountTokenPath"]
	if len(projectSvcAccTokenPath) == 0 {
		return "", time.Time{}, fmt.Errorf("aksOIDCIssuerProjectedServiceAccountTokenPath is not set in the federated authentication credential provider config")
	}

	ttl := appConfig["aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds"]
	if len(ttl) == 0 {
		return "", time.Time{}, fmt.Errorf("aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds is not set in the federated authentication credential provider config")
	}

	// To simplify the workflow, set the expiry time to a fixed percentage of the token's expected lifetime.
	//
	// Note that Kubernetes will rotate the token automatically when it is close to expiry (80% of TTL).
	ttlSeconds, err := strconv.Atoi(ttl)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds: %w", err)
	}
	ttlSecondsDuration := int64(float64(time.Duration(ttlSeconds)*time.Second) * 0.2)
	return projectSvcAccTokenPath, time.Now().Add(time.Duration(ttlSecondsDuration)), nil
}
