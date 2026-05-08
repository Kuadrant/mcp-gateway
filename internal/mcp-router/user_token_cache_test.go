package mcprouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func buildJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"exp": exp.Unix(),
		"sub": "user1",
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("%s.%s.%s", header, payload, sig)
}

func TestMemoryTokenCache_SetGetDelete(t *testing.T) {
	cache := NewMemoryUserTokenCache(5 * time.Minute)
	ctx := context.Background()

	token, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	if err != nil || ok || token != "" {
		t.Fatal("expected miss on empty cache")
	}

	if err := cache.SetUserToken(ctx, "sess1", "server1", "my-pat-token"); err != nil {
		t.Fatal(err)
	}

	token, ok, err = cache.GetUserToken(ctx, "sess1", "server1")
	if err != nil || !ok || token != "my-pat-token" {
		t.Fatalf("expected hit, got ok=%v token=%q err=%v", ok, token, err)
	}

	// different server is a miss
	_, ok, _ = cache.GetUserToken(ctx, "sess1", "server2")
	if ok {
		t.Fatal("expected miss for different server")
	}

	if err := cache.DeleteUserToken(ctx, "sess1", "server1"); err != nil {
		t.Fatal(err)
	}

	_, ok, _ = cache.GetUserToken(ctx, "sess1", "server1")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestMemoryTokenCache_OpaqueTokenNoExpiryCheck(t *testing.T) {
	cache := NewMemoryUserTokenCache(5 * time.Minute)
	ctx := context.Background()

	opaque := "ghp_abc123XYZ"
	if err := cache.SetUserToken(ctx, "sess1", "github", opaque); err != nil {
		t.Fatal(err)
	}

	token, ok, err := cache.GetUserToken(ctx, "sess1", "github")
	if err != nil || !ok || token != opaque {
		t.Fatalf("opaque token should be returned as-is, got ok=%v token=%q", ok, token)
	}
}

func TestMemoryTokenCache_ExpiredJWTDeletedOnGet(t *testing.T) {
	cache := NewMemoryUserTokenCache(5 * time.Minute)
	ctx := context.Background()

	expired := buildJWT(time.Now().Add(-1 * time.Hour))
	if err := cache.SetUserToken(ctx, "sess1", "server1", expired); err != nil {
		t.Fatal(err)
	}

	_, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expired JWT should return miss")
	}

	// verify it was deleted
	_, ok, _ = cache.GetUserToken(ctx, "sess1", "server1")
	if ok {
		t.Fatal("expired JWT should have been deleted from cache")
	}
}

func TestMemoryTokenCache_ValidJWTReturned(t *testing.T) {
	cache := NewMemoryUserTokenCache(5 * time.Minute)
	ctx := context.Background()

	valid := buildJWT(time.Now().Add(1 * time.Hour))
	if err := cache.SetUserToken(ctx, "sess1", "server1", valid); err != nil {
		t.Fatal(err)
	}

	token, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	if err != nil || !ok || token != valid {
		t.Fatalf("valid JWT should be returned, got ok=%v err=%v", ok, err)
	}
}

func TestDeriveEncryptionKey_ShortKeyRejected(t *testing.T) {
	_, err := deriveEncryptionKey([]byte("short"))
	if err == nil {
		t.Fatal("expected error for signing key shorter than 16 bytes")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	signingKey := []byte("test-signing-key-for-encryption")
	key, err := deriveEncryptionKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := "ghp_secrettoken123"
	ciphertext, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	if ciphertext == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}
	if strings.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext should not contain plaintext")
	}

	decrypted, err := decrypt(key, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1, _ := deriveEncryptionKey([]byte("key-one-long-enough"))
	key2, _ := deriveEncryptionKey([]byte("key-two-long-enough"))

	ciphertext, _ := encrypt(key1, "secret")
	_, err := decrypt(key2, ciphertext)
	if err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestCheckJWTExpiry(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		expired bool
	}{
		{"opaque PAT", "ghp_abc123", false},
		{"not base64", "a.b.c", false},
		{"expired JWT", buildJWT(time.Now().Add(-1 * time.Hour)), true},
		{"valid JWT", buildJWT(time.Now().Add(1 * time.Hour)), false},
		{"no exp claim", func() string {
			h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
			p := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
			s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
			return h + "." + p + "." + s
		}(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkUpstreamJWTExpiry(tt.token); got != tt.expired {
				t.Errorf("checkUpstreamJWTExpiry() = %v, want %v", got, tt.expired)
			}
		})
	}
}
