// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"
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
	GatewayCACertPEM           string
	GlobalGuardrails           *GuardrailsConfig
	MaxBodyBytes               int64
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

// SetGatewayCACertPEM sets the gateway-level CA certificate bundle PEM.
func (config *MCPServersConfig) SetGatewayCACertPEM(pem string) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.GatewayCACertPEM = pem
}

// GetGatewayCACertPEM returns the gateway-level CA certificate bundle PEM.
func (config *MCPServersConfig) GetGatewayCACertPEM() string {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return config.GatewayCACertPEM
}

// SetGlobalGuardrails stores the resolved gateway-level guardrails config.
// A nil value clears it (guardrails disabled).
func (config *MCPServersConfig) SetGlobalGuardrails(cfg *GuardrailsConfig) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.GlobalGuardrails = cfg
}

// GetGlobalGuardrails returns the resolved gateway-level guardrails config,
// or nil when guardrails is not configured.
func (config *MCPServersConfig) GetGlobalGuardrails() *GuardrailsConfig {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return config.GlobalGuardrails
}

// DefaultMaxBodyBytes is the MCPGatewayExtension maxBodyBytes default (1 MiB).
const DefaultMaxBodyBytes int64 = 1 << 20

// SetMaxBodyBytes sets the router body-buffer cap from the
// MCPGatewayExtension spec. Non-positive values are treated as the default
// by GetMaxBodyBytes.
func (config *MCPServersConfig) SetMaxBodyBytes(n int64) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.MaxBodyBytes = n
}

// GetMaxBodyBytes returns the router body-buffer cap, defaulting to
// DefaultMaxBodyBytes when unset.
func (config *MCPServersConfig) GetMaxBodyBytes() int64 {
	config.lock.RLock()
	defer config.lock.RUnlock()
	if config.MaxBodyBytes > 0 {
		return config.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
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
	Name                string                     `json:"name"                          yaml:"name"`
	URL                 string                     `json:"url"                           yaml:"url"`
	Hostname            string                     `json:"hostname,omitempty"            yaml:"hostname,omitempty"`
	Prefix              string                     `json:"prefix,omitempty"              yaml:"prefix,omitempty"`
	Auth                *AuthConfig                `json:"auth,omitempty"                yaml:"auth,omitempty"`
	Credential          string                     `json:"credential,omitempty"          yaml:"credential,omitempty"`
	CACert              string                     `json:"caCert,omitempty"              yaml:"caCert,omitempty"`
	State               string                     `json:"state"                         yaml:"state"`
	TokenURLElicitation *TokenURLElicitationConfig `json:"tokenURLElicitation,omitempty" yaml:"tokenURLElicitation,omitempty"`
	UserSpecificList    bool                       `json:"userSpecificList,omitempty"    yaml:"userSpecificList,omitempty"`
	Category            []string                   `json:"category,omitempty"            yaml:"category,omitempty"`
	Hint                string                     `json:"hint,omitempty"                yaml:"hint,omitempty"`
	Tags                []string                   `json:"tags,omitempty"                yaml:"tags,omitempty"`
	GuardrailsConfigIDs []string                   `json:"guardrailsConfigIDs,omitempty" yaml:"guardrailsConfigIDs,omitempty"`
}

// GuardrailsConfig holds the resolved guardrails server config parsed from
// the guardrails Secret referenced by the MCPGatewayExtension.
type GuardrailsConfig struct {
	URL       string   `json:"url"                 yaml:"url"`
	ConfigIDs []string `json:"configIDs,omitempty" yaml:"configIDs,omitempty"`
	Model     string   `json:"model"               yaml:"model"`
	FailMode  string   `json:"failMode,omitempty"  yaml:"failMode,omitempty"` // "deny" | "allow"
}

// TokenURLElicitationConfig configures per-user token collection via URL elicitation.
type TokenURLElicitationConfig struct {
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
}

// ID returns a unique id for the a registered server
func (mcpServer *MCPServer) ID() UpstreamMCPID {
	return UpstreamMCPID(fmt.Sprintf("%s:%s:%s", mcpServer.Name, mcpServer.Prefix, mcpServer.Hostname))
}

func normalizeState(state string) string {
	if state == "" {
		return "Enabled"
	}
	return state
}

// ConfigChanged checks if a server's config has changed in a way that will affect the gateway.
// This means having a different name, prefix, url, hostname, credential, state, category, hint, or tags.
func (mcpServer *MCPServer) ConfigChanged(existingConfig MCPServer) bool {
	if existingConfig.Name != mcpServer.Name ||
		existingConfig.Prefix != mcpServer.Prefix ||
		existingConfig.URL != mcpServer.URL ||
		existingConfig.Hostname != mcpServer.Hostname ||
		existingConfig.Credential != mcpServer.Credential ||
		existingConfig.CACert != mcpServer.CACert ||
		normalizeState(existingConfig.State) != normalizeState(mcpServer.State) ||
		existingConfig.UserSpecificList != mcpServer.UserSpecificList ||
		existingConfig.Hint != mcpServer.Hint ||
		guardrailsConfigChanged(existingConfig.GuardrailsConfigIDs, mcpServer.GuardrailsConfigIDs) ||
		tokenURLElicitationChanged(mcpServer.TokenURLElicitation, existingConfig.TokenURLElicitation) {
		return true
	}
	if !slices.Equal(existingConfig.Category, mcpServer.Category) {
		return true
	}
	return !tagsEqual(mcpServer.Tags, existingConfig.Tags)
}

// tagsEqual returns true if the two tag slices contain the same elements regardless of order.
func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, t := range a {
		counts[t]++
	}
	for _, t := range b {
		counts[t]--
		if counts[t] < 0 {
			return false
		}
	}
	return true
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

// guardrailsConfigChanged reports whether a server's per-server guardrails
// config IDs changed. The router evaluates rails in the order the annotation
// lists them.
func guardrailsConfigChanged(a, b []string) bool {
	return !slices.Equal(a, b)
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
	Servers          []MCPServer           `json:"servers"                     yaml:"servers"`
	VirtualServers   []VirtualServerConfig `json:"virtualServers,omitempty"    yaml:"virtualServers,omitempty"`
	GatewayCACertPEM string                `json:"gatewayCACertPEM,omitempty"  yaml:"gatewayCACertPEM,omitempty"`
	// GlobalGuardrails is the resolved guardrails config for this gateway,
	// parsed from the Secret referenced by the guardrails-ref annotation. Nil
	// when guardrails isn't configured.
	GlobalGuardrails *GuardrailsConfig `json:"globalGuardrails,omitempty" yaml:"globalGuardrails,omitempty"`
	// MaxBodyBytes caps any body the router buffers, from MCPGatewayExtension.spec.maxBodyBytes.
	MaxBodyBytes int64 `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
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
