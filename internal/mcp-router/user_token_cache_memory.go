package mcprouter

import (
	"context"
	"sync"
)

type memoryTokenCache struct {
	data sync.Map
}

func memoryKey(sessionID, serverName string) string {
	return userCredPrefix + sessionID + ":" + serverName
}

func (c *memoryTokenCache) SetUserToken(_ context.Context, sessionID, serverName, token string) error {
	c.data.Store(memoryKey(sessionID, serverName), token)
	return nil
}

func (c *memoryTokenCache) GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error) {
	val, ok := c.data.Load(memoryKey(sessionID, serverName))
	if !ok {
		return "", false, nil
	}
	token := val.(string)
	if checkUpstreamJWTExpiry(token) {
		_ = c.DeleteUserToken(ctx, sessionID, serverName) // sync.Map delete cannot fail
		return "", false, nil
	}
	return token, true, nil
}

func (c *memoryTokenCache) DeleteUserToken(_ context.Context, sessionID, serverName string) error {
	c.data.Delete(memoryKey(sessionID, serverName))
	return nil
}

// NewMemoryUserTokenCache returns an in-memory UserTokenCache (no encryption)
func NewMemoryUserTokenCache() UserTokenCache {
	return &memoryTokenCache{}
}
