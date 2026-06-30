// Package jwt provides lightweight JWT decoding utilities.
package jwt

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// DecodePayload decodes the payload of a JWT token (3-part dot-separated string)
// into the provided claims struct. Does NOT validate the signature.
// Returns false if the token is not a valid JWT structure.
func DecodePayload(token string, claims any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	if err := json.Unmarshal(payload, claims); err != nil {
		return false
	}
	return true
}

// LogSafeSessionID returns a log-safe identifier for a session ID. gateway
// session IDs are bearer JWTs, so the raw value must never appear in logs
// or span attributes. returns the jti claim when decodable, otherwise a
// truncated hash. deterministic, so log lines remain correlatable.
func LogSafeSessionID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var claims struct {
		JTI string `json:"jti"`
	}
	if DecodePayload(sessionID, &claims) && claims.JTI != "" {
		return "jti:" + claims.JTI
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "sha256:" + hex.EncodeToString(sum[:6])
}

// ExtractSubClaim parses a Bearer JWT from the Authorization header value and
// returns the sub claim. Does NOT validate the JWT — assumes AuthPolicy already
// verified it. Returns ("", nil) if no header value or not a Bearer JWT.
// Returns ("", error) if JWT is present but has no sub claim.
func ExtractSubClaim(authHeader string) (string, error) {
	if authHeader == "" {
		return "", nil
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", nil
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	var claims struct {
		Sub string `json:"sub"`
	}
	if !DecodePayload(token, &claims) {
		return "", nil
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("JWT has no sub claim")
	}
	return claims.Sub, nil
}
