package broker

import (
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func newTestGatewayServer() *gatewayServer {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	return newGatewayServer(server)
}

func TestShouldDeliver_NoPendingState_Broadcasts(t *testing.T) {
	gs := newTestGatewayServer()
	require.True(t, gs.shouldDeliver("any-session"))
	require.True(t, gs.shouldDeliver(""))
}

func TestShouldDeliver_TargetedSuppressesOthers(t *testing.T) {
	gs := newTestGatewayServer()
	gs.markNotifyTarget("s1")

	require.True(t, gs.shouldDeliver("s1"))
	require.False(t, gs.shouldDeliver("s2"))
	require.False(t, gs.shouldDeliver(""))
}

func TestShouldDeliver_MultipleTargets(t *testing.T) {
	gs := newTestGatewayServer()
	gs.markNotifyTarget("s1")
	gs.markNotifyTarget("s2")

	require.True(t, gs.shouldDeliver("s1"))
	require.True(t, gs.shouldDeliver("s2"))
	require.False(t, gs.shouldDeliver("s3"))
}

func TestShouldDeliver_BroadcastOverridesTargets(t *testing.T) {
	gs := newTestGatewayServer()
	gs.markNotifyTarget("s1")
	gs.markBroadcast()

	require.True(t, gs.shouldDeliver("s1"))
	require.True(t, gs.shouldDeliver("s2"))
}

func TestShouldDeliver_ExpiredTargetsFallBackToBroadcast(t *testing.T) {
	gs := newTestGatewayServer()
	gs.markNotifyTarget("s1")

	// force the claim to expire
	gs.notifyMu.Lock()
	gs.notifyTargets["s1"] = time.Now().Add(-time.Second)
	gs.notifyMu.Unlock()

	require.True(t, gs.shouldDeliver("s2"), "expired targets must not suppress delivery")

	gs.notifyMu.Lock()
	remaining := len(gs.notifyTargets)
	gs.notifyMu.Unlock()
	require.Zero(t, remaining, "expired entries should be pruned")
}

func TestNotifyState_ConcurrentAccess(_ *testing.T) {
	gs := newTestGatewayServer()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			switch n % 3 {
			case 0:
				gs.markNotifyTarget("session")
			case 1:
				gs.markBroadcast()
			default:
				gs.shouldDeliver("session")
			}
		}(i)
	}
	wg.Wait()
}
