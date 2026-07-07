package broker

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionResurrection_ValidUnknownIDServed(t *testing.T) {
	h := newResurrectionHarness(t)

	// sessions created through the wrapped handler still work
	cs := h.connect(t)
	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, 0, h.liveSize(), "sdk-owned session must not be resurrected")

	// a valid JWT this pod has never seen is resurrected and served
	res := h.postToolsList(t, "valid-restart-1")
	require.Equal(t, http.StatusOK, res.status)
	require.Contains(t, res.body, `"tools"`)
	require.Equal(t, 1, h.liveSize())

	// subsequent requests reuse the resurrected session
	res = h.postToolsList(t, "valid-restart-1")
	require.Equal(t, http.StatusOK, res.status)
	require.Equal(t, 1, h.liveSize())
	require.Equal(t, 1, h.serverSessionCount("valid-restart-1"))
}

func TestSessionResurrection_InvalidJWTStill404(t *testing.T) {
	h := newResurrectionHarness(t)

	res := h.postToolsList(t, "bogus-1")
	require.Equal(t, http.StatusNotFound, res.status)
	require.Equal(t, sessionNotFoundBody, res.body, "the withheld 404 must be released unchanged")
	require.Equal(t, 0, h.liveSize())
}

func TestSessionResurrection_DeleteClosesAndEvicts(t *testing.T) {
	h := newResurrectionHarness(t)
	sid := "valid-del-1"

	res := h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
	require.Equal(t, 1, h.liveSize())

	// seed per-session state that session-end cleanup must release
	var initCount atomic.Int32
	upstreamTS := newTestMCPServer(&initCount, "upstream-sess")
	defer upstreamTS.Close()
	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: upstreamTS.URL, prefix: "us_"}
	seedUserSession(t, h.b, srv, sid)
	require.Equal(t, 1, poolSize(h.b))

	res = h.do(t, http.MethodDelete, sid, "", nil)
	require.Equal(t, http.StatusNoContent, res.status)

	require.Eventually(t, func() bool {
		_, terminated := h.terminated.Load(sid)
		return h.liveSize() == 0 && poolSize(h.b) == 0 && terminated
	}, 5*time.Second, 20*time.Millisecond,
		"DELETE must close the resurrected session and fire pool eviction and terminator")

	// the JWT is still valid, so continued use resurrects again (parity
	// with stateless validation on main)
	res = h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
}

// a DELETE for a valid JWT this pod has never seen must not connect a
// session solely to close it: the withheld 404 is released instead. the
// production caller (dropSession/serveDELETE) discards the response.
func TestSessionResurrection_DeleteUnknownIDNotResurrected(t *testing.T) {
	h := newResurrectionHarness(t)

	res := h.do(t, http.MethodDelete, "valid-unknown-1", "", nil)
	require.Equal(t, http.StatusNotFound, res.status)
	require.Equal(t, 0, h.liveSize(), "DELETE must not resurrect")
	require.Equal(t, 0, h.serverSessionCount("valid-unknown-1"))
}

func TestSessionResurrection_InvalidatedMidSessionClosedWith404(t *testing.T) {
	h := newCompatHarness(t)
	sid := "valid-expire-1"

	res := h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
	require.Equal(t, 1, h.liveSize())

	h.invalid.Store(true)

	// the compat gate 404s the invalidated session and drops it; once the
	// Wait goroutine clears the live table, resurrection is refused too
	require.Eventually(t, func() bool {
		res := h.postToolsList(t, sid)
		return res.status == http.StatusNotFound && h.liveSize() == 0
	}, 5*time.Second, 20*time.Millisecond)
}

// idle eviction of an SDK-owned session must be invisible: the SDK's
// SessionTimeout closes it, the InitializedHandler-wired cleanup releases
// per-session state, and the same JWT keeps working via resurrection.
func TestSessionResurrection_IdleEvictionInvisible_SDKSession(t *testing.T) {
	h := newResurrectionHarnessIdle(t, 300*time.Millisecond)
	cs := h.connect(t)
	sid := cs.ID()
	require.NotEmpty(t, sid)

	// seed per-session state that idle eviction must release
	var initCount atomic.Int32
	upstreamTS := newTestMCPServer(&initCount, "upstream-sess")
	defer upstreamTS.Close()
	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: upstreamTS.URL, prefix: "us_"}
	seedUserSession(t, h.b, srv, sid)
	require.Equal(t, 1, poolSize(h.b))

	// no client requests: the SDK timeout closes the session and the full
	// cleanup chain fires (pool eviction, terminator)
	require.Eventually(t, func() bool {
		return poolSize(h.b) == 0 && h.isTerminated(sid)
	}, 5*time.Second, 20*time.Millisecond,
		"idle timeout must fire session-end cleanup")

	// invisibility: the same JWT still serves via resurrection
	res := h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
	require.Contains(t, res.body, `"tools"`)
}

// idle eviction must also cover wrapper-owned resurrected sessions: the
// wrapper's timer closes them, the Wait goroutine drops the live-map entry
// and fires cleanup, and the same JWT resurrects again on next use.
func TestSessionResurrection_IdleEvictionInvisible_ResurrectedSession(t *testing.T) {
	h := newResurrectionHarnessIdle(t, 300*time.Millisecond)
	sid := "valid-idle-1"

	res := h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
	require.Equal(t, 1, h.liveSize())

	require.Eventually(t, func() bool {
		return h.liveSize() == 0 && h.isTerminated(sid)
	}, 5*time.Second, 20*time.Millisecond,
		"wrapper idle timer must close the session and fire cleanup")

	res = h.postToolsList(t, sid)
	require.Equal(t, http.StatusOK, res.status)
	require.Contains(t, res.body, `"tools"`)
	require.Equal(t, 1, h.liveSize())
}

func TestSessionResurrection_ConcurrentSameID_OneSession(t *testing.T) {
	h := newResurrectionHarness(t)
	sid := "valid-concurrent-1"

	const n = 20
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = h.postToolsList(t, sid).status
		}()
	}
	wg.Wait()

	for i, status := range statuses {
		require.Equal(t, http.StatusOK, status, "request %d", i)
	}
	require.Equal(t, 1, h.liveSize())
	require.Equal(t, 1, h.serverSessionCount(sid))
}
