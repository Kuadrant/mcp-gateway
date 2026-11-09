package upstream

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"time"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/stretchr/testify/require"
)

// GetConfig hand-copies a fixed field list and broker.go compares the result
// against the freshly loaded config via ConfigChanged. a field missing from the
// copy makes every reload see a phantom diff and rebuild every manager, so the
// fixture carries every field GetConfig is expected to round trip.
func TestNewUpstreamMCP(t *testing.T) {
	testServer := config.MCPServer{
		Name:                "test-server",
		URL:                 "http://localhost:8088/mcp",
		Prefix:              "",
		State:               string(mcpv1.ServerStateEnabled),
		Hostname:            "dummy",
		GuardrailsConfigIDs: []string{"pii"},
		OAuth2: &config.OAuth2ClientCredentials{
			TokenURL:     "https://as.example.com/token",
			ClientID:     "broker",
			ClientSecret: "secret1",
			Scopes:       []string{"mcp.read"},
		},
	}
	up := NewUpstreamMCP(&testServer, "", nil)
	require.NotNil(t, up)
	require.Equal(t, testServer, up.GetConfig())
}

func TestGetConfig_CopiesGuardrailsConfigIDs(t *testing.T) {
	ids := []string{"pii", "toxicity"}
	up := NewUpstreamMCP(&config.MCPServer{Name: "s", GuardrailsConfigIDs: ids}, "", nil)

	cfg := up.GetConfig()
	require.Equal(t, ids, cfg.GuardrailsConfigIDs)

	cfg.GuardrailsConfigIDs[0] = "mutated"
	require.Equal(t, "pii", up.GetConfig().GuardrailsConfigIDs[0], "returned slice must not alias upstream state")
}

func TestMCPServer_IsEnabled(t *testing.T) {
	testCases := []struct {
		name     string
		state    string
		expected bool
	}{
		{
			name:     "empty state defaults to enabled",
			state:    "",
			expected: true,
		},
		{
			name:     "Enabled state returns true",
			state:    string(mcpv1.ServerStateEnabled),
			expected: true,
		},
		{
			name:     "Disabled state returns false",
			state:    string(mcpv1.ServerStateDisabled),
			expected: false,
		},
		{
			name:     "unknown state returns false",
			state:    "Unknown",
			expected: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := config.MCPServer{
				Name:  "test",
				State: tc.state,
			}
			up := NewUpstreamMCP(&server, "", nil)
			require.Equal(t, tc.expected, up.IsEnabled())
		})
	}
}

func TestNewUpstreamMCP_WithCACert(t *testing.T) {
	testServer := config.MCPServer{
		Name:     "test-server",
		URL:      "https://localhost:8443/mcp",
		Prefix:   "",
		State:    string(mcpv1.ServerStateEnabled),
		Hostname: "dummy",
		CACert:   "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
	}
	up := NewUpstreamMCP(&testServer, "", nil)
	require.NotNil(t, up)
	cfg := up.GetConfig()
	require.Equal(t, testServer.CACert, cfg.CACert)
}

func TestNewUpstreamMCP_WithGuardrailsConfigIDs(t *testing.T) {
	testServer := config.MCPServer{
		Name:                "test-server",
		URL:                 "http://localhost:8088/mcp",
		Prefix:              "",
		State:               string(mcpv1.ServerStateEnabled),
		Hostname:            "dummy",
		GuardrailsConfigIDs: []string{"phi3-judge-everything"},
	}
	up := NewUpstreamMCP(&testServer, "", nil)
	require.NotNil(t, up)
	cfg := up.GetConfig()

	// preserve per-server guardrails IDs in the config snapshot.
	require.Equal(t, testServer.GuardrailsConfigIDs, cfg.GuardrailsConfigIDs)
	require.Equal(t, testServer, cfg)
}

func generateSelfSignedCA(t *testing.T) (certPEM []byte, key *ecdsa.PrivateKey, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err = x509.ParseCertificate(certDER)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return certPEM, key, cert
}

func generateServerCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &serverKey.PublicKey, caKey)
	require.NoError(t, err)

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	tlsCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	require.NoError(t, err)
	return tlsCert
}

func TestBuildHTTPClient_NoCACert(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name: "no-ca",
		URL:  "http://localhost:8080/mcp",
	}, "", nil)
	client, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, client, "should always return a client with timeouts set")

	dc, ok := client.Transport.(*discoverCapture)
	require.True(t, ok, "transport should be *discoverCapture")
	tee, ok := dc.base.(*toolHintsTee)
	require.True(t, ok, "discoverCapture base should be *toolHintsTee")
	hrt, ok := tee.base.(*transport.HeaderRoundTripper)
	require.True(t, ok, "tee base should be *transport.HeaderRoundTripper")
	tr, ok := hrt.Base.(*http.Transport)
	require.True(t, ok, "base transport should be *http.Transport")
	require.Equal(t, defaultTLSHandshakeTimeout, tr.TLSHandshakeTimeout)
	// bounds header wait only; SSE bodies stream untouched. zero here lets a
	// silent upstream wedge the manager on any POST (initialize, tools/list).
	require.Equal(t, defaultResponseHeaderTimeout, tr.ResponseHeaderTimeout)
}

// regression: the sdk opens a standalone GET SSE stream synchronously inside
// Connect, on a context detached from the connect context, and treats its
// failure as session-fatal after MaxRetries attempts each bounded only by
// ResponseHeaderTimeout (~125s blocked, then a dead session). the sdk stream
// is disabled and the GET belongs to the broker's notification watcher, so
// an upstream that swallows GETs (seen with proxies and logging middleware
// that eat Flush) must not delay or fail Connect at all, and the watcher
// must keep retrying without harming the session.
func TestConnectNotBlockedByStandaloneSSE(t *testing.T) {
	old := defaultResponseHeaderTimeout
	defaultResponseHeaderTimeout = 50 * time.Millisecond
	defer func() { defaultResponseHeaderTimeout = old }()
	oldBackoff := watchBackoff
	watchBackoff.Duration = 20 * time.Millisecond
	defer func() { watchBackoff = oldBackoff }()

	s := mcp.NewServer(&mcp.Implementation{Name: "swallows-gets", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			<-r.Context().Done() // swallow the GET, send nothing
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "swallows-gets", URL: srv.URL}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	require.NoError(t, up.Connect(ctx, func() {}), "connect must not depend on the standalone SSE GET")
	require.Less(t, time.Since(start), 5*time.Second, "connect must not wait on the standalone GET")
	defer func() { _ = up.Disconnect() }()

	_, err := up.ListTools(ctx)
	require.NoError(t, err)

	// the watcher owns the GET and keeps retrying non-fatally
	require.Eventually(t, func() bool { return gets.Load() >= 2 }, 10*time.Second, 10*time.Millisecond,
		"watcher should retry the swallowed GET")
	_, err = up.ListTools(ctx)
	require.NoError(t, err, "session must stay healthy while the GET is swallowed")
}

func TestBuildHTTPClient_WithValidCACert(t *testing.T) {
	caPEM, _, _ := generateSelfSignedCA(t)

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "with-ca",
		URL:    "https://localhost:8443/mcp",
		CACert: string(caPEM),
	}, "", nil)
	client, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, client, "should return custom client when CACert configured")
}

func TestBuildHTTPClient_WithInvalidPEM(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "bad-ca",
		URL:    "https://localhost:8443/mcp",
		CACert: "not-valid-pem-data",
	}, "", nil)
	_, err := up.buildHTTPClient(t.Context())
	require.Error(t, err, "should error on invalid PEM")
	require.Contains(t, err.Error(), "failed to parse CA certificate")
}

func TestBuildHTTPClient_TLSConnection(t *testing.T) {
	caPEM, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "tls-test",
		URL:    srv.URL + "/mcp",
		CACert: string(caPEM),
	}, "", nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, httpClient)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestBuildHTTPClient_TLSConnectionFailsWithoutCA(t *testing.T) {
	_, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{
		Name: "no-ca-test",
		URL:  srv.URL + "/mcp",
	}, "", nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, httpClient, "client is always returned, only TLS pool varies")

	req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, reqErr)
	_, err = httpClient.Do(req) //nolint:bodyclose // expected to fail, no body to close
	require.Error(t, err, "upstream client without CACert should not trust self-signed cert")
}

func TestBuildHTTPClient_WrongCACertFailsTLS(t *testing.T) {
	_, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	wrongCaPEM, _, _ := generateSelfSignedCA(t)

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "wrong-ca-test",
		URL:    srv.URL + "/mcp",
		CACert: string(wrongCaPEM),
	}, "", nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, httpClient)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	_, err = httpClient.Do(req) //nolint:bodyclose // expected to fail
	require.Error(t, err, "wrong CA should not verify server cert")
}

func TestBuildHTTPClient_MultiCertBundle(t *testing.T) {
	caPEM1, caKey1, caCert1 := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert1, caKey1)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	caPEM2, _, _ := generateSelfSignedCA(t)
	bundle := append(caPEM2, caPEM1...)

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "bundle-test",
		URL:    srv.URL + "/mcp",
		CACert: string(bundle),
	}, "", nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, httpClient)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// ResponseHeaderTimeout bounds only the wait for response headers; it must
// not tear down an SSE stream whose body outlives the timeout.
func TestResponseHeaderTimeoutDoesNotKillEstablishedSSE(t *testing.T) {
	old := defaultResponseHeaderTimeout
	defaultResponseHeaderTimeout = 200 * time.Millisecond
	defer func() { defaultResponseHeaderTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, http.NewResponseController(w).Flush())
		// deliver an event well after the header timeout has elapsed
		time.Sleep(3 * defaultResponseHeaderTimeout)
		_, _ = w.Write([]byte("data: late\n\n"))
		require.NoError(t, http.NewResponseController(w).Flush())
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "sse-alive", URL: srv.URL}, "", nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "reading the SSE body past the header timeout must not error")
	require.Contains(t, string(body), "data: late")
}

// regression: OnNotification used to be a no-op before Connect (nil client),
// silently dropping list-changed deliveries. handlers are now stored on the
// upstream and dispatched via middleware wired into every client before its
// session connects. with the standalone SSE stream disabled the only push
// channel left is a request-scoped stream, so the wiring is exercised at the
// dispatch layer here; end-to-end refresh is covered by the manager tests.
func TestOnNotification_RegisteredBeforeConnect(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: "http://unused/mcp"}, "", nil)
	got := make(chan string, 1)
	up.OnNotification(func(method string) { got <- method })

	up.notify("notifications/tools/list_changed")

	select {
	case method := <-got:
		require.Equal(t, "notifications/tools/list_changed", method)
	default:
		t.Fatal("handler registered before Connect was not dispatched")
	}
}

// regression: a stateless streamable-HTTP upstream (no Mcp-Session-Id)
// caused OnConnectionLost to fire immediately after Connect because the
// SDK's subscriptions/listen stream died with an empty session ID,
// producing a tight reconnect loop with zero backoff. the fix: skip
// session.Wait when session.ID() is empty.
func TestOnConnectionLost_SkippedWhenNoSession(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "stateless", URL: "http://unused"}, "", nil)

	called := make(chan struct{}, 1)
	up.OnConnectionLost(func(_ error) {
		called <- struct{}{}
	})

	select {
	case <-called:
		t.Fatal("OnConnectionLost handler must not fire with nil session")
	case <-time.After(100 * time.Millisecond):
	}
}

// newSessionlessUpstreamServer starts a streamable-HTTP MCP server behind a
// proxy that strips Mcp-Session-Id from every response and rejects GET, the
// wire behaviour of an older-SDK upstream running the transport in stateless
// mode.
func newSessionlessUpstreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "stateless", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return srv }, nil)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r)
		for k, vs := range rec.Header() {
			if k == "Mcp-Session-Id" {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(ts.Close)
	return ts
}

// a session-less upstream negotiates a 2025 revision but issues no
// Mcp-Session-Id, so it must be classified stateless: no GET SSE watcher (the
// stream is keyed by session ID and the upstream 405s the GET) and no session
// ping (there is no session to ping).
func TestSessionlessUpstream_TreatedAsStateless(t *testing.T) {
	ts := newSessionlessUpstreamServer(t)

	up := NewUpstreamMCP(&config.MCPServer{Name: "stateless", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.Empty(t, up.currentSession().ID(), "session ID must be empty for stateless upstream")
	require.Less(t, up.init.ProtocolVersion, protocol.Version2026, "upstream must negotiate a 2025 revision")

	require.True(t, up.UsesStatelessProtocol(), "session-less upstream must be stateless")
	require.NoError(t, up.Ping(ctx), "session-less upstream must skip the session ping")

	up.clientMu.RLock()
	watcher := up.watcher
	up.clientMu.RUnlock()
	require.Nil(t, watcher, "session-less upstream must not start the GET SSE notification watcher")
}

// regression: stateless streamable-HTTP upstream (responds to initialize but
// returns no Mcp-Session-Id, returns 405 on GET). session.ID() is empty so
// OnConnectionLost must not start a session.Wait goroutine.
func TestOnConnectionLost_SkippedForStatelessUpstream(t *testing.T) {
	ts := newSessionlessUpstreamServer(t)

	up := NewUpstreamMCP(&config.MCPServer{Name: "stateless", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.Empty(t, up.currentSession().ID(), "session ID must be empty for stateless upstream")

	connectionLost := make(chan error, 1)
	up.OnConnectionLost(func(err error) {
		connectionLost <- err
	})

	select {
	case err := <-connectionLost:
		t.Fatalf("OnConnectionLost fired for stateless upstream: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

// newStateless2026Server returns a go-sdk server that only speaks 2026,
// seeded with one tool and one prompt so it advertises listChanged for both.
func newStateless2026Server() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{protocol.Version2026},
	})
	srv.AddTool(&mcp.Tool{Name: "t1", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "p1"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	return srv
}

// ackSpyWriter calls onAck when the server writes its
// subscriptions/acknowledged notification, i.e. the listen is registered.
type ackSpyWriter struct {
	http.ResponseWriter
	onAck func()
}

func (w ackSpyWriter) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("notifications/subscriptions/acknowledged")) {
		w.onAck()
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying Flusher.
func (w ackSpyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// regression (#1643): 2026 upstreams only push list changes over
// subscriptions/listen, which the sdk opens only when list-changed handlers
// are set. without them the broker only sees changes on the TTL poll.
func TestConnect_2026ListChangedPushReachesBroker(t *testing.T) {
	srv := newStateless2026Server()
	// 2026 lists carry ttlMs, and the sdk client caches them for that long
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			switch r := res.(type) {
			case *mcp.ListToolsResult:
				r.TTLMs = 60000
			case *mcp.ListPromptsResult:
				r.TTLMs = 60000
			}
			return res, nil
		}
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	acks := make(chan struct{}, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "subscriptions/listen" {
			w = ackSpyWriter{ResponseWriter: w, onAck: func() { acks <- struct{}{} }}
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	type listing struct {
		method string
		names  []string
	}
	got := make(chan listing, 16)
	// the manager re-lists as soon as it is notified, so do the same inline:
	// the pushed change must be visible then, not hidden by the sdk cache
	up.OnNotification(func(method string) {
		l := listing{method: method}
		switch method {
		case "notifications/tools/list_changed":
			if res, err := up.ListTools(ctx); err == nil {
				for _, tool := range res.Tools {
					l.names = append(l.names, tool.Name)
				}
			}
		case "notifications/prompts/list_changed":
			if res, err := up.ListPrompts(ctx); err == nil {
				for _, prompt := range res.Prompts {
					l.names = append(l.names, prompt.Name)
				}
			}
		}
		got <- l
	})
	onConnection, stale := staleSignal(up)
	require.NoError(t, up.Connect(ctx, onConnection))
	defer func() { _ = up.Disconnect() }()
	require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)

	// the listen is fire-and-forget, so wait for the upstream to register it
	// before changing the lists, or the change could land before the subscription
	waitForListen := func() {
		t.Helper()
		select {
		case <-acks:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream never acknowledged a subscriptions/listen")
		}
	}
	waitForListen()

	// the manager's first tick lists both, filling the sdk cache
	_, err := up.ListTools(ctx)
	require.NoError(t, err)
	_, err = up.ListPrompts(ctx)
	require.NoError(t, err)

	waitFor := func(method, name string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		var last []string
		for {
			select {
			case l := <-got:
				if l.method != method {
					continue
				}
				if slices.Contains(l.names, name) {
					return
				}
				last = l.names
			case <-deadline:
				t.Fatalf("re-list after %s never showed %s, last saw %v", method, name, last)
			}
		}
	}
	addTool := func(name string) {
		srv.AddTool(&mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	addTool("t2")
	waitFor("notifications/tools/list_changed", "t2")
	srv.AddPrompt(&mcp.Prompt{Name: "p2"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	waitFor("notifications/prompts/list_changed", "p2")

	// a live listen stream keeps the session
	session := up.currentSession()
	require.NoError(t, up.Connect(ctx, onConnection))
	require.Same(t, session, up.currentSession())

	// the sdk never reopens a listen stream that ends (upstream restart, idle
	// timeout), so the session must report stale and be replaced to get push
	// back
	ts.CloseClientConnections()
	replaceStaleSession(ctx, t, up, onConnection, stale)
	waitForListen()
	addTool("t3")
	waitFor("notifications/tools/list_changed", "t3")
}

// staleSignal returns an onConnection that, like the manager's, watches each
// new session for staleness, and the channel those signals arrive on.
func staleSignal(up *MCPServer) (func(), <-chan struct{}) {
	stale := make(chan struct{}, 4)
	return func() { up.OnSessionStale(func() { stale <- struct{}{} }) }, stale
}

// replaceStaleSession waits for the current session to report stale, then
// replaces it as the manager's stale reconnect would. Connect alone must keep
// it: a list-changed push runs Connect too, and replacing there would bypass
// the stale backoff.
func replaceStaleSession(ctx context.Context, t *testing.T, up *MCPServer, onConnection func(), stale <-chan struct{}) {
	t.Helper()
	session := up.currentSession()
	select {
	case <-stale:
	case <-time.After(5 * time.Second):
		t.Fatal("session never reported stale")
	}
	require.NoError(t, up.Connect(ctx, onConnection))
	require.Same(t, session, up.currentSession(), "Connect replaced a stale session, bypassing the stale backoff")
	_ = up.Disconnect()
	require.NoError(t, up.Connect(ctx, onConnection))
	require.NotSame(t, session, up.currentSession())
}

// regression (#1484): upstreams such as GitHub MCP and FastMCP 4.x answer
// subscriptions/listen with a plain-text 404. go-sdk <= v1.7.0 failed Connect
// on that; it must stay non-fatal now that the handlers are set again.
func TestConnect_2026ListenRejectedIsNonFatal(t *testing.T) {
	srv := newStateless2026Server()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	var listens atomic.Int32
	var failList atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "subscriptions/listen" {
			listens.Add(1)
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Mcp-Method") == "tools/list" && failList.CompareAndSwap(true, false) {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	onConnection, stale := staleSignal(up)
	require.NoError(t, up.Connect(ctx, onConnection))
	defer func() { _ = up.Disconnect() }()
	require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)

	// the listen POST is written synchronously inside Connect; only its
	// stream is read in the background, so the 404 has been handled by now
	require.Positive(t, listens.Load(), "broker never sent subscriptions/listen")
	tools, err := up.ListTools(ctx)
	require.NoError(t, err, "a rejected listen must not break the connection")
	require.Len(t, tools.Tools, 1)

	// the next tick's Connect must keep the live session, not recycle it
	session := up.currentSession()
	require.NoError(t, up.Connect(ctx, onConnection))
	require.Same(t, session, up.currentSession())

	// a plain-text 4xx on a call makes the sdk fail the connection. with no
	// listen stream to watch, the session must still report stale and be
	// replaced
	failList.Store(true)
	_, err = up.ListTools(ctx)
	require.Error(t, err)
	replaceStaleSession(ctx, t, up, onConnection, stale)
	tools, err = up.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
}

// unlike a 404, a listen that fails for a reason that may pass (the upstream
// restarting, a dropped connection) is worth retrying. the sdk treats it as
// non-fatal and never retries it, so the session must be replaced to get push.
func TestConnect_2026ListenTemporaryFailureReconnects(t *testing.T) {
	for name, fail := range map[string]func(http.ResponseWriter){
		"503": func(w http.ResponseWriter) {
			http.Error(w, "upstream restarting", http.StatusServiceUnavailable)
		},
		// cloudflare's "web server returned an unknown error"
		"520": func(w http.ResponseWriter) {
			http.Error(w, "unknown error", 520)
		},
		"dropped connection": func(w http.ResponseWriter) {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newStateless2026Server()
			inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
				&mcp.StreamableHTTPOptions{Stateless: true})
			var failed atomic.Bool
			ackCh := make(chan struct{}, 4)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Mcp-Method") == "subscriptions/listen" {
					if failed.CompareAndSwap(false, true) {
						fail(w)
						return
					}
					w = ackSpyWriter{ResponseWriter: w, onAck: func() { ackCh <- struct{}{} }}
				}
				inner.ServeHTTP(w, r)
			}))
			defer ts.Close()

			up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			onConnection, stale := staleSignal(up)
			require.NoError(t, up.Connect(ctx, onConnection))
			defer func() { _ = up.Disconnect() }()
			require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)
			require.True(t, failed.Load(), "broker never sent subscriptions/listen")

			replaceStaleSession(ctx, t, up, onConnection, stale)
			select {
			case <-ackCh:
			case <-time.After(5 * time.Second):
				t.Fatal("the replacement session's listen was never acknowledged")
			}
		})
	}
}

// an upstream that sends SSE event IDs on its listen stream (e.g. a resumable
// server's SEP-1699 priming event) and then closes it makes the sdk try to
// resume with a GET. when that GET is refused the sdk fails the connection;
// the upstream must still be listable, directly or once the manager replaces
// the stale session.
func TestConnect_2026ListenEndedWithEventIDDoesNotStickUpstream(t *testing.T) {
	srv := newStateless2026Server()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	var listens atomic.Int32
	resumed := make(chan struct{}, 8)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// only the first: a replacement session's listen must stay open, or
		// it ends before the test can list through that session
		case r.Header.Get("Mcp-Method") == "subscriptions/listen" && listens.Add(1) == 1:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// retry keeps the sdk's resume delay short
			_, _ = io.WriteString(w, "id: 1\nretry: 10\ndata: \n\n")
		case r.Method == http.MethodGet:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			resumed <- struct{}{}
		default:
			inner.ServeHTTP(w, r)
		}
	}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	onConnection, stale := staleSignal(up)
	require.NoError(t, up.Connect(ctx, onConnection))
	defer func() { _ = up.Disconnect() }()
	require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)
	require.Positive(t, listens.Load(), "broker never sent subscriptions/listen")

	select {
	case <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("sdk never tried to resume the listen stream")
	}
	// the sdk acts on the refused GET asynchronously; give it time to fail
	// the connection, if it is going to, before listing
	closed := make(chan struct{})
	go func() { _ = up.currentSession().Wait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
	}

	tools, err := up.ListTools(ctx)
	if err != nil {
		t.Logf("ListTools after the listen stream ended: %v", err)
		replaceStaleSession(ctx, t, up, onConnection, stale)
		tools, err = up.ListTools(ctx)
	}
	require.NoError(t, err, "upstream stuck after its listen stream ended with an event ID")
	require.Len(t, tools.Tools, 1)
}

// regression: an upstream that answers every listen with a list change and
// then closes the stream. the re-list for that change must not replace the
// stale session, or each new session's listen drives the next reconnect with
// no backoff (thousands of listens a second).
func TestManager_2026ListenNotifyThenCloseIsBackedOff(t *testing.T) {
	srv := newStateless2026Server()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	var listens atomic.Int32
	firstListen := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") != "subscriptions/listen" {
			inner.ServeHTTP(w, r)
			return
		}
		if listens.Add(1) == 1 {
			close(firstListen)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\n")
	}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	man, err := NewUpstreamMCPManager(up, newMockToolsAdderDeleter(), nil, slog.New(slog.DiscardHandler), 0, InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	active := man.Start(t.Context())
	// the initial connect runs async; time the backoff from the first listen
	select {
	case <-firstListen:
	case <-time.After(5 * time.Second):
		active.Stop()
		t.Fatal("broker never sent subscriptions/listen")
	}
	// the stale backoff allows one more listen about 1s after the first; the
	// next is at least 2s after that
	time.Sleep(1500 * time.Millisecond)
	active.Stop()
	require.LessOrEqual(t, listens.Load(), int32(2), "a list-changed push bypassed the stale backoff")
}

// regression: an upstream that rejects the broker's token on tools/list and
// answers every listen with a list change. the re-list for that change must
// not replace the token-rejected session, or each new session's listen drives
// the next reconnect, and token mint, with no backoff.
func TestManager_2026TokenRejectedPushIsBackedOff(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	srv := newStateless2026Server()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	var listens atomic.Int32
	firstListen := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Mcp-Method") {
		case "tools/list":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "subscriptions/listen":
			if listens.Add(1) == 1 {
				close(firstListen)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\n")
		default:
			inner.ServeHTTP(w, r)
		}
	}))
	defer ts.Close()

	up := newOAuth2Upstream(ts.URL, as.URL+"/token", caPEM)
	man, err := NewUpstreamMCPManager(up, newMockToolsAdderDeleter(), nil, slog.New(slog.DiscardHandler), 0, InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	active := man.Start(t.Context())
	select {
	case <-firstListen:
	case <-time.After(5 * time.Second):
		active.Stop()
		t.Fatal("broker never sent subscriptions/listen")
	}
	// the stale backoff allows one more listen about 1s after the first
	time.Sleep(1500 * time.Millisecond)
	active.Stop()
	require.LessOrEqual(t, listens.Load(), int32(2), "a list-changed push reconnected a token-rejected session without backoff")
	require.LessOrEqual(t, len(asSeen()), 2, "a list-changed push re-minted the token without backoff")
}

// a 2026 upstream may still send an Mcp-Session-Id. its push also runs over
// subscriptions/listen, so a listen that ends must report the session stale
// like any other 2026 session; OnConnectionLost only sees closed connections.
func TestConnect_2026SessionWithIDReportsStale(t *testing.T) {
	srv := newStateless2026Server()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ackCh := make(chan struct{}, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Mcp-Session-Id", "s1")
		if r.Header.Get("Mcp-Method") == "subscriptions/listen" {
			w = ackSpyWriter{ResponseWriter: w, onAck: func() { ackCh <- struct{}{} }}
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	onConnection, stale := staleSignal(up)
	require.NoError(t, up.Connect(ctx, onConnection))
	defer func() { _ = up.Disconnect() }()
	require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)
	require.Equal(t, "s1", up.currentSession().ID())

	select {
	case <-ackCh:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never acknowledged a subscriptions/listen")
	}
	ts.CloseClientConnections()
	replaceStaleSession(ctx, t, up, onConnection, stale)
}

func TestBuildHTTPClient_GatewayCACertBundle(t *testing.T) {
	caPEM, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{
		Name: "gw-ca-test",
		URL:  srv.URL + "/mcp",
	}, string(caPEM), nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestBuildHTTPClient_GatewayCAPlusPerServerCA(t *testing.T) {
	gwCAPEM, _, _ := generateSelfSignedCA(t)
	serverCAPEM, serverCAKey, serverCACert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, serverCACert, serverCAKey)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "combined-ca-test",
		URL:    srv.URL + "/mcp",
		CACert: string(serverCAPEM),
	}, string(gwCAPEM), nil)
	httpClient, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestBuildHTTPClient_InvalidGatewayCACert(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name: "bad-gw-ca",
		URL:  "https://localhost:8443/mcp",
	}, "not-valid-pem", nil)
	_, err := up.buildHTTPClient(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "gateway CA certificate bundle")
}

func TestMCPServer_ListResources(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	srv.AddResource(&mcp.Resource{
		Name: "template",
		URI:  "ui://template.html",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "ui://template.html", Text: "<html></html>"}},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.True(t, up.SupportsResources())

	result, err := up.ListResources(ctx)
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	require.Equal(t, "ui://template.html", result.Resources[0].URI)
}

func TestMCPServer_SupportsResources_NoCapability(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.False(t, up.SupportsResources())
}

func TestMCPServer_ListResources_NotConnected(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "up", Prefix: "up_"}, "", nil)
	_, err := up.ListResources(context.Background())
	require.Error(t, err)
}

func TestMCPServer_ListResources_ReflectsChangesWithinSameSession(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	srv.AddResource(&mcp.Resource{
		Name: "template",
		URI:  "ui://template.html",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "ui://template.html", Text: "<html></html>"}},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	first, err := up.ListResources(ctx)
	require.NoError(t, err)
	require.Len(t, first.Resources, 1, "expected only the resource registered at startup")

	// simulate the upstream's resource set changing mid-session, without reconnecting
	srv.AddResource(&mcp.Resource{
		Name: "second",
		URI:  "ui://second.html",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "ui://second.html", Text: "<html></html>"}},
		}, nil
	})

	second, err := up.ListResources(ctx)
	require.NoError(t, err)
	require.Len(t, second.Resources, 2, "ListResources must not cache — a second call on the same session should see the newly added resource")
}

func TestMCPServer_ListResources_UpstreamError(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	ts.Close()

	_, err := up.ListResources(ctx)
	require.Error(t, err)
}

func TestUsesStatelessProtocol(t *testing.T) {
	tests := []struct {
		name     string
		init     *mcp.InitializeResult
		expected bool
	}{
		{"nil init", nil, false},
		// no session: not connected, so nothing to classify. the session-less
		// case is covered by TestSessionlessUpstream_TreatedAsStateless, which
		// needs a real connection to produce an empty session ID.
		{"2025 without session", &mcp.InitializeResult{ProtocolVersion: "2025-11-25"}, false},
		{"2026", &mcp.InitializeResult{ProtocolVersion: "2026-07-28"}, true},
		{"future version", &mcp.InitializeResult{ProtocolVersion: "2027-01-01"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := &MCPServer{init: tt.init}
			if got := up.UsesStatelessProtocol(); got != tt.expected {
				t.Errorf("UsesStatelessProtocol() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSupportedVersions(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		expected []string
	}{
		{"not connected", nil, nil},
		{"empty", []string{}, nil},
		{"2025 only", []string{"2025-11-25"}, []string{"2025-11-25"}},
		{"2026 only", []string{"2026-07-28"}, []string{"2026-07-28"}},
		{"both", []string{"2025-11-25", "2026-07-28"}, []string{"2025-11-25", "2026-07-28"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := &MCPServer{supportedVersions: tt.versions}
			got := up.SupportedVersions()
			if tt.expected == nil {
				if got != nil {
					t.Errorf("SupportedVersions() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.expected) {
				t.Fatalf("SupportedVersions() len = %d, want %d", len(got), len(tt.expected))
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("SupportedVersions()[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestSupportedVersions_ReturnsCopy(t *testing.T) {
	up := &MCPServer{supportedVersions: []string{"2025-11-25"}}
	got := up.SupportedVersions()
	got[0] = "mutated"
	if up.supportedVersions[0] != "2025-11-25" {
		t.Error("SupportedVersions() should return a copy, not a reference")
	}
}

func TestSupportsVersion(t *testing.T) {
	up := &MCPServer{supportedVersions: []string{"2025-11-25", "2026-07-28"}}
	if !up.SupportsVersion("2025-11-25") {
		t.Error("should support 2025-11-25")
	}
	if !up.SupportsVersion("2026-07-28") {
		t.Error("should support 2026-07-28")
	}
	if up.SupportsVersion("9999-01-01") {
		t.Error("should not support unknown version")
	}
}

func TestCacheMetadata_Defaults(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "defaults"}, "", nil)
	meta := up.ToolsCacheMetadata()
	require.Equal(t, 0, meta.TTLMs)
	require.Equal(t, "", meta.CacheScope)
	require.False(t, meta.UserSpecificList)

	pmeta := up.PromptsCacheMetadata()
	require.Equal(t, 0, pmeta.TTLMs)
	require.Equal(t, "", pmeta.CacheScope)
	require.False(t, pmeta.UserSpecificList)
}

func TestCacheMetadata_UserSpecificListFromCRD(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name:             "user-specific",
		UserSpecificList: true,
	}, "", nil)
	require.True(t, up.ToolsCacheMetadata().UserSpecificList)
	require.True(t, up.PromptsCacheMetadata().UserSpecificList)
}

func TestCacheMetadata_PopulatedFromListTools(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "t1",
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	// before listing, defaults
	require.Equal(t, 0, up.ToolsCacheMetadata().TTLMs)

	_, err := up.ListTools(ctx)
	require.NoError(t, err)

	// SDK defaults: TTLMs 0 (immediately stale), CacheScope "public"
	meta := up.ToolsCacheMetadata()
	require.Equal(t, 0, meta.TTLMs)
	require.Equal(t, "public", meta.CacheScope)
}

// the static credentialRef value must reach the upstream verbatim as
// Authorization on every broker request. guards the header path against
// anything later inserted into the transport chain.
func TestStaticCredential_SentToUpstream(t *testing.T) {
	const credential = "Bearer static-test-token" // #nosec G101 -- test fixture

	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "t1",
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Credential: credential}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	_, err := up.ListTools(ctx)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen, "upstream saw no requests")
	for _, got := range seen {
		require.Equal(t, credential, got, "every broker request must carry the configured credential")
	}
}

const testClientSecret = "cc-client-secret" // #nosec G101 -- test fixture

// tokenRequest records one /token exchange as the authorization server saw it.
type tokenRequest struct {
	grantType     string
	scope         string
	clientID      string
	clientSecret  string
	brokerHeaders []string // broker header values that must never reach the AS
}

// newTestAuthServer starts an https authorization server behind a private CA
// that issues sequential opaque tokens with the given expires_in. returns the
// server, its CA PEM for the broker trust pool, and an accessor for the
// requests it received.
func newTestAuthServer(t *testing.T, expiresIn int) (*httptest.Server, string, func() []tokenRequest) {
	t.Helper()
	caPEM, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	var mu sync.Mutex
	var reqs []tokenRequest
	issued := 0

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		tr := tokenRequest{
			grantType:    r.PostForm.Get("grant_type"),
			scope:        r.PostForm.Get("scope"),
			clientID:     r.PostForm.Get("client_id"),
			clientSecret: r.PostForm.Get("client_secret"),
			brokerHeaders: []string{
				r.Header.Get("Gateway-Server-Id"),
				r.Header.Get("X-Client-Id"),
			},
		}
		if id, secret, ok := r.BasicAuth(); ok {
			tr.clientID, tr.clientSecret = id, secret
		}
		mu.Lock()
		issued++
		n := issued
		reqs = append(reqs, tr)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("as-token-%d", n),
			"token_type":   "Bearer",
			"expires_in":   expiresIn,
		})
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, string(caPEM), func() []tokenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]tokenRequest(nil), reqs...)
	}
}

// recordingUpstream returns a plain http upstream that records the
// Authorization header of every request it serves.
func recordingUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// bearerGuardedMCPUpstream serves a one-tool mcp server that 401s anything
// not presenting the given bearer token.
func bearerGuardedMCPUpstream(t *testing.T, want string) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "t1",
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newOAuth2Upstream builds an upstream whose broker credentials come from the
// client credentials grant at tokenURL. gatewayCA seeds the trust pool used for
// both the AS and the upstream. scopes may be omitted.
func newOAuth2Upstream(upstreamURL, tokenURL, gatewayCA string, scopes ...string) *MCPServer {
	return NewUpstreamMCP(&config.MCPServer{
		Name: "oauth-up",
		URL:  upstreamURL,
		OAuth2: &config.OAuth2ClientCredentials{
			TokenURL:     tokenURL,
			ClientID:     "broker",
			ClientSecret: testClientSecret,
			Scopes:       scopes,
		},
	}, gatewayCA, nil)
}

// logBuffer collects log output for assertions. the transport retries the
// token request, so writes can arrive from more than one goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func getThrough(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func TestOAuth2_UpstreamCarriesMintedToken(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM, "mcp.read", "mcp.write")

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))

	require.Equal(t, []string{"Bearer as-token-1"}, upSeen())

	reqs := asSeen()
	require.Len(t, reqs, 1)
	require.Equal(t, "client_credentials", reqs[0].grantType)
	require.Equal(t, "mcp.read mcp.write", reqs[0].scope)
	require.Equal(t, "broker", reqs[0].clientID)
	require.Equal(t, testClientSecret, reqs[0].clientSecret)
	// the AS is not an upstream mcp server: it must not see broker identity headers
	require.Equal(t, []string{"", ""}, reqs[0].brokerHeaders)
}

func TestOAuth2_UpstreamRedirectDoesNotForwardToken(t *testing.T) {
	as, caPEM, _ := newTestAuthServer(t, 3600)
	sink, sinkSeen := recordingUpstream(t)
	firstHop := make(chan string, 1)
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHop <- r.Header.Get("Authorization")
		http.Redirect(w, r, sink.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	redirectPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redirect.Certificate().Raw})
	up := newOAuth2Upstream(redirect.URL+"/mcp", as.URL+"/token", caPEM+string(redirectPEM))
	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.Error(t, getThrough(t, c, redirect.URL+"/mcp"))
	require.Equal(t, "Bearer as-token-1", <-firstHop)
	require.Empty(t, sinkSeen(), "a redirected request must not carry the bearer token to a plaintext endpoint")
}

func TestOAuth2_ConnectCancellationStopsTokenRequest(t *testing.T) {
	caPEM, caKey, caCert := generateSelfSignedCA(t)
	started := make(chan struct{}, 1)
	requestCanceled := make(chan struct{}, 1)
	release := make(chan struct{})
	as := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			requestCanceled <- struct{}{}
		case <-release:
			http.Error(w, "released", http.StatusInternalServerError)
		}
	}))
	as.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{generateServerCert(t, caCert, caKey)}}
	as.StartTLS()
	defer as.Close()
	defer close(release)

	upSrv := bearerGuardedMCPUpstream(t, "Bearer as-token-1")
	up := newOAuth2Upstream(upSrv.URL, as.URL+"/token", string(caPEM))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- up.Connect(ctx, func() {}) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "token request did not start")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "Connect continued token acquisition after cancellation")
	}
	require.Error(t, err)
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		require.FailNow(t, "token endpoint request continued after cancellation")
	}
}

func TestOAuth2_TokenReusedWithinLifetime(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))

	require.Len(t, asSeen(), 1, "a live token must not be re-requested")
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-1"}, upSeen())
}

// expires_in of 1s is inside x/oauth2's ~10s expiry skew, so the cached token
// is never valid and every request re-mints. guards against the token source
// being double-wrapped, which would silently disable refresh.
func TestOAuth2_TokenRefreshedOnExpiry(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 1)
	upSrv, upSeen := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))

	require.Len(t, asSeen(), 2, "an expired token must be re-requested")
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-2"}, upSeen())
}

// Connect rebuilds the transport chain on every reconnect. the token source is
// cached on the upstream instead, so a flapping upstream does not re-mint a
// token that is still live.
func TestOAuth2_TokenSurvivesReconnect(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	for range 2 {
		c, err := up.buildHTTPClient(t.Context())
		require.NoError(t, err)
		require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	}

	require.Len(t, asSeen(), 1, "rebuilding the client must not re-mint a live token")
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-1"}, upSeen())
}

func TestOAuth2_CanceledTokenSourceReplacedOnReconnect(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)
	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	ctx, cancel := context.WithCancel(t.Context())
	_, err := up.buildHTTPClient(ctx)
	require.NoError(t, err)
	cancel()

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	require.Len(t, asSeen(), 1)
	require.Equal(t, []string{"Bearer as-token-1"}, upSeen())
}

func TestOAuth2_NonHTTPSTokenURLRejected(t *testing.T) {
	upSrv, _ := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", "http://as.example.com/token", "")

	_, err := up.buildHTTPClient(t.Context())
	require.Error(t, err, "a plaintext token endpoint would expose the client secret")
	require.NotContains(t, err.Error(), testClientSecret)
}

// the https check covers the first hop only. following a 307 would re-POST the
// client secret wherever the AS points, plaintext included.
func TestOAuth2_TokenEndpointRedirectRejected(t *testing.T) {
	var mu sync.Mutex
	var secretsLeaked int
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		_, secret, _ := r.BasicAuth()
		if secret == testClientSecret || r.PostForm.Get("client_secret") == testClientSecret {
			mu.Lock()
			secretsLeaked++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "leaked-token", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer sink.Close()

	caPEM, caKey, caCert := generateSelfSignedCA(t)
	as := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/token", http.StatusTemporaryRedirect)
	}))
	as.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{generateServerCert(t, caCert, caKey)}}
	as.StartTLS()
	defer as.Close()

	upSrv, upSeen := recordingUpstream(t)
	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", string(caPEM))

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.Error(t, getThrough(t, c, upSrv.URL+"/mcp"))

	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, secretsLeaked, "the client secret must not follow a redirect off the verified https endpoint")
	require.Empty(t, upSeen(), "no token means no upstream request")
}

// a revoked token is still unexpired, so the cached source would keep serving
// it until expiry. recovery must not need a reconnect: the manager's only
// probe against a connected stateless upstream is tools/list, which reuses the
// client it was handed.
func TestOAuth2_RejectedTokenReplacedWithoutReconnect(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)

	var mu sync.Mutex
	var seen []string
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		// the first token is revoked upstream while still inside its lifetime
		if auth == "Bearer as-token-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upSrv.Close()

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	// one client for both requests: the 401 must be recovered from in place
	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	}

	require.Len(t, asSeen(), 2, "a rejected token must be re-minted, not reused until expiry")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-2"}, seen)
}

func TestOAuth2_StatelessUpstreamReconnectsAfterRejectedToken(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{protocol.Version2026},
	})
	srv.AddTool(&mcp.Tool{
		Name: "t1", InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "tools/list" && r.Header.Get("Authorization") == "Bearer as-token-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer upSrv.Close()

	up := newOAuth2Upstream(upSrv.URL, as.URL+"/token", caPEM)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()
	require.Equal(t, protocol.Version2026, up.ProtocolInfo().ProtocolVersion)
	require.True(t, up.UsesStatelessProtocol())
	require.Empty(t, up.currentSession().ID())
	firstSession := up.currentSession()

	_, err := up.ListTools(ctx)
	require.Error(t, err, "the first token is rejected after initialize")
	require.NoError(t, up.Connect(ctx, func() {}), "the next manager tick must reconnect")
	require.NotSame(t, firstSession, up.currentSession(), "the retry must create a fresh SDK session")
	tools, err := up.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	require.Len(t, asSeen(), 2, "the retry must mint a fresh token")
}

func TestOAuth2_ConnectAndListToolsWithMintedToken(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv := bearerGuardedMCPUpstream(t, "Bearer as-token-1")

	up := newOAuth2Upstream(upSrv.URL, as.URL+"/token", caPEM)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	tools, err := up.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	require.Len(t, asSeen(), 1, "one token covers the whole connect + list exchange")
}

func TestOAuth2_TokenEndpointFailureFailsConnect(t *testing.T) {
	caPEM, caKey, caCert := generateSelfSignedCA(t)
	serverCert := generateServerCert(t, caCert, caKey)

	const asResponseBody = "internal-as-detail"
	as := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, asResponseBody, http.StatusInternalServerError)
	}))
	as.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	as.StartTLS()
	defer as.Close()

	upSrv := bearerGuardedMCPUpstream(t, "Bearer as-token-1")

	var logs logBuffer
	up := newOAuth2Upstream(upSrv.URL, as.URL+"/token", string(caPEM))
	up.logger = slog.New(slog.NewTextHandler(&logs, nil))

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err := up.Connect(ctx, func() {})
	require.Error(t, err)
	require.NotContains(t, err.Error(), testClientSecret, "the client secret must never reach an error surface")
	// the error becomes the status Message, so it names the upstream only
	require.Contains(t, err.Error(), "failed to obtain access token for upstream "+string(up.ID()))
	require.NotContains(t, err.Error(), asResponseBody, "the AS response body must not be echoed")

	// the log is the other half: oauth2's RetrieveError quotes the AS body, and
	// it raises one for a mislabelled 2xx too, where that body holds a token
	logged := logs.String()
	require.NotContains(t, logged, asResponseBody, "the AS response body must not be logged")
	require.NotContains(t, logged, testClientSecret, "the client secret must never reach the log")
	require.Contains(t, logged, "500 Internal Server Error", "the status is what makes the failure triageable")
}

// the token endpoint is reached through the same trust pool as the upstream, so
// a private CA in the gateway bundle covers both and nothing else does. the
// trusted case is proven by every other oauth2 test here.
func TestOAuth2_TokenEndpointOnPrivateCARequiresGatewayBundle(t *testing.T) {
	as, _, _ := newTestAuthServer(t, 3600)
	upSrv, _ := recordingUpstream(t)

	untrusted, err := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", "").buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.Error(t, getThrough(t, untrusted, upSrv.URL+"/mcp"),
		"an AS on a private CA must not be trusted without the gateway bundle")
}

// the token source sits below the header injector so it sets Authorization
// last. a config carrying both credentials must reach the upstream with the
// minted token, never the static one.
func TestOAuth2_MintedTokenOverridesStaticCredential(t *testing.T) {
	as, caPEM, _ := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)

	up := NewUpstreamMCP(&config.MCPServer{
		Name:       "oauth-up",
		URL:        upSrv.URL + "/mcp",
		Credential: "Bearer should-be-ignored",
		OAuth2: &config.OAuth2ClientCredentials{
			TokenURL:     as.URL + "/token",
			ClientID:     "broker",
			ClientSecret: testClientSecret,
		},
	}, caPEM, nil)

	c, err := up.buildHTTPClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))

	require.Equal(t, []string{"Bearer as-token-1"}, upSeen())
}

func TestCacheMetadata_ToolsAndPromptsIndependent(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "indep"}, "", nil)

	// simulate storing different metadata
	up.clientMu.Lock()
	up.toolsCacheMeta = CacheMetadata{TTLMs: 5000, CacheScope: "public"}
	up.promptsCacheMeta = CacheMetadata{TTLMs: 10000, CacheScope: "private"}
	up.clientMu.Unlock()

	tmeta := up.ToolsCacheMetadata()
	pmeta := up.PromptsCacheMetadata()
	require.Equal(t, 5000, tmeta.TTLMs)
	require.Equal(t, "public", tmeta.CacheScope)
	require.Equal(t, 10000, pmeta.TTLMs)
	require.Equal(t, "private", pmeta.CacheScope)
}
