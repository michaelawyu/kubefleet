package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kubefleet-dev/kubefleet/apis/clusterinventory/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken/aksoidc"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken/spiffe"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange/akspubliccloud"
	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/tokenexchange/gke"
)

var (
	idTokenIssuer      string
	tokenExchanger     string
	clusterProfileYAML string
)

var (
	aksOIDCIssuerProjectedServiceAccountTokenPath       string
	aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds string

	spireAgentSocketPath string
	svidTTLSeconds       string
)

var (
	aksClientID string

	gkeDefaultCredPath string
	gkeTokenPath       string
)

var rootCmd = &cobra.Command{
	Use:   "federated-authn",
	Short: "kubeconfig exec plugin for federated identity based authentication",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Running federated authentication plugin with ID token issuer: %s\n", idTokenIssuer)
		fmt.Printf("Running federated authentication plugin with token exchanger: %s\n", tokenExchanger)

		var clusterProfile *v1alpha1.ClusterProfile
		if err := json.Unmarshal([]byte(clusterProfileYAML), &clusterProfile); err != nil {
			return fmt.Errorf("failed to unmarshal cluster profile YAML: %w", err)
		}

		fmt.Printf("Cluster profile loaded: %s (display name: %s), managed by %s", clusterProfile.Name, clusterProfile.Spec.DisplayName, clusterProfile.Spec.ClusterManager)

		var idTokenIssuerImpl identitytoken.Issuer
		var tokenExchangerImpl tokenexchange.Exchanger
		appConfig := map[string]string{}
		switch idTokenIssuer {
		case "aks-oidc-issuer":
			idTokenIssuerImpl = &aksoidc.AKSOIDCIssuer{}
			appConfig["aksOIDCIssuerProjectedServiceAccountTokenPath"] = aksOIDCIssuerProjectedServiceAccountTokenPath
			appConfig["aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds"] = aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds
		case "spiffe":
			idTokenIssuerImpl = &spiffe.SPIFFEIssuer{}
			appConfig["spireAgentSocketPath"] = spireAgentSocketPath
			appConfig["svidTTLSeconds"] = svidTTLSeconds
		default:
			return fmt.Errorf("unsupported ID token issuer: %s", idTokenIssuer)
		}

		switch tokenExchanger {
		case "aks-public-cloud":
			tokenExchangerImpl = &akspubliccloud.AKSPublicCloudTokenExchanger{}
			appConfig["aksClientID"] = aksClientID
		case "gke":
			tokenExchangerImpl = &gke.GKETokenExchanger{}
			appConfig["gkeDefaultCredPath"] = gkeDefaultCredPath
			appConfig["gkeTokenPath"] = gkeTokenPath
		default:
			return fmt.Errorf("unsupported token exchanger: %s", tokenExchanger)
		}

		fedAuthnImpl := federatedauthn.NewFederatedAuthnPlugin(
			idTokenIssuerImpl,
			tokenExchangerImpl,
			appConfig,
		)
		ec, err := fedAuthnImpl.Credential(clusterProfile)
		if err != nil {
			return fmt.Errorf("failed to get credential from federated authentication plugin: %w", err)
		}

		// Pretty print the ExecCredential.
		ecJSON, err := json.MarshalIndent(ec, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal ExecCredential: %w", err)
		}

		_, _ = fmt.Fprint(os.Stdout, ecJSON)
		return nil
	},
}

func init() {
	rootCmd.Flags().StringVar(&idTokenIssuer, "id-token-issuer", "", "The identity token issuer to use for federated authentication")
	rootCmd.MarkFlagRequired("id-token-issuer")
	rootCmd.Flags().StringVar(&tokenExchanger, "token-exchanger", "", "The token exchanger to use for federated authentication")
	rootCmd.MarkFlagRequired("token-exchanger")
	rootCmd.Flags().StringVar(&clusterProfileYAML, "cluster-profile", "", "Path to the cluster profile YAML file")
	rootCmd.MarkFlagRequired("cluster-profile")

	rootCmd.Flags().StringVar(&aksOIDCIssuerProjectedServiceAccountTokenPath, "aks-oidc-issuer-projected-service-account-token-path", "/var/run/secrets/tokens/aks-oidc-issuer", "Path to the projected service account token for AKS OIDC issuer")
	rootCmd.Flags().StringVar(&aksOIDCIssuerProjectedServiceAccountTokenTTLSeconds, "aks-oidc-issuer-projected-service-account-token-ttl-seconds", "3600", "TTL in seconds for the projected service account token for AKS OIDC issuer")

	rootCmd.Flags().StringVar(&spireAgentSocketPath, "spire-agent-socket-path", "/run/spire/sockets/agent.sock", "Path to the SPIRE agent socket for SPIFFE issuer")
	rootCmd.Flags().StringVar(&svidTTLSeconds, "svid-ttl-seconds", "3600", "TTL in seconds for the SVID issued by SPIFFE issuer")

	rootCmd.Flags().StringVar(&aksClientID, "aks-client-id", "", "Client ID for AKS OIDC issuer")

	rootCmd.Flags().StringVar(&gkeDefaultCredPath, "gke-default-cred-path", "", "Path to the GKE default credentials file")
	rootCmd.Flags().StringVar(&gkeTokenPath, "gke-token-path", "", "Path to the GKE token file")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("failed to run the authn plugin: %v", err)
		os.Exit(1)
	}
	os.Exit(0)
}
