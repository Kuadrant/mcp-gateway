// Package idmap maps gateway-assigned request IDs to backend server request IDs.
package idmap

import (
	"context"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Map stores and retrieves request ID mappings.
type Map interface {
	// Store a new id mapping, returning the downstream gateway id
	Store(ctx context.Context, backendID any, serverName string, sessionID string, gatewaySessionID string) (string, error)
	// Lookup gets an entry for a gateway id without removing it.
	// Callers must call Remove explicitly after successful processing.
	Lookup(ctx context.Context, gatewayID string) (Entry, bool, error)
	// Remove is explicit best-effort removal for a gateway id
	Remove(ctx context.Context, gatewayID string)
}

// Entry holds a backend request ID and its associated server/session info.
type Entry struct {
	BackendID        any    `json:"backendID"` // per mcp spec, the ID can be string, int64, or float64
	ServerName       string `json:"serverName"`
	SessionID        string `json:"sessionID"`
	GatewaySessionID string `json:"gatewaySessionID"`
}

type mapConfig struct {
	redisClient *redis.Client
	entryTTL    time.Duration
}

// New returns an initialized Map. Pass WithRedisClient to use a Redis-backed
// store; otherwise an in-memory store is returned.
func New(opts ...func(*mapConfig)) (Map, error) {
	cfg := &mapConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.redisClient != nil {
		return newRedisMapFromClient(cfg.redisClient, cfg.entryTTL), nil
	}
	return newInMemoryMap(), nil
}

// WithRedisClient configures the Map to use an existing Redis client.
func WithRedisClient(client *redis.Client) func(*mapConfig) {
	return func(c *mapConfig) {
		c.redisClient = client
	}
}

// WithEntryTTL sets the safety-net TTL for Redis-backed entries.
// Only applies when a Redis client is configured.
func WithEntryTTL(ttl time.Duration) func(*mapConfig) {
	return func(c *mapConfig) {
		c.entryTTL = ttl
	}
}
