// Package mcprouter ext proc process
package mcprouter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var _ config.Observer = &ExtProcServer{}

// ExtProcServer is the ext_proc adapter that translates between Envoy's
// external processing protocol and the Router interface.
type ExtProcServer struct {
	RoutingConfig      atomic.Pointer[config.MCPServersConfig]
	Logger             *slog.Logger
	SessionCache       routing.SessionCache
	ElicitationMap     idmap.Map
	MaxRequestBodySize int
	Router             routing.Router
	ResponseHandler    routing.ResponseHandler
}

// OnConfigChange is used to register the router for config changes
func (s *ExtProcServer) OnConfigChange(_ context.Context, newConfig *config.MCPServersConfig) {
	s.RoutingConfig.Store(newConfig)
}

// HandleRequestHeaders sets the gateway authority and extracts the verified sub claim.
func (s *ExtProcServer) HandleRequestHeaders(ctx context.Context, headers *extProcV3.HttpHeaders) ([]*extProcV3.ProcessingResponse, error) {
	s.Logger.DebugContext(ctx, "Request Handler: HandleRequestHeaders called")
	requestHeaders := NewHeaders()
	response := NewResponse()
	requestHeaders.WithAuthority(s.RoutingConfig.Load().MCPGatewayExternalHostname)
	authHeader := getSingleValueHeader(headers.GetHeaders(), routing.AuthorizationHeader)
	if sub, _ := internaljwt.ExtractSubClaim(authHeader); sub != "" {
		requestHeaders.WithVerifiedSub(sub)
	}
	return response.WithRequestHeadersResponse(requestHeaders.Build(), routing.InternalOnlyHeaders...).Build(), nil
}

// decisionToResponse translates a transport-agnostic RoutingDecision into
// ext_proc ProcessingResponse(s) for the body phase.
func decisionToResponse(d *routing.Decision) []*extProcV3.ProcessingResponse {
	rb := NewResponse()

	if d.Error != nil {
		if d.Error.JSONRPCErr != "" {
			rb.WithImmediateJSONRPCResponse(int32(d.Error.StatusCode), decisionHeaders(d), d.Error.JSONRPCErr) //nolint:gosec // HTTP status codes are bounded 100-599
		} else {
			rb.WithImmediateResponse(int32(d.Error.StatusCode), d.Error.Message) //nolint:gosec // HTTP status codes are bounded 100-599
		}
		return rb.Build()
	}

	headers := decisionHeaders(d)
	if d.BodyMutation != nil {
		rb.WithRequestBodyHeadersAndBodyResponse(headers, d.BodyMutation, d.UnsetHeaders...)
	} else if len(d.UnsetHeaders) > 0 {
		rb.WithRequestBodySetUnsetHeadersResponse(headers, d.UnsetHeaders)
	} else {
		rb.WithRequestBodyHeadersResponse(headers)
	}
	return rb.Build()
}

// decisionHeaders merges Authority and Path from the decision into the
// SetHeaders map, ensuring pseudo-headers are always present when set.
func decisionHeaders(d *routing.Decision) []*basepb.HeaderValueOption {
	headers := make([]*basepb.HeaderValueOption, 0, len(d.SetHeaders)+2)
	if d.Authority != "" {
		headers = append(headers, &basepb.HeaderValueOption{
			Header: &basepb.HeaderValue{Key: ":authority", RawValue: []byte(d.Authority)},
		})
	}
	if d.Path != "" {
		headers = append(headers, &basepb.HeaderValueOption{
			Header: &basepb.HeaderValue{Key: ":path", RawValue: []byte(d.Path)},
		})
	}
	for k, v := range d.SetHeaders {
		if k == ":authority" || k == ":path" {
			continue
		}
		headers = append(headers, &basepb.HeaderValueOption{
			Header: &basepb.HeaderValue{Key: k, RawValue: []byte(v)},
		})
	}
	return headers
}

// responseDecisionToResponse translates a ResponseDecision to ext_proc responses.
func responseDecisionToResponse(d *routing.ResponseDecision) []*extProcV3.ProcessingResponse {
	rb := NewResponse()
	headers := make([]*basepb.HeaderValueOption, 0, len(d.SetHeaders))
	for k, v := range d.SetHeaders {
		headers = append(headers, &basepb.HeaderValueOption{
			Header: &basepb.HeaderValue{Key: k, RawValue: []byte(v)},
		})
	}
	responses := rb.WithResponseHeaderResponse(headers).Build()

	if d.StreamBody && len(responses) > 0 {
		responses[0].ModeOverride = &extprochttp.ProcessingMode{
			RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
			ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
			RequestBodyMode:     extprochttp.ProcessingMode_STREAMED,
			ResponseBodyMode:    extprochttp.ProcessingMode_STREAMED,
			RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
			ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
		}
	}

	return responses
}

// headerMapToMap converts an Envoy HeaderMap to a plain map for the portable routing layer.
func headerMapToMap(hm *basepb.HeaderMap) map[string]string {
	if hm == nil {
		return nil
	}
	m := make(map[string]string, len(hm.Headers))
	for _, h := range hm.Headers {
		if h != nil {
			m[h.Key] = string(h.RawValue)
		}
	}
	return m
}

// Process function
func (s *ExtProcServer) Process(stream extProcV3.ExternalProcessor_ProcessServer) error {
	var (
		localRequestHeaders *extProcV3.HttpHeaders
		requestID           string
		requestPath         string
		endOfStream         = false
		mcpRequest          *routing.MCPRequest
		ctx                 = stream.Context()
		rewriter            *sseRewriter // nil until a tool call response arrives
	)
	span := trace.SpanFromContext(ctx)
	defer func() { span.End() }()
	// ensure orphaned elicitation idmap entries are cleaned up on any exit path
	// (e.g. stream.Recv/Send errors before endOfStream). Flush is idempotent so
	// this is a no-op on the happy path where it has already run.
	defer func() {
		if rewriter != nil {
			_ = rewriter.Flush(ctx)
		}
	}()
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
				recordError(span, err, 500)
				resp := responseBuilder.WithImmediateResponse(500, "internal error").Build()
				for _, res := range resp {
					if sendErr := stream.Send(res); sendErr != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", sendErr)
					}
				}
				return err
			}
			localRequestHeaders = r.RequestHeaders
			endOfStream = r.RequestHeaders.EndOfStream

			ctx = extractTraceContext(ctx, localRequestHeaders.Headers)
			requestID = getSingleValueHeader(localRequestHeaders.Headers, "x-request-id")
			requestPath = getSingleValueHeader(localRequestHeaders.Headers, ":path")
			method := getSingleValueHeader(localRequestHeaders.Headers, ":method")

			span.End()
			ctx, span = tracer().Start(ctx, "mcp-router.process", //nolint:spancheck // ended via defer closure
				trace.WithAttributes(
					componentAttr,
					attribute.String("http.method", method),
					attribute.String("http.path", requestPath),
					attribute.String("http.request_id", requestID),
				),
			)

			responses, _ := s.HandleRequestHeaders(ctx, r.RequestHeaders)
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_RequestHeaders", "request id:", requestID, "path", requestPath, "method", method)
			for _, response := range responses {
				s.Logger.DebugContext(ctx, "sending header processing instructions to envoy", "response", response)
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, "error sending response", "error", err)
					recordError(span, err, 500)
					return err //nolint:spancheck // ended via defer closure
				}
			}
			continue

		case *extProcV3.ProcessingRequest_RequestBody:
			// endOfStream was set on request headers, meaning no body was expected.
			// respond with do-nothing so envoy can continue to the response phase.
			if endOfStream {
				s.Logger.DebugContext(ctx, "body phase received but EndOfStream was set on headers, skipping", "request id", requestID)
				resp := responseBuilder.WithDoNothingResponse(false).Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
			}
			if localRequestHeaders == nil || localRequestHeaders.Headers == nil {
				err := fmt.Errorf("request body received before headers")
				s.Logger.ErrorContext(ctx, err.Error())
				recordError(span, err, 500)
				resp := responseBuilder.WithImmediateResponse(500, "internal error").Build()
				for _, res := range resp {
					if sendErr := stream.Send(res); sendErr != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", sendErr)
					}
				}
				return err
			}
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_RequestBody", "request id:", requestID)
			body := r.RequestBody.Body

			if s.MaxRequestBodySize > 0 && len(body) > s.MaxRequestBodySize {
				err := fmt.Errorf("request body too large: %d bytes exceeds limit of %d", len(body), s.MaxRequestBodySize)
				s.Logger.ErrorContext(ctx, err.Error(), "request id", requestID)
				recordError(span, err, 413)
				resp := responseBuilder.WithImmediateResponse(413, "request body too large").Build()
				for _, res := range resp {
					if sendErr := stream.Send(res); sendErr != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", sendErr)
					}
				}
				return err
			}

			// non-JSON requests (e.g. form submissions to /tokens) pass through
			contentType := getSingleValueHeader(localRequestHeaders.Headers, "content-type")
			if !strings.Contains(strings.ToLower(contentType), "application/json") {
				s.Logger.DebugContext(ctx, "non-JSON content-type, passing through", "content-type", contentType)
				resp := responseBuilder.WithDoNothingResponse(false).Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
			}

			if len(body) == 0 {
				s.Logger.DebugContext(ctx, "empty request body, skipping", "request id", requestID)
				resp := responseBuilder.WithDoNothingResponse(false).Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
			}
			if err := json.Unmarshal(body, &mcpRequest); err != nil {
				s.Logger.ErrorContext(ctx, "error unmarshalling request body", "error", err)
				recordError(span, err, 400)
				resp := responseBuilder.WithImmediateResponse(400, "invalid request body").Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
			}
			if _, err := mcpRequest.Validate(); err != nil {
				s.Logger.ErrorContext(ctx, "Invalid MCPRequest", "error", err)
				recordError(span, err, 400)
				resp := responseBuilder.WithImmediateResponse(400, "invalid mcp request").Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
			}
			mcpRequest.Headers = headerMapToMap(localRequestHeaders.Headers)
			span.SetAttributes(spanAttributes(mcpRequest)...)

			routingReq := &routing.Request{
				MCPMethod:       mcpRequest.Method,
				MCPName:         mcpRequest.ToolName(),
				ProtocolVersion: getSingleValueHeader(localRequestHeaders.Headers, "mcp-protocol-version"),
				Authority:       getSingleValueHeader(localRequestHeaders.Headers, routing.AuthorityHeader),
				SessionID:       mcpRequest.GetSessionID(),
				Path:            requestPath,
				RequestID:       requestID,
				Body:            body,
				Parsed:          mcpRequest,
			}
			decision := s.Router.RouteRequest(ctx, routingReq)
			routeResponses := decisionToResponse(decision)
			for _, response := range routeResponses {
				s.Logger.DebugContext(ctx, "sending mcp body routing instructions to envoy", "response", response)
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, "error sending response", "error", err)
					recordError(span, err, 500)
					return err
				}
			}
			continue

		case *extProcV3.ProcessingRequest_ResponseHeaders:
			if r.ResponseHeaders == nil || localRequestHeaders == nil {
				err := fmt.Errorf("no response headers or request headers")
				recordError(span, err, 500)
				resp := responseBuilder.WithImmediateResponse(500, "internal error").Build()
				for _, res := range resp {
					if sendErr := stream.Send(res); sendErr != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", sendErr)
					}
				}
				return err
			}
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_ResponseHeaders", "request id:", requestID)

			statusCode := getSingleValueHeader(r.ResponseHeaders.Headers, ":status")
			span.SetAttributes(attribute.String("http.status_code", statusCode))

			respInput := &routing.ResponseInput{
				StatusCode:        statusCode,
				GatewaySessionID:  getSingleValueHeader(localRequestHeaders.Headers, routing.SessionHeader),
				ResponseSessionID: getSingleValueHeader(r.ResponseHeaders.Headers, routing.SessionHeader),
				InitHost:          getSingleValueHeader(localRequestHeaders.Headers, "mcp-init-host"),
				Request:           mcpRequest,
			}

			// populate client elicitation flag before response handling
			if mcpRequest != nil && mcpRequest.IsToolCall() {
				clientElicitation, elErr := s.SessionCache.GetClientElicitation(ctx, mcpRequest.GetSessionID())
				if elErr != nil {
					s.Logger.ErrorContext(ctx, "failed to check client elicitation", "error", elErr)
				}
				mcpRequest.ClientElicitation = clientElicitation
			}

			respDecision := s.ResponseHandler.HandleResponse(ctx, respInput)
			responses := responseDecisionToResponse(respDecision)

			if respDecision.StreamBody {
				rewriter = &sseRewriter{
					idMap:      s.ElicitationMap,
					req:        mcpRequest,
					logger:     s.Logger,
					gatewayIDs: make([]string, 0),
				}
			}

			for _, response := range responses {
				s.Logger.DebugContext(ctx, "sending response header processing instructions to envoy", "response", response)
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, "error sending response", "error", err)
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
					remaining := rewriter.Flush(ctx)
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
