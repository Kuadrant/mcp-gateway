package mcprouter

import (
	"context"
	"sync"
	"time"
)

type memoryTokenEntry struct {
	token     string
	expiresAt time.Time
}

type memoryTokenCache struct {
	mu       sync.Mutex
	data     map[string]memoryTokenEntry
	entryTTL time.Duration
	stopCh   chan struct{}
}

func memoryKey(sessionID, serverName string) string {
	return userCredPrefix + sessionID + ":" + serverName
}

func (c *memoryTokenCache) SetUserToken(_ context.Context, sessionID, serverName, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[memoryKey(sessionID, serverName)] = memoryTokenEntry{
		token:     token,
		expiresAt: time.Now().Add(c.entryTTL),
	}
	return nil
}

func (c *memoryTokenCache) GetUserToken(_ context.Context, sessionID, serverName string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := memoryKey(sessionID, serverName)
	e, ok := c.data[key]
	if !ok {
		return "", false, nil
	}
	if time.Now().After(e.expiresAt) {
		delete(c.data, key)
		return "", false, nil
	}
	if checkUpstreamJWTExpiry(e.token) {
		delete(c.data, key)
		return "", false, nil
	}
	return e.token, true, nil
}

func (c *memoryTokenCache) DeleteUserToken(_ context.Context, sessionID, serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, memoryKey(sessionID, serverName))
	return nil
}

func (c *memoryTokenCache) reapLoop() {
	ticker := time.NewTicker(c.entryTTL)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case now := <-ticker.C:
			c.mu.Lock()
			for k, e := range c.data {
				if now.After(e.expiresAt) {
					delete(c.data, k)
				}
			}
			c.mu.Unlock()
		}
	}
}

func (c *memoryTokenCache) Close() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

// NewMemoryUserTokenCache returns an in-memory UserTokenCache with TTL-based eviction.
func NewMemoryUserTokenCache(sessionTTL time.Duration) UserTokenCache {
	c := &memoryTokenCache{
		data:     make(map[string]memoryTokenEntry),
		entryTTL: sessionTTL,
		stopCh:   make(chan struct{}),
	}
	go c.reapLoop()
	return c
}
