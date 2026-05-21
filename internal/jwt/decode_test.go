package jwt

import (
	"encoding/base64"
	"fmt"
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
