package routing

import (
	"context"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/clients"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionCache defines how the router interacts with a store to store and retrieve sessions.
type SessionCache interface {
	GetSession(ctx context.Context, key string) (map[string]string, error)
	AddSession(ctx context.Context, key, mcpID, mcpSession string, ttl time.Duration) (bool, error)
	DeleteSessions(ctx context.Context, key ...string) error
	RemoveServerSession(ctx context.Context, key, mcpServerID string) error
	KeyExists(ctx context.Context, key string) (bool, error)
	SetClientElicitation(ctx context.Context, gatewaySessionID string, ttl time.Duration) error
	GetClientElicitation(ctx context.Context, gatewaySessionID string) (bool, error)
	SetUserToken(ctx context.Context, sessionID, serverName, token string, ttl time.Duration) error
	GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error)
	DeleteUserToken(ctx context.Context, sessionID, serverName string) error
}

// InitForClient defines a function for initializing an MCP server for a client.
type InitForClient func(ctx context.Context, gatewayHost string, conf *config.MCPServer, passThroughHeaders map[string]string, clientElicitation bool, hairpinClientPool *clients.HairpinClientPool) (*mcp.ClientSession, error)
