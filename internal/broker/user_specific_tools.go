package broker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// userSessionKey builds the pool key for a per-user upstream session.
func userSessionKey(gatewaySessionID, serverName string) string {
	return gatewaySessionID + "/" + serverName
}

// cachedUserSession holds a reusable upstream client session for a specific
// user + server combination. the session is kept alive across tools/list
// calls and closed on ListTools error, gateway session end or broker
// shutdown. headers are resolved per request from the holder so a client
// that refreshes its token mid-session always reaches the upstream with
// the current one, as mark3labs did by connecting fresh per fetch.
type cachedUserSession struct {
	session *mcp.ClientSession
	headers atomic.Pointer[map[string]string]
}

// FetchUserSpecificTools fetches tools from userSpecificList servers using the
// caller's session headers and merges them into the result before FilterTools runs.
func (broker *mcpBrokerImpl) FetchUserSpecificTools(ctx context.Context, headers http.Header, result *mcp.ListToolsResult) {
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

	gatewaySessionID := headers.Get(gatewaySessionHeader)
	if gatewaySessionID == "" {
		broker.logger.Error("no gateway session ID for user-specific tool fetch")
		span.SetStatus(codes.Error, "missing gateway session ID")
		return
	}

	userHeaders := filterUserHeaders(headers)

	var mu sync.Mutex
	var allTools []mcp.Tool

	g, gCtx := errgroup.WithContext(ctx)
	for _, srv := range servers {
		g.Go(func() error {
			tools, err := broker.fetchToolsFromServer(gCtx, srv, userHeaders, gatewaySessionID)
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

	// convert value tools to pointers for ListToolsResult
	for i := range allTools {
		result.Tools = append(result.Tools, &allTools[i])
	}
}

func (broker *mcpBrokerImpl) fetchToolsFromServer(ctx context.Context, srv userSpecificServer, userHeaders map[string]string, gatewaySessionID string) ([]mcp.Tool, error) {
	tools, err := broker.doFetchTools(ctx, srv, userHeaders, gatewaySessionID)
	if err != nil {
		return nil, err
	}

	// cache the upstream session ID for reuse by tools/call routing
	if broker.sessionCache != nil {
		sessionID := tools.sessionID
		if sessionID != "" {
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

func (broker *mcpBrokerImpl) doFetchTools(ctx context.Context, srv userSpecificServer, userHeaders map[string]string, gatewaySessionID string) (result *fetchResult, retErr error) {
	ctx, span := brokerTracer().Start(ctx, "broker.user-specific-tools.fetch-server",
		trace.WithAttributes(
			attribute.String("mcp.server.name", srv.name),
			attribute.String("mcp.server.url", srv.url),
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

	session, err := broker.getOrCreateUserSession(fetchCtx, srv, userHeaders, gatewaySessionID)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	toolsResult, err := session.ListTools(fetchCtx, nil)
	if err != nil {
		// stale session; evict and retry once
		broker.evictUserSession(gatewaySessionID, srv.name)
		session, err = broker.getOrCreateUserSession(fetchCtx, srv, userHeaders, gatewaySessionID)
		if err != nil {
			return nil, fmt.Errorf("reconnect: %w", err)
		}
		toolsResult, err = session.ListTools(fetchCtx, nil)
		if err != nil {
			broker.evictUserSession(gatewaySessionID, srv.name)
			return nil, fmt.Errorf("list tools: %w", err)
		}
	}

	// dereference pointer tools from result
	valueTools := make([]mcp.Tool, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		if t != nil {
			valueTools = append(valueTools, *t)
		}
	}

	validTools, invalids := upstream.ValidateTools(valueTools)
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
		validTools[i].Meta = mcp.Meta{
			"kuadrant/id": string(srv.id),
		}
	}

	return &fetchResult{tools: validTools, sessionID: session.ID()}, nil
}

// getOrCreateUserSession returns a cached upstream session or creates a new
// one. the session is kept alive in the pool for reuse by subsequent calls;
// the caller's current headers replace the pooled ones on every call so
// reuse never pins a stale Authorization.
func (broker *mcpBrokerImpl) getOrCreateUserSession(ctx context.Context, srv userSpecificServer, userHeaders map[string]string, gatewaySessionID string) (*mcp.ClientSession, error) {
	key := userSessionKey(gatewaySessionID, srv.name)

	if val, ok := broker.userSessionPool.Load(key); ok {
		cached := val.(*cachedUserSession)
		cached.headers.Store(&userHeaders)
		return cached.session, nil
	}

	cached := &cachedUserSession{}
	cached.headers.Store(&userHeaders)

	httpClient := &http.Client{
		Transport: &transport.DynamicHeaderRoundTripper{
			Base:    http.DefaultTransport,
			Headers: func() map[string]string { return *cached.headers.Load() },
		},
	}

	t := &mcp.StreamableClientTransport{
		Endpoint:   srv.url,
		HTTPClient: httpClient,
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.0.1",
	}, nil)

	session, err := mcpClient.Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}

	cached.session = session
	// if another goroutine raced us, close ours and use theirs with our
	// headers, which are at least as fresh
	if existing, loaded := broker.userSessionPool.LoadOrStore(key, cached); loaded {
		_ = session.Close()
		winner := existing.(*cachedUserSession)
		winner.headers.Store(&userHeaders)
		return winner.session, nil
	}

	return session, nil
}

// evictUserSession removes and closes a cached upstream session.
func (broker *mcpBrokerImpl) evictUserSession(gatewaySessionID, serverName string) {
	broker.closePoolEntry(userSessionKey(gatewaySessionID, serverName))
}

// evictUserSessions removes and closes every pooled upstream session
// belonging to the given gateway session. called when the session ends.
func (broker *mcpBrokerImpl) evictUserSessions(gatewaySessionID string) {
	prefix := gatewaySessionID + "/"
	broker.userSessionPool.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			broker.closePoolEntry(k)
		}
		return true
	})
}

// drainUserSessionPool closes every pooled upstream session. called on
// broker shutdown.
func (broker *mcpBrokerImpl) drainUserSessionPool() {
	broker.userSessionPool.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok {
			broker.closePoolEntry(k)
		}
		return true
	})
}

func (broker *mcpBrokerImpl) closePoolEntry(key string) {
	if val, loaded := broker.userSessionPool.LoadAndDelete(key); loaded {
		cached := val.(*cachedUserSession)
		if err := cached.session.Close(); err != nil {
			broker.logger.Debug("failed to close pooled user session", "error", err)
		}
	}
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
