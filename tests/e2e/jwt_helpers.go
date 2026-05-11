//go:build e2e

package e2e

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"

	jwt "github.com/golang-jwt/jwt/v5"
)

// testHeaderSigningKey is the EC private key used to sign test JWTs
const testHeaderSigningKey = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIEY3QeiP9B9Bm3NHG3SgyiDHcbckwsGsQLKgv4fJxjJWoAoGCCqGSM49
AwEHoUQDQgAE7WdMdvC8hviEAL4wcebqaYbLEtVOVEiyi/nozagw7BaWXmzbOWyy
95gZLirTkhUb1P4Z4lgKLU2rD5NCbGPHAA==
-----END EC PRIVATE KEY-----`

// GetTestHeaderSigningKey returns the EC private key for signing test headers
func GetTestHeaderSigningKey() string {
	return testHeaderSigningKey
}

// CreateAuthorizedCapabilitiesJWT creates a signed JWT for the x-mcp-authorized header
// allowedTools is a map of server namespace/name to list of tool names
func CreateAuthorizedCapabilitiesJWT(allowedTools map[string][]string) (string, error) {
	keyBytes := []byte(testHeaderSigningKey)
	capabilities := map[string]map[string][]string{
		"tools": allowedTools,
	}
	claimPayload, err := json.Marshal(capabilities)
	if err != nil {
		return "", fmt.Errorf("failed to marshal allowed capabilities: %w", err)
	}

	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	parsedKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse EC private key: %w", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"allowed-capabilities": string(claimPayload),
	})

	jwtToken, err := token.SignedString(parsedKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return jwtToken, nil
}
