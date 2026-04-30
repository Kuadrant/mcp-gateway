package mcprouter

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/stretchr/testify/require"
)

func testLogger(_ *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

func TestA2ATaskExtractor_SSEStream(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "weather-agent",
		cache:      cache,
	}

	// first chunk: SSE event with task ID
	chunk := []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":\"task-abc\",\"status\":{\"state\":\"submitted\"}}}\n")
	out := ext.Process(ctx, chunk)
	require.Equal(t, chunk, out, "body should pass through unchanged")

	got, err := cache.ResolveTaskRoute(ctx, "task-abc")
	require.NoError(t, err)
	require.Equal(t, "weather-agent", got)
}

func TestA2ATaskExtractor_PlainJSON(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "code-agent",
		cache:      cache,
	}

	// message/send returns a plain JSON response (no SSE framing)
	chunk := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":\"task-xyz\",\"status\":{\"state\":\"completed\"}}}\n")
	out := ext.Process(ctx, chunk)
	require.Equal(t, chunk, out)

	got, err := cache.ResolveTaskRoute(ctx, "task-xyz")
	require.NoError(t, err)
	require.Equal(t, "code-agent", got)
}

func TestA2ATaskExtractor_MultipleChunks(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "agent-1",
		cache:      cache,
	}

	// SSE event split across two chunks — newline only in second chunk
	chunk1 := []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":")
	chunk2 := []byte("\"task-split\",\"status\":{\"state\":\"working\"}}}\n")

	ext.Process(ctx, chunk1)  // no newline yet — buffered
	ext.Process(ctx, chunk2)  // newline arrives — complete line parsed

	got, err := cache.ResolveTaskRoute(ctx, "task-split")
	require.NoError(t, err)
	require.Equal(t, "agent-1", got)
}

func TestA2ATaskExtractor_FlushPlainJSON(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "sync-agent",
		cache:      cache,
	}

	// message/send plain JSON response without a trailing newline
	chunk := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":\"task-flush\",\"status\":{\"state\":\"completed\"}}}")
	ext.Process(ctx, chunk)

	// task ID not stored yet (no newline → stuck in buffer)
	_, err = cache.ResolveTaskRoute(ctx, "task-flush")
	require.Error(t, err)

	// Flush triggered at end-of-stream
	ext.Flush(ctx)

	got, err := cache.ResolveTaskRoute(ctx, "task-flush")
	require.NoError(t, err)
	require.Equal(t, "sync-agent", got)
}

func TestA2ATaskExtractor_DoneAfterFirstID(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "agent-1",
		cache:      cache,
	}

	first := []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":\"task-first\",\"status\":{\"state\":\"working\"}}}\n")
	second := []byte("data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"id\":\"task-second\",\"status\":{\"state\":\"completed\"}}}\n")

	ext.Process(ctx, first)
	ext.Process(ctx, second)

	// only the first task ID should be stored
	_, err = cache.ResolveTaskRoute(ctx, "task-first")
	require.NoError(t, err)
	_, err = cache.ResolveTaskRoute(ctx, "task-second")
	require.Error(t, err, "second task ID should not be stored")
}

func TestA2ATaskExtractor_NonTaskEvent(t *testing.T) {
	ctx := context.Background()
	cache, err := session.NewCache()
	require.NoError(t, err)

	ext := &a2aTaskExtractor{
		logger:     testLogger(t),
		serverName: "agent-1",
		cache:      cache,
	}

	// SSE comment / keepalive line followed by real event
	ext.Process(ctx, []byte(": keepalive\n"))
	ext.Process(ctx, []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"id\":\"task-real\",\"status\":{\"state\":\"submitted\"}}}\n"))

	got, err := cache.ResolveTaskRoute(ctx, "task-real")
	require.NoError(t, err)
	require.Equal(t, "agent-1", got)
}

func TestMCPRequest_A2AMethods(t *testing.T) {
	for _, m := range []string{"message/send", "message/stream"} {
		req := &MCPRequest{Method: m}
		require.True(t, req.isA2AStreamingMethod(), m)
		require.False(t, req.isA2ATaskOperation(), m)
	}
	for _, m := range []string{"tasks/get", "tasks/cancel", "tasks/resubscribe"} {
		req := &MCPRequest{Method: m}
		require.True(t, req.isA2ATaskOperation(), m)
		require.False(t, req.isA2AStreamingMethod(), m)
	}
}

func TestMCPRequest_A2ATaskID(t *testing.T) {
	req := &MCPRequest{
		Method: "tasks/get",
		Params: map[string]any{"id": "task-abc"},
	}
	require.Equal(t, "task-abc", req.a2aTaskID())

	req.Params = nil
	require.Equal(t, "", req.a2aTaskID())

	req.Params = map[string]any{"id": 42} // wrong type
	require.Equal(t, "", req.a2aTaskID())
}
