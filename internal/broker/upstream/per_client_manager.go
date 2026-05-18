package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// PerClientNotificationManager manages per-client, per-server backend SSE connections.
// When a gateway client opens GET /mcp, OpenConnections creates one backend SSE
// connection for each registered server that advertises per-client notification
// capabilities (logging or resource subscriptions). Servers that do not advertise
// these capabilities are skipped. Notifications received on per-client connections
// are forwarded only to the specific client, never broadcast.
//
// The existing shared MCPManager broadcast path is untouched.
type PerClientNotificationManager struct {
	// connections maps sessionKey(gatewaySessionID, serverName) → context.CancelFunc
	connections   sync.Map
	gatewayServer *server.MCPServer
	serverConfigs func() map[config.UpstreamMCPID]ActiveMCPServer
	logger        *slog.Logger
}

// NewPerClientNotificationManager creates a manager. gatewayServer is the mcp-go
// server used to deliver notifications to clients. serverConfigs is a closure that
// returns the current set of upstream managers; it is evaluated at OpenConnections
// time so newly registered backends are picked up automatically.
func NewPerClientNotificationManager(
	gatewayServer *server.MCPServer,
	serverConfigs func() map[config.UpstreamMCPID]ActiveMCPServer,
	logger *slog.Logger,
) *PerClientNotificationManager {
	return &PerClientNotificationManager{
		gatewayServer: gatewayServer,
		serverConfigs: serverConfigs,
		logger:        logger,
	}
}

// OpenConnections opens per-client backend SSE connections for all registered
// servers that advertise per-client notification capabilities. It is called
// eagerly from the OnRegisterSession hook when a client opens GET /mcp.
// Each connection runs in its own goroutine and is tied to ctx; when ctx is
// cancelled (client disconnects) all connections for this session stop.
func (m *PerClientNotificationManager) OpenConnections(ctx context.Context, gatewaySessionID string, headers map[string]string) {
	servers := m.serverConfigs()
	for _, man := range servers {
		if !man.SupportsLogging() && !man.SupportsResourceSubscribe() {
			continue
		}
		cfg := man.Config()
		key := sessionKey(gatewaySessionID, cfg.Name)

		connCtx, cancel := context.WithCancel(ctx)
		if _, loaded := m.connections.LoadOrStore(key, cancel); loaded {
			// another goroutine already created this connection
			cancel()
			continue
		}

		pc := newPerClientMCPServer(cfg, gatewaySessionID, headers, m.logger.With(
			"sub-component", "per-client-mcp",
			"server", cfg.Name,
			"gatewaySessionID", gatewaySessionID,
		))

		onNotification := m.notificationHandler(gatewaySessionID, cfg.Name)

		go pc.run(connCtx, onNotification)

		m.logger.Debug("opened per-client backend connection",
			"server", cfg.Name,
			"gatewaySessionID", gatewaySessionID,
		)
	}
}

// TeardownSession cancels all per-client backend connections for the given
// gateway session. Called from the OnUnregisterSession hook.
func (m *PerClientNotificationManager) TeardownSession(gatewaySessionID string) {
	prefix := gatewaySessionID + ":"
	m.connections.Range(func(k, v any) bool {
		if strings.HasPrefix(k.(string), prefix) {
			if cancel, ok := v.(context.CancelFunc); ok {
				cancel()
			}
			m.connections.Delete(k)
		}
		return true
	})
}

// notificationHandler returns the callback that routes backend notifications to
// the specific gateway client identified by gatewaySessionID.
func (m *PerClientNotificationManager) notificationHandler(gatewaySessionID, serverName string) func(mcp.JSONRPCNotification) {
	return func(n mcp.JSONRPCNotification) {
		switch n.Method {
		case "notifications/tools/list_changed":
			// suppress: the shared MCPManager already broadcasts this to all clients
			return
		}

		params := n.Params.AdditionalFields
		if err := m.gatewayServer.SendNotificationToSpecificClient(gatewaySessionID, n.Method, params); err != nil {
			m.logger.Debug("failed to forward per-client notification",
				"gatewaySessionID", gatewaySessionID,
				"server", serverName,
				"method", n.Method,
				"error", err,
			)
		}
	}
}

func sessionKey(gatewaySessionID, serverName string) string {
	return fmt.Sprintf("%s:%s", gatewaySessionID, serverName)
}
