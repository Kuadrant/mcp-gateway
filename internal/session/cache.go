package session

import (
	"context"
	"errors"
	"sync"

	redis "github.com/redis/go-redis/v9"
)

const clientElicitationPrefix = "clientelicitation:"

// Cache implements a cache
type Cache struct {
	inmemory  *sync.Map
	extClient *redis.Client
}

// KeyExists checks if a key exists in the cache
func (c *Cache) KeyExists(ctx context.Context, key string) (bool, error) {
	if c.inmemory != nil {
		_, ok := c.inmemory.Load(key)
		return ok, nil
	}
	count, err := c.extClient.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	return false, nil

}

// GetSession returns a session from the cache
func (c *Cache) GetSession(ctx context.Context, key string) (map[string]string, error) {
	if c.inmemory != nil {
		val, ok := c.inmemory.Load(key)
		if ok {
			return val.(map[string]string), nil
		}
		return map[string]string{}, nil
	}
	return c.extClient.HGetAll(ctx, key).Result()
}

// DeleteSessions deletes sessions and associated metadata from the cache
func (c *Cache) DeleteSessions(ctx context.Context, key ...string) error {
	if c.inmemory != nil {
		for _, k := range key {
			c.inmemory.Delete(k)
			c.inmemory.Delete(clientElicitationPrefix + k)
		}
		return nil
	}
	allKeys := make([]string, 0, len(key)*2)
	for _, k := range key {
		allKeys = append(allKeys, k, clientElicitationPrefix+k)
	}
	return c.extClient.Del(ctx, allKeys...).Err()
}

// AddSession will add a session under the key. If the key exists it will append that session
func (c *Cache) AddSession(ctx context.Context, key, mcpServerID, mcpSession string) (bool, error) {
	if c.inmemory != nil {
		session, err := c.GetSession(ctx, key)
		if err != nil {
			return false, err
		}
		session[mcpServerID] = mcpSession
		c.inmemory.Store(key, session)
		return true, nil
	}
	err := c.extClient.HSet(ctx, key, mcpServerID, mcpSession).Err()
	if err != nil {
		return false, err
	}
	return true, nil
}

// RemoveServerSession remove specific server session form cache
func (c *Cache) RemoveServerSession(ctx context.Context, key, mcpServerID string) error {
	if c.inmemory != nil {
		session, err := c.GetSession(ctx, key)
		if err != nil {
			return err
		}
		delete(session, mcpServerID)
		c.inmemory.Store(key, session)
		return nil
	}
	return c.extClient.HDel(ctx, key, mcpServerID).Err()
}

// SetClientElicitation records that the client for this gateway session supports elicitation
func (c *Cache) SetClientElicitation(ctx context.Context, gatewaySessionID string) error {
	key := clientElicitationPrefix + gatewaySessionID
	if c.inmemory != nil {
		c.inmemory.Store(key, true)
		return nil
	}
	return c.extClient.Set(ctx, key, "1", 0).Err()
}

// GetClientElicitation returns whether the client for this gateway session supports elicitation
func (c *Cache) GetClientElicitation(ctx context.Context, gatewaySessionID string) (bool, error) {
	key := clientElicitationPrefix + gatewaySessionID
	if c.inmemory != nil {
		_, ok := c.inmemory.Load(key)
		return ok, nil
	}
	val, err := c.extClient.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

// NewCache returns a new cache. Pass WithRedisClient to use an external redis
// store; otherwise an in-memory cache is returned.
func NewCache(opts ...func(*Cache)) (*Cache, error) {
	c := &Cache{}
	for _, opt := range opts {
		opt(c)
	}
	if c.extClient != nil {
		return c, nil
	}
	c.inmemory = &sync.Map{}
	return c, nil
}

// WithRedisClient configures the cache to use an existing redis client
func WithRedisClient(client *redis.Client) func(c *Cache) {
	return func(c *Cache) {
		if client != nil {
			c.extClient = client
		}
	}
}
