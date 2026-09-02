// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
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
	GlobalGuardrails           *guardrails.Config
	MaxBodyBytes               int64
	// guardrailsChecker is the live HTTP checker built from GlobalGuardrails.
	guardrailsChecker guardrails.Checker
}

// GuardrailsConfig is the serializable guardrails server config stored on
// MCPServersConfig and BrokerConfig.
type GuardrailsConfig = guardrails.Config

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
// A nil value clears it (guardrails disabled). Prefer SetGuardrails when
// updating the checker in the same step so readers cannot observe a tear.
func (config *MCPServersConfig) SetGlobalGuardrails(cfg *guardrails.Config) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.GlobalGuardrails = cfg
}

// GetGlobalGuardrails returns the resolved gateway-level guardrails config,
// or nil when guardrails is not configured.
func (config *MCPServersConfig) GetGlobalGuardrails() *guardrails.Config {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return config.GlobalGuardrails
}

// SetGuardrailsChecker stores the checker, or nil when guardrails is disabled.
// Prefer SetGuardrails when updating GlobalGuardrails in the same step.
func (config *MCPServersConfig) SetGuardrailsChecker(c guardrails.Checker) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.guardrailsChecker = c
}

// SetGuardrails stores global config and checker under one lock.
func (config *MCPServersConfig) SetGuardrails(global *guardrails.Config, checker guardrails.Checker) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.GlobalGuardrails = global
	config.guardrailsChecker = checker
}

// GetGuardrailsChecker returns the checker, or nil when guardrails is not configured.
func (config *MCPServersConfig) GetGuardrailsChecker() guardrails.Checker {
	c, _ := config.GetGuardrails()
	return c
}

// GetGuardrails returns the checker and resolved global config under one lock.
func (config *MCPServersConfig) GetGuardrails() (guardrails.Checker, *guardrails.Config) {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return config.guardrailsChecker, config.GlobalGuardrails
}

// GuardrailsSnapshot is a consistent view of guardrails state for one server.
// Taken under a single lock so checker, global config, and per-server IDs
// cannot tear across an in-place reload.
type GuardrailsSnapshot struct {
	Checker         guardrails.Checker
	Global          *guardrails.Config
	ServerConfigIDs []string
	Server          *MCPServer
}

// GuardrailsSnapshotFor returns a consistent snapshot for serverName.
// Server is nil and ServerConfigIDs empty when the server is unknown; Checker
// and Global are still returned from the same lock acquisition.
func (config *MCPServersConfig) GuardrailsSnapshotFor(serverName string) GuardrailsSnapshot {
	config.lock.RLock()
	defer config.lock.RUnlock()

	snap := GuardrailsSnapshot{
		Checker: config.guardrailsChecker,
		Global:  config.GlobalGuardrails,
	}
	for _, server := range config.Servers {
		if server.Name != serverName {
			continue
		}
		snap.Server = server
		if n := len(server.GuardrailsConfigIDs); n > 0 {
			snap.ServerConfigIDs = make([]string, n)
			copy(snap.ServerConfigIDs, server.GuardrailsConfigIDs)
		}
		break
	}
	return snap
}

// ApplyReload replaces servers and guardrails runtime state under one write
// lock so readers using GuardrailsSnapshotFor cannot observe a partial update.
func (config *MCPServersConfig) ApplyReload(
	servers []*MCPServer,
	virtualServers []*VirtualServer,
	gatewayCACertPEM string,
	maxBodyBytes int64,
	global *guardrails.Config,
	checker guardrails.Checker,
) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.Servers = servers
	config.VirtualServers = virtualServers
	config.GatewayCACertPEM = gatewayCACertPEM
	config.MaxBodyBytes = maxBodyBytes
	config.GlobalGuardrails = global
	config.guardrailsChecker = checker
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
	GlobalGuardrails *guardrails.Config `json:"globalGuardrails,omitempty" yaml:"globalGuardrails,omitempty"`
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
