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
	"testing"

	"time"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewUpstreamMCP(t *testing.T) {
	testServer := config.MCPServer{
		Name:     "test-server",
		URL:      "http://localhost:8088/mcp",
		Prefix:   "",
		State:    string(mcpv1alpha1.ServerStateEnabled),
		Hostname: "dummy",
	}
	up := NewUpstreamMCP(&testServer, "")
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
			state:    string(mcpv1alpha1.ServerStateEnabled),
			expected: true,
		},
		{
			name:     "Disabled state returns false",
			state:    string(mcpv1alpha1.ServerStateDisabled),
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
			up := NewUpstreamMCP(&server, "")
			require.Equal(t, tc.expected, up.IsEnabled())
		})
	}
}

func TestNewUpstreamMCP_WithCACert(t *testing.T) {
	testServer := config.MCPServer{
		Name:     "test-server",
		URL:      "https://localhost:8443/mcp",
		Prefix:   "",
		State:    string(mcpv1alpha1.ServerStateEnabled),
		Hostname: "dummy",
		CACert:   "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
	}
	up := NewUpstreamMCP(&testServer, "")
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
	}, "")
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
	// silent upstream hang Connect forever via the standalone SSE GET.
	require.Equal(t, defaultResponseHeaderTimeout, tr.ResponseHeaderTimeout)
}

// regression: an upstream that accepts the standalone SSE GET but never sends
// response headers must not hang Connect forever. observed with a test server
// whose logging middleware swallowed Flush: the manager goroutine wedged, the
// server never became ready, and readiness flapping took down the data plane.
func TestConnectBoundedWhenUpstreamNeverAnswersSSE(t *testing.T) {
	old := defaultResponseHeaderTimeout
	defaultResponseHeaderTimeout = 100 * time.Millisecond
	defer func() { defaultResponseHeaderTimeout = old }()

	s := mcp.NewServer(&mcp.Implementation{Name: "hangs-sse", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			<-r.Context().Done() // swallow the standalone SSE GET, send nothing
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "hangs-sse", URL: srv.URL}, "")
	done := make(chan error, 1)
	go func() {
		done <- up.Connect(context.Background(), func() {})
	}()

	// worst case is the SDK's standalone SSE retry cycle (~10s with backoff
	// and jitter); anything past 30s means Connect is hung again
	select {
	case err := <-done:
		// connect may fail outright or return a poisoned session; either is
		// fine, the invariant is that it returns
		t.Logf("Connect returned: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("Connect hung waiting for standalone SSE response headers")
	}
	_ = up.Disconnect()
}

func TestBuildHTTPClient_WithValidCACert(t *testing.T) {
	caPEM, _, _ := generateSelfSignedCA(t)

	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "with-ca",
		URL:    "https://localhost:8443/mcp",
		CACert: string(caPEM),
	}, "")
	client, err := up.buildHTTPClient()
	require.NoError(t, err)
	require.NotNil(t, client, "should return custom client when CACert configured")
}

func TestBuildHTTPClient_WithInvalidPEM(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{
		Name:   "bad-ca",
		URL:    "https://localhost:8443/mcp",
		CACert: "not-valid-pem-data",
	}, "")
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
	}, "")
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
	}, "")
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
	}, "")
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
	}, "")
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

	up := NewUpstreamMCP(&config.MCPServer{Name: "sse-alive", URL: srv.URL}, "")
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

// regression: OnNotification used to be a no-op before Connect (nil client)
// and was only wired after the session was live, leaving a gap where
// list-changed notifications were silently dropped. the manager registers
// the handler before connecting; deliveries must work in that order.
func TestOnNotification_RegisteredBeforeConnect(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "")
	got := make(chan string, 4)
	up.OnNotification(func(method string) { got <- method })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	srv.AddTool(&mcp.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})

	select {
	case method := <-got:
		require.Equal(t, "notifications/tools/list_changed", method)
	case <-time.After(10 * time.Second):
		t.Fatal("notification never reached a handler registered before Connect")
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
	}, string(caPEM))
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
	}, string(gwCAPEM))
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
	}, "not-valid-pem")
	_, err := up.buildHTTPClient()
	require.Error(t, err)
	require.Contains(t, err.Error(), "gateway CA certificate bundle")
}
