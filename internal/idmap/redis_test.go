package idmap

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestRedisMap(t *testing.T, ttl time.Duration) (*redisMap, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return newRedisMapFromClient(client, ttl), srv
}

func TestRedisMap_StoreLookup(t *testing.T) {
	ctx := context.Background()

	tests := map[string]any{
		"string ID":      "req-42",
		"int64 ID":       int64(42),
		"large int64 ID": int64(1<<53 + 1),
		"float64 ID":     3.14,
	}

	for name, backendID := range tests {
		t.Run(name, func(t *testing.T) {
			m, srv := newTestRedisMap(t, 0)

			id, err := m.Store(ctx, backendID, "server1", "session-1", "gw-1")
			require.NoError(t, err)
			require.True(t, srv.Exists("elicitation:"+id))

			entry, ok, err := m.Lookup(ctx, id)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, Entry{BackendID: backendID, ServerName: "server1", SessionID: "session-1", GatewaySessionID: "gw-1"}, entry)
		})
	}
}

func TestRedisMap_Remove(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestRedisMap(t, 0)

	id, err := m.Store(ctx, "req-1", "server1", "session-1", "gw-1")
	require.NoError(t, err)

	m.Remove(ctx, id)

	entry, ok, err := m.Lookup(ctx, id)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, Entry{}, entry)
}

func TestRedisMap_CorruptValue(t *testing.T) {
	m, srv := newTestRedisMap(t, 0)
	require.NoError(t, srv.Set("elicitation:bad", "not-json"))

	_, ok, err := m.Lookup(context.Background(), "bad")
	require.Error(t, err)
	require.False(t, ok)
}

func TestRedisMap_EntryTTL(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		ttl, want time.Duration
	}{
		"default TTL": {ttl: 0, want: defaultEntryTTL},
		"custom TTL":  {ttl: 5 * time.Minute, want: 5 * time.Minute},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m, srv := newTestRedisMap(t, tt.ttl)

			id, err := m.Store(ctx, "req-1", "server1", "session-1", "gw-1")
			require.NoError(t, err)
			require.Equal(t, tt.want, srv.TTL("elicitation:"+id))

			srv.FastForward(tt.want + time.Second)
			_, ok, err := m.Lookup(ctx, id)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}
