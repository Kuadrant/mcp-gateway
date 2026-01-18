// Package config provides configuration types
package config

import (
	"context"
	"fmt"
	"net/url"
)

// UpstreamMCPID is used as type for identifying individual upstreams
type UpstreamMCPID string

// MCPServersConfig holds server configuration
type MCPServersConfig struct {
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
	config.observers = append(config.observers, obs)
}

// Notify notifies registered observers of config changes
func (config *MCPServersConfig) Notify(ctx context.Context) {
	for _, observer := range config.observers {
		go observer.OnConfigChange(ctx, config)
	}
}

// GetServerConfigByName get the routing config by server name
func (config *MCPServersConfig) GetServerConfigByName(serverName string) *MCPServer {
	for _, server := range config.Servers {
		if server.Name == serverName {
			return server
		}
	}
	return nil
}

// MCPServer represents a server
type MCPServer struct {
	Name       string
	URL        string
	ToolPrefix string
	Enabled    bool
	Hostname   string
	Credential string // env var name for auth
}

// ID returns a unique id for the a registered server
func (mcpServer *MCPServer) ID() UpstreamMCPID {
	return UpstreamMCPID(fmt.Sprintf("%s:%s:%s", mcpServer.Name, mcpServer.ToolPrefix, mcpServer.Hostname))
}

// ConfigChanged checks if a server's config has changed in a way that will affect the gateway.
// This means having a different name, prefix, hostname, or credential variable.
func (mcpServer *MCPServer) ConfigChanged(existingConfig MCPServer) bool {
	return existingConfig.Name != mcpServer.Name ||
		existingConfig.ToolPrefix != mcpServer.ToolPrefix ||
		existingConfig.Hostname != mcpServer.Hostname ||
		existingConfig.Credential != mcpServer.Credential
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
