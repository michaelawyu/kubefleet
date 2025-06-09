package gke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2/google"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange"
)

var (
	gkeScopes = []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
	}
)

var _ tokenexchange.Exchanger = &GKETokenExchanger{}

type GoogleCredentialSourceFormat struct {
	Type string `json:"type"`
}

type GoogleCredentialSource struct {
	File   string                       `json:"file"`
	Format GoogleCredentialSourceFormat `json:"format"`
}

type GoogleDefaultCredentials struct {
	UniverseDomain   string                 `json:"universe_domain"`
	Type             string                 `json:"type"`
	Audience         string                 `json:"audience"`
	SubjectTokenType string                 `json:"subject_token_type"`
	TokenURL         string                 `json:"token_url"`
	CredentialSource GoogleCredentialSource `json:"credential_source"`
	TokenInfoURL     string                 `json:"token_info_url"`
}

type GKETokenExchanger struct{}

func (exchanger *GKETokenExchanger) TargetCloudEnvironment() string {
	return "gke"
}

func (exchanger *GKETokenExchanger) GetAccessToken(
	fedCPConfig *runtime.RawExtension,
	appConfig map[string]string,
	idToken string,
) (string, time.Time, error) {
	audience, err := retrieveAudienceFromFedCPConfig(fedCPConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve audience from federated authentication credential provider config: %w", err)
	}
	defaultCredPath, tokenPath, err := retrieveDefaultCredPathAndTokenPathFromAppConfig(appConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve token path from application config: %w", err)
	}

	defaultCred := &GoogleDefaultCredentials{
		UniverseDomain:   "googleapis.com",
		Type:             "external_account",
		Audience:         audience,
		SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt",
		TokenURL:         "https://sts.googleapis.com/v1/token",
		CredentialSource: GoogleCredentialSource{
			File: defaultCredPath,
			Format: GoogleCredentialSourceFormat{
				Type: "text",
			},
		},
		TokenInfoURL: "https://sts.googleapis.com/v1/introspect",
	}
	if err := saveDefaultCredentialsToFile(defaultCred, defaultCredPath); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to save default credentials to file %s: %w", defaultCredPath, err)
	}

	if err := saveTokenToFile(idToken, tokenPath); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to save token to file %s: %w", tokenPath, err)
	}

	// Set the GOOGLE_APPLICATION_CREDENTIALS environment variable to the default credentials file path.
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", defaultCredPath)

	ctx := context.Background()
	cred, err := google.FindDefaultCredentials(ctx, gkeScopes...)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to find default credentials: %w", err)
	}
	if cred == nil {
		return "", time.Time{}, fmt.Errorf("no default credentials found")
	}

	token, err := cred.TokenSource.Token()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get access token: %w", err)
	}
	if token == nil {
		return "", time.Time{}, fmt.Errorf("access token is nil")
	}

	return token.AccessToken, token.Expiry, nil
}

func retrieveAudienceFromFedCPConfig(fedCPConfig *runtime.RawExtension) (string, error) {
	var marshalled map[string]string
	if err := json.Unmarshal(fedCPConfig.Raw, &marshalled); err != nil {
		return "", fmt.Errorf("failed to unmarshal federated authentication credential provider config: %w", err)
	}

	audience := marshalled["audience"]
	if len(audience) == 0 {
		return "", fmt.Errorf("audience is not set in the federated authentication credential provider config")
	}
	return audience, nil
}

func retrieveDefaultCredPathAndTokenPathFromAppConfig(appConfig map[string]string) (string, string, error) {
	defaultCredPath := appConfig["defaultCredPath"]
	if len(defaultCredPath) == 0 {
		return "", "", fmt.Errorf("defaultCredPath is not set in the application config")
	}

	tokenPath := appConfig["tokenPath"]
	if len(tokenPath) == 0 {
		return "", "", fmt.Errorf("tokenPath is not set in the application config")
	}
	return defaultCredPath, tokenPath, nil
}

func saveDefaultCredentialsToFile(defaultCred *GoogleDefaultCredentials, filePath string) error {
	data, err := json.Marshal(defaultCred)
	if err != nil {
		return fmt.Errorf("failed to marshal default credentials: %w", err)
	}

	_ = os.Remove(filePath)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write default credentials to file %s: %w", filePath, err)
	}
	return nil
}

func saveTokenToFile(token string, filePath string) error {
	_ = os.Remove(filePath)
	if err := os.WriteFile(filePath, []byte(token), 0644); err != nil {
		return fmt.Errorf("failed to write token to file %s: %w", filePath, err)
	}
	return nil
}
