package mcprouter

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/stretchr/testify/require"
)

func TestSSERewriter_Process(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name           string
		chunks         []string
		expectedOutput []string
		expectStored   int // number of IDs expected in gatewayIDs
	}{
		{
			name:           "passes through non-SSE lines",
			chunks:         []string{"event: message\n"},
			expectedOutput: []string{"event: message\n"},
		},
		{
			name:           "passes through non-elicitation SSE data",
			chunks:         []string{`data: {"jsonrpc":"2.0","id":1,"result":{}}` + "\n"},
			expectedOutput: []string{`data: {"jsonrpc":"2.0","id":1,"result":{}}` + "\n"},
		},
		{
			name:           "holds incomplete lines until newline arrives",
			chunks:         []string{"data: partial", " more data\n"},
			expectedOutput: []string{"", "data: partial more data\n"},
		},
		{
			name:           "passes through empty lines",
			chunks:         []string{"\n"},
			expectedOutput: []string{"\n"},
		},
		{
			name: "rewrites elicitation/create ID",
			chunks: []string{
				`data: {"jsonrpc":"2.0","method":"elicitation/create","id":42,"params":{"message":"enter value"}}` + "\n",
			},
			expectStored: 1,
		},
		{
			name: "ignores elicitation/create without ID",
			chunks: []string{
				`data: {"jsonrpc":"2.0","method":"elicitation/create","params":{}}` + "\n",
			},
			expectedOutput: []string{
				`data: {"jsonrpc":"2.0","method":"elicitation/create","params":{}}` + "\n",
			},
		},
		{
			name: "multiple lines in one chunk",
			chunks: []string{
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
			},
			expectedOutput: []string{
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
			},
		},
		{
			name: "non-json data line passes through",
			chunks: []string{
				"data: not json at all\n",
			},
			expectedOutput: []string{
				"data: not json at all\n",
			},
		},
		{
			name: "non-elicitation method passes through unchanged",
			chunks: []string{
				`data: {"jsonrpc":"2.0","method":"tools/call","id":5,"params":{}}` + "\n",
			},
			expectedOutput: []string{
				`data: {"jsonrpc":"2.0","method":"tools/call","id":5,"params":{}}` + "\n",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := idmap.New()
			w := &sseRewriter{
				idMap:  m,
				logger: logger,
				req: &MCPRequest{
					serverName:       "test-server",
					backendSessionID: "test-session",
				},
			}

			var outputs []string
			for _, chunk := range tc.chunks {
				out := w.Process(context.Background(), []byte(chunk))
				outputs = append(outputs, string(out))
			}

			if tc.expectStored > 0 {
				require.Len(t, w.gatewayIDs, tc.expectStored)
				// verify the rewritten line is valid SSE with a new ID
				for _, out := range outputs {
					if out == "" {
						continue
					}
					require.Contains(t, out, "data: ")
					require.Contains(t, out, `"method":"elicitation/create"`)
					require.Contains(t, out, w.gatewayIDs[0])
				}
				return
			}

			require.Equal(t, len(tc.expectedOutput), len(outputs))
			for i, expected := range tc.expectedOutput {
				require.Equal(t, expected, outputs[i])
			}
		})
	}
}

func TestSSERewriter_Process_RewrittenIDIsValid(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	m := idmap.New()
	w := &sseRewriter{
		idMap:  m,
		logger: logger,
		req: &MCPRequest{
			serverName:       "weather-server",
			backendSessionID: "session-abc",
		},
	}

	input := `data: {"jsonrpc":"2.0","method":"elicitation/create","id":99,"params":{"message":"confirm?"}}` + "\n"
	out := w.Process(context.Background(), []byte(input))

	require.Len(t, w.gatewayIDs, 1)
	gatewayID := w.gatewayIDs[0]

	// parse the rewritten output
	trimmed := out[len("data: ") : len(out)-1] // strip "data: " prefix and trailing \n
	var msg jsonRPCMessage
	err := json.Unmarshal(trimmed, &msg)
	require.NoError(t, err)
	require.Equal(t, "elicitation/create", msg.Method)
	require.Equal(t, gatewayID, msg.ID)

	// verify the original backend ID is retrievable from the idmap
	entry, ok := m.Lookup(gatewayID)
	require.True(t, ok)
	require.Equal(t, float64(99), entry.BackendID) // json.Unmarshal decodes numbers as float64
	require.Equal(t, "weather-server", entry.ServerName)
	require.Equal(t, "session-abc", entry.SessionID)
}

func TestSSERewriter_Process_MultipleElicitations(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	m := idmap.New()
	w := &sseRewriter{
		idMap:  m,
		logger: logger,
		req: &MCPRequest{
			serverName:       "multi-server",
			backendSessionID: "session-multi",
		},
	}

	line1 := `data: {"jsonrpc":"2.0","method":"elicitation/create","id":"req-1","params":{}}` + "\n"
	line2 := `data: {"jsonrpc":"2.0","method":"elicitation/create","id":"req-2","params":{}}` + "\n"

	w.Process(context.Background(), []byte(line1))
	w.Process(context.Background(), []byte(line2))

	require.Len(t, w.gatewayIDs, 2)
	require.NotEqual(t, w.gatewayIDs[0], w.gatewayIDs[1])

	// both should be retrievable
	for _, gid := range w.gatewayIDs {
		entry, ok := m.Lookup(gid)
		require.True(t, ok)
		require.Equal(t, "multi-server", entry.ServerName)
	}
}

func TestSSERewriter_Flush(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("returns remaining buffer", func(t *testing.T) {
		m := idmap.New()
		w := &sseRewriter{
			idMap:  m,
			logger: logger,
			req: &MCPRequest{
				serverName:       "test-server",
				backendSessionID: "test-session",
			},
		}

		// send a partial line (no newline)
		out := w.Process(context.Background(), []byte("data: partial"))
		require.Empty(t, out)

		flushed := w.Flush()
		require.Equal(t, "data: partial", string(flushed))
		require.Nil(t, w.buf)
	})

	t.Run("cleans up gateway IDs from idmap", func(t *testing.T) {
		m := idmap.New()
		w := &sseRewriter{
			idMap:  m,
			logger: logger,
			req: &MCPRequest{
				serverName:       "test-server",
				backendSessionID: "test-session",
			},
		}

		input := `data: {"jsonrpc":"2.0","method":"elicitation/create","id":1,"params":{}}` + "\n"
		w.Process(context.Background(), []byte(input))
		require.Len(t, w.gatewayIDs, 1)

		gatewayID := w.gatewayIDs[0]

		w.Flush()

		// the gateway ID should have been removed from the idmap
		_, ok := m.Lookup(gatewayID)
		require.False(t, ok)
	})

	t.Run("empty buffer returns nil", func(t *testing.T) {
		m := idmap.New()
		w := &sseRewriter{
			idMap:  m,
			logger: logger,
			req: &MCPRequest{
				serverName:       "test-server",
				backendSessionID: "test-session",
			},
		}

		flushed := w.Flush()
		require.Nil(t, flushed)
	})
}

func TestSSERewriter_MaybeRewriteElicitation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name          string
		line          string
		expectRewrite bool
	}{
		{
			name:          "rewrites elicitation/create with numeric ID",
			line:          `data: {"jsonrpc":"2.0","method":"elicitation/create","id":1,"params":{}}` + "\n",
			expectRewrite: true,
		},
		{
			name:          "rewrites elicitation/create with string ID",
			line:          `data: {"jsonrpc":"2.0","method":"elicitation/create","id":"abc","params":{}}` + "\n",
			expectRewrite: true,
		},
		{
			name:          "skips non-elicitation method",
			line:          `data: {"jsonrpc":"2.0","method":"tools/call","id":1,"params":{}}` + "\n",
			expectRewrite: false,
		},
		{
			name:          "skips elicitation without ID",
			line:          `data: {"jsonrpc":"2.0","method":"elicitation/create","params":{}}` + "\n",
			expectRewrite: false,
		},
		{
			name:          "skips invalid json",
			line:          "data: not-valid-json\n",
			expectRewrite: false,
		},
		{
			name:          "skips result message",
			line:          `data: {"jsonrpc":"2.0","id":1,"result":{"content":"hello"}}` + "\n",
			expectRewrite: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := idmap.New()
			w := &sseRewriter{
				idMap:  m,
				logger: logger,
				req: &MCPRequest{
					serverName:       "test-server",
					backendSessionID: "test-session",
				},
			}

			result := w.maybeRewriteElicitation(context.Background(), []byte(tc.line))

			if !tc.expectRewrite {
				require.Equal(t, tc.line, string(result))
				require.Empty(t, w.gatewayIDs)
				return
			}

			require.Len(t, w.gatewayIDs, 1)

			// verify the rewritten line has SSE format
			require.True(t, len(result) > len("data: "))
			require.Equal(t, byte('\n'), result[len(result)-1])

			// parse the rewritten JSON
			jsonData := result[len("data: ") : len(result)-1]
			var msg jsonRPCMessage
			err := json.Unmarshal(jsonData, &msg)
			require.NoError(t, err)
			require.Equal(t, "elicitation/create", msg.Method)
			require.Equal(t, w.gatewayIDs[0], msg.ID)
		})
	}
}
