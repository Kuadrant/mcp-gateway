// Package jwt provides lightweight JWT decoding utilities.
package jwt

import (
	"encoding/base64"
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
