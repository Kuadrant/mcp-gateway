// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
	"sync"
)

// UpstreamMCPID is used as type for identifying individual upstreams
type UpstreamMCPID string

// UpstreamID is a generic identifier for any upstream server (MCP, A2A, etc.)
type UpstreamID string

// Protocol represents the communication protocol of an upstream server
type Protocol string

const (
	// ProtocolMCP represents the Model Context Protocol
	ProtocolMCP Protocol = "mcp"
	// ProtocolA2A represents the Agent-to-Agent protocol
	ProtocolA2A Protocol = "a2a"
)

// UpstreamServer represents the interface that any upstream server config must satisfy,
// regardless of the protocol (MCP, A2A, etc.)
type UpstreamServer interface {
	// GetName returns the unique name of the upstream server
	GetName() string
	// GetProtocol returns the protocol type (e.g., "mcp", "a2a")
	GetProtocol() Protocol
	// GetURL returns the backend endpoint URL
	GetURL() string
	// GetHostname returns the target hostname
	GetHostname() string
	// GetPrefix returns the prefix used for resource/tool naming isolation
	GetPrefix() string
	// IsEnabled returns true if the server is active
	IsEnabled() bool
	// GetID returns a unique UpstreamID
	GetID() UpstreamID
}

// UpstreamRegistry represents a protocol-neutral registry for looking up upstream servers
type UpstreamRegistry interface {
	// ListUpstreams returns all registered upstream servers across all protocols
	ListUpstreams() []UpstreamServer
	// GetUpstreamByName retrieves an upstream server by its unique name
	GetUpstreamByName(name string) (UpstreamServer, error)
	// GetExternalHostname returns the public hostname of the gateway
	GetExternalHostname() string
}

// MCPServersConfig holds server configuration
type MCPServersConfig struct {
	lock sync.RWMutex

	Servers        []*MCPServer
	VirtualServers []*VirtualServer
	observers      []Observer
	//MCPGatewayExternalHostname is the accessible host of the gateway listener
	MCPGatewayExternalHostname string
	MCPGatewayInternalHostname string
}

// RegisterObserver registers an observer to be notified of changes to the config
func (config *MCPServersConfig) RegisterObserver(obs Observer) {
	config.lock.Lock()
	defer config.lock.Unlock()

	config.observers = append(config.observers, obs)
}

// SetServers atomically replaces the server and virtual-server lists.
func (config *MCPServersConfig) SetServers(servers []*MCPServer, virtualServers []*VirtualServer) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.Servers = servers
	config.VirtualServers = virtualServers
}

// ListServers returns a consistent snapshot of the current server list.
func (config *MCPServersConfig) ListServers() []*MCPServer {
	config.lock.RLock()
	defer config.lock.RUnlock()
	out := make([]*MCPServer, len(config.Servers))
	copy(out, config.Servers)
	return out
}

// ListVirtualServers returns a consistent snapshot of the current virtual-server list.
func (config *MCPServersConfig) ListVirtualServers() []*VirtualServer {
	config.lock.RLock()
	defer config.lock.RUnlock()
	out := make([]*VirtualServer, len(config.VirtualServers))
	copy(out, config.VirtualServers)
	return out
}

// Notify notifies registered observers of config changes
func (config *MCPServersConfig) Notify(ctx context.Context) {
	config.lock.RLock()
	defer config.lock.RUnlock()

	for _, observer := range config.observers {
		go observer.OnConfigChange(ctx, config)
	}
}

// GetExternalHostname returns the public hostname of the gateway
func (config *MCPServersConfig) GetExternalHostname() string {
	return config.MCPGatewayExternalHostname
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

// ListUpstreams returns all registered upstream servers across all protocols
func (config *MCPServersConfig) ListUpstreams() []UpstreamServer {
	config.lock.RLock()
	defer config.lock.RUnlock()
	out := make([]UpstreamServer, len(config.Servers))
	for i, server := range config.Servers {
		out[i] = server
	}
	return out
}

// GetUpstreamByName retrieves an upstream server by its unique name
func (config *MCPServersConfig) GetUpstreamByName(name string) (UpstreamServer, error) {
	server, err := config.GetServerConfigByName(name)
	if err != nil {
		return nil, err
	}
	return server, nil
}

// MCPServer represents a server
type MCPServer struct {
	Name                string                     `json:"name"                              yaml:"name"`
	URL                 string                     `json:"url"                               yaml:"url"`
	Hostname            string                     `json:"hostname,omitempty"                yaml:"hostname,omitempty"`
	Prefix              string                     `json:"prefix,omitempty"                  yaml:"prefix,omitempty"`
	Auth                *AuthConfig                `json:"auth,omitempty"                    yaml:"auth,omitempty"`
	Credential          string                     `json:"credential,omitempty"              yaml:"credential,omitempty"`
	Enabled             bool                       `json:"enabled"                           yaml:"enabled"`
	TokenURLElicitation *TokenURLElicitationConfig `json:"tokenURLElicitation,omitempty" yaml:"tokenURLElicitation,omitempty"`
}

// TokenURLElicitationConfig configures per-user token collection via URL elicitation.
type TokenURLElicitationConfig struct {
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
}

// ID returns a unique id for the a registered server
func (mcpServer *MCPServer) ID() UpstreamMCPID {
	return UpstreamMCPID(fmt.Sprintf("%s:%s:%s", mcpServer.Name, mcpServer.Prefix, mcpServer.Hostname))
}

// GetName returns the unique name of the upstream server
func (mcpServer *MCPServer) GetName() string {
	return mcpServer.Name
}

// GetProtocol returns the protocol type (always ProtocolMCP for MCPServer)
func (mcpServer *MCPServer) GetProtocol() Protocol {
	return ProtocolMCP
}

// GetURL returns the backend endpoint URL
func (mcpServer *MCPServer) GetURL() string {
	return mcpServer.URL
}

// GetHostname returns the target hostname
func (mcpServer *MCPServer) GetHostname() string {
	return mcpServer.Hostname
}

// GetPrefix returns the prefix used for resource/tool naming isolation
func (mcpServer *MCPServer) GetPrefix() string {
	return mcpServer.Prefix
}

// IsEnabled returns true if the server is active
func (mcpServer *MCPServer) IsEnabled() bool {
	return mcpServer.Enabled
}

// GetID returns a unique UpstreamID
func (mcpServer *MCPServer) GetID() UpstreamID {
	return UpstreamID(mcpServer.ID())
}

// A2AServer represents the configuration for an Agent-to-Agent (A2A) upstream server.
// It implements the UpstreamServer interface.
type A2AServer struct {
	Name            string            `json:"name"                      yaml:"name"`
	URL             string            `json:"url"                       yaml:"url"`
	Hostname        string            `json:"hostname,omitempty"        yaml:"hostname,omitempty"`
	Prefix          string            `json:"prefix,omitempty"          yaml:"prefix,omitempty"`
	Auth            *AuthConfig       `json:"auth,omitempty"            yaml:"auth,omitempty"`
	Enabled         bool              `json:"enabled"                   yaml:"enabled"`
	AgentID         string            `json:"agentId,omitempty"         yaml:"agentId,omitempty"`
	AgentCardURL    string            `json:"agentCardUrl,omitempty"    yaml:"agentCardUrl,omitempty"`
	TaskEndpoint    string            `json:"taskEndpoint,omitempty"    yaml:"taskEndpoint,omitempty"`
	ProtocolBinding string            `json:"protocolBinding,omitempty" yaml:"protocolBinding,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"        yaml:"metadata,omitempty"`
}

// GetName returns the unique name of the upstream server
func (a2aServer *A2AServer) GetName() string {
	return a2aServer.Name
}

// GetProtocol returns the protocol type (always ProtocolA2A for A2AServer)
func (a2aServer *A2AServer) GetProtocol() Protocol {
	return ProtocolA2A
}

// GetURL returns the backend endpoint URL
func (a2aServer *A2AServer) GetURL() string {
	return a2aServer.URL
}

// GetHostname returns the target hostname
func (a2aServer *A2AServer) GetHostname() string {
	return a2aServer.Hostname
}

// GetPrefix returns the prefix used for resource/tool naming isolation
func (a2aServer *A2AServer) GetPrefix() string {
	return a2aServer.Prefix
}

// IsEnabled returns true if the server is active
func (a2aServer *A2AServer) IsEnabled() bool {
	return a2aServer.Enabled
}

// GetID returns a unique UpstreamID
func (a2aServer *A2AServer) GetID() UpstreamID {
	return UpstreamID(fmt.Sprintf("%s:%s:%s", a2aServer.Name, a2aServer.Prefix, a2aServer.Hostname))
}

// ConfigChanged checks if a server's config has changed in a way that will affect the gateway.
// This means having a different name, prefix, hostname, or credential variable.
func (mcpServer *MCPServer) ConfigChanged(existingConfig MCPServer) bool {
	return existingConfig.Name != mcpServer.Name ||
		existingConfig.Prefix != mcpServer.Prefix ||
		existingConfig.Hostname != mcpServer.Hostname ||
		existingConfig.Credential != mcpServer.Credential ||
		tokenURLElicitationChanged(mcpServer.TokenURLElicitation, existingConfig.TokenURLElicitation)
}

func tokenURLElicitationChanged(a, b *TokenURLElicitationConfig) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	if a == nil {
		return false
	}
	return a.URL != b.URL
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
	Name    string
	Tools   []string
	Prompts []string
}

// Observer provides an interface to implement in order to register as an Observer of config changes
type Observer interface {
	OnConfigChange(ctx context.Context, config *MCPServersConfig)
}

// BrokerConfig holds broker configuration
type BrokerConfig struct {
	Servers        []MCPServer           `json:"servers"                  yaml:"servers"`
	A2AServers     []A2AServer           `json:"a2aServers,omitempty"     yaml:"a2aServers,omitempty"`
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
	Name    string   `json:"name"    yaml:"name"`
	Tools   []string `json:"tools"   yaml:"tools"`
	Prompts []string `json:"prompts,omitempty" yaml:"prompts,omitempty"`
}
