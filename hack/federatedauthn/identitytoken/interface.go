package identitytoken

import (
	"k8s.io/apimachinery/pkg/runtime"
)

type Issuer interface {
	Name() string

	GetIDToken(fedCPConfig *runtime.RawExtension) (string, error)
}
