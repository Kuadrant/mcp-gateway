package session

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInMemoryCache_AddSession(t *testing.T) {

	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// add first session for a key
	ok, err := cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1")
	require.NoError(t, err)
	require.True(t, ok)

	// verify session was stored
	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-1", sessions["server1"])

	// add second session for same key but different server
	ok, err = cache.AddSession(ctx, "gateway-session-1", "server2", "upstream-session-2")
	require.NoError(t, err)
	require.True(t, ok)

	// verify both sessions are stored
	sessions, err = cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, "upstream-session-1", sessions["server1"])
	require.Equal(t, "upstream-session-2", sessions["server2"])
}

func TestInMemoryCache_GetSession(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// get non-existent session returns empty map
	sessions, err := cache.GetSession(ctx, "non-existent")
	require.NoError(t, err)
	require.Empty(t, sessions)

}

func TestInMemoryCache_KeyExists(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// key doesn't exist initially
	exists, err := cache.KeyExists(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.False(t, exists)

	// add session
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1")
	require.NoError(t, err)

	// key now exists
	exists, err = cache.KeyExists(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.True(t, exists)
}

func TestInMemoryCache_DeleteSessions(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// add sessions
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1")
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, "gateway-session-2", "server1", "upstream-session-2")
	require.NoError(t, err)

	// verify both exist
	exists, err := cache.KeyExists(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.True(t, exists)

	exists, err = cache.KeyExists(ctx, "gateway-session-2")
	require.NoError(t, err)
	require.True(t, exists)

	// delete first session
	err = cache.DeleteSessions(ctx, "gateway-session-1")
	require.NoError(t, err)

	// verify first is deleted but second still exists
	exists, err = cache.KeyExists(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.False(t, exists)

	exists, err = cache.KeyExists(ctx, "gateway-session-2")
	require.NoError(t, err)
	require.True(t, exists)
}

func TestInMemoryCache_UpdateExistingSession(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// add initial session
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1")
	require.NoError(t, err)

	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Equal(t, "upstream-session-1", sessions["server1"])

	// update same server with new session id
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1-updated")
	require.NoError(t, err)

	// verify session was updated
	sessions, err = cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-1-updated", sessions["server1"])
}

func TestInMemoryCache_MultipleServersPerGatewaySession(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	gatewaySession := "gateway-session-1"

	// add sessions for multiple servers
	servers := map[string]string{
		"weather-service": "weather-upstream-123",
		"time-service":    "time-upstream-456",
		"calc-service":    "calc-upstream-789",
	}

	for serverName, upstreamSession := range servers {
		_, err = cache.AddSession(ctx, gatewaySession, serverName, upstreamSession)
		require.NoError(t, err)
	}

	// verify all sessions are stored
	sessions, err := cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Len(t, sessions, 3)

	for serverName, expectedSession := range servers {
		require.Equal(t, expectedSession, sessions[serverName])
	}
}

func TestNewCache_DefaultsToInMemory(t *testing.T) {
	cache, err := NewCache()
	require.NoError(t, err)
	require.NotNil(t, cache.inmemory)
	require.Nil(t, cache.extClient)
}

func TestInMemoryCache_RemoveServerSession(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	gatewaySession := "gateway-session-1"

	// add sessions for multiple servers
	_, err = cache.AddSession(ctx, gatewaySession, "server1", "upstream-session-1")
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, gatewaySession, "server2", "upstream-session-2")
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, gatewaySession, "server3", "upstream-session-3")
	require.NoError(t, err)

	// verify all sessions are stored
	sessions, err := cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Len(t, sessions, 3)

	// remove server2 session
	err = cache.RemoveServerSession(ctx, gatewaySession, "server2")
	require.NoError(t, err)

	// verify server2 session is removed but others remain
	sessions, err = cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, "upstream-session-1", sessions["server1"])
	require.Equal(t, "upstream-session-3", sessions["server3"])
	_, exists := sessions["server2"]
	require.False(t, exists)

	// remove another session
	err = cache.RemoveServerSession(ctx, gatewaySession, "server1")
	require.NoError(t, err)

	// verify only server3 remains
	sessions, err = cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-3", sessions["server3"])

	// remove non-existent session (should not error)
	err = cache.RemoveServerSession(ctx, gatewaySession, "non-existent")
	require.NoError(t, err)

	// verify server3 still exists
	sessions, err = cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	// remove last session
	err = cache.RemoveServerSession(ctx, gatewaySession, "server3")
	require.NoError(t, err)

	// verify no sessions remain
	sessions, err = cache.GetSession(ctx, gatewaySession)
	require.NoError(t, err)
	require.Empty(t, sessions)
}

func TestInMemoryCache_RemoveServerSession_NonExistentGatewaySession(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// try to remove server session from non-existent gateway session
	err = cache.RemoveServerSession(ctx, "non-existent-gateway", "server1")
	require.NoError(t, err)
}

func TestInMemoryCache_SetAndGetClientElicitation(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	sessionID := "gateway-session-1"

	// not set initially
	val, err := cache.GetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	require.False(t, val)

	// set elicitation
	err = cache.SetClientElicitation(ctx, sessionID)
	require.NoError(t, err)

	// now returns true
	val, err = cache.GetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	require.True(t, val)
}

func TestInMemoryCache_DeleteSessionsCleansUpElicitation(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	sessionID := "gateway-session-1"

	// set elicitation and add a session
	err = cache.SetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, sessionID, "server1", "upstream-1")
	require.NoError(t, err)

	// verify both exist
	val, err := cache.GetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	require.True(t, val)
	sessions, err := cache.GetSession(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	// delete sessions cleans up both
	err = cache.DeleteSessions(ctx, sessionID)
	require.NoError(t, err)

	val, err = cache.GetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	require.False(t, val)
	sessions, err = cache.GetSession(ctx, sessionID)
	require.NoError(t, err)
	require.Empty(t, sessions)
}
