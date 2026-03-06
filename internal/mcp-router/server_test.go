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
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc/metadata"
)

type mockProcessServerMessageAndErr struct {
	msg     *extProcV3.ProcessingRequest
	msgErr  error
	resp    []*extProcV3.ProcessingResponse
	sendErr error // if set, Send returns this error for any response in this step
}

type mockProcessServer struct {
	t              *testing.T
	requestCursor  int
	serverStream   []mockProcessServerMessageAndErr
	responseCursor int
}

// verifyAllResponsesConsumed checks that every step's expected responses were fully sent
// that every step was reached and all its expected responses were sent.
// Earlier steps are validated inline by Send; this catches the last step having fewer sends
// than expected and any steps that were never reached at all.
func (m *mockProcessServer) verifyAllResponsesConsumed() {
	for i, step := range m.serverStream {
		if step.msgErr != nil {
			// error steps don't produce responses
			continue
		}
		if i > m.requestCursor {
			require.Failf(m.t, "unreached step", "step %d was never processed (stopped at step %d)", i, m.requestCursor)
		}
		if i == m.requestCursor {
			require.Equal(m.t, len(step.resp), m.responseCursor,
				"step %d: expected %d responses but only %d were sent", i, len(step.resp), m.responseCursor)
		}
	}
}

// this ensures that mockProcessServer implements the MCPBroker interface
var _ extProcV3.ExternalProcessor_ProcessServer = &mockProcessServer{}

func TestProcess_InvalidBody(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		// invalid MCP body (missing jsonrpc 2.0) triggers ImmediateResponse(400)
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body: []byte("{}"),
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(400),
			},
		},
		// nil response headers triggers ImmediateResponse(500)
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_ResponseHeaders{},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(500),
			},
		},
	})

	err := srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no response headers or request headers")
	mock.verifyAllResponsesConsumed()
}

func TestProcess_HappyPath(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		// valid MCP request routed to broker
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{
									SetHeaders: []*corev3.HeaderValueOption{
										{Header: &corev3.HeaderValue{Key: "x-mcp-method", RawValue: []byte("tools/list")}},
										{Header: &corev3.HeaderValue{Key: "x-mcp-servername", RawValue: []byte("mcpBroker")}},
									},
								},
							},
						},
					},
				},
			},
		},
		responseHeadersStep("200"),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_EmptyBody(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body: []byte{},
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{},
						},
					},
				},
			},
		},
		responseHeadersStep("200"),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_UnmarshalError(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body: []byte("not json at all"),
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(400),
			},
		},
		responseHeadersStep("200"),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_NilRequestHeaders(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(500),
			},
		},
	})

	err := srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no request headers present")
	mock.verifyAllResponsesConsumed()
}

func TestProcess_RecvError(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		{
			msgErr: fmt.Errorf("connection reset"),
		},
	})

	err := srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection reset")
}

func TestProcess_EndOfStream(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		// headers with EndOfStream=true (GET request)
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{
					RequestHeaders: &extProcV3.HttpHeaders{
						EndOfStream: true,
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{
								{Key: ":method", Value: "GET"},
								{Key: ":path", Value: "/mcp"},
							},
						},
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestHeaders{
						RequestHeaders: &extProcV3.HeadersResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{
									SetHeaders: []*corev3.HeaderValueOption{
										{Header: &corev3.HeaderValue{Key: ":authority"}},
									},
								},
							},
						},
					},
				},
			},
		},
		// body phase arrives despite EndOfStream — should get do-nothing response
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{},
						},
					},
				},
			},
		},
		responseHeadersStep("200"),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()
}

func TestProcess_SendError(t *testing.T) {
	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
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
			sendErr: fmt.Errorf("broken pipe"),
		},
	})

	err := srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken pipe")
}

func TestProcessSpanEnded(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	srv := newTestServer(t)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		responseHeadersStep("200"),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()

	spans := exporter.GetSpans()
	found := false
	for _, s := range spans {
		if s.Name == "mcp-router.process" {
			found = true
			require.True(t, s.EndTime.After(s.StartTime), "span should be ended")
		}
	}
	require.True(t, found, "expected mcp-router.process span to be recorded")
}

func newTestServer(t *testing.T) *ExtProcServer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache(context.Background())
	require.NoError(t, err)
	return &ExtProcServer{
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
}

// requestHeadersStep returns a standard request headers step
func requestHeadersStep() mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{},
					},
				},
			},
		},
		resp: []*extProcV3.ProcessingResponse{
			{
				Response: &extProcV3.ProcessingResponse_RequestHeaders{
					RequestHeaders: &extProcV3.HeadersResponse{
						Response: &extProcV3.CommonResponse{
							HeaderMutation: &extProcV3.HeaderMutation{
								SetHeaders: []*corev3.HeaderValueOption{
									{Header: &corev3.HeaderValue{Key: ":authority"}},
								},
							},
						},
					},
				},
			},
		},
	}
}

// responseHeadersStep returns a standard response headers step with the given status
func responseHeadersStep(status string) mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_ResponseHeaders{
				ResponseHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{
							{Key: ":status", Value: status},
						},
					},
				},
			},
		},
		resp: []*extProcV3.ProcessingResponse{
			{
				Response: &extProcV3.ProcessingResponse_ResponseHeaders{
					ResponseHeaders: &extProcV3.HeadersResponse{},
				},
			},
		},
	}
}

// immediateResponse returns an expected ImmediateResponse with the given status code
func immediateResponse(code typev3.StatusCode) *extProcV3.ProcessingResponse {
	return &extProcV3.ProcessingResponse{
		Response: &extProcV3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extProcV3.ImmediateResponse{
				Body:   []byte("dummy"),
				Status: &typev3.HttpStatus{Code: code},
			},
		},
	}
}

func makeMockProcessServer(t *testing.T, expected []mockProcessServerMessageAndErr) *mockProcessServer {
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
	step := m.serverStream[m.requestCursor]

	if step.msgErr != nil {
		return nil, step.msgErr
	}

	fmt.Printf("Mocking ext proc request of %#v\n", step.msg.Request)
	return step.msg, nil
}

// RecvMsg implements ext_procv3.ExternalProcessor_ProcessServer.
func (*mockProcessServer) RecvMsg(_ any) error {
	panic("unimplemented")
}

// Send implements ext_procv3.ExternalProcessor_ProcessServer.
func (m *mockProcessServer) Send(actualResp *extProcV3.ProcessingResponse) error {
	require.NotNil(m.t, actualResp)

	fmt.Printf("On step %d/%d, Handling actual response of %#v\n", m.requestCursor, m.responseCursor, actualResp.Response)

	step := m.serverStream[m.requestCursor]
	if step.sendErr != nil {
		return step.sendErr
	}

	require.Less(m.t, m.responseCursor, len(step.resp), "no more expected responses left in the mock stream")
	expectedResponse := step.resp[m.responseCursor]
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
	case *extProcV3.ProcessingResponse_ResponseHeaders:
		_, ok := actualResp.Response.(*extProcV3.ProcessingResponse_ResponseHeaders)
		require.True(m.t, ok, "expected response type to be ResponseHeaders, but it was a %T", actualResp.Response)
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
		exp := expected.SetHeaders[i].Header
		act := actualHeaderValueOption.Header
		require.Equal(t, exp.Key, act.Key)
		if exp.Value != "" || act.Value != "" {
			require.Equal(t, exp.Value, act.Value, "mismatch on header %q Value", act.Key)
		}
		if len(exp.RawValue) > 0 || len(act.RawValue) > 0 {
			require.Equal(t, string(exp.RawValue), string(act.RawValue), "mismatch on header %q RawValue", act.Key)
		}
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
