package conformance
import (
	"testing"
	confflags "sigs.k8s.io/gateway-api/conformance/utils/flags"
)
func TestDebug(t *testing.T) {
	t.Logf("GATEWAY_CLASS = %s", *confflags.GatewayClassName)
}
