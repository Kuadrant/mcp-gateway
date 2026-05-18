// Package session provides JWT-based session ID generation and validation
package session

import (
	"context"
	"errors"
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
	// sessionAudience is the audience claim for client-facing session JWTs
	sessionAudience = "mcp-gateway"
	// backendInitAudience is the audience claim for router-internal backend-init JWTs
	backendInitAudience = "mcp-router"
	// backendInitPurpose identifies a JWT issued for the hairpin backend-init flow
	backendInitPurpose = "backend-init"
	// backendInitDuration is the validity window for backend-init JWTs
	backendInitDuration = 30 * time.Second
	// signingMethodHS256 is the only allowed JWT signing method
	signingMethodHS256 = "HS256"
)

// ErrInvalidBackendInitToken is returned when a backend-init JWT cannot be validated
var ErrInvalidBackendInitToken = errors.New("invalid backend-init token")

// Deleter interface for providing session deletion
type Deleter interface {
	DeleteSessions(ctx context.Context, key ...string) error
}

var _ server.SessionIdManager = &JWTManager{}

// Claims represents the claims in a session JWT
type Claims struct {
	jwt.RegisteredClaims
}

// BackendInitClaims represents the claims in a short-lived JWT used to
// authenticate the hairpin backend-init request the router makes when lazily
// initializing a session with an upstream MCP server.
type BackendInitClaims struct {
	jwt.RegisteredClaims
	Purpose string `json:"purpose"`
	Host    string `json:"host"`
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
			Audience:  jwt.ClaimStrings{sessionAudience},
			ID:        uuid.NewString(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.signingKey)
}

// GenerateBackendInitToken creates a short-lived JWT bound to a specific upstream
// host, used to authenticate the router's hairpin backend-init request. The
// token is signed with the same HMAC key used for client session JWTs but uses
// a distinct audience and purpose claim so it cannot be confused with a client
// session token.
func (m *JWTManager) GenerateBackendInitToken(host string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("host is required for backend-init token")
	}
	now := time.Now()
	claims := BackendInitClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(backendInitDuration)),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{backendInitAudience},
			ID:        uuid.NewString(),
		},
		Purpose: backendInitPurpose,
		Host:    host,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.signingKey)
}

// ValidateBackendInitToken verifies a short-lived backend-init JWT. The token
// must be signed with the manager's HMAC key, have issuer=mcp-gateway,
// audience=mcp-router, purpose=backend-init and a host claim equal to the
// expected target host. Any mismatch or expiry returns ErrInvalidBackendInitToken.
func (m *JWTManager) ValidateBackendInitToken(tokenValue, expectedHost string) error {
	claims := &BackendInitClaims{}
	parsed, err := jwt.ParseWithClaims(tokenValue, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.signingKey, nil
	},
		jwt.WithIssuer(issuer),
		jwt.WithAudience(backendInitAudience),
		jwt.WithValidMethods([]string{signingMethodHS256}),
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBackendInitToken, err)
	}
	if !parsed.Valid {
		return ErrInvalidBackendInitToken
	}
	if claims.Purpose != backendInitPurpose {
		return fmt.Errorf("%w: wrong purpose claim", ErrInvalidBackendInitToken)
	}
	if expectedHost == "" || claims.Host != expectedHost {
		return fmt.Errorf("%w: host claim mismatch", ErrInvalidBackendInitToken)
	}
	return nil
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
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.signingKey, nil
	},
		jwt.WithIssuer(issuer),
		jwt.WithAudience(sessionAudience),
		jwt.WithValidMethods([]string{signingMethodHS256}),
	)
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
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.signingKey, nil
	},
		jwt.WithIssuer(issuer),
		jwt.WithAudience(sessionAudience),
		jwt.WithValidMethods([]string{signingMethodHS256}),
	)
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
