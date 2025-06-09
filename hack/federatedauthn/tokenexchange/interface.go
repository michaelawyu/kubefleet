package tokenexchange

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

type Exchanger interface {
	TargetCloudEnvironment() string

	GetAccessToken(fedCPConfig *runtime.RawExtension, appConfig map[string]string, idToken string) (string, time.Time, error)
}
