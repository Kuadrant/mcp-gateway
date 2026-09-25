package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func generateTestCAPEM(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestBuildCertPool(t *testing.T) {
	gatewayPEM := generateTestCAPEM(t, "gateway-ca")
	serverPEM := generateTestCAPEM(t, "server-ca")

	systemOnly, err := BuildCertPool("")
	require.NoError(t, err)
	require.NotNil(t, systemOnly)

	t.Run("no PEMs returns the system pool", func(t *testing.T) {
		pool, err := BuildCertPool("")
		require.NoError(t, err)
		require.NotNil(t, pool)
		require.True(t, pool.Equal(systemOnly), "should not add certs when no PEMs given")
	})

	t.Run("gateway CA is appended", func(t *testing.T) {
		pool, err := BuildCertPool(gatewayPEM)
		require.NoError(t, err)
		require.False(t, pool.Equal(systemOnly), "gateway CA should be added to system roots")
	})

	t.Run("per-server CAs are appended on top of the gateway CA", func(t *testing.T) {
		gatewayOnly, err := BuildCertPool(gatewayPEM)
		require.NoError(t, err)

		pool, err := BuildCertPool(gatewayPEM, serverPEM)
		require.NoError(t, err)
		require.False(t, pool.Equal(gatewayOnly), "per-server CA should be added as well")
	})

	t.Run("empty per-server PEMs are skipped", func(t *testing.T) {
		pool, err := BuildCertPool("", "", "")
		require.NoError(t, err)
		require.True(t, pool.Equal(systemOnly))
	})

	t.Run("invalid gateway PEM errors", func(t *testing.T) {
		pool, err := BuildCertPool("not a certificate")
		require.Error(t, err)
		require.Nil(t, pool)
		require.Contains(t, err.Error(), "gateway CA certificate bundle")
	})

	t.Run("invalid per-server PEM errors", func(t *testing.T) {
		pool, err := BuildCertPool(gatewayPEM, "not a certificate")
		require.Error(t, err)
		require.Nil(t, pool)
		require.Contains(t, err.Error(), "per-server CA certificate")
	})
}
