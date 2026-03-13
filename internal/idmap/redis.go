package idmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "elicitation:"
	// default safety-net TTL for entries that are not cleaned up via Flush (e.g. process crash)
	defaultEntryTTL = 1 * time.Hour
)

type redisMap struct {
	client   *redis.Client
	entryTTL time.Duration
}

func newRedisMapFromClient(client *redis.Client, entryTTL time.Duration) *redisMap {
	if entryTTL <= 0 {
		entryTTL = defaultEntryTTL
	}
	return &redisMap{client: client, entryTTL: entryTTL}
}

func (m *redisMap) Store(ctx context.Context, backendID any, serverName string, sessionID string, gatewaySessionID string) (string, error) {
	id := uuid.NewString()

	entry := Entry{
		BackendID:        backendID,
		ServerName:       serverName,
		SessionID:        sessionID,
		GatewaySessionID: gatewaySessionID,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal elicitation entry: %w", err)
	}

	if err := m.client.Set(ctx, keyPrefix+id, data, m.entryTTL).Err(); err != nil {
		return "", fmt.Errorf("store elicitation entry: %w", err)
	}

	return id, nil
}

func (m *redisMap) Lookup(ctx context.Context, gatewayID string) (Entry, bool, error) {
	data, err := m.client.Get(ctx, keyPrefix+gatewayID).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("lookup elicitation entry: %w", err)
	}

	entry, err := decodeEntry(data)
	if err != nil {
		return Entry{}, false, fmt.Errorf("unmarshal elicitation entry: %w", err)
	}

	return entry, true, nil
}

func (m *redisMap) Remove(ctx context.Context, gatewayID string) {
	// best-effort: errors are not propagated
	m.client.Del(ctx, keyPrefix+gatewayID)
}

// decodeEntry decodes a JSON-encoded Entry using UseNumber to preserve
// numeric precision, then normalizes BackendID back to int64 or float64.
func decodeEntry(data []byte) (Entry, error) {
	var entry Entry

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&entry); err != nil {
		return Entry{}, err
	}

	entry.BackendID = normalizeNumber(entry.BackendID)
	return entry, nil
}

// normalizeNumber converts json.Number to int64 (for integers) or float64 (for decimals).
func normalizeNumber(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}

	if i, err := n.Int64(); err == nil {
		return i
	}

	if f, err := n.Float64(); err == nil {
		return f
	}

	return v
}
