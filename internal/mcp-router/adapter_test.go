package mcprouter

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func TestHandleRequestHeaders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// helper: build a minimal unsigned JWT payload with the given sub
	makeBearer := func(sub string) string {
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
		return "Bearer " + hdr + "." + payload + ".sig"
	}

	testCases := []struct {
		Name              string
		GatewayHostname   string
		AuthHeader        string
		wantVerifiedSub   string // "" means header must NOT appear in SetHeaders
		wantSetHeadersLen int
	}{
		{
			Name:              "sets authority — no Authorization header",
			GatewayHostname:   "mcp.example.com",
			wantSetHeadersLen: 1, // only :authority
		},
		{
			Name:              "handles wildcard gateway hostname",
			GatewayHostname:   "*.mcp.local",
			wantSetHeadersLen: 1,
		},
		{
			Name:              "injects x-mcp-verified-sub when Authorization has JWT with sub",
			GatewayHostname:   "mcp.example.com",
			AuthHeader:        makeBearer("alice"),
			wantVerifiedSub:   "alice",
			wantSetHeadersLen: 2, // :authority + x-mcp-verified-sub
		},
		{
			Name:              "does not inject x-mcp-verified-sub when JWT has no sub",
			GatewayHostname:   "mcp.example.com",
			AuthHeader:        "Bearer " + base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".sig",
			wantSetHeadersLen: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			server := &ExtProcServer{
				Logger: logger,
			}
			server.RoutingConfig.Store(&config.MCPServersConfig{
				MCPGatewayExternalHostname: tc.GatewayHostname,
			})

			incomingHeaders := []*corev3.HeaderValue{
				{Key: ":authority", RawValue: []byte("original.host.com")},
			}
			if tc.AuthHeader != "" {
				incomingHeaders = append(incomingHeaders, &corev3.HeaderValue{
					Key:      "authorization",
					RawValue: []byte(tc.AuthHeader),
				})
			}
			// simulate a client trying to forge x-mcp-verified-sub
			incomingHeaders = append(incomingHeaders, &corev3.HeaderValue{
				Key:      "x-mcp-verified-sub",
				RawValue: []byte("forged"),
			})

			headers := &eppb.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: incomingHeaders},
			}

			responses, err := server.HandleRequestHeaders(context.Background(), headers)

			require.NoError(t, err)
			require.Len(t, responses, 1)
			require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
			rh := responses[0].Response.(*eppb.ProcessingResponse_RequestHeaders)
			headerMutation := rh.RequestHeaders.Response.HeaderMutation
			require.NotNil(t, headerMutation)

			require.Len(t, headerMutation.SetHeaders, tc.wantSetHeadersLen)
			var authorityVal string
			for _, h := range headerMutation.SetHeaders {
				if h.Header.Key == ":authority" {
					authorityVal = string(h.Header.RawValue)
				}
			}
			require.Equal(t, tc.GatewayHostname, authorityVal, ":authority header not found or wrong value")

			if tc.wantVerifiedSub != "" {
				found := ""
				for _, h := range headerMutation.SetHeaders {
					if h.Header.Key == "x-mcp-verified-sub" {
						found = string(h.Header.RawValue)
					}
				}
				require.Equal(t, tc.wantVerifiedSub, found, "x-mcp-verified-sub mismatch")
			}

			// x-mcp-verified-sub must always be in RemoveHeaders (strips client forgery)
			require.Contains(t, headerMutation.RemoveHeaders, "x-mcp-verified-sub",
				"x-mcp-verified-sub must be stripped from client requests")
		})
	}
}
