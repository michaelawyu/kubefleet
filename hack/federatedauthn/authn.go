package federatedauthn

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientauth "k8s.io/client-go/pkg/apis/clientauthentication/v1beta1"

	"github.com/kubefleet-dev/kubefleet/apis/clusterinventory/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange"
)

type FederatedAuthnPlugin struct {
	identityTokenIssuer  identitytoken.Issuer
	accessTokenExchanger tokenexchange.Exchanger

	appConfig map[string]string
}

func NewFederatedAuthnPlugin(
	identityTokenIssuer identitytoken.Issuer,
	accessTokenExchanger tokenexchange.Exchanger,
	appConfig map[string]string,
) *FederatedAuthnPlugin {
	return &FederatedAuthnPlugin{
		identityTokenIssuer:  identityTokenIssuer,
		accessTokenExchanger: accessTokenExchanger,
		appConfig:            appConfig,
	}
}

func (f *FederatedAuthnPlugin) Name() string {
	return "federated"
}

func (f *FederatedAuthnPlugin) Credential(cp *v1alpha1.ClusterProfile) (*clientauth.ExecCredential, error) {
	var fedCPConfig *runtime.RawExtension
	for _, cp := range cp.Status.CredentialProviders {
		if cp.Name == f.Name() {
			fedCPConfig = &cp.Config
			break
		}
	}

	if fedCPConfig == nil {
		return nil, fmt.Errorf("federated authentication credential provider config not found in cluster profile %s", cp.Name)
	}

	idToken, idTokenExpiryTime, err := f.identityTokenIssuer.GetIDToken(fedCPConfig, f.appConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get ID token from federated authentication credential provider config: %w", err)
	}

	if f.accessTokenExchanger == nil {
		// No token exchanger is set up, return the ID token directly as a special case.
		exp := metav1.NewTime(idTokenExpiryTime)
		return &clientauth.ExecCredential{
			TypeMeta: metav1.TypeMeta{
				APIVersion: clientauth.SchemeGroupVersion.Identifier(),
				Kind:       "ExecCredential",
			},
			Status: &clientauth.ExecCredentialStatus{
				ExpirationTimestamp: &exp,
				Token:               idToken,
			},
		}, nil
	}

	accessToken, accessTokenExpiryTime, err := f.accessTokenExchanger.GetAccessToken(fedCPConfig, f.appConfig, idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token from federated authentication credential provider config: %w", err)
	}
	exp := metav1.NewTime(accessTokenExpiryTime)
	return &clientauth.ExecCredential{
		TypeMeta: metav1.TypeMeta{
			APIVersion: clientauth.SchemeGroupVersion.Identifier(),
			Kind:       "ExecCredential",
		},
		Status: &clientauth.ExecCredentialStatus{
			ExpirationTimestamp: &exp,
			Token:               accessToken,
		},
	}, nil
}
