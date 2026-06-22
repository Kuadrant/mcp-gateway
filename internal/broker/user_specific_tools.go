package broker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

const gatewaySessionHeader = "Mcp-Session-Id"

// userSpecificServer holds the minimal info needed to fetch tools from a userSpecificList server
type userSpecificServer struct {
	id     config.UpstreamMCPID
	name   string
	url    string
	prefix string
}

// FetchUserSpecificTools fetches tools from userSpecificList servers using the
// caller's session headers and merges them into the result before FilterTools runs.
func (broker *mcpBrokerImpl) FetchUserSpecificTools(ctx context.Context, _ any, mcpReq *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
	broker.mcpLock.RLock()
	servers := broker.userSpecificServers
	broker.mcpLock.RUnlock()

	if len(servers) == 0 {
		return
	}

	ctx, span := brokerTracer().Start(ctx, "broker.user-specific-tools.fetch-all",
		trace.WithAttributes(
			attribute.Int("mcp.user_specific.server_count", len(servers)),
		),
	)
	defer span.End()

	broker.logger.Debug("fetching user-specific tools", "serverCount", len(servers))

	gatewaySessionID := mcpReq.Header.Get(gatewaySessionHeader)
	if gatewaySessionID == "" {
		broker.logger.Error("no gateway session ID for user-specific tool fetch")
		span.SetStatus(codes.Error, "missing gateway session ID")
		return
	}

	userHeaders := filterUserHeaders(mcpReq.Header)

	var cachedSessions map[string]string
	if broker.sessionCache != nil {
		var err error
		cachedSessions, err = broker.sessionCache.GetSession(ctx, gatewaySessionID)
		if err != nil {
			broker.logger.Error("failed to get cached sessions", "error", err)
			cachedSessions = map[string]string{}
		}
	}

	var mu sync.Mutex
	var allTools []mcp.Tool

	g, gCtx := errgroup.WithContext(ctx)
	for _, srv := range servers {
		g.Go(func() error {
			tools, err := broker.fetchToolsFromServer(gCtx, srv, userHeaders, gatewaySessionID, cachedSessions[srv.name])
			if err != nil {
				broker.logger.Error("failed to fetch user-specific tools", "server", srv.name, "error", err)
				return nil // graceful degradation
			}
			broker.logger.Debug("fetched user-specific tools", "server", srv.name, "toolCount", len(tools))
			mu.Lock()
			allTools = append(allTools, tools...)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	span.SetAttributes(attribute.Int("mcp.user_specific.tools_fetched", len(allTools)))
	result.Tools = append(result.Tools, allTools...)
}

func (broker *mcpBrokerImpl) fetchToolsFromServer(ctx context.Context, srv userSpecificServer, userHeaders map[string]string, gatewaySessionID, cachedUpstreamSessionID string) ([]mcp.Tool, error) {
	tools, err := broker.doFetchTools(ctx, srv, userHeaders, cachedUpstreamSessionID)
	if err != nil && cachedUpstreamSessionID != "" {
		// cached session may be stale — clear and retry with fresh init
		broker.logger.Debug("retrying user-specific fetch with fresh init", "server", srv.name, "error", err)
		if broker.sessionCache != nil {
			_ = broker.sessionCache.RemoveServerSession(ctx, gatewaySessionID, srv.name)
		}
		tools, err = broker.doFetchTools(ctx, srv, userHeaders, "")
	}
	if err != nil {
		return nil, err
	}

	// cache the upstream session for reuse by subsequent tools/list and tools/call
	if broker.sessionCache != nil {
		sessionID := tools.sessionID
		if sessionID != "" && sessionID != cachedUpstreamSessionID {
			ttl := gatewaySessionTTL(gatewaySessionID)
			if ttl > 0 {
				if _, storeErr := broker.sessionCache.AddSession(ctx, gatewaySessionID, srv.name, sessionID, ttl); storeErr != nil {
					broker.logger.Error("failed to cache user-specific session", "server", srv.name, "error", storeErr)
				}
			}
		}
	}

	return tools.tools, nil
}

type fetchResult struct {
	tools     []mcp.Tool
	sessionID string
}

func (broker *mcpBrokerImpl) doFetchTools(ctx context.Context, srv userSpecificServer, userHeaders map[string]string, upstreamSessionID string) (result *fetchResult, retErr error) {
	sessionReused := upstreamSessionID != ""
	ctx, span := brokerTracer().Start(ctx, "broker.user-specific-tools.fetch-server",
		trace.WithAttributes(
			attribute.String("mcp.server.name", srv.name),
			attribute.String("mcp.server.url", srv.url),
			attribute.Bool("mcp.user_specific.session_reused", sessionReused),
		),
	)
	defer func() {
		if retErr != nil {
			span.SetStatus(codes.Error, retErr.Error())
			span.RecordError(retErr)
		} else if result != nil {
			span.SetAttributes(attribute.Int("mcp.user_specific.tools_count", len(result.tools)))
		}
		span.End()
	}()

	fetchCtx, cancel := context.WithTimeout(ctx, broker.userSpecificFetchTimeout)
	defer cancel()

	options := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(userHeaders),
	}
	if sessionReused {
		broker.logger.Debug("using cached upstream session for user-specific server", "server", srv.name, "upstreamSession", upstreamSessionID)
		options = append(options, transport.WithSession(upstreamSessionID))
	}

	mcpClient, err := client.NewStreamableHttpClient(srv.url, options...)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	// don't call mcpClient.Close() — it sends HTTP DELETE which terminates
	// the upstream session. We don't explicitly close these sessions.

	if err := mcpClient.Start(fetchCtx); err != nil {
		return nil, fmt.Errorf("start client: %w", err)
	}

	if upstreamSessionID == "" {
		broker.logger.Debug("initializing user-specific server (no cached session)", "server", srv.name)
		_, err := mcpClient.Initialize(fetchCtx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo: mcp.Implementation{
					Name:    "mcp-broker",
					Version: "0.0.1",
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("initialize: %w", err)
		}
	}

	toolsResult, err := mcpClient.ListTools(fetchCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	validTools, invalids := upstream.ValidateTools(toolsResult.Tools)
	if len(invalids) > 0 {
		switch broker.invalidToolPolicy {
		case mcpv1alpha1.InvalidToolPolicyFilterOut:
			broker.logger.Error("invalid user-specific tools filtered", "server", srv.name, "count", len(invalids))
		case mcpv1alpha1.InvalidToolPolicyRejectServer:
			broker.logger.Error("user-specific server rejected due to invalid tools", "server", srv.name, "count", len(invalids))
			return nil, fmt.Errorf("server %s rejected: %d invalid tools", srv.id, len(invalids))
		}
	}

	for i := range validTools {
		validTools[i].Name = srv.prefix + validTools[i].Name
		validTools[i].Meta = mcp.NewMetaFromMap(map[string]any{
			"kuadrant/id": string(srv.id),
		})
	}

	return &fetchResult{tools: validTools, sessionID: mcpClient.GetSessionId()}, nil
}

// gatewaySessionTTL extracts the remaining TTL from a gateway session JWT
// without verifying the signature (the router already validated it).
func gatewaySessionTTL(gatewaySessionID string) time.Duration {
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if !internaljwt.DecodePayload(gatewaySessionID, &claims) || claims.Exp == 0 {
		return 0
	}
	ttl := time.Until(time.Unix(int64(claims.Exp), 0))
	if ttl <= 0 {
		return 0
	}
	return ttl
}

// sensitiveForwardHeaders are client headers that must never be forwarded to
// upstream MCP servers. cookie and proxy-authorization are scoped to the
// gateway origin/hop, not the upstream, so forwarding them would leak
// gateway-scoped credentials to every user-specific upstream queried.
var sensitiveForwardHeaders = map[string]struct{}{
	"cookie":              {},
	"proxy-authorization": {},
}

// filterUserHeaders returns user headers suitable for forwarding to upstream,
// stripping internal gateway headers and gateway-scoped credentials. the
// client's Authorization header is intentionally preserved: user-specific
// servers rely on it to return a per-user tool list.
func filterUserHeaders(h http.Header) map[string]string {
	headers := make(map[string]string, len(h))
	for key, vals := range h {
		lower := strings.ToLower(key)
		if lower == "mcp-session-id" {
			continue
		}
		if strings.HasPrefix(lower, "x-mcp-") {
			continue
		}
		if _, sensitive := sensitiveForwardHeaders[lower]; sensitive {
			continue
		}
		if len(vals) > 0 {
			headers[key] = vals[0]
		}
	}
	return headers
}
