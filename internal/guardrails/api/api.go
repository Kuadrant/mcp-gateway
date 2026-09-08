// Package api defines the shared guardrails API types:
// config, decision, and checker. Implementations live in internal/guardrails.
package api

import (
	"context"
	"encoding/json"
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
}
