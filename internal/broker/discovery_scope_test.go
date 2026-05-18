package broker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScopeStore_SetAndGet(t *testing.T) {
	store := newScopeStore(time.Hour, 1000)
	defer store.stop()

	state, tools := store.getScope("session-1")
	require.Equal(t, scopeUnset, state)
	require.Nil(t, tools)

	store.setScope("session-1", []string{"tool_a", "tool_b"})
	state, tools = store.getScope("session-1")
	require.Equal(t, scopeFiltered, state)
	require.Len(t, tools, 2)
	_, ok := tools["tool_a"]
	require.True(t, ok)
}

func TestScopeStore_DefensiveCopy(t *testing.T) {
	store := newScopeStore(time.Hour, 1000)
	defer store.stop()

	store.setScope("s1", []string{"a", "b"})
	_, tools1 := store.getScope("s1")
	tools1["c"] = struct{}{} // mutate the copy

	_, tools2 := store.getScope("s1")
	require.Len(t, tools2, 2, "mutation of returned copy must not affect store")
}

func TestScopeStore_Reset(t *testing.T) {
	store := newScopeStore(time.Hour, 1000)
	defer store.stop()

	store.setScope("s1", []string{"a"})
	store.resetScope("s1")

	state, tools := store.getScope("s1")
	require.Equal(t, scopeAll, state)
	require.Nil(t, tools)
}

func TestScopeStore_Delete(t *testing.T) {
	store := newScopeStore(time.Hour, 1000)
	defer store.stop()

	store.setScope("s1", []string{"a"})
	store.deleteScope("s1")

	state, _ := store.getScope("s1")
	require.Equal(t, scopeUnset, state)
}

func TestScopeStore_Size(t *testing.T) {
	store := newScopeStore(time.Hour, 1000)
	defer store.stop()

	require.Equal(t, 0, store.size())
	store.setScope("s1", []string{"a"})
	store.setScope("s2", []string{"b"})
	require.Equal(t, 2, store.size())
}

func TestScopeStore_TTLExpiry(t *testing.T) {
	store := newScopeStore(10*time.Millisecond, 1000)
	defer store.stop()

	store.setScope("s1", []string{"a"})

	require.Eventually(t, func() bool {
		state, _ := store.getScope("s1")
		return state == scopeUnset
	}, 200*time.Millisecond, 10*time.Millisecond, "expired scope should return unset")
}

func TestScopeStore_MaxSizeEviction(t *testing.T) {
	store := newScopeStore(time.Hour, 2)
	defer store.stop()

	store.setScope("s1", []string{"a"})
	store.setScope("s2", []string{"b"})
	// third entry should evict oldest
	store.setScope("s3", []string{"c"})

	require.LessOrEqual(t, store.size(), 2)

	state, _ := store.getScope("s1")
	require.Equal(t, scopeUnset, state, "oldest entry should be evicted")
	state2, _ := store.getScope("s2")
	require.NotEqual(t, scopeUnset, state2, "s2 should still exist")
	state3, _ := store.getScope("s3")
	require.NotEqual(t, scopeUnset, state3, "s3 should still exist")
}

func TestScopeStore_ResetMaxSizeEviction(t *testing.T) {
	store := newScopeStore(time.Hour, 2)
	defer store.stop()

	store.setScope("s1", []string{"a"})
	store.setScope("s2", []string{"b"})
	// reset on a new session should evict oldest
	store.resetScope("s3")

	require.LessOrEqual(t, store.size(), 2)

	state, _ := store.getScope("s1")
	require.Equal(t, scopeUnset, state, "oldest entry should be evicted")
	state2, _ := store.getScope("s2")
	require.NotEqual(t, scopeUnset, state2, "s2 should still exist")
	state3, _ := store.getScope("s3")
	require.NotEqual(t, scopeUnset, state3, "s3 should still exist")

	// reset on an existing session should not evict
	store.resetScope("s3")
	require.Equal(t, 2, store.size())
}
