// Package mcprouter ext proc process
package mcprouter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/mark3labs/mcp-go/client"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var _ config.Observer = &ExtProcServer{}

// SessionCache defines how the router interacts with a store to store and retrieves sessions
type SessionCache interface {
	GetSession(ctx context.Context, key string) (map[string]string, error)
	AddSession(ctx context.Context, key, mcpID, mcpSession string) (bool, error)
	DeleteSessions(ctx context.Context, key ...string) error
	RemoveServerSession(ctx context.Context, key, mcpServerID string) error
	KeyExists(ctx context.Context, key string) (bool, error)
}

// InitForClient defines a function for initializing an MCP server for a client
type InitForClient func(ctx context.Context, gatewayHost, routerKey string, conf *config.MCPServer, passThroughHeaders map[string]string, clientElicitation bool) (*client.Client, error)

// ExtProcServer struct boolean for streaming & Store headers for later use in body processing
type ExtProcServer struct {
	RoutingConfig     *config.MCPServersConfig
	JWTManager        *session.JWTManager
	Logger            *slog.Logger
	InitForClient     InitForClient
	SessionCache      SessionCache
	ElicitationMap    idmap.Map
	clientElicitation sync.Map // gateway session ID -> bool (true if client supports elicitation)
	//TODO this should not be needed
	Broker broker.MCPBroker
}

// OnConfigChange is used to register the router for config changes
func (s *ExtProcServer) OnConfigChange(_ context.Context, newConfig *config.MCPServersConfig) {
	s.RoutingConfig = newConfig
}

// Process function
func (s *ExtProcServer) Process(stream extProcV3.ExternalProcessor_ProcessServer) error {
	var (
		localRequestHeaders *extProcV3.HttpHeaders
		requestID           string
		streaming           = false
		mcpRequest          *MCPRequest
		ctx                 = stream.Context()
		rewriter            *sseRewriter // nil until a tool call response arrives
	)
	span := trace.SpanFromContext(ctx)
	defer func() { span.End() }()
	for {
		req, err := stream.Recv()

		if err != nil {
			s.Logger.ErrorContext(ctx, "[ext_proc] Process: Error receiving request", "error", err)
			recordError(span, err, 500)
			return err
		}
		responseBuilder := NewResponse()
		switch r := req.Request.(type) {
		case *extProcV3.ProcessingRequest_RequestHeaders:
			if r.RequestHeaders == nil {
				err := fmt.Errorf("no request headers present")
				recordError(span, err, 400)
				return err
			}
			localRequestHeaders = r.RequestHeaders

			ctx = extractTraceContext(ctx, localRequestHeaders.Headers)
			requestID = getSingleValueHeader(localRequestHeaders.Headers, "x-request-id")
			path := getSingleValueHeader(localRequestHeaders.Headers, ":path")
			method := getSingleValueHeader(localRequestHeaders.Headers, ":method")

			span.End()
			ctx, span = tracer().Start(ctx, "mcp-router.process", //nolint:spancheck // ended via defer closure
				trace.WithAttributes(
					attribute.String("http.method", method),
					attribute.String("http.path", path),
					attribute.String("http.request_id", requestID),
				),
			)

			responses, _ := s.HandleRequestHeaders(r.RequestHeaders)
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_RequestHeaders", "request id:", requestID, "path", path, "method", method)
			for _, response := range responses {
				s.Logger.DebugContext(ctx, fmt.Sprintf("Sending header processing instructions to Envoy: %+v", response))
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
					recordError(span, err, 500)
					return err //nolint:spancheck // ended via defer closure
				}
			}
			continue

		case *extProcV3.ProcessingRequest_RequestBody:
			responses := responseBuilder.WithDoNothingResponse(streaming).Build()
			if localRequestHeaders == nil || localRequestHeaders.Headers == nil {
				s.Logger.ErrorContext(ctx, "Error no request headers present. Exiting")
				for _, response := range responses {
					if err := stream.Send(response); err != nil {
						s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
						return fmt.Errorf("error sending response")
					}
				}
				if localRequestHeaders == nil {
					s.Logger.DebugContext(ctx, "Body process requested before headers arrived")
					err := fmt.Errorf("protocol error: no request headers")
					recordError(span, err, 400)
					return err
				}
				if mcpRequest == nil {
					s.Logger.DebugContext(ctx, "Body process did not receive body")
					err := fmt.Errorf("protocol error: no body")
					recordError(span, err, 400)
					return err
				}
			}
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_RequestBody", "request id:", requestID)
			if len(r.RequestBody.Body) > 0 {
				if err := json.Unmarshal(r.RequestBody.Body, &mcpRequest); err != nil {
					s.Logger.ErrorContext(ctx, fmt.Sprintf("Error unmarshalling request body: %v", err))
					recordError(span, err, 400)
					for _, response := range responses {
						if err := stream.Send(response); err != nil {
							s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
							return err
						}
					}
				}
				if _, err := mcpRequest.Validate(); err != nil {
					s.Logger.ErrorContext(ctx, "Invalid MCPRequest", "error", err)
					recordError(span, err, 400)
					resp := responseBuilder.WithImmediateResponse(400, "invalid mcp request").Build()
					for _, res := range resp {
						if err := stream.Send(res); err != nil {
							s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
							return err
						}
					}
					continue
				}
			}
			mcpRequest.Headers = localRequestHeaders.Headers
			mcpRequest.Streaming = streaming

			if mcpRequest != nil {
				span.SetAttributes(spanAttributes(mcpRequest)...)
			}

			responses = s.RouteMCPRequest(ctx, mcpRequest)
			for _, response := range responses {
				s.Logger.DebugContext(ctx, fmt.Sprintf("Sending MCP body routing instructions to Envoy: %+v", response))
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
					recordError(span, err, 500)
					return err
				}
			}
			continue

		case *extProcV3.ProcessingRequest_ResponseHeaders:
			if r.ResponseHeaders == nil || localRequestHeaders == nil {
				err := fmt.Errorf("no response headers or request headers")
				recordError(span, err, 400)
				return err
			}
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_ResponseHeaders", "request id:", requestID)

			statusCode := getSingleValueHeader(r.ResponseHeaders.Headers, ":status")
			span.SetAttributes(attribute.String("http.status_code", statusCode))

			if mcpRequest != nil && mcpRequest.isToolCall() {
				rewriter = &sseRewriter{
					idMap:      s.ElicitationMap,
					req:        mcpRequest,
					logger:     s.Logger,
					gatewayIDs: make([]string, 0),
				}
			}

			responses, _ := s.HandleResponseHeaders(ctx, r.ResponseHeaders, localRequestHeaders, mcpRequest)
			for _, response := range responses {
				s.Logger.DebugContext(ctx, fmt.Sprintf("Sending response header processing instructions to Envoy: %+v", response))
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, fmt.Sprintf("Error sending response: %v", err))
					recordError(span, err, 500)
					return err
				}
			}
			if rewriter != nil {
				continue // tool call: response body is streamed
			}
			return nil // non-tool-call: response body is not streamed
		case *extProcV3.ProcessingRequest_ResponseBody:
			body := r.ResponseBody.GetBody()
			endOfStream := r.ResponseBody.GetEndOfStream()

			if rewriter != nil {
				body = rewriter.Process(ctx, body)

				if endOfStream {
					remaining := rewriter.Flush()
					body = append(body, remaining...)
				}

			}

			response := &extProcV3.ProcessingResponse{
				Response: &extProcV3.ProcessingResponse_ResponseBody{
					ResponseBody: &extProcV3.BodyResponse{
						Response: &extProcV3.CommonResponse{
							BodyMutation: &extProcV3.BodyMutation{
								Mutation: &extProcV3.BodyMutation_Body{
									Body: body,
								},
							},
						},
					},
				},
			}

			if err := stream.Send(response); err != nil {
				s.Logger.ErrorContext(ctx, "error sending response body", "error", err)
				recordError(span, err, 500)
				return err
			}
			if endOfStream {
				return nil
			}

			continue
		}
	}
}
