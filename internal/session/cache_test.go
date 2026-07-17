package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryCache_AddSession(t *testing.T) {

	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// add first session for a key
	ok, err := cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1", 0)
	require.NoError(t, err)
	require.True(t, ok)

	// verify session was stored
	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-1", sessions["server1"])

	// add second session for same key but different server
	ok, err = cache.AddSession(ctx, "gateway-session-1", "server2", "upstream-session-2", 0)
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
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1", 0)
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
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1", 0)
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, "gateway-session-2", "server1", "upstream-session-2", 0)
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
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1", 0)
	require.NoError(t, err)

	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Equal(t, "upstream-session-1", sessions["server1"])

	// update same server with new session id
	_, err = cache.AddSession(ctx, "gateway-session-1", "server1", "upstream-session-1-updated", 0)
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
		_, err = cache.AddSession(ctx, gatewaySession, serverName, upstreamSession, 0)
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
	_, err = cache.AddSession(ctx, gatewaySession, "server1", "upstream-session-1", 0)
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, gatewaySession, "server2", "upstream-session-2", 0)
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, gatewaySession, "server3", "upstream-session-3", 0)
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
	err = cache.SetClientElicitation(ctx, sessionID, 0)
	require.NoError(t, err)

	// now returns true
	val, err = cache.GetClientElicitation(ctx, sessionID)
	require.NoError(t, err)
	require.True(t, val)
}

func TestInMemoryCache_AddSession_Concurrent(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	// seed one entry so concurrent callers retrieve the same stored map reference
	_, err = cache.AddSession(ctx, "gateway-session-1", "seed", "seed-session", 0)
	require.NoError(t, err)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_, addErr := cache.AddSession(ctx, "gateway-session-1",
				fmt.Sprintf("server%d", i),
				fmt.Sprintf("upstream-session-%d", i),
				0,
			)
			assert.NoError(t, addErr)
		}(i)
	}
	wg.Wait()

	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Len(t, sessions, n+1) // n concurrent entries + seed
}

func TestInMemoryCache_RemoveServerSession_Concurrent(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	const n = 50
	for i := range n {
		_, err = cache.AddSession(ctx, "gateway-session-1",
			fmt.Sprintf("server%d", i),
			fmt.Sprintf("upstream-session-%d", i),
			0,
		)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, cache.RemoveServerSession(ctx, "gateway-session-1",
				fmt.Sprintf("server%d", i),
			))
		}(i)
	}
	wg.Wait()

	sessions, err := cache.GetSession(ctx, "gateway-session-1")
	require.NoError(t, err)
	require.Empty(t, sessions)
}

func TestInMemoryCache_DeleteSessions_Concurrent(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)

	// concurrent AddSession and DeleteSessions on the same key
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_, addErr := cache.AddSession(ctx, "gateway-session-1",
				fmt.Sprintf("server%d", i),
				fmt.Sprintf("upstream-session-%d", i),
				0,
			)
			assert.NoError(t, addErr)
		}(i)

		go func() {
			defer wg.Done()
			assert.NoError(t, cache.DeleteSessions(ctx, "gateway-session-1"))
		}()
	}
	wg.Wait()
}

func TestInMemoryCache_DeleteSessionsCleansUpElicitation(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	sessionID := "gateway-session-1"

	// set elicitation and add a session
	err = cache.SetClientElicitation(ctx, sessionID, 0)
	require.NoError(t, err)
	_, err = cache.AddSession(ctx, sessionID, "server1", "upstream-1", 0)
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

// --- User token tests ---

func buildJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"exp": exp.Unix(),
		"sub": "user1",
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("%s.%s.%s", header, payload, sig)
}

func TestCache_SetGetDeleteUserToken(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	token, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, token)

	err = cache.SetUserToken(ctx, "sess1", "server1", "my-pat-token", 0)
	require.NoError(t, err)

	token, ok, err = cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "my-pat-token", token)

	// different server is a miss
	_, ok, err = cache.GetUserToken(ctx, "sess1", "server2")
	require.NoError(t, err)
	require.False(t, ok)

	err = cache.DeleteUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)

	_, ok, err = cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCache_OpaqueTokenNoExpiryCheck(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	opaque := "ghp_abc123XYZ"
	err = cache.SetUserToken(ctx, "sess1", "github", opaque, 0)
	require.NoError(t, err)

	token, ok, err := cache.GetUserToken(ctx, "sess1", "github")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, opaque, token)
}

func TestCache_SetUserTokenRedisTTL(t *testing.T) {
	ctx := context.Background()
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})
	cache, err := NewCache(WithRedisClient(client))
	require.NoError(t, err)

	err = cache.SetUserToken(ctx, "sess1", "github", "ghp_abc123XYZ", time.Hour)
	require.NoError(t, err)

	ttl, err := client.TTL(ctx, "sess1").Result()
	require.NoError(t, err)
	require.Positive(t, ttl)

	redisServer.FastForward(time.Hour + time.Second)
	_, ok, err := cache.GetUserToken(ctx, "sess1", "github")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCache_ExpiredJWTDeletedOnGet(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	expired := buildJWT(time.Now().Add(-1 * time.Hour))
	err = cache.SetUserToken(ctx, "sess1", "server1", expired, 0)
	require.NoError(t, err)

	_, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.False(t, ok, "expired JWT should return miss")

	// verify it was deleted
	_, ok, err = cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.False(t, ok, "expired JWT should have been deleted")
}

func TestCache_ValidJWTReturned(t *testing.T) {
	ctx := context.Background()
	cache, err := NewCache()
	require.NoError(t, err)

	valid := buildJWT(time.Now().Add(1 * time.Hour))
	err = cache.SetUserToken(ctx, "sess1", "server1", valid, 0)
	require.NoError(t, err)

	token, ok, err := cache.GetUserToken(ctx, "sess1", "server1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, valid, token)
}

func TestDeriveEncryptionKey_ShortKeyRejected(t *testing.T) {
	_, err := DeriveEncryptionKey([]byte("short"))
	require.Error(t, err)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := DeriveEncryptionKey([]byte("test-signing-key-for-encryption-32"))
	require.NoError(t, err)

	plaintext := "ghp_secrettoken123"
	ciphertext, err := encrypt(key, plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, ciphertext)
	require.False(t, strings.Contains(ciphertext, plaintext))

	decrypted, err := decrypt(key, ciphertext)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1, _ := DeriveEncryptionKey([]byte("key-one-long-enough-for-32-bytes"))
	key2, _ := DeriveEncryptionKey([]byte("key-two-long-enough-for-32-bytes"))

	ciphertext, _ := encrypt(key1, "secret")
	_, err := decrypt(key2, ciphertext)
	require.Error(t, err)
}

func TestCheckJWTExpiry(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		expired bool
	}{
		{"opaque PAT", "ghp_abc123", false},
		{"not base64", "a.b.c", false},
		{"expired JWT", buildJWT(time.Now().Add(-1 * time.Hour)), true},
		{"valid JWT", buildJWT(time.Now().Add(1 * time.Hour)), false},
		{"no exp claim", func() string {
			h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
			p := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
			s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
			return h + "." + p + "." + s
		}(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expired, checkUpstreamJWTExpiry(tt.token))
		})
	}
}

// TestAddSession_TTLPassedToRedis verifies that a positive TTL is forwarded to
// the Redis Expire command and that TTL=0 results in no expiry being set.
// This test uses the in-memory path as a contract smoke-test; the Redis pipeline
// path is exercised in integration tests that start a real Redis instance.
func TestAddSession_TTLPassedToRedis(t *testing.T) {
	ctx := context.Background()

	t.Run("zero TTL is accepted in-memory", func(t *testing.T) {
		cache, err := NewCache()
		require.NoError(t, err)

		ok, err := cache.AddSession(ctx, "sess", "srv", "remote", 0)
		require.NoError(t, err)
		require.True(t, ok)

		sessions, err := cache.GetSession(ctx, "sess")
		require.NoError(t, err)
		require.Equal(t, "remote", sessions["srv"])
	})

	t.Run("positive TTL is accepted in-memory", func(t *testing.T) {
		cache, err := NewCache()
		require.NoError(t, err)

		ok, err := cache.AddSession(ctx, "sess2", "srv", "remote2", time.Hour)
		require.NoError(t, err)
		require.True(t, ok)

		sessions, err := cache.GetSession(ctx, "sess2")
		require.NoError(t, err)
		require.Equal(t, "remote2", sessions["srv"])
	})
}

// TestSetClientElicitation_TTL verifies that TTL is accepted by both the
// zero and positive cases for in-memory mode.
func TestSetClientElicitation_TTL(t *testing.T) {
	ctx := context.Background()

	t.Run("zero TTL", func(t *testing.T) {
		cache, err := NewCache()
		require.NoError(t, err)

		require.NoError(t, cache.SetClientElicitation(ctx, "sess", 0))
		ok, err := cache.GetClientElicitation(ctx, "sess")
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("positive TTL", func(t *testing.T) {
		cache, err := NewCache()
		require.NoError(t, err)

		require.NoError(t, cache.SetClientElicitation(ctx, "sess2", time.Hour))
		ok, err := cache.GetClientElicitation(ctx, "sess2")
		require.NoError(t, err)
		require.True(t, ok)
	})
}
