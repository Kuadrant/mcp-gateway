// Package mcprouter ext proc process
package mcprouter

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
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
						Body:        []byte("{}"),
						EndOfStream: true,
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
						Body:        []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
						EndOfStream: true,
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
		responseHeadersStep(),
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
						Body:        []byte{},
						EndOfStream: true,
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
		responseHeadersStep(),
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
						Body:        []byte("not json at all"),
						EndOfStream: true,
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(400),
			},
		},
		responseHeadersStep(),
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
									RemoveHeaders: []string{"x-mcp-authorized", "x-mcp-virtualserver", "x-mcp-verified-sub"},
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
		responseHeadersStep(),
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
		responseHeadersStep(),
	})

	err := srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()

	spans := exporter.GetSpans()
	found := false
	for _, s := range spans {
		if s.Name == "mcp-router.process" {
			found = true
			require.False(t, s.EndTime.IsZero(), "span should have end time set")
			require.False(t, s.EndTime.Before(s.StartTime), "span end should not precede start (sync tracer may use equal timestamps)")
		}
	}
	require.True(t, found, "expected mcp-router.process span to be recorded")
}

func TestProcess_BufferedBodyExceedsMaxSize(t *testing.T) {
	srv := newTestServer(t)
	srv.MaxRequestBodySize = 50

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		requestHeadersStep(),
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body:        []byte(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"extra":"data"}}`),
						EndOfStream: true,
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				immediateResponse(413),
			},
		},
	})

	err := srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "request body too large")
	mock.verifyAllResponsesConsumed()
}

func newTestServer(t *testing.T) *ExtProcServer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	serverConfigs := []*config.MCPServer{
		{
			Name:     "dummy",
			URL:      "http://localhost:9090",
			Prefix:   "",
			State:    "Enabled",
			Hostname: "dummy",
		},
	}

	table := routing.NewTableBuilder().Build()
	routingConfig := atomic.Pointer[config.MCPServersConfig]{}
	routingConfig.Store(&config.MCPServersConfig{Servers: serverConfigs})

	router := &routing.Router202511{
		RoutingConfig: &routingConfig,
		Table:         func() routing.RoutingTable { return table },
		SessionCache:  cache,
		Logger:        logger,
	}

	mcpConfig := &config.MCPServersConfig{Servers: serverConfigs}

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Router:       router,
	}
	server.RoutingConfig.Store(mcpConfig)
	server.ResponseHandler = &routing.ResponseHandler202511{
		RoutingConfig:      &server.RoutingConfig,
		SessionCache:       cache,
		ElicitationEnabled: false,
		Logger:             logger,
	}
	return server
}

// requestHeadersStep returns a standard request headers step
func requestHeadersStep() mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{
							{Key: "content-type", RawValue: []byte("application/json")},
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
								RemoveHeaders: []string{"x-mcp-authorized", "x-mcp-virtualserver", "x-mcp-verified-sub"},
							},
						},
					},
				},
			},
		},
	}
}

// responseHeadersStep returns a standard response headers step with 200 status
func responseHeadersStep() mockProcessServerMessageAndErr {
	return mockProcessServerMessageAndErr{
		msg: &extProcV3.ProcessingRequest{
			Request: &extProcV3.ProcessingRequest_ResponseHeaders{
				ResponseHeaders: &extProcV3.HttpHeaders{
					Headers: &corev3.HeaderMap{
						Headers: []*corev3.HeaderValue{
							{Key: ":status", Value: "200"},
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
				Headers: &extProcV3.HeaderMutation{
					SetHeaders: []*corev3.HeaderValueOption{
						{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("text/plain")}},
					},
				},
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
		actualResponseHeaders, ok := actualResp.Response.(*extProcV3.ProcessingResponse_ResponseHeaders)
		require.True(m.t, ok, "expected response type to be ResponseHeaders, but it was a %T", actualResp.Response)
		requireMatchingCommonHeaderMutation(m.t, v.ResponseHeaders.Response, actualResponseHeaders.ResponseHeaders.Response)
	case *extProcV3.ProcessingResponse_ImmediateResponse:
		actualImmediateBody, ok := actualResp.Response.(*extProcV3.ProcessingResponse_ImmediateResponse)
		require.True(m.t, ok, "expected response type to be ImmediateResponse, but it was a %T", actualResp.Response)
		require.NotNil(m.t, actualImmediateBody.ImmediateResponse, "expected response needs body")
		require.NotNil(m.t, actualImmediateBody.ImmediateResponse.Body, "expected response needs body response")
		require.NotNil(m.t, v.ImmediateResponse.Body, "expected response needs body")
		// when the expected body is a real payload (not the "dummy" sentinel), assert
		// it exactly — this lets a test distinguish, e.g., a -32700 from a -32600 body
		if string(v.ImmediateResponse.Body) != "dummy" {
			require.Equal(m.t, string(v.ImmediateResponse.Body), string(actualImmediateBody.ImmediateResponse.Body))
		}
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
		if actual != nil && actual.HeaderMutation != nil {
			require.Empty(t, actual.HeaderMutation.SetHeaders)
			require.Empty(t, actual.HeaderMutation.RemoveHeaders)
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
	actualByKey := make(map[string]*corev3.HeaderValueOption, len(actual.SetHeaders))
	for _, h := range actual.SetHeaders {
		actualByKey[h.Header.Key] = h
	}
	for _, expOpt := range expected.SetHeaders {
		exp := expOpt.Header
		actOpt, ok := actualByKey[exp.Key]
		require.True(t, ok, "expected header %q not found in actual headers", exp.Key)
		require.Equal(t, expOpt.AppendAction, actOpt.AppendAction, "mismatch on header %q append action", exp.Key)
		act := actOpt.Header
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

// auditLogRecord holds fields captured from a single slog record.
type auditLogRecord struct {
	msg   string
	attrs map[string]string
}

// captureLogHandler is a minimal slog.Handler that calls fn for every record at Info+.
type captureLogHandler struct {
	fn func(slog.Record)
}

func (c *captureLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (c *captureLogHandler) Handle(_ context.Context, r slog.Record) error {
	c.fn(r)
	return nil
}

func (c *captureLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *captureLogHandler) WithGroup(_ string) slog.Handler      { return c }

// stubRouter is a Router that always returns a successful passthrough decision.
type stubRouter struct{}

func (s *stubRouter) RouteRequest(_ context.Context, _ *routing.Request) *routing.Decision {
	return &routing.Decision{}
}

// stubErrorRouter is a Router that always returns an error decision with the given status code.
type stubErrorRouter struct{ statusCode int }

func (s *stubErrorRouter) RouteRequest(_ context.Context, _ *routing.Request) *routing.Decision {
	return &routing.Decision{Error: &routing.Error{StatusCode: s.statusCode, Message: "routing error"}}
}

func TestProcessBrowserCORS(t *testing.T) {
	t.Run("answers preflight", func(t *testing.T) {
		expected := immediateResponse(typev3.StatusCode_NoContent)
		expected.Response.(*extProcV3.ProcessingResponse_ImmediateResponse).ImmediateResponse.Headers.SetHeaders = append(
			append([]*corev3.HeaderValueOption{}, browserCORSHeaders...),
			headerOption("content-type", "text/plain"),
		)
		mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{{
			msg:  browserRequestHeaders(http.MethodOptions, true),
			resp: []*extProcV3.ProcessingResponse{expected},
		}})

		require.NoError(t, newTestServer(t).Process(mock))
		mock.verifyAllResponsesConsumed()
	})

	t.Run("adds response headers", func(t *testing.T) {
		requestStep := requestHeadersStep()
		requestStep.msg = browserRequestHeaders(http.MethodGet, false)
		requestStep.msg.GetRequestHeaders().EndOfStream = true
		responseStep := responseHeadersStep()
		responseStep.resp[0].Response.(*extProcV3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response = &extProcV3.CommonResponse{
			HeaderMutation: &extProcV3.HeaderMutation{SetHeaders: browserCORSHeaders},
		}
		mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
			requestStep,
			responseStep,
		})

		require.NoError(t, newTestServer(t).Process(mock))
		mock.verifyAllResponsesConsumed()
	})
}

func browserRequestHeaders(method string, preflight bool) *extProcV3.ProcessingRequest {
	headers := []*corev3.HeaderValue{
		{Key: ":method", RawValue: []byte(method)},
		{Key: "content-type", RawValue: []byte("application/json")},
		{Key: "origin", RawValue: []byte("https://console.example.com")},
	}
	if preflight {
		headers = append(headers, &corev3.HeaderValue{Key: "access-control-request-method", RawValue: []byte(http.MethodPost)})
	}
	return &extProcV3.ProcessingRequest{
		Request: &extProcV3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extProcV3.HttpHeaders{Headers: &corev3.HeaderMap{Headers: headers}},
		},
	}
}

// stubResponseHandler is a ResponseHandler that always returns an empty decision.
type stubResponseHandler struct{}

func (s *stubResponseHandler) HandleResponse(_ context.Context, _ *routing.ResponseInput) *routing.ResponseDecision {
	return &routing.ResponseDecision{SetHeaders: map[string]string{}}
}

// makeTestBearer builds a minimal unsigned JWT with the given sub claim, sufficient
// for ExtractSubClaim (which does not validate signatures).
func makeTestBearer(sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
	return "Bearer " + hdr + "." + payload + ".sig"
}

// TestProcess_ToolCallAuditLog verifies that an Info-level "tool call" audit log entry
// is emitted when a tools/call request reaches the response headers phase, and that
// it carries user, tool, server, status, request_id, and session fields.
func TestProcess_ToolCallAuditLog(t *testing.T) {
	var mu sync.Mutex
	var logs []auditLogRecord

	captureHandler := &captureLogHandler{fn: func(r slog.Record) {
		attrs := make(map[string]string)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		mu.Lock()
		logs = append(logs, auditLogRecord{msg: r.Message, attrs: attrs})
		mu.Unlock()
	}}

	cache, err := session.NewCache()
	require.NoError(t, err)

	srv := &ExtProcServer{
		Logger:          slog.New(captureHandler),
		SessionCache:    cache,
		Router:          &stubRouter{},
		ResponseHandler: &stubResponseHandler{},
	}
	srv.RoutingConfig.Store(&config.MCPServersConfig{})

	toolCallBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"myserver__echo","arguments":{}}}`)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{
					RequestHeaders: &extProcV3.HttpHeaders{
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{
								{Key: "content-type", RawValue: []byte("application/json")},
								{Key: "x-request-id", RawValue: []byte("req-abc")},
								{Key: "authorization", RawValue: []byte(makeTestBearer("alice@example.com"))},
								{Key: "mcp-session-id", RawValue: []byte("gw-sess-1")},
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
										{Header: &corev3.HeaderValue{Key: "x-mcp-verified-sub", RawValue: []byte("alice@example.com")}},
									},
									RemoveHeaders: []string{"x-mcp-authorized", "x-mcp-virtualserver", "x-mcp-verified-sub"},
								},
							},
						},
					},
				},
			},
		},
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body:        toolCallBody,
						EndOfStream: true,
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_RequestBody{
						RequestBody: &extProcV3.BodyResponse{
							Response: &extProcV3.CommonResponse{
								HeaderMutation: &extProcV3.HeaderMutation{},
							},
						},
					},
				},
			},
		},
		// response headers — use RawValue so getSingleValueHeader picks it up
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_ResponseHeaders{
					ResponseHeaders: &extProcV3.HttpHeaders{
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{
								{Key: ":status", RawValue: []byte("200")},
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
		},
	})

	err = srv.Process(mock)
	require.NoError(t, err)
	mock.verifyAllResponsesConsumed()

	mu.Lock()
	defer mu.Unlock()
	var found *auditLogRecord
	for i := range logs {
		if logs[i].msg == "tool call" {
			found = &logs[i]
			break
		}
	}
	require.NotNil(t, found, "expected 'tool call' audit log entry")
	require.Equal(t, "true", found.attrs["audit"])
	require.Equal(t, "alice@example.com", found.attrs["user"])
	require.Equal(t, "200", found.attrs["status"])
	require.Equal(t, "req-abc", found.attrs["request_id"])
	require.NotEmpty(t, found.attrs["tool"])
	require.Empty(t, found.attrs["server"]) // stub router does not populate ServerName
	// session must be the log-safe form (jti: or sha256: prefix), never a raw JWT
	session := found.attrs["session"]
	require.True(t,
		strings.HasPrefix(session, "jti:") || strings.HasPrefix(session, "sha256:"),
		"session field must be log-safe, got: %s", session)
}

// TestProcess_ToolCallAuditLog_RouterError verifies that an audit log entry is
// emitted even when the router returns an error decision (e.g. session init
// failure), where the response headers phase is never reached.
func TestProcess_ToolCallAuditLog_RouterError(t *testing.T) {
	var mu sync.Mutex
	var logs []auditLogRecord

	captureHandler := &captureLogHandler{fn: func(r slog.Record) {
		attrs := make(map[string]string)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		mu.Lock()
		logs = append(logs, auditLogRecord{msg: r.Message, attrs: attrs})
		mu.Unlock()
	}}

	cache, err := session.NewCache()
	require.NoError(t, err)

	srv := &ExtProcServer{
		Logger:          slog.New(captureHandler),
		SessionCache:    cache,
		Router:          &stubErrorRouter{statusCode: 500},
		ResponseHandler: &stubResponseHandler{},
	}
	srv.RoutingConfig.Store(&config.MCPServersConfig{})

	toolCallBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"myserver__echo","arguments":{}}}`)

	mock := makeMockProcessServer(t, []mockProcessServerMessageAndErr{
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestHeaders{
					RequestHeaders: &extProcV3.HttpHeaders{
						Headers: &corev3.HeaderMap{
							Headers: []*corev3.HeaderValue{
								{Key: "content-type", RawValue: []byte("application/json")},
								{Key: "x-request-id", RawValue: []byte("req-err-001")},
								{Key: "authorization", RawValue: []byte(makeTestBearer("alice@example.com"))},
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
										{Header: &corev3.HeaderValue{Key: "x-mcp-verified-sub", RawValue: []byte("alice@example.com")}},
									},
									RemoveHeaders: []string{"x-mcp-authorized", "x-mcp-virtualserver", "x-mcp-verified-sub"},
								},
							},
						},
					},
				},
			},
		},
		{
			msg: &extProcV3.ProcessingRequest{
				Request: &extProcV3.ProcessingRequest_RequestBody{
					RequestBody: &extProcV3.HttpBody{
						Body:        toolCallBody,
						EndOfStream: true,
					},
				},
			},
			resp: []*extProcV3.ProcessingResponse{
				{
					Response: &extProcV3.ProcessingResponse_ImmediateResponse{
						ImmediateResponse: &extProcV3.ImmediateResponse{
							Body:   []byte("dummy"),
							Status: &typev3.HttpStatus{Code: typev3.StatusCode_InternalServerError},
							Headers: &extProcV3.HeaderMutation{
								SetHeaders: []*corev3.HeaderValueOption{
									{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("text/plain")}},
								},
							},
						},
					},
				},
			},
		},
	})

	// After the immediate response, Process loops back to Recv. Signal stream end.
	mock.serverStream = append(mock.serverStream, mockProcessServerMessageAndErr{
		msgErr: fmt.Errorf("EOF"),
	})

	err = srv.Process(mock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "EOF")

	mu.Lock()
	defer mu.Unlock()
	var found *auditLogRecord
	for i := range logs {
		if logs[i].msg == "tool call" {
			found = &logs[i]
			break
		}
	}
	require.NotNil(t, found, "expected 'tool call' audit log entry for error path")
	require.Equal(t, "true", found.attrs["audit"])
	require.Equal(t, "alice@example.com", found.attrs["user"])
	require.Equal(t, "500", found.attrs["status"])
	require.Equal(t, "req-err-001", found.attrs["request_id"])
	require.NotEmpty(t, found.attrs["tool"])
	require.Empty(t, found.attrs["server"]) // stub router does not populate ServerName
	// session is empty on the error path when no gateway session was established
	require.Empty(t, found.attrs["session"])
}

// TestExtProcServer_OnConfigChange_DataRace exercises a config-reload landing
// concurrently with a request-handler read of RoutingConfig. The race detector
// is the assertion; run with go test -race ./internal/mcp-router/...
func TestExtProcServer_OnConfigChange_DataRace(t *testing.T) {
	server := &ExtProcServer{
		Logger: slog.Default(),
	}
	server.RoutingConfig.Store(&config.MCPServersConfig{
		MCPGatewayExternalHostname: "initial.gateway",
	})

	const iterations = 5000

	var wg sync.WaitGroup
	start := make(chan struct{})

	// reader: invoke a handler that reads RoutingConfig.Load() on the hot path
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			if _, err := server.HandleRequestHeaders(context.Background(), nil); err != nil {
				t.Errorf("HandleRequestHeaders returned error during race exercise: %v", err)
				return
			}
		}
	}()

	// writer: mirror OnConfigChange firing repeatedly from viper fsnotify
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			server.OnConfigChange(context.Background(), &config.MCPServersConfig{
				MCPGatewayExternalHostname: "replacement.gateway",
			})
		}
	}()

	// release both goroutines together for deterministic concurrent exercise of Load/Store
	close(start)
	wg.Wait()
}
