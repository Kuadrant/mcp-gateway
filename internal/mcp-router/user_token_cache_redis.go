package mcprouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type redisTokenCache struct {
	client        *redis.Client
	encryptionKey []byte
	sessionTTL    time.Duration
	logger        *slog.Logger
}

func redisKey(sessionID string) string {
	return userCredPrefix + sessionID
}

func (c *redisTokenCache) SetUserToken(ctx context.Context, sessionID, serverName, token string) error {
	encrypted, err := encrypt(c.encryptionKey, token)
	if err != nil {
		return fmt.Errorf("encrypting user token: %w", err)
	}
	key := redisKey(sessionID)
	if err := c.client.HSet(ctx, key, serverName, encrypted).Err(); err != nil {
		return err
	}
	return c.client.Expire(ctx, key, c.sessionTTL).Err()
}

func (c *redisTokenCache) GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error) {
	encrypted, err := c.client.HGet(ctx, redisKey(sessionID), serverName).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	token, err := decrypt(c.encryptionKey, encrypted)
	if err != nil {
		return "", false, fmt.Errorf("decrypting user token: %w", err)
	}
	if checkUpstreamJWTExpiry(token) {
		if err := c.DeleteUserToken(ctx, sessionID, serverName); err != nil {
			c.logger.Debug("failed to delete expired upstream JWT from cache", "session", sessionID, "server", serverName, "error", err)
		}
		return "", false, nil
	}
	return token, true, nil
}

func (c *redisTokenCache) DeleteUserToken(ctx context.Context, sessionID, serverName string) error {
	return c.client.HDel(ctx, redisKey(sessionID), serverName).Err()
}

// NewRedisUserTokenCache returns a Redis-backed UserTokenCache with AES-GCM encryption.
// signingKey is the session JWT signing key; an encryption key is derived from it via HKDF.
// sessionTTL sets the Redis key expiry matching the session JWT lifetime.
func NewRedisUserTokenCache(client *redis.Client, signingKey []byte, sessionTTL time.Duration, logger *slog.Logger) (UserTokenCache, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	encKey, err := deriveEncryptionKey(signingKey)
	if err != nil {
		return nil, err
	}
	return &redisTokenCache{
		client:        client,
		encryptionKey: encKey,
		sessionTTL:    sessionTTL,
		logger:        logger,
	}, nil
}
