// Package mcprouter ext proc process
package mcprouter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/headers"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
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
	RoutingConfig       atomic.Pointer[config.MCPServersConfig]
	Logger              *slog.Logger
	SessionCache        routing.SessionCache
	ElicitationMap      idmap.Map
	MaxRequestBodySize  int
	Router              routing.Router
	ResponseHandler     routing.ResponseHandler
	Router202607        routing.Router
	ResponseHandler2026 routing.ResponseHandler
	// EnableA2A turns on the experimental A2A passthrough: /a2a traffic gets its
	// protocol metadata lifted into headers for Telemetry and AuthPolicy. Off by
	// default; no A2A code path runs unless it is set.
	EnableA2A bool
}

// OnConfigChange is used to register the router for config changes
func (s *ExtProcServer) OnConfigChange(_ context.Context, newConfig *config.MCPServersConfig) {
	s.RoutingConfig.Store(newConfig)
}

func (s *ExtProcServer) requestBodyLimit() int {
	limit := int(config.DefaultMaxBodyBytes)
	if cfg := s.RoutingConfig.Load(); cfg != nil {
		limit = int(cfg.GetMaxBodyBytes())
	}
	if s.MaxRequestBodySize > 0 && s.MaxRequestBodySize < limit {
		return s.MaxRequestBodySize
	}
	return limit
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
			contentType := d.Error.ContentType
			if contentType == "" {
				contentType = "text/event-stream"
			}
			rb.WithImmediateJSONRPCResponse(int32(d.Error.StatusCode), decisionHeaders(d), d.Error.JSONRPCErr, contentType) //nolint:gosec // HTTP status codes are bounded 100-599
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
		protocolVersion     string
		mcpMethodHeader     string
		mcpNameHeader       string
		endOfStream         = false
		mcpRequest          *routing.MCPRequest
		ctx                 = stream.Context()
		isA2A               = false              // true for /a2a traffic when A2A passthrough is enabled
		rewriter            *elicitationRewriter // nil until a tool call response arrives
		resourceRewriter    *resourceURIRewriter // nil until a tool call response with resources arrives
	)
	span := trace.SpanFromContext(ctx)
	defer func() { span.End() }()
	// ensure orphaned elicitation idmap entries and response mutations are cleaned up on any exit path
	// (e.g. stream.Recv/Send errors before endOfStream). Flush is idempotent so
	// this is a no-op on the happy path where it has already run.
	defer func() {
		if rewriter != nil {
			_ = rewriter.Flush(ctx)
		}
		if resourceRewriter != nil {
			_ = resourceRewriter.Flush(ctx)
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
			protocolVersion = getSingleValueHeader(localRequestHeaders.Headers, "mcp-protocol-version")
			mcpMethodHeader = getSingleValueHeader(localRequestHeaders.Headers, "mcp-method")
			mcpNameHeader = getSingleValueHeader(localRequestHeaders.Headers, "mcp-name")

			span.End()
			ctx, span = tracer().Start(ctx, "mcp-router.process", //nolint:spancheck // ended via defer closure
				trace.WithAttributes(
					componentAttr,
					attribute.String("http.method", method),
					attribute.String("http.path", requestPath),
					attribute.String("http.request_id", requestID),
					attribute.String("mcp.protocol_version", protocolVersion),
					attribute.String("mcp.header.method", mcpMethodHeader),
					attribute.String("mcp.header.name", mcpNameHeader),
				),
			)

			// A2A passthrough: on /a2a traffic, strip any client-supplied x-a2a-*
			// and set x-a2a-agent from the path, then let the request continue to
			// the user's own route. Routing and :authority are not touched — the
			// user's HTTPRoute carries the request to the agent. The method is only
			// known at the body, so x-a2a-method is set there.
			if s.EnableA2A && isA2APath(requestPath) {
				isA2A = true
				// a POST with no body has no request-body phase, so x-a2a-method would
				// never be set — fail closed rather than forward it to the agent
				// unlabeled. GET (discovery) legitimately has no body and passes through.
				if method == http.MethodPost && endOfStream {
					s.Logger.DebugContext(ctx, "[ext_proc] Process: A2A POST with no body, failing closed", "request id", requestID, "path", requestPath)
					resp := responseBuilder.WithImmediateJSONRPCResponse(200, nil, a2aErrorBody(nil, a2aErrParse, "invalid json-rpc request"), "application/json").Build()
					for _, response := range resp {
						if err := stream.Send(response); err != nil {
							s.Logger.ErrorContext(ctx, "error sending response", "error", err)
							recordError(span, err, 500)
							return err //nolint:spancheck // ended via defer closure
						}
					}
					return nil
				}
				a2aHeaders := NewHeaders()
				if agent := a2aAgentFromPath(requestPath); agent != "" {
					a2aHeaders.WithCustomHeader(headers.A2AAgentHeader, agent)
					span.SetAttributes(attribute.String("a2a.agent", agent))
				}
				s.Logger.DebugContext(ctx, "[ext_proc] Process: A2A request headers", "request id", requestID, "path", requestPath, "method", method)
				resp := responseBuilder.WithRequestHeadersResponse(a2aHeaders.Build(), a2aInternalHeaders...).Build()
				for _, response := range resp {
					if err := stream.Send(response); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						recordError(span, err, 500)
						return err
					}
				}
				continue
			}

			responses, _ := s.HandleRequestHeaders(ctx, r.RequestHeaders)
			s.Logger.DebugContext(ctx, "[ext_proc ] Process: ProcessingRequest_RequestHeaders", "request id:", requestID, "path", requestPath, "method", method)
			for _, response := range responses {
				s.Logger.DebugContext(ctx, "sending header processing instructions to envoy", "response", response)
				if err := stream.Send(response); err != nil {
					s.Logger.ErrorContext(ctx, "error sending response", "error", err)
					recordError(span, err, 500)
					return err
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

			if limit := s.requestBodyLimit(); limit > 0 && len(body) > limit {
				err := fmt.Errorf("request body too large: %d bytes exceeds limit of %d", len(body), limit)
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

			// A2A passthrough: parse the JSON-RPC envelope only and set x-a2a-method
			// (normalized to a bounded set). An unparseable body fails closed with a
			// JSON-RPC -32700 rather than reaching the agent without the metadata this
			// phase records. The body is never mutated.
			if isA2A {
				method, _, parseErr := parseA2AMethod(body)
				// fail closed on anything that isn't a usable JSON-RPC request: an
				// unparseable body (-32700), or valid JSON with no method to label
				// (-32600). The immediate response terminates the request, so nothing
				// reaches the agent without the metadata this phase records.
				if parseErr != nil || method == "" {
					code := a2aErrParse
					if parseErr == nil {
						code = a2aErrInvalidRequest
					}
					s.Logger.DebugContext(ctx, "[ext_proc] Process: A2A body not a valid json-rpc request, failing closed", "request id", requestID, "error", parseErr)
					resp := responseBuilder.WithImmediateJSONRPCResponse(200, nil, a2aErrorBody(nil, code, "invalid json-rpc request"), "application/json").Build()
					for _, res := range resp {
						if err := stream.Send(res); err != nil {
							s.Logger.ErrorContext(ctx, "error sending response", "error", err)
							return err
						}
					}
					return nil
				}
				norm := normalizeA2AMethod(method)
				span.SetAttributes(attribute.String("a2a.method", norm))
				s.Logger.DebugContext(ctx, "[ext_proc] Process: A2A request body", "request id", requestID, "method", norm)
				a2aHeaders := NewHeaders().WithCustomHeader(headers.A2AMethodHeader, norm)
				resp := responseBuilder.WithRequestBodyHeadersResponse(a2aHeaders.Build()).Build()
				for _, res := range resp {
					if err := stream.Send(res); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						return err
					}
				}
				continue
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
				ProtocolVersion: protocolVersion,
				// use the configured hostname, not the original :authority from the
				// client request which may include a port. HandleRequestHeaders
				// already rewrites :authority to this value for Envoy.
				Authority: s.RoutingConfig.Load().MCPGatewayExternalHostname,
				SessionID: mcpRequest.GetSessionID(),
				Path:      requestPath,
				RequestID: requestID,
				Body:      body,
				Parsed:    mcpRequest,
			}

			// path-based protocol override: /mcp/stateful forces 2025
			effectiveVersion := protocolVersion
			if strings.HasSuffix(requestPath, protocol.PathSuffixStateful) {
				effectiveVersion = protocol.Version2025
			}

			router := s.Router
			routerName := "202511"
			if s.Router202607 != nil && effectiveVersion == protocol.Version2026 {
				routerName = "202607"
				routingReq.MCPMethod = mcpMethodHeader
				routingReq.MCPName = mcpNameHeader
				router = s.Router202607
			}

			span.SetAttributes(attribute.String("mcp.router", routerName))
			s.Logger.DebugContext(ctx, "routing request", "router", routerName, "protocol-version", protocolVersion, "mcp-method", routingReq.MCPMethod, "mcp-name", routingReq.MCPName)
			decision := router.RouteRequest(ctx, routingReq)
			if decision.Error != nil && mcpRequest.IsToolCall() {
				authSub, _ := internaljwt.ExtractSubClaim(mcpRequest.Headers[routing.AuthorizationHeader])
				s.Logger.InfoContext(ctx, "tool call",
					"audit", true,
					"user", authSub,
					"tool", mcpRequest.ToolName(),
					"server", mcpRequest.ServerName,
					"status", strconv.Itoa(decision.Error.StatusCode),
					"request_id", requestID,
					"session", internaljwt.LogSafeSessionID(mcpRequest.GetSessionID()),
				)
			}
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

			// A2A passthrough: the response is not observed in this phase — pass it
			// through unchanged. No ModeOverride is set, so no response body follows.
			if isA2A {
				resp := responseBuilder.WithDoNothingResponseHeaderResponse().Build()
				for _, response := range resp {
					if err := stream.Send(response); err != nil {
						s.Logger.ErrorContext(ctx, "error sending response", "error", err)
						recordError(span, err, 500)
						return err
					}
				}
				return nil
			}

			statusCode := getSingleValueHeader(r.ResponseHeaders.Headers, ":status")
			span.SetAttributes(
				attribute.String("http.status_code", statusCode),
				attribute.String("mcp.response.protocol_version", protocolVersion),
			)

			respInput := &routing.ResponseInput{
				StatusCode:        statusCode,
				GatewaySessionID:  getSingleValueHeader(localRequestHeaders.Headers, routing.SessionHeader),
				ResponseSessionID: getSingleValueHeader(r.ResponseHeaders.Headers, routing.SessionHeader),
				InitHost:          getSingleValueHeader(localRequestHeaders.Headers, "mcp-init-host"),
				Request:           mcpRequest,
			}

			respHandler := s.ResponseHandler
			if s.ResponseHandler2026 != nil && protocolVersion == protocol.Version2026 {
				respHandler = s.ResponseHandler2026
			} else {
				if mcpRequest != nil && mcpRequest.IsToolCall() {
					clientElicitation, elErr := s.SessionCache.GetClientElicitation(ctx, mcpRequest.GetSessionID())
					if elErr != nil {
						s.Logger.ErrorContext(ctx, "failed to check client elicitation", "error", elErr)
					}
					mcpRequest.ClientElicitation = clientElicitation
				}
			}

			respDecision := respHandler.HandleResponse(ctx, respInput)

			if mcpRequest != nil && mcpRequest.IsToolCall() {
				authSub, _ := internaljwt.ExtractSubClaim(mcpRequest.Headers[routing.AuthorizationHeader])
				s.Logger.InfoContext(ctx, "tool call",
					"audit", true,
					"user", authSub,
					"tool", mcpRequest.ToolName(),
					"server", mcpRequest.ServerName,
					"status", statusCode,
					"request_id", requestID,
					"session", internaljwt.LogSafeSessionID(mcpRequest.GetSessionID()),
				)
			}

			responses := responseDecisionToResponse(respDecision)

			if respDecision.StreamBody {
				rewriter = &elicitationRewriter{
					idMap:      s.ElicitationMap,
					req:        mcpRequest,
					logger:     s.Logger,
					gatewayIDs: make([]string, 0),
				}
				// also construct resourceURIRewriter for tool calls with resources on 200 responses
				if mcpRequest.ServerPrefix != "" && statusCode == "200" {
					resourceRewriter = &resourceURIRewriter{
						prefix: mcpRequest.ServerPrefix,
						logger: s.Logger,
					}
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

			if resourceRewriter != nil {
				body = resourceRewriter.Process(ctx, body)

				if endOfStream {
					remaining := resourceRewriter.Flush(ctx)
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
