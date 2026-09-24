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
	client, err := up.buildHTTPClient()
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
	client, err := up.buildHTTPClient()
	require.NoError(t, err)
	require.NotNil(t, client, "should return custom client when CACert configured")
}

func TestBuildHTTPClient_WithInvalidPEM(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "bad-ca",
		URL:    "https://localhost:8443/mcp",
		CACert: "not-valid-pem-data",
	}, "", nil)
	_, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	httpClient, err := up.buildHTTPClient()
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
	_, err := up.buildHTTPClient()
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

	c, err := up.buildHTTPClient()
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

func TestOAuth2_TokenReusedWithinLifetime(t *testing.T) {
	as, caPEM, asSeen := newTestAuthServer(t, 3600)
	upSrv, upSeen := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", caPEM)

	c, err := up.buildHTTPClient()
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

	c, err := up.buildHTTPClient()
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
		c, err := up.buildHTTPClient()
		require.NoError(t, err)
		require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	}

	require.Len(t, asSeen(), 1, "rebuilding the client must not re-mint a live token")
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-1"}, upSeen())
}

func TestOAuth2_NonHTTPSTokenURLRejected(t *testing.T) {
	upSrv, _ := recordingUpstream(t)

	up := newOAuth2Upstream(upSrv.URL+"/mcp", "http://as.example.com/token", "")

	_, err := up.buildHTTPClient()
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

	c, err := up.buildHTTPClient()
	require.NoError(t, err)
	require.Error(t, getThrough(t, c, upSrv.URL+"/mcp"))

	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, secretsLeaked, "the client secret must not follow a redirect off the verified https endpoint")
	require.Empty(t, upSeen(), "no token means no upstream request")
}

// a revoked token is still unexpired, so the cached source would keep serving it
// until expiry and every reconnect would fail the same way.
func TestOAuth2_RejectedTokenDiscardedOnReconnect(t *testing.T) {
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

	for range 2 {
		c, err := up.buildHTTPClient()
		require.NoError(t, err)
		require.NoError(t, getThrough(t, c, upSrv.URL+"/mcp"))
	}

	require.Len(t, asSeen(), 2, "a rejected token must be re-minted, not reused until expiry")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"Bearer as-token-1", "Bearer as-token-2"}, seen)
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

	untrusted, err := newOAuth2Upstream(upSrv.URL+"/mcp", as.URL+"/token", "").buildHTTPClient()
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

	c, err := up.buildHTTPClient()
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
