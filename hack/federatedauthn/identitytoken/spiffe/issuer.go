package spiffe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"github.com/spiffe/spire/pkg/common/util"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubefleet-dev/kubefleet/hack/federatedauthn/identitytoken"
)

var _ identitytoken.Issuer = &SPIFFEIssuer{}

type SPIFFEIssuer struct{}

func (issuer *SPIFFEIssuer) Name() string {
	return "spiffe"
}

// TO-DO (chenyu1): cache the ID token as needed.
func (issuer *SPIFFEIssuer) GetIDToken(fedCPConfig *runtime.RawExtension, appConfig map[string]string) (string, time.Time, error) {
	audience, err := retrieveAudienceFromFedCPConfig(fedCPConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve agent socket path and audience: %w", err)
	}

	agentSocketPath, expiryTime, err := retrieveAgentSocketPathAndExpiryTimeFromAppConfig(appConfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve agent socket path and expiry time: %w", err)
	}

	socket, err := net.ResolveUnixAddr("unix", agentSocketPath)
	target, err := util.GetTargetName(socket)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get target name from agent socket path %s: %w", agentSocketPath, err)
	}
	conn, err := util.NewGRPCClient(target)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create gRPC client for agent socket path %s: %w", agentSocketPath, err)
	}
	client := workload.NewSpiffeWorkloadAPIClient(conn)

	ctx := context.Background()
	svid, err := client.FetchJWTSVID(ctx, &workload.JWTSVIDRequest{
		Audience: []string{audience},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to fetch JWT SVID from agent socket path %s: %w", agentSocketPath, err)
	}
	return svid.Svids[0].Svid, expiryTime, nil
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

func retrieveAgentSocketPathAndExpiryTimeFromAppConfig(appConfig map[string]string) (string, time.Time, error) {
	agentSocketPath := appConfig["spireAgentSocketPath"]
	if len(agentSocketPath) == 0 {
		return "", time.Time{}, fmt.Errorf("spireAgentSocketPath is not set in the application config")
	}

	ttl := appConfig["svidTTLSeconds"]
	if len(ttl) == 0 {
		return "", time.Time{}, fmt.Errorf("svidTTLSeconds is not set in the application config")
	}
	ttlSeconds, err := strconv.Atoi(ttl)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse svidTTLSeconds: %w", err)
	}
	ttlSecondsDuration := int64(float64(time.Duration(ttlSeconds) * time.Second))
	return agentSocketPath, time.Now().Add(time.Duration(ttlSecondsDuration)), nil
}
