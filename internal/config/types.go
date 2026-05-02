// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"
)

// UpstreamMCPID is used as type for identifying individual upstreams
type UpstreamMCPID string

// MCPServersConfig holds server configuration
type MCPServersConfig struct {
	lock sync.RWMutex

	Servers        []*MCPServer
	VirtualServers []*VirtualServer
	observers      []Observer
	//MCPGatewayExternalHostname is the accessible host of the gateway listener
	MCPGatewayExternalHostname string
	MCPGatewayInternalHostname string
	RouterAPIKey               string
}

// RegisterObserver registers an observer to be notified of changes to the config
func (config *MCPServersConfig) RegisterObserver(obs Observer) {
	config.lock.Lock()
	defer config.lock.Unlock()

	config.observers = append(config.observers, obs)
}

// Notify notifies registered observers of config changes
func (config *MCPServersConfig) Notify(ctx context.Context) {
	config.lock.RLock()
	defer config.lock.RUnlock()

	for _, observer := range config.observers {
		go observer.OnConfigChange(ctx, config)
	}
}

// GetServerConfigByName get the routing config by server name
func (config *MCPServersConfig) GetServerConfigByName(serverName string) (*MCPServer, error) {
	config.lock.RLock()
	defer config.lock.RUnlock()

	for _, server := range config.Servers {
		if server.Name == serverName {
			return server, nil
		}
	}
	return nil, fmt.Errorf("unknown server")
}

// MCPServer represents a server
type MCPServer struct {
	Name       string         `json:"name"                 yaml:"name"`
	URL        string         `json:"url"                  yaml:"url"`
	Hostname   string         `json:"hostname,omitempty"   yaml:"hostname,omitempty"`
	ToolPrefix string         `json:"toolPrefix,omitempty" yaml:"toolPrefix,omitempty"`
	Auth       *AuthConfig    `json:"auth,omitempty"       yaml:"auth,omitempty"`
	Credential string         `json:"credential,omitempty" yaml:"credential,omitempty"`
	Enabled    bool           `json:"enabled"              yaml:"enabled"`
	Timeouts   *ServerTimeouts `json:"timeouts,omitempty"   yaml:"timeouts,omitempty"`
}

// ServerTimeouts holds parsed, validated timeout configuration for an MCPServer.
// All durations are positive; zero means "no override at this level".
type ServerTimeouts struct {
	// ToolCall is the default timeout applied to every tools/call routed to this server.
	ToolCall time.Duration `json:"toolCall,omitempty" yaml:"toolCall,omitempty"`
	// PerTool maps an upstream tool name (the unprefixed name reported by the backend)
	// to the timeout that should be applied when that specific tool is invoked.
	PerTool map[string]time.Duration `json:"perTool,omitempty" yaml:"perTool,omitempty"`
}

// ResolveToolCallTimeout returns the effective timeout for a given upstream tool name.
// Per-tool overrides take precedence over the server-wide default. The boolean result
// reports whether any explicit timeout was configured: callers should treat
// (0, false) as "no gateway-enforced timeout".
func (t *ServerTimeouts) ResolveToolCallTimeout(upstreamToolName string) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	if d, ok := t.PerTool[upstreamToolName]; ok && d > 0 {
		return d, true
	}
	if t.ToolCall > 0 {
		return t.ToolCall, true
	}
	return 0, false
}

// ID returns a unique id for the a registered server
func (mcpServer *MCPServer) ID() UpstreamMCPID {
	return UpstreamMCPID(fmt.Sprintf("%s:%s:%s", mcpServer.Name, mcpServer.ToolPrefix, mcpServer.Hostname))
}

// ConfigChanged checks if a server's config has changed in a way that will affect the gateway.
// This means having a different name, prefix, hostname, credential variable, or timeouts.
func (mcpServer *MCPServer) ConfigChanged(existingConfig MCPServer) bool {
	return existingConfig.Name != mcpServer.Name ||
		existingConfig.ToolPrefix != mcpServer.ToolPrefix ||
		existingConfig.Hostname != mcpServer.Hostname ||
		existingConfig.Credential != mcpServer.Credential ||
		!timeoutsEqual(existingConfig.Timeouts, mcpServer.Timeouts)
}

func timeoutsEqual(a, b *ServerTimeouts) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.ToolCall != b.ToolCall {
		return false
	}
	if len(a.PerTool) != len(b.PerTool) {
		return false
	}
	for name, d := range a.PerTool {
		if other, ok := b.PerTool[name]; !ok || other != d {
			return false
		}
	}
	return true
}

// Path returns the path part of the mcp url
func (mcpServer *MCPServer) Path() (string, error) {
	parsedURL, err := url.Parse(mcpServer.URL)
	if err != nil {
		return "", err
	}
	return parsedURL.Path, nil
}

// VirtualServer represents a virtual server configuration
type VirtualServer struct {
	Name  string
	Tools []string
}

// Observer provides an interface to implement in order to register as an Observer of config changes
type Observer interface {
	OnConfigChange(ctx context.Context, config *MCPServersConfig)
}

// BrokerConfig holds broker configuration
type BrokerConfig struct {
	Servers        []MCPServer           `json:"servers" yaml:"servers"`
	VirtualServers []VirtualServerConfig `json:"virtualServers,omitempty" yaml:"virtualServers,omitempty"`
}

// AuthConfig holds auth configuration
type AuthConfig struct {
	Type     string `json:"type"               yaml:"type"`
	Token    string `json:"token,omitempty"    yaml:"token,omitempty"`
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
}

// VirtualServerConfig represents virtual server config
type VirtualServerConfig struct {
	Name  string   `json:"name"  yaml:"name"`
	Tools []string `json:"tools" yaml:"tools"`
}
