package upstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"time"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewUpstreamMCP(t *testing.T) {
	testServer := config.MCPServer{
		Name:     "test-server",
		URL:      "http://localhost:8088/mcp",
		Prefix:   "",
		State:    string(mcpv1.ServerStateEnabled),
		Hostname: "dummy",
	}
	up := NewUpstreamMCP(&testServer, "", nil)
	require.NotNil(t, up)
	require.Equal(t, testServer, up.GetConfig())
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

	tee, ok := client.Transport.(*toolHintsTee)
	require.True(t, ok, "transport should be *toolHintsTee")
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
