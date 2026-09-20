// Package tlsutil builds the TLS trust pools shared by the broker, router and
// hairpin clients.
package tlsutil

import (
	"crypto/x509"
	"fmt"
)

// BuildCertPool returns the system trust pool with the gateway CA bundle and
// any per-server CAs appended, mirroring the additive trust model: system
// roots, then gatewayCAPEM, then serverCAPEMs. empty PEMs are skipped, and
// unavailable system roots fall back to an empty pool.
func BuildCertPool(gatewayCAPEM string, serverCAPEMs ...string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if gatewayCAPEM != "" && !pool.AppendCertsFromPEM([]byte(gatewayCAPEM)) {
		return nil, fmt.Errorf("failed to parse gateway CA certificate bundle PEM")
	}

	for i := range serverCAPEMs {
		if serverCAPEMs[i] == "" {
			continue
		}
		if !pool.AppendCertsFromPEM([]byte(serverCAPEMs[i])) {
			return nil, fmt.Errorf("failed to parse per-server CA certificate PEM")
		}
	}

	return pool, nil
}
