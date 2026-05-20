package elicitation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
)

const tokenElicitationPrefix = "tokenelicitation:"

type redisMap struct {
	client   *redis.Client
	entryTTL time.Duration
}

func newRedisMap(client *redis.Client, entryTTL time.Duration) *redisMap {
	return &redisMap{client: client, entryTTL: entryTTL}
}

func (m *redisMap) Store(ctx context.Context, sessionID, serverName, sub string) (string, error) {
	id := uuid.NewString()
	entry := Entry{
		SessionID:  sessionID,
		ServerName: serverName,
		Sub:        sub,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal token elicitation entry: %w", err)
	}
	if err := m.client.Set(ctx, tokenElicitationPrefix+id, data, m.entryTTL).Err(); err != nil {
		return "", fmt.Errorf("store token elicitation entry: %w", err)
	}
	return id, nil
}

func (m *redisMap) Lookup(ctx context.Context, elicitationID string) (Entry, bool, error) {
	data, err := m.client.Get(ctx, tokenElicitationPrefix+elicitationID).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("lookup token elicitation entry: %w", err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false, fmt.Errorf("unmarshal token elicitation entry: %w", err)
	}
	return entry, true, nil
}

func (m *redisMap) Claim(ctx context.Context, elicitationID string) (Entry, bool, error) {
	data, err := m.client.GetDel(ctx, tokenElicitationPrefix+elicitationID).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("claim token elicitation entry: %w", err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false, fmt.Errorf("unmarshal token elicitation entry: %w", err)
	}
	return entry, true, nil
}

func (m *redisMap) Remove(ctx context.Context, elicitationID string) {
	m.client.Del(ctx, tokenElicitationPrefix+elicitationID)
}

func (m *redisMap) Close() {}
