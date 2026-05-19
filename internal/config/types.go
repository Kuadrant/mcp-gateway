// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
	"sync"
)

// UpstreamServer defines common properties for any upstream provider.
type UpstreamServer interface {
	ServerName() string
	ServerURL() string
	Protocol() string
}

// A2AServer represents a future Agent-to-Agent upstream configuration
type A2AServer struct {
	Name            string            `json:"name" yaml:"name"`
	URL             string            `json:"url" yaml:"url"`
	AgentCardURL    string            `json:"agentCardUrl,omitempty" yaml:"agentCardUrl,omitempty"`
	TaskEndpoint    string            `json:"taskEndpoint,omitempty" yaml:"taskEndpoint,omitempty"`
	ProtocolBinding string            `json:"protocolBinding,omitempty" yaml:"protocolBinding,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

func (a2aServer *A2AServer) ServerName() string { return a2aServer.Name }
func (a2aServer *A2AServer) ServerURL() string  { return a2aServer.URL }
func (a2aServer *A2AServer) Protocol() string {
	if a2aServer.ProtocolBinding != "" {
		return a2aServer.ProtocolBinding
	}
	return "a2a"
}

// UpstreamMCPID is used as type for identifying individual upstreams
type UpstreamMCPID string

// MCPServersConfig holds server configuration
type MCPServersConfig struct {
	lock sync.RWMutex

	Servers        []*MCPServer
	A2AServers     []*A2AServer
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
func (config *MCPServersConfig) SetServers(servers []*MCPServer, a2aServers []*A2AServer, virtualServers []*VirtualServer) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.Servers = servers
	config.A2AServers = a2aServers
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

// ListA2AServers returns a consistent snapshot of the current A2A server list.
func (config *MCPServersConfig) ListA2AServers() []*A2AServer {
	config.lock.RLock()
	defer config.lock.RUnlock()
	out := make([]*A2AServer, len(config.A2AServers))
	copy(out, config.A2AServers)
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

func (mcpServer *MCPServer) ServerName() string { return mcpServer.Name }
func (mcpServer *MCPServer) ServerURL() string  { return mcpServer.URL }
func (mcpServer *MCPServer) Protocol() string   { return "mcp" }

// TokenURLElicitationConfig configures per-user token collection via URL elicitation.
type TokenURLElicitationConfig struct {
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
}

// ID returns a unique id for the a registered server
func (mcpServer *MCPServer) ID() UpstreamMCPID {
	return UpstreamMCPID(fmt.Sprintf("%s:%s:%s", mcpServer.Name, mcpServer.Prefix, mcpServer.Hostname))
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
	Servers        []MCPServer           `json:"servers" yaml:"servers"`
	A2AServers     []A2AServer           `json:"a2aServers,omitempty" yaml:"a2aServers,omitempty"`
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
