package jwt

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func buildAuthHeader(claims string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("Bearer %s.%s.%s", header, payload, sig)
}

func TestExtractSubClaim(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		wantSub    string
		wantErr    bool
	}{
		{
			name:       "Bearer JWT with sub",
			authHeader: buildAuthHeader(`{"sub":"user123","aud":"gateway"}`),
			wantSub:    "user123",
		},
		{
			name:       "Bearer JWT without sub",
			authHeader: buildAuthHeader(`{"aud":"gateway"}`),
			wantErr:    true,
		},
		{
			name:       "empty header",
			authHeader: "",
			wantSub:    "",
		},
		{
			name:       "non-Bearer auth",
			authHeader: "Basic dXNlcjpwYXNz",
			wantSub:    "",
		},
		{
			name:       "Bearer but not a JWT",
			authHeader: "Bearer not-a-jwt",
			wantSub:    "",
		},
		{
			name:       "Bearer with malformed base64",
			authHeader: "Bearer aaa.!!!.ccc",
			wantSub:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, err := ExtractSubClaim(tt.authHeader)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sub != tt.wantSub {
				t.Fatalf("got sub=%q, want %q", sub, tt.wantSub)
			}
		})
	}
}

func TestLogSafeSessionID(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"jti":"abc-123","exp":9999999999}`))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"

	if got := LogSafeSessionID(token); got != "jti:abc-123" {
		t.Errorf("expected jti:abc-123, got %q", got)
	}

	opaque := LogSafeSessionID("not-a-jwt-token")
	if opaque == "not-a-jwt-token" || opaque == "" {
		t.Errorf("opaque IDs must be hashed, got %q", opaque)
	}
	if opaque != LogSafeSessionID("not-a-jwt-token") {
		t.Error("hashing must be deterministic")
	}
	if !strings.HasPrefix(opaque, "sha256:") {
		t.Errorf("expected sha256 prefix, got %q", opaque)
	}

	if got := LogSafeSessionID(""); got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}

	// the raw token must never appear in the output
	if strings.Contains(LogSafeSessionID(token), ".sig") {
		t.Error("output must not contain the raw token")
	}
}
