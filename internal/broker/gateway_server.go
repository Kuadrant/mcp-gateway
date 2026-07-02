package broker

import (
	"context"
	"sync"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gatewayServer wraps an mcp.Server and tracks registered tools/prompts
// for conflict detection and listing, since the official SDK doesn't
// expose a public ListTools method.
type gatewayServer struct {
	server  *mcp.Server
	mu      sync.RWMutex
	tools   map[string]*upstream.GatewayTool
	prompts map[string]*upstream.GatewayPrompt

	// notifyMu guards the pending tools/list_changed delivery state below.
	// the SDK dispatches list_changed asynchronously (debounced ~10ms via
	// time.AfterFunc), so targeting state must outlive the trigger call:
	// entries are claimed for notifyWindow and expire lazily rather than
	// being cleared synchronously.
	notifyMu sync.Mutex
	// notifyTargets maps session IDs owed the next dispatch to the time
	// their claim expires.
	notifyTargets map[string]time.Time
	// broadcastUntil marks a pending broadcast (upstream-driven change);
	// while set, every session receives the dispatch even if targets are
	// pending, so coalesced dispatches never starve non-target sessions.
	broadcastUntil time.Time
}

// notifyWindow bounds how long a pending target or broadcast claim lives.
// it only needs to cover the SDK's dispatch debounce plus delivery; a
// generous window can at worst cause a duplicate notification (benign:
// the client re-lists).
const notifyWindow = 5 * time.Second

var _ upstream.ToolsAdderDeleter = &gatewayServer{}
var _ upstream.PromptsAdderDeleter = &gatewayServer{}

func newGatewayServer(server *mcp.Server) *gatewayServer {
	return &gatewayServer{
		server:        server,
		tools:         make(map[string]*upstream.GatewayTool),
		prompts:       make(map[string]*upstream.GatewayPrompt),
		notifyTargets: make(map[string]time.Time),
	}
}

func (g *gatewayServer) AddTools(tools ...upstream.GatewayTool) {
	if len(tools) == 0 {
		return
	}
	// the SDK fires its own list_changed for these; it must reach everyone
	g.markBroadcast()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range tools {
		g.tools[t.Tool.Name] = &t
		g.server.AddTool(&t.Tool, t.Handler)
	}
}

func (g *gatewayServer) DeleteTools(names ...string) {
	if len(names) == 0 {
		return
	}
	g.markBroadcast()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, name := range names {
		delete(g.tools, name)
	}
	g.server.RemoveTools(names...)
}

func (g *gatewayServer) ListTools() map[string]*upstream.GatewayTool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	// return a shallow copy to avoid callers holding the lock
	result := make(map[string]*upstream.GatewayTool, len(g.tools))
	for k, v := range g.tools {
		result[k] = v
	}
	return result
}

// AddTool adds a single tool (convenience for broker-internal tools)
func (g *gatewayServer) AddTool(tool mcp.Tool, handler mcp.ToolHandler) {
	g.AddTools(upstream.GatewayTool{Tool: tool, Handler: handler})
}

func (g *gatewayServer) AddPrompts(prompts ...upstream.GatewayPrompt) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range prompts {
		g.prompts[p.Prompt.Name] = &p
		g.server.AddPrompt(&p.Prompt, p.Handler)
	}
}

func (g *gatewayServer) DeletePrompts(names ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, name := range names {
		delete(g.prompts, name)
	}
	g.server.RemovePrompts(names...)
}

func (g *gatewayServer) ListPrompts() map[string]*upstream.GatewayPrompt {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]*upstream.GatewayPrompt, len(g.prompts))
	for k, v := range g.prompts {
		result[k] = v
	}
	return result
}

// MCPServer returns the underlying mcp.Server
func (g *gatewayServer) MCPServer() *mcp.Server {
	return g.server
}

// markBroadcast records that the next tools/list_changed dispatch must
// reach all sessions.
func (g *gatewayServer) markBroadcast() {
	g.notifyMu.Lock()
	g.broadcastUntil = time.Now().Add(notifyWindow)
	g.notifyMu.Unlock()
}

// markNotifyTarget records that sessionID is owed the next dispatch.
func (g *gatewayServer) markNotifyTarget(sessionID string) {
	now := time.Now()
	g.notifyMu.Lock()
	defer g.notifyMu.Unlock()
	for id, exp := range g.notifyTargets {
		if now.After(exp) {
			delete(g.notifyTargets, id)
		}
	}
	g.notifyTargets[sessionID] = now.Add(notifyWindow)
}

// shouldDeliver reports whether a tools/list_changed send to sessionID
// should proceed. fail-open: delivery is suppressed only when live targets
// exist, no broadcast is pending, and the session is not among the targets.
// a spurious delivery merely causes a re-list; a missed one loses state.
func (g *gatewayServer) shouldDeliver(sessionID string) bool {
	now := time.Now()
	g.notifyMu.Lock()
	defer g.notifyMu.Unlock()
	if now.Before(g.broadcastUntil) {
		return true
	}
	live, targeted := false, false
	for id, exp := range g.notifyTargets {
		if now.After(exp) {
			delete(g.notifyTargets, id)
			continue
		}
		live = true
		if id == sessionID {
			targeted = true
		}
	}
	return !live || targeted
}

const toolsListChangedMethod = "notifications/tools/list_changed"

// scopeChangeSentinelName is the no-op tool cycled to force a
// tools/list_changed dispatch. it exists in the SDK's tool set for a
// moment between add and remove, so FilterTools drops it from responses.
const scopeChangeSentinelName = "__gateway_scope_change"

// notifyTargetMiddleware returns a Middleware that filters
// tools/list_changed notifications to pending target sessions.
// when no targets are pending (or a broadcast is), all sessions receive
// the notification.
func (g *gatewayServer) notifyTargetMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != toolsListChangedMethod {
				return next(ctx, method, req)
			}
			if g.shouldDeliver(req.GetSession().ID()) {
				return next(ctx, method, req)
			}
			return nil, nil
		}
	}
}

// TriggerToolsListChanged forces a tools/list_changed notification via a
// no-op add/remove cycle (the SDK debounces the pair into one dispatch).
// a non-empty targetSessionID limits delivery to that session; empty
// broadcasts to all sessions. targets expire after notifyWindow rather
// than being cleared synchronously, because the SDK dispatch is async.
func (g *gatewayServer) TriggerToolsListChanged(targetSessionID string) {
	if targetSessionID != "" {
		g.markNotifyTarget(targetSessionID)
	} else {
		g.markBroadcast()
	}
	sentinel := mcp.Tool{Name: scopeChangeSentinelName, InputSchema: map[string]any{"type": "object"}}
	g.server.AddTool(&sentinel, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, nil //nolint:nilnil // sentinel tool, never called
	})
	g.server.RemoveTools(scopeChangeSentinelName)
}
