// Package api defines the shared guardrails API types:
// config, decision, and checker. Implementations live in internal/guardrails.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// Config holds the resolved guardrails server config parsed from
// the guardrails Secret referenced by the MCPGatewayExtension.
type Config struct {
	URL       string   `json:"url"                 yaml:"url"`
	ConfigIDs []string `json:"configIDs,omitempty" yaml:"configIDs,omitempty"`
	Model     string   `json:"model"               yaml:"model"`
	FailMode  string   `json:"failMode,omitempty"  yaml:"failMode,omitempty"` // "deny" | "allow"
}

// Fail modes applied when the guardrails server is unreachable or errors.
const (
	FailModeDeny  = "deny"
	FailModeAllow = "allow"
)

// Validate checks the resolved guardrails configuration before it is published
// to the broker or used to construct an outbound checker. Literal local and
// private destinations are rejected; DNS names remain supported for in-cluster
// guardrails services.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("guardrails config is nil")
	}
	if c.URL == "" {
		return fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("url %q is not a valid absolute URL", c.URL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	normalizedHost := strings.TrimSuffix(strings.ToLower(host), ".")
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return fmt.Errorf("url must not target a private or loopback address")
	}
	ipHost := strings.TrimSuffix(host, ".")
	if zone := strings.LastIndexByte(ipHost, '%'); zone >= 0 {
		ipHost = ipHost[:zone]
	}
	if ip := net.ParseIP(ipHost); ip != nil &&
		(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return fmt.Errorf("url must not target a private or loopback address")
	}

	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	switch c.FailMode {
	case "", FailModeDeny, FailModeAllow:
		return nil
	default:
		return fmt.Errorf("failMode must be %q or %q, got %q", FailModeDeny, FailModeAllow, c.FailMode)
	}
}

// Equal reports whether c and other represent the same guardrails config.
// nil equals nil; empty FailMode is treated as FailModeDeny.
func (c *Config) Equal(other *Config) bool {
	if c == nil && other == nil {
		return true
	}
	if c == nil || other == nil {
		return false
	}
	normalizeFailMode := func(fm string) string {
		if fm == "" {
			return FailModeDeny
		}
		return fm
	}
	return c.URL == other.URL &&
		c.Model == other.Model &&
		normalizeFailMode(c.FailMode) == normalizeFailMode(other.FailMode) &&
		slices.Equal(c.ConfigIDs, other.ConfigIDs)
}

// Status is the outcome of a guardrails check.
type Status string

// Status values a Decision can carry.
const (
	StatusAllowed  Status = "allowed"
	StatusBlocked  Status = "blocked"
	StatusModified Status = "modified"
)

// Decision is the outcome of a single guardrails check, translated from the
// provider response into a form the router acts on.
type Decision struct {
	Status Status
	// Content is the text to forward: the original content unless Status
	// is StatusModified, in which case it's the guardrails modified text.
	Content string
	// Reason names the triggering rail. Empty when Status is StatusAllowed.
	Reason string
	// Err is set when Status was resolved by failMode after a transport
	// failure or unparseable response.
	Err error
}

// Checker runs guardrails checks against tools/call requests and responses.
type Checker interface {
	CheckRequest(ctx context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*Decision, error)
	CheckResponse(ctx context.Context, toolName string, content []byte, configIDs []string) (*Decision, error)
	// Close releases idle HTTP connections held by the checker. Should be
	// called when the checker is replaced after a config reload.
	Close() error
}
