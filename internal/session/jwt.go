// Package session provides JWT-based session ID generation and validation
package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
)

const (
	// DefaultSessionDuration is the default duration for session JWTs
	DefaultSessionDuration = 24 * time.Hour
	issuer                 = "mcp-gateway"
)

// Deleter interface for providing session deletion
type Deleter interface {
	DeleteSessions(ctx context.Context, key ...string) error
}

var _ server.SessionIdManager = &JWTManager{}

// Claims represents the claims in a session JWT
type Claims struct {
	jwt.RegisteredClaims
}

// JWTManager handles JWT generation and validation for session IDs
type JWTManager struct {
	signingKey     []byte
	duration       time.Duration
	logger         *slog.Logger
	sessionDeleter Deleter
}

// NewJWTManager creates a new JWT manager with the provided signing key
func NewJWTManager(signingKey string, sessionLength int64, logger *slog.Logger, sessionHandler Deleter) (*JWTManager, error) {
	if signingKey == "" {
		return nil, fmt.Errorf("no signing key provided")
	}
	var sessionDuration = DefaultSessionDuration
	if sessionLength != 0 {
		sessionDuration = time.Duration(sessionLength) * time.Minute
	}

	return &JWTManager{
		signingKey:     []byte(signingKey),
		duration:       sessionDuration,
		logger:         logger,
		sessionDeleter: sessionHandler,
	}, nil
}

// generateSessionJWT creates a JWT token
func (m *JWTManager) generateSessionJWT() (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.duration)),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{issuer},
			ID:        uuid.NewString(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.signingKey)
}

// Generate returns a session id JWT to fullfil SessionIdManager interface
func (m *JWTManager) Generate() string {
	m.logger.Debug("generating session id in jwt session manager")
	sessID, err := m.generateSessionJWT()
	if err != nil {
		m.logger.Error("failed to generate session id", "error", err)
		return ""
	}
	return sessID
}

// Validate validates a JWT token and fulfils SessionIdManager interface. returns IsInValid as a bool
func (m *JWTManager) Validate(tokenValue string) (bool, error) {
	m.logger.Debug("validating JWT session")
	token, err := jwt.ParseWithClaims(tokenValue, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// verify signing method
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.signingKey, nil

	})
	if err != nil {
		return true, fmt.Errorf("failed to parse token: %w", err)
	}

	if !token.Valid {
		return true, nil
	}
	return false, nil
}

// GetExpiresIn returns the time a token will expire
func (m *JWTManager) GetExpiresIn(tokenValue string) (time.Time, error) {
	token, err := jwt.ParseWithClaims(tokenValue, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// verify signing method
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.signingKey, nil

	})
	if err != nil {
		return time.Now(), fmt.Errorf("failed to parse token: %w", err)
	}
	nd, err := token.Claims.GetExpirationTime()
	if err != nil {
		return time.Now(), fmt.Errorf("failed to parse token: %w", err)
	}
	return nd.Time, nil
}

// Terminate part of the SessionIDManager interface. Will remove the associated sessions from cache
func (m *JWTManager) Terminate(sessionID string) (isNotAllowed bool, err error) {
	m.logger.Info("terminate session id in jwt session manager", "session", sessionID)
	if m.sessionDeleter != nil {
		// TODO(craig) this method will be invoked by the MCPBroker so we can probably do the cache deletion there rather than in this manager
		ctx := context.TODO()
		if err := m.sessionDeleter.DeleteSessions(ctx, sessionID); err != nil {
			return false, fmt.Errorf("error clearing out associated sessions : %w", err)
		}
	}
	return false, nil
}
