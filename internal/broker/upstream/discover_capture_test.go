package upstream

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverCapture_JSONResponse(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2025-11-25","2026-07-28"]}}`
	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	req := discoverPostRequest(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)

	// SDK reads the body
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	dc.SetConnected()
	versions, err := dc.Versions()
	require.NoError(t, err)
	require.Equal(t, []string{"2025-11-25", "2026-07-28"}, versions)
}

func TestDiscoverCapture_SSEResponse(t *testing.T) {
	sse := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"supportedVersions\":[\"2025-11-25\",\"2026-07-28\"]}}\n\n"
	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil
		}),
	}

	req := discoverPostRequest(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)

	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	dc.SetConnected()
	versions, err := dc.Versions()
	require.NoError(t, err)
	require.Equal(t, []string{"2025-11-25", "2026-07-28"}, versions)
}

func TestDiscoverCapture_NonDiscoverPassthrough(t *testing.T) {
	called := false
	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			called = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"result":{}}`)),
			}, nil
		}),
	}

	req := discoverPostRequest(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.True(t, called)

	// body should not be wrapped
	_, isWrapped := resp.Body.(*discoverTeeBody)
	require.False(t, isWrapped, "non-discover response body should not be wrapped")
}

func TestDiscoverCapture_Truncation(t *testing.T) {
	// response larger than maxDiscoverResponseBytes — the buffer is truncated
	// mid-JSON so parsing fails and no versions are captured
	padding := strings.Repeat("x", maxDiscoverResponseBytes+1024)
	body := `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2025-11-25"],"extra":"` + padding + `"}}`

	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	req := discoverPostRequest(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)

	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	dc.SetConnected()
	versions, err := dc.Versions()
	require.NoError(t, err)
	// truncated JSON can't be parsed, so no versions captured
	require.Empty(t, versions)
}

func TestDiscoverCapture_VersionsBeforeConnect(t *testing.T) {
	dc := &discoverCapture{base: http.DefaultTransport}
	_, err := dc.Versions()
	require.Error(t, err)
	require.Contains(t, err.Error(), "before connect")
}

func TestDiscoverCapture_NoVersionsInResponse(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`
	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	req := discoverPostRequest(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	dc.SetConnected()
	versions, err := dc.Versions()
	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestDiscoverCapture_GETPassthrough(t *testing.T) {
	dc := &discoverCapture{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/mcp", nil)
	require.NoError(t, err)
	resp, err := dc.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	_ = resp.Body.Close()
}

// roundTripFunc adapts a function to http.RoundTripper
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func discoverPostRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/mcp", io.NopCloser(bytes.NewReader([]byte(body))))
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte(body))), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	return req
}
