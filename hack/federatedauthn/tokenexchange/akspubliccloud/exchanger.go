package akspubliccloud

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange"
)

var _ tokenexchange.Exchanger = &AKSPublicCloudTokenExchanger{}

type AKSPublicCloudTokenExchanger struct{}

func (exchanger *AKSPublicCloudTokenExchanger) TargetCloudEnvironment() string {
	return "aks-public-cloud"
}

func (exchanger *AKSPublicCloudTokenExchanger) GetAccessToken(fedCPConfig *runtime.RawExtension, appConfig map[string]string, idToken string) (string, time.Time, error) {
	tenantID, authorityHost, scope, err := retrieveTokenExchangeInfoFromFedCPConfig(fedCPConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve token exchange info from federated authentication credential provider config: %w", err)
	}
	clientID, err := retrieveClientIDFromAppConfig(appConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve client ID from application config: %w", err)
	}

	ctx := context.Background()
	cred := confidential.NewCredFromAssertionCallback(func(ctx context.Context, aro confidential.AssertionRequestOptions) (string, error) {
		return string(idToken), nil
	})
	authority := fmt.Sprintf("%s/%s", authorityHost, tenantID)
	client, err := confidential.New(authority, clientID, cred)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create confidential client: %w", err)
	}

	result, err := client.AcquireTokenByCredential(ctx, []string{scope})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to acquire access token: %w", err)
	}

	return result.AccessToken, result.ExpiresOn, nil
}

func retrieveTokenExchangeInfoFromFedCPConfig(fedCPConfig *runtime.RawExtension) (tenantID, authorityHost, scope string, err error) {
	var marshalled map[string]string
	if err := json.Unmarshal(fedCPConfig.Raw, &marshalled); err != nil {
		return "", "", "", fmt.Errorf("failed to unmarshal federated authentication credential provider config: %w", err)
	}

	tenantID = marshalled["tenantID"]
	authorityHost = marshalled["authorityHost"]
	scope = marshalled["scope"]
	switch {
	case len(tenantID) == 0:
		return "", "", "", fmt.Errorf("tenantID is not set in the federated authentication credential provider config")
	case len(authorityHost) == 0:
		return "", "", "", fmt.Errorf("authority host is not set in the federated authentication credential provider config")
	case len(scope) == 0:
		return "", "", "", fmt.Errorf("scope is not set in the federated authentication credential provider config")
	}

	return tenantID, authorityHost, scope, nil
}

func retrieveClientIDFromAppConfig(appConfig map[string]string) (string, error) {
	clientID := appConfig["clientID"]
	if len(clientID) == 0 {
		return "", fmt.Errorf("clientID is not set in the application config")
	}
	return clientID, nil
}
