package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"golang.org/x/crypto/hkdf"
)

// DeriveEncryptionKey derives a 32-byte AES-256 key from the session signing key using HKDF.
func DeriveEncryptionKey(signingKey []byte) ([]byte, error) {
	if len(signingKey) < 16 {
		return nil, fmt.Errorf("signing key too short: need at least 16 bytes")
	}
	r := hkdf.New(sha256.New, signingKey, nil, []byte("mcp-gateway-user-token-encryption"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("hkdf key derivation failed: %w", err)
	}
	return key, nil
}

func encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("encrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("encrypt: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: generating nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(key []byte, encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decrypt: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("decrypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("decrypt: new gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("decrypt: ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: gcm open: %w", err)
	}
	return string(plaintext), nil
}

// checkUpstreamJWTExpiry returns true if the token looks like a JWT and is expired.
// Non-JWT tokens (opaque PATs) return false.
func checkUpstreamJWTExpiry(token string) bool {
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if !internaljwt.DecodePayload(token, &claims) || claims.Exp == nil {
		return false
	}
	return time.Unix(int64(*claims.Exp), 0).Before(time.Now())
}
