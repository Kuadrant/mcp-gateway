package elicitation

import (
	"context"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Entry holds the context for a token elicitation request.
type Entry struct {
	SessionID  string `json:"sessionID"`
	ServerName string `json:"serverName"`
	Sub        string `json:"sub,omitempty"`
}

// Map stores and retrieves token elicitation entries.
// Entries are short-lived and single-use.
type Map interface {
	Store(ctx context.Context, sessionID, serverName, sub string) (string, error)
	Lookup(ctx context.Context, elicitationID string) (Entry, bool, error)
	// Claim atomically looks up and deletes an entry, ensuring single-use.
	Claim(ctx context.Context, elicitationID string) (Entry, bool, error)
	Remove(ctx context.Context, elicitationID string)
	// Close stops background goroutines. Safe to call multiple times.
	Close()
}

type mapConfig struct {
	redisClient *redis.Client
	entryTTL    time.Duration
}

const defaultElicitationTTL = 2 * time.Minute

// New returns an initialized Map. Pass WithRedisClient to use a Redis-backed
// store; otherwise an in-memory store is returned.
func New(opts ...func(*mapConfig)) (Map, error) {
	cfg := &mapConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.entryTTL <= 0 {
		cfg.entryTTL = defaultElicitationTTL
	}
	if cfg.redisClient != nil {
		return newRedisMap(cfg.redisClient, cfg.entryTTL), nil
	}
	return newInMemoryMap(cfg.entryTTL), nil
}

// WithRedisClient configures the Map to use an existing Redis client.
func WithRedisClient(client *redis.Client) func(*mapConfig) {
	return func(c *mapConfig) {
		c.redisClient = client
	}
}

// WithEntryTTL sets the TTL for elicitation entries.
func WithEntryTTL(ttl time.Duration) func(*mapConfig) {
	return func(c *mapConfig) {
		c.entryTTL = ttl
	}
}
