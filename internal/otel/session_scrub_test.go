package otel

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/stretchr/testify/require"
)

func TestSessionScrubHandler(t *testing.T) {
	const backendSID = "7f3c9a1e-backend-session"
	// formats from go-sdk mcp/streamable.go
	reconnect := fmt.Errorf("calling %q: failed to reconnect (session ID: %v): %w", "tools/list", backendSID, errors.New("EOF"))
	mismatch := fmt.Errorf("mismatching session IDs %q and %q", backendSID, "other-session")
	safeSID := "session ID: " + internaljwt.LogSafeSessionID(backendSID)
	enc := base64.RawURLEncoding.EncodeToString
	gatewayJWT := enc([]byte(`{"alg":"HS256"}`)) + "." + enc([]byte(`{"jti":"abc-123"}`)) + ".sig"

	tests := []struct {
		name string
		log  func(*slog.Logger)
		want []string
	}{
		{
			name: "error attr",
			log:  func(l *slog.Logger) { l.Error("upstream failed", "error", fmt.Errorf("connect: %w", reconnect)) },
			want: []string{safeSID, "tools/list", "EOF"},
		},
		{
			name: "mismatching ids",
			log:  func(l *slog.Logger) { l.Error("upstream failed", "error", mismatch) },
			want: []string{`session IDs \"` + internaljwt.LogSafeSessionID(backendSID) + `\" and \"` + internaljwt.LogSafeSessionID("other-session") + `\"`},
		},
		{
			name: "string attr",
			log:  func(l *slog.Logger) { l.Info("upstream failed", "error", reconnect.Error()) },
			want: []string{safeSID},
		},
		{
			name: "message",
			log:  func(l *slog.Logger) { l.Error("upstream failed: " + reconnect.Error()) },
			want: []string{safeSID},
		},
		{
			name: "gateway jwt becomes jti",
			log: func(l *slog.Logger) {
				l.Error("upstream failed", "error", fmt.Errorf("failed to connect (session ID: %v): %w", gatewayJWT, errors.New("EOF")))
			},
			want: []string{"session ID: jti:abc-123"},
		},
		{
			name: "with attrs and group",
			log: func(l *slog.Logger) {
				l.With("cause", reconnect).Info("upstream failed", slog.Group("upstream", "error", reconnect))
			},
			want: []string{"cause=", "upstream.error=", safeSID},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.log(NewTracingLogger(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, false, nil))
			out := buf.String()
			require.NotContains(t, out, backendSID)
			require.NotContains(t, out, "other-session")
			require.NotContains(t, out, gatewayJWT)
			for _, w := range tc.want {
				require.Contains(t, out, w)
			}
		})
	}

	t.Run("unrelated records pass through unchanged", func(t *testing.T) {
		var buf bytes.Buffer
		NewTracingLogger(&buf, nil, true, nil).Info("connected", "upstream", "svc", "error", errors.New("dial tcp 10.0.0.5:443: connection refused"))
		require.Contains(t, buf.String(), `"error":"dial tcp 10.0.0.5:443: connection refused"`)
	})
}

func TestScrubSessionIDs_SpecCharacters(t *testing.T) {
	// MCP 2025-11-25: session IDs may contain any visible ASCII (0x21-0x7E)
	var all strings.Builder
	for c := byte(0x21); c <= 0x7e; c++ {
		all.WriteByte(c)
	}
	// the go-sdk does not enforce the charset, so non-conforming IDs with spaces too
	ids := []string{"plain-id", "ab)cd", "x)", "a):b", `q"uo"te`, `back\slash`, "co:lon:", all.String(), "has space", "sp a)ce"}

	for _, id := range ids {
		safe := internaljwt.LogSafeSessionID(id)
		other := internaljwt.LogSafeSessionID("other")
		cases := []struct {
			name, in, want string
		}{
			{
				name: "reconnect",
				in:   fmt.Sprintf("calling %q: failed to reconnect (session ID: %v): %v", "tools/list", id, errors.New("EOF")),
				want: `calling "tools/list": failed to reconnect (session ID: ` + safe + `): EOF`,
			},
			{
				name: "retries at end of text",
				in:   fmt.Sprintf("%s: exceeded %d retries without progress (session ID: %v)", "tools/list", 3, id),
				want: "tools/list: exceeded 3 retries without progress (session ID: " + safe + ")",
			},
			{
				name: "wrapped and joined",
				in:   errors.Join(fmt.Errorf("connect: %w", fmt.Errorf("x: failed to connect (session ID: %v): %w", id, errors.New("session missing"))), errors.New("second")).Error(),
				want: "connect: x: failed to connect (session ID: " + safe + "): session missing\nsecond",
			},
			{
				name: "wrapped with trailing text",
				in:   fmt.Sprintf("upstream error [failed to connect (session ID: %v)]", id),
				want: "upstream error [failed to connect (session ID: " + safe + ")]",
			},
			{
				name: "mismatching",
				in:   fmt.Sprintf("mismatching session IDs %q and %q", id, "other"),
				want: "mismatching session IDs " + strconv.Quote(safe) + " and " + strconv.Quote(other),
			},
			{
				name: "event store",
				in:   fmt.Sprintf("MemoryEventStore.After: unknown session ID %q", id),
				want: "MemoryEventStore.After: unknown session ID " + strconv.Quote(safe),
			},
		}
		for _, tc := range cases {
			t.Run(tc.name+"/"+id, func(t *testing.T) {
				require.Equal(t, tc.want, scrubSessionIDs(tc.in))
			})
		}
	}
}
