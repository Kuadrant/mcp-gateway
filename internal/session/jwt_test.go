package session

import (
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewJWTManager(t *testing.T) {
	t.Run("with custom key", func(t *testing.T) {
		key := "test-signing-key"
		manager, err := NewJWTManager(key, 0, testLogger(), nil)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if manager == nil {
			t.Fatal("expected manager to be created")
		}
		if string(manager.signingKey) != key {
			t.Errorf("expected signing key %s, got %s", key, string(manager.signingKey))
		}
		if manager.duration != DefaultSessionDuration {
			t.Errorf("expected duration %v, got %v", DefaultSessionDuration, manager.duration)
		}
	})

	t.Run("with custom session duration", func(t *testing.T) {
		key := "test-signing-key"
		manager, err := NewJWTManager(key, 48, testLogger(), nil)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expectedDuration := 48 * time.Minute
		if manager.duration != expectedDuration {
			t.Errorf("expected duration %v, got %v", expectedDuration, manager.duration)
		}
	})

	t.Run("with empty key returns error", func(t *testing.T) {
		manager, err := NewJWTManager("", 0, testLogger(), nil)

		if err == nil {
			t.Error("expected error for empty signing key")
		}
		if manager != nil {
			t.Error("expected nil manager for empty key")
		}
	})
}

func TestGenerate(t *testing.T) {
	manager, _ := NewJWTManager("test-key", 0, testLogger(), nil)

	t.Run("generates valid JWT", func(t *testing.T) {
		token := manager.Generate()

		if token == "" {
			t.Error("expected non-empty token")
		}

		// validate the token can be parsed
		isNotAllowed, err := manager.Validate(token)
		if err != nil {
			t.Fatalf("failed to validate token: %v", err)
		}
		if isNotAllowed {
			t.Error("expected token to be allowed")
		}
	})

	t.Run("generates tokens that can be validated", func(t *testing.T) {
		token := manager.Generate()

		// parse and check claims directly
		parsedToken, err := jwt.ParseWithClaims(token, &Claims{}, func(_ *jwt.Token) (interface{}, error) {
			return manager.signingKey, nil
		})
		if err != nil {
			t.Fatalf("failed to parse token: %v", err)
		}

		claims, ok := parsedToken.Claims.(*Claims)
		if !ok {
			t.Fatal("failed to extract claims")
		}

		if claims.Issuer != "mcp-gateway" {
			t.Errorf("expected issuer 'mcp-gateway', got %s", claims.Issuer)
		}
		if claims.IssuedAt == nil {
			t.Error("expected issued at timestamp")
		}
		if claims.ExpiresAt == nil {
			t.Error("expected expiration timestamp")
		}
		if len(claims.Audience) == 0 || claims.Audience[0] != "mcp-gateway" {
			t.Errorf("expected audience 'mcp-gateway', got %v", claims.Audience)
		}
	})
}

func TestValidate(t *testing.T) {
	manager, _ := NewJWTManager("test-key", 0, testLogger(), nil)

	t.Run("validates correct token", func(t *testing.T) {
		token := manager.Generate()

		isNotAllowed, err := manager.Validate(token)
		if err != nil {
			t.Fatalf("failed to validate valid token: %v", err)
		}
		if isNotAllowed {
			t.Error("expected token to be allowed (isNotAllowed should be false)")
		}
	})

	t.Run("rejects token with wrong signing key", func(t *testing.T) {
		otherManager, _ := NewJWTManager("different-key", 0, testLogger(), nil)
		token := otherManager.Generate()

		isNotAllowed, err := manager.Validate(token)
		if err == nil {
			t.Error("expected error for token signed with different key")
		}
		if !isNotAllowed {
			t.Error("expected isNotAllowed to be true for invalid token")
		}
	})

	t.Run("rejects invalid token format", func(t *testing.T) {
		isNotAllowed, err := manager.Validate("not-a-jwt-token")
		if err == nil {
			t.Error("expected error for invalid token format")
		}
		if !isNotAllowed {
			t.Error("expected isNotAllowed to be true for malformed token")
		}
	})

	t.Run("rejects expired token", func(t *testing.T) {
		// create a manager with very short duration
		shortManager, _ := NewJWTManager("test-key", 0, testLogger(), nil)
		shortManager.duration = 1 * time.Nanosecond

		token := shortManager.Generate()
		time.Sleep(10 * time.Millisecond)

		isNotAllowed, err := manager.Validate(token)
		if err == nil {
			t.Error("expected error for expired token")
		}
		if !isNotAllowed {
			t.Error("expected isNotAllowed to be true for expired token")
		}
	})

	t.Run("rejects token with wrong algorithm", func(t *testing.T) {
		// create token with None algorithm instead of HS256
		claims := Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: "mcp-gateway",
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		tokenString, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)

		isNotAllowed, err := manager.Validate(tokenString)
		if err == nil {
			t.Error("expected error for wrong signing algorithm")
		}
		if !isNotAllowed {
			t.Error("expected isNotAllowed to be true for wrong algorithm")
		}
	})
}

func TestBackendInitToken(t *testing.T) {
	manager, err := NewJWTManager("test-key", 0, testLogger(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("generate requires host", func(t *testing.T) {
		if _, err := manager.GenerateBackendInitToken(""); err == nil {
			t.Error("expected error when host is empty")
		}
	})

	t.Run("validates own token", func(t *testing.T) {
		token, err := manager.GenerateBackendInitToken("backend.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := manager.ValidateBackendInitToken(token, "backend.example.com"); err != nil {
			t.Errorf("expected valid token, got error: %v", err)
		}
	})

	t.Run("rejects host mismatch", func(t *testing.T) {
		token, err := manager.GenerateBackendInitToken("backend.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		err = manager.ValidateBackendInitToken(token, "attacker.example.com")
		if err == nil {
			t.Fatal("expected error for host mismatch")
		}
		if !errors.Is(err, ErrInvalidBackendInitToken) {
			t.Errorf("expected ErrInvalidBackendInitToken, got %v", err)
		}
	})

	t.Run("rejects empty expected host", func(t *testing.T) {
		token, err := manager.GenerateBackendInitToken("backend.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := manager.ValidateBackendInitToken(token, ""); err == nil {
			t.Error("expected error when expected host is empty")
		}
	})

	t.Run("rejects token signed with different key", func(t *testing.T) {
		other, _ := NewJWTManager("different-key", 0, testLogger(), nil)
		token, err := other.GenerateBackendInitToken("backend.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := manager.ValidateBackendInitToken(token, "backend.example.com"); err == nil {
			t.Error("expected error for token signed with different key")
		}
	})

	t.Run("rejects expired token", func(t *testing.T) {
		expiredToken := func() string {
			now := time.Now().Add(-2 * time.Minute)
			claims := BackendInitClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					IssuedAt:  jwt.NewNumericDate(now),
					ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
					NotBefore: jwt.NewNumericDate(now),
					Issuer:    issuer,
					Audience:  jwt.ClaimStrings{backendInitAudience},
				},
				Purpose: backendInitPurpose,
				Host:    "backend.example.com",
			}
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
			s, _ := tok.SignedString(manager.signingKey)
			return s
		}()
		if err := manager.ValidateBackendInitToken(expiredToken, "backend.example.com"); err == nil {
			t.Error("expected error for expired token")
		}
	})

	t.Run("rejects HMAC token with wrong audience", func(t *testing.T) {
		// hand-craft a token signed with the same key but with the client
		// session audience claim. Must not be accepted as a backend-init token.
		sessionToken := manager.Generate()
		if err := manager.ValidateBackendInitToken(sessionToken, "backend.example.com"); err == nil {
			t.Error("expected error: session token must not be accepted as backend-init token")
		}
	})

	t.Run("rejects token signed with None algorithm", func(t *testing.T) {
		claims := BackendInitClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * time.Second)),
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{backendInitAudience},
			},
			Purpose: backendInitPurpose,
			Host:    "backend.example.com",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err := manager.ValidateBackendInitToken(s, "backend.example.com"); err == nil {
			t.Error("expected error for token signed with None algorithm")
		}
	})

	t.Run("rejects wrong purpose claim", func(t *testing.T) {
		// craft a token that has correct iss/aud/alg but wrong purpose
		now := time.Now()
		claims := BackendInitClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{backendInitAudience},
			},
			Purpose: "something-else",
			Host:    "backend.example.com",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		s, _ := tok.SignedString(manager.signingKey)
		if err := manager.ValidateBackendInitToken(s, "backend.example.com"); err == nil {
			t.Error("expected error for wrong purpose claim")
		}
	})
}

func TestValidate_RejectsTokenWithWrongIssuer(t *testing.T) {
	manager, _ := NewJWTManager("test-key", 0, testLogger(), nil)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "evil-issuer",
			Audience:  jwt.ClaimStrings{sessionAudience},
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString(manager.signingKey)
	isNotAllowed, err := manager.Validate(s)
	if err == nil {
		t.Error("expected error for token with wrong issuer")
	}
	if !isNotAllowed {
		t.Error("expected isNotAllowed true for token with wrong issuer")
	}
}

func TestValidate_RejectsTokenWithWrongAudience(t *testing.T) {
	manager, _ := NewJWTManager("test-key", 0, testLogger(), nil)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{"some-other-audience"},
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString(manager.signingKey)
	isNotAllowed, err := manager.Validate(s)
	if err == nil {
		t.Error("expected error for token with wrong audience")
	}
	if !isNotAllowed {
		t.Error("expected isNotAllowed true for token with wrong audience")
	}
}

func TestTerminate(t *testing.T) {
	manager, _ := NewJWTManager("test-key", 0, testLogger(), nil)

	t.Run("terminate returns no error", func(t *testing.T) {
		token := manager.Generate()

		isNotAllowed, err := manager.Terminate(token)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if isNotAllowed {
			t.Error("expected isNotAllowed to be false")
		}
	})
}
