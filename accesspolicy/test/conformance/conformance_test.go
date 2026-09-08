package conformance

import (
	"testing"

	"sigs.k8s.io/kube-agentic-networking/conformance"
)

func TestConformance(t *testing.T) {
	conformance.RunConformance(t)
}
