package idmap

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreLookup(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		entries []struct {
			backendID        any
			serverName       string
			sessionID        string
			gatewaySessionID string
		}
		expectFound   bool
		expectDeleted bool
	}{
		{
			name: "string backend ID",
			entries: []struct {
				backendID        any
				serverName       string
				sessionID        string
				gatewaySessionID string
			}{
				{backendID: "req-42", serverName: "weather-server", sessionID: "upstream-session-1", gatewaySessionID: "gw-1"},
			},
			expectFound: true,
		},
		{
			name: "int64 backend ID",
			entries: []struct {
				backendID        any
				serverName       string
				sessionID        string
				gatewaySessionID string
			}{
				{backendID: int64(42), serverName: "time-server", sessionID: "upstream-session-2", gatewaySessionID: "gw-2"},
			},
			expectFound: true,
		},
		{
			name: "float64 backend ID",
			entries: []struct {
				backendID        any
				serverName       string
				sessionID        string
				gatewaySessionID string
			}{
				{backendID: float64(3.14), serverName: "calc-server", sessionID: "upstream-session-3", gatewaySessionID: "gw-3"},
			},
			expectFound: true,
		},
		{
			name:        "lookup miss returns zero entry",
			entries:     nil,
			expectFound: false,
		},
		{
			name: "lookup does not delete entry",
			entries: []struct {
				backendID        any
				serverName       string
				sessionID        string
				gatewaySessionID string
			}{
				{backendID: "req-1", serverName: "server1", sessionID: "session-1", gatewaySessionID: "gw-1"},
			},
			expectFound:   true,
			expectDeleted: false,
		},
		{
			name: "multiple entries are independent",
			entries: []struct {
				backendID        any
				serverName       string
				sessionID        string
				gatewaySessionID string
			}{
				{backendID: "req-1", serverName: "server1", sessionID: "session-1", gatewaySessionID: "gw-1"},
				{backendID: int64(2), serverName: "server2", sessionID: "session-2", gatewaySessionID: "gw-2"},
				{backendID: float64(3.0), serverName: "server3", sessionID: "session-3", gatewaySessionID: "gw-3"},
			},
			expectFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := New()
			require.NoError(t, err)

			if len(tt.entries) == 0 {
				entry, ok, err := m.Lookup(ctx, "nonexistent")
				require.NoError(t, err)
				require.False(t, ok)
				require.Equal(t, Entry{}, entry)
				return
			}

			ids := make([]string, len(tt.entries))
			for i, e := range tt.entries {
				ids[i], err = m.Store(ctx, e.backendID, e.serverName, e.sessionID, e.gatewaySessionID)
				require.NoError(t, err)
				require.NotEmpty(t, ids[i])
			}

			// verify unique IDs
			for i := range ids {
				for j := i + 1; j < len(ids); j++ {
					require.NotEqual(t, ids[i], ids[j])
				}
			}

			for i, id := range ids {
				entry, ok, err := m.Lookup(ctx, id)
				require.NoError(t, err)
				require.Equal(t, tt.expectFound, ok)

				if tt.expectFound {
					require.IsType(t, tt.entries[i].backendID, entry.BackendID)
					require.Equal(t, tt.entries[i].backendID, entry.BackendID)
					require.Equal(t, tt.entries[i].serverName, entry.ServerName)
					require.Equal(t, tt.entries[i].sessionID, entry.SessionID)
					require.Equal(t, tt.entries[i].gatewaySessionID, entry.GatewaySessionID)
				}
			}

			if tt.expectDeleted {
				for _, id := range ids {
					_, ok, err := m.Lookup(ctx, id)
					require.NoError(t, err)
					require.False(t, ok)
				}
			}
		})
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		storeID bool
	}{
		{
			name:    "removes existing entry",
			storeID: true,
		},
		{
			name:    "no-op for unknown ID",
			storeID: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := New()
			require.NoError(t, err)

			id := "nonexistent"
			if tt.storeID {
				id, err = m.Store(ctx, "req-1", "server1", "session-1", "gw-session-1")
				require.NoError(t, err)
			}

			m.Remove(ctx, id)

			_, ok, err := m.Lookup(ctx, id)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

// TestEntryJSONRoundTrip exercises the Redis serialize/deserialize path
// (json.Marshal → decodeEntry) without requiring a live Redis instance.
func TestEntryJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		backendID any
		wantType  any
		wantValue any
	}{
		{
			name:      "string ID preserved",
			backendID: "req-42",
			wantType:  "",
			wantValue: "req-42",
		},
		{
			name:      "int64 preserved",
			backendID: int64(42),
			wantType:  int64(0),
			wantValue: int64(42),
		},
		{
			name:      "int64 zero preserved",
			backendID: int64(0),
			wantType:  int64(0),
			wantValue: int64(0),
		},
		{
			name:      "negative int64 preserved",
			backendID: int64(-100),
			wantType:  int64(0),
			wantValue: int64(-100),
		},
		{
			name:      "large int64 beyond float64 precision",
			backendID: int64(1<<53 + 1),
			wantType:  int64(0),
			wantValue: int64(1<<53 + 1),
		},
		{
			name:      "max int64 preserved",
			backendID: int64(math.MaxInt64),
			wantType:  int64(0),
			wantValue: int64(math.MaxInt64),
		},
		{
			name:      "float64 with fraction preserved",
			backendID: float64(3.14),
			wantType:  float64(0),
			wantValue: float64(3.14),
		},
		{
			name:      "float64 from json.Unmarshal normalizes to int64",
			backendID: float64(99),
			wantType:  int64(0),
			wantValue: int64(99),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := Entry{
				BackendID:        tt.backendID,
				ServerName:       "test-server",
				SessionID:        "session-1",
				GatewaySessionID: "gw-1",
			}

			data, err := json.Marshal(entry)
			require.NoError(t, err)

			got, err := decodeEntry(data)
			require.NoError(t, err)

			require.IsType(t, tt.wantType, got.BackendID)
			require.Equal(t, tt.wantValue, got.BackendID)
			require.Equal(t, "test-server", got.ServerName)
			require.Equal(t, "session-1", got.SessionID)
			require.Equal(t, "gw-1", got.GatewaySessionID)
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	m, err := New()
	require.NoError(t, err)
	var wg sync.WaitGroup

	ids := make([]string, 100)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := m.Store(ctx, int64(i), "server", "session", "gw-session")
			require.NoError(t, err)
			ids[i] = id
		}(i)
	}
	wg.Wait()

	// even indices: lookup and verify values; odd indices: remove
	results := make([]Entry, len(ids))
	found := make([]bool, len(ids))
	for i, id := range ids {
		wg.Add(1)
		if i%2 == 0 {
			go func(i int, id string) {
				defer wg.Done()
				entry, ok, err := m.Lookup(ctx, id)
				require.NoError(t, err)
				results[i] = entry
				found[i] = ok
			}(i, id)
		} else {
			go func(id string) {
				defer wg.Done()
				m.Remove(ctx, id)
			}(id)
		}
	}
	wg.Wait()

	for i := 0; i < len(ids); i += 2 {
		require.True(t, found[i], "entry %d should be found", i)
		require.Equal(t, int64(i), results[i].BackendID)
		require.Equal(t, "server", results[i].ServerName)
		require.Equal(t, "session", results[i].SessionID)
	}
}
