package mcprouter

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
)

func TestLogSafeResponse(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	sessionJWT := enc([]byte(`{"alg":"HS256"}`)) + "." + enc([]byte(`{"jti":"abc-123"}`)) + ".sig"
	const (
		bearer = "Bearer alice-upstream-token"
		body   = `{"secret":"payload"}`
	)
	common := &extProcV3.CommonResponse{
		HeaderMutation: &extProcV3.HeaderMutation{
			SetHeaders: []*corev3.HeaderValueOption{
				{Header: &corev3.HeaderValue{Key: "mcp-session-id", RawValue: []byte(sessionJWT)}},
				{Header: &corev3.HeaderValue{Key: "authorization", RawValue: []byte(bearer)}},
			},
			RemoveHeaders: []string{"x-mcp-authorized"},
		},
		BodyMutation: &extProcV3.BodyMutation{Mutation: &extProcV3.BodyMutation_Body{Body: []byte(body)}},
	}

	tests := []struct {
		name string
		resp *extProcV3.ProcessingResponse
		want []string
	}{
		{
			name: "request headers",
			resp: &extProcV3.ProcessingResponse{Response: &extProcV3.ProcessingResponse_RequestHeaders{
				RequestHeaders: &extProcV3.HeadersResponse{Response: common}}},
			want: []string{"request_headers", "mcp-session-id", "authorization", "x-mcp-authorized", "jti:abc-123"},
		},
		{
			name: "request body",
			resp: &extProcV3.ProcessingResponse{Response: &extProcV3.ProcessingResponse_RequestBody{
				RequestBody: &extProcV3.BodyResponse{Response: common}}},
			want: []string{"request_body", "body_mutated=true", "jti:abc-123"},
		},
		{
			name: "response headers",
			resp: &extProcV3.ProcessingResponse{Response: &extProcV3.ProcessingResponse_ResponseHeaders{
				ResponseHeaders: &extProcV3.HeadersResponse{Response: common}}},
			want: []string{"response_headers", "jti:abc-123"},
		},
		{
			name: "immediate response",
			resp: &extProcV3.ProcessingResponse{Response: &extProcV3.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcV3.ImmediateResponse{
					Status:  &typev3.HttpStatus{Code: typev3.StatusCode_Unauthorized},
					Headers: common.HeaderMutation,
					Body:    []byte(body),
				}}},
			want: []string{"immediate_response", "status=401"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			logger.Debug("sending", "response", logSafeResponse{tc.resp})
			out := buf.String()
			for _, leak := range []string{sessionJWT, "alice-upstream-token", "payload"} {
				require.NotContains(t, out, leak)
			}
			for _, w := range tc.want {
				require.Contains(t, out, w)
			}
		})
	}
}
