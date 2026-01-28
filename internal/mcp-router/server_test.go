// Package mcprouter ext proc process
package mcprouter

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

type mockProcessServerMessageAndErr struct {
	msg    *extProcV3.ProcessingRequest
	msgErr error
	resp   []*extProcV3.ProcessingResponse
}

type mockProcessServer struct {
	t              *testing.T
	requestCursor  int
	serverStream   []mockProcessServerMessageAndErr
	responseCursor int
}

// this ensures that mockProcessServer implements the MCPBroker interface
var _ extProcV3.ExternalProcessor_ProcessServer = &mockProcessServer{}

func TestProcess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache(context.Background())
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
		RoutingConfig: &config.MCPServersConfig{
			Servers: []*config.MCPServer{
				{
					Name:       "dummy",
					URL:        "http://localhost:9090",
					ToolPrefix: "",
					Enabled:    true,
					Hostname:   "dummy",
				},
			},
		},
	}

	// Create a mock process server that always generates a literal stream and expected responses
	_ = server.Process(makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		// Step 0: First ext proc message
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{
					RequestHeaders: &extProcV3.HttpHeaders{
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{},
						},
					},
				},
			},
			msgErr: nil,
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestHeaders{
						RequestHeaders: &extProcV3.HeadersResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{
									SetHeaders: []*corev3.HeaderValueOption{
										{
											Header: &corev3.HeaderValue{
												Key: ":authority", // This is an Envoy pseudo-header
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},

		// Step 1: second ext proc msg
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						// Note: This is invalid MCP, it doesn't have `"jsonrpc": "2.0"` etc
						Body: []byte("{}"),
					},
				},
			},
			msgErr: nil,
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{},
						},
					},
				},
				{
					Response: &extProcV3.ProcessingResponse_ImmediateResponse{
						ImmediateResponse: &extProcV3.ImmediateResponse{
							Body: []byte("dummy"),
							Status: &typev3.HttpStatus{
								Code: 400,
							},
						},
					},
				},
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{
									SetHeaders: []*corev3.HeaderValueOption{
										{
											Header: &corev3.HeaderValue{
												Key: "x-mcp-method",
											},
										}, {
											Header: &corev3.HeaderValue{
												Key: "x-mcp-servername",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		// third ext proc msg
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_ResponseHeaders{},
			},
			msgErr: nil,
			resp:   []*extProcV3.ProcessingResponse{},
		},

		// fourth ext proc msg
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{},
			},
			msgErr: nil,
			resp:   []*extProcV3.ProcessingResponse{},
		},

		// end of stream
		{
			msgErr: fmt.Errorf("End of mock stream"),
		},
	}))
}

func makeMockProcessServer(t *testing.T, expected []mockProcessServerMessageAndErr) extProcV3.ExternalProcessor_ProcessServer {
	return &mockProcessServer{
		t:             t,
		requestCursor: -1,
		serverStream:  expected,
	}
}

// Context implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) Context() context.Context {
	return context.Background()
}

// Recv implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) Recv() (*extProcV3.ProcessingRequest, error) {
	m.requestCursor++
	m.responseCursor = 0
	retval := m.serverStream[m.requestCursor].msg
	retErr := m.serverStream[m.requestCursor].msgErr

	fmt.Printf("Mocking ext proc request of %#v\n", retval.Request)

	return retval, retErr
}

// RecvMsg implements ext_procv3.ExternalProcessor_ProcessServer.
func (*mockProcessServer) RecvMsg(_ any) error {
	panic("unimplemented")
}

// Send implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) Send(actualResp *extProcV3.ProcessingResponse) error {
	require.NotNil(m.t, actualResp)

	fmt.Printf("On step %d/%d, Handling actual response of %#v\n", m.requestCursor, m.responseCursor, actualResp.Response)

	require.Less(m.t, m.responseCursor, len(m.serverStream[m.requestCursor].resp), "no more expected responses left in the mock stream")
	expectedResponse := m.serverStream[m.requestCursor].resp[m.responseCursor]
	require.NotNil(m.t, expectedResponse)

	switch v := expectedResponse.Response.(type) {
	case *extProcV3.ProcessingResponse_RequestHeaders:
		actualRequestHeaders, ok := actualResp.Response.(*extProcV3.ProcessingResponse_RequestHeaders)
		require.True(m.t, ok, "expected response type to be RequestHeaders, but it was a %T", actualResp.Response)
		require.Equal(m.t, v.RequestHeaders.Response.Status, actualRequestHeaders.RequestHeaders.Response.Status)
		requireMatchingCommonHeaderMutation(m.t, v.RequestHeaders.Response, actualRequestHeaders.RequestHeaders.Response)
	case *extProcV3.ProcessingResponse_RequestBody:
		actualRequestBody, ok := actualResp.Response.(*extProcV3.ProcessingResponse_RequestBody)
		require.True(m.t, ok, "expected response type to be RequestBody, but it was a %T", actualResp.Response)
		require.NotNil(m.t, v.RequestBody, "expected response needs body")
		require.NotNil(m.t, v.RequestBody.Response, "expected response needs response")
		if actualRequestBody.RequestBody.Response != nil && actualRequestBody.RequestBody.Response.Status != 0 {
			require.NotNil(m.t, v.RequestBody.Response)
			require.Equal(m.t, v.RequestBody.Response.Status, actualRequestBody.RequestBody.Response.Status)
		}
		requireMatchingCommonHeaderMutation(m.t, v.RequestBody.Response, actualRequestBody.RequestBody.Response)
		requireMatchingBodyMutation(m.t, v.RequestBody.Response, actualRequestBody.RequestBody.Response)
	case *extProcV3.ProcessingResponse_ImmediateResponse:
		actualImmediateBody, ok := actualResp.Response.(*extProcV3.ProcessingResponse_ImmediateResponse)
		require.True(m.t, ok, "expected response type to be ImmediateResponse, but it was a %T", actualResp.Response)
		require.NotNil(m.t, actualImmediateBody.ImmediateResponse, "expected response needs body")
		require.NotNil(m.t, actualImmediateBody.ImmediateResponse.Body, "expected response needs body response")
		require.NotNil(m.t, v.ImmediateResponse.Body, "expected response needs body")
		requireMatchingHeaderMutation(m.t, v.ImmediateResponse.Headers, actualImmediateBody.ImmediateResponse.Headers)
		require.Equal(m.t, v.ImmediateResponse.GrpcStatus, actualImmediateBody.ImmediateResponse.GrpcStatus)
		requireMatchingHTTPStatus(m.t, v.ImmediateResponse.Status, actualImmediateBody.ImmediateResponse.Status)
	default:
		m.t.Fatalf("Unexpected response type %T", v)
		return nil
	}

	m.responseCursor++
	return nil
}

// SendHeader implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) SendHeader(metadata.MD) error {
	panic("unimplemented")
}

// SendMsg implements ext_procv3.ExternalProcessor_ProcessServer.
func (*mockProcessServer) SendMsg(_ any) error {
	panic("unimplemented")
}

// SetHeader implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) SetHeader(metadata.MD) error {
	panic("unimplemented")
}

// SetTrailer implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) SetTrailer(metadata.MD) {
	panic("unimplemented")
}

func requireMatchingCommonHeaderMutation(t *testing.T, expected, actual *extProcV3.CommonResponse) {
	if expected == nil || expected.HeaderMutation == nil {
		if actual != nil {
			require.Nil(t, actual.HeaderMutation, "expected no response, got %+v", actual)
		}
		return
	}

	requireMatchingHeaderMutation(t, expected.HeaderMutation, actual.HeaderMutation)
}

func requireMatchingHeaderMutation(t *testing.T, expected, actual *extProcV3.HeaderMutation) {
	if expected == nil {
		if actual != nil {
			require.Nil(t, actual)
		}
		return
	}

	require.Equal(t, expected.RemoveHeaders, actual.RemoveHeaders)

	if len(expected.SetHeaders) < len(actual.SetHeaders) {
		for _, headerValueOption := range actual.SetHeaders {
			fmt.Printf("Unexpected set header difference, actual set header: %+v\n", headerValueOption)
		}
	}
	require.Equal(t, len(expected.SetHeaders), len(actual.SetHeaders))
	for i, actualHeaderValueOption := range actual.SetHeaders {
		require.Equal(t, expected.SetHeaders[i].Header.Key, actualHeaderValueOption.Header.Key)
		require.Equal(t, expected.SetHeaders[i].Header.Value, actualHeaderValueOption.Header.Value,
			"mismatch on header %q", actualHeaderValueOption.Header.Key)
	}
}

func requireMatchingBodyMutation(t *testing.T, expected, actual *extProcV3.CommonResponse) {
	require.NotNil(t, expected, "expected response needs response")
	if expected.BodyMutation == nil {
		if actual != nil {
			require.Nil(t, actual.BodyMutation,
				"expected response needs body mutation; actual response has %+v",
				actual.BodyMutation)
		}
		return
	}

	require.Equal(t, expected.BodyMutation, actual.BodyMutation)
}

func requireMatchingHTTPStatus(t *testing.T, expected, actual *typev3.HttpStatus) {
	require.NotNil(t, expected, "actual HTTP status is %d", actual.Code)
	require.NotNil(t, actual)
	require.Equal(t, expected.Code, actual.Code)
}
