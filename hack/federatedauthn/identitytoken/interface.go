package identitytoken

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

type Issuer interface {
	Name() string

	GetIDToken(fedCPConfig *runtime.RawExtension, appConfig map[string]string) (string, time.Time, error)
}
