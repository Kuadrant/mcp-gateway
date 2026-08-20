// Package guardrails checks tools/call requests and responses against an
// external guardrails server. Checker owns HTTP transport, timeout, TLS,
// fail mode, config ID merging, and provider translation.
package guardrails

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails/external/nemo"
)

// checksPath is the guardrails server endpoint all checks are sent to.
const checksPath = "/v1/guardrail/checks"

// checkTimeout bounds a single guardrails HTTP round trip, comfortably
// inside the 10s ext_proc message_timeout.
const checkTimeout = 3 * time.Second

// dialTimeout bounds DNS/TCP connection setup so an unreachable guardrails
// server fails fast rather than eating the full checkTimeout on dial alone.
const dialTimeout = 1 * time.Second

// defaultMaxIdleConnsPerHost is used when the caller doesn't specify a
// concurrency hint.
const defaultMaxIdleConnsPerHost = 100

// defaultMaxBodyBytes bounds the guardrails server's check response when the
// caller doesn't specify a limit, matching the MCPGatewayExtension
// maxBodyBytes default (1 MiB).
const defaultMaxBodyBytes = 1 << 20

// Status is the outcome of a guardrails check.
type Status string

// Status values a Decision can carry.
const (
	StatusAllowed  Status = "allowed"
	StatusBlocked  Status = "blocked"
	StatusModified Status = "modified"
)

// Decision is the outcome of a single guardrails check, translated from the
// NeMo Guardrails server response into a form the router acts on.
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

// provider translates between MCP and a guardrails backend's check
// request/response schema, and classifies a raw verdict into a Status.
// Secret type determines which implementation is used; nemoProvider is the
// only one today.
type provider interface {
	TransformRequest(toolName string, arguments json.RawMessage, configIDs []string) ([]byte, error)
	TransformResponse(toolName string, content []byte, configIDs []string) ([]byte, error)
	ParseCheckResponse(body []byte) (status Status, content, reason string, err error)
}

// nemoProvider adapts *nemo.Transformer to the provider interface,
// translating NeMo's status strings into the transport-agnostic Status.
type nemoProvider struct {
	transformer *nemo.Transformer
}

func (p *nemoProvider) TransformRequest(toolName string, arguments json.RawMessage, configIDs []string) ([]byte, error) {
	return p.transformer.TransformRequest(toolName, arguments, configIDs)
}

func (p *nemoProvider) TransformResponse(toolName string, content []byte, configIDs []string) ([]byte, error) {
	return p.transformer.TransformResponse(toolName, content, configIDs)
}

func (p *nemoProvider) ParseCheckResponse(body []byte) (Status, string, string, error) {
	resp, err := p.transformer.ParseCheckResponse(body)
	if err != nil {
		return "", "", "", err
	}
	switch resp.Status {
	case nemo.StatusSuccess:
		return StatusAllowed, resp.Content, resp.Rail, nil
	case nemo.StatusModified:
		return StatusModified, resp.Content, resp.Rail, nil
	case nemo.StatusBlocked:
		return StatusBlocked, resp.Content, resp.Rail, nil
	default:
		// unreachable: nemo.Transformer.ParseCheckResponse already rejects
		// unrecognized status values.
		return "", "", "", fmt.Errorf("guardrails: unrecognized status %q", resp.Status)
	}
}

// nemoChecker implements Checker against a NeMo Guardrails server.
type nemoChecker struct {
	httpClient      *http.Client
	baseURL         string
	globalConfigIDs []string
	failMode        string
	maxBodyBytes    int64
	provider        provider
}

// NewChecker constructs a Checker for the given resolved guardrails config.
// maxBodyBytes bounds the guardrails server's check response; non-positive
// values fall back to defaultMaxBodyBytes.
func NewChecker(cfg *config.GuardrailsConfig, tlsConfig *tls.Config, maxIdleConnsPerHost int, maxBodyBytes int64) Checker {
	if maxIdleConnsPerHost <= 0 {
		maxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tlsConfig,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
	}

	return &nemoChecker{
		// no Client.Timeout: each call sets its own context deadline instead
		httpClient:      &http.Client{Transport: transport},
		baseURL:         strings.TrimSuffix(cfg.URL, "/"),
		globalConfigIDs: cfg.ConfigIDs,
		failMode:        normalizeFailMode(cfg.FailMode),
		maxBodyBytes:    maxBodyBytes,
		provider:        &nemoProvider{transformer: nemo.NewTransformer(cfg.Model)},
	}
}

// CheckRequest translates and checks a tools/call request. A translation
// failure is always a hard deny regardless of failMode; a transport failure
// or an unparseable guardrails response falls back to failMode instead.
func (c *nemoChecker) CheckRequest(ctx context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*Decision, error) {
	body, err := c.provider.TransformRequest(toolName, arguments, mergeConfigIDs(c.globalConfigIDs, configIDs))
	if err != nil {
		return nil, fmt.Errorf("guardrails: request translation failed: %w", err)
	}
	return c.check(ctx, body)
}

// CheckResponse translates and checks a tools/call response's text content.
// Same failure semantics as CheckRequest.
func (c *nemoChecker) CheckResponse(ctx context.Context, toolName string, content []byte, configIDs []string) (*Decision, error) {
	body, err := c.provider.TransformResponse(toolName, content, mergeConfigIDs(c.globalConfigIDs, configIDs))
	if err != nil {
		return nil, fmt.Errorf("guardrails: response translation failed: %w", err)
	}
	return c.check(ctx, body)
}

// check performs the guardrails HTTP round trip and maps the outcome to a
// Decision. Non-2xx, transport errors, oversized bodies, and unparseable
// responses all fall back to failMode rather than propagating an error —
// only a translation failure (handled by the caller) skips failMode
// entirely.
func (c *nemoChecker) check(ctx context.Context, body []byte) (*Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checksPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("guardrails: failed to build check request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.failModeDecision(fmt.Errorf("guardrails: request failed: %w", err)), nil
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close, response already consumed

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBodyBytes+1))
	if err != nil {
		return c.failModeDecision(fmt.Errorf("guardrails: failed to read response: %w", err)), nil
	}
	if int64(len(respBody)) > c.maxBodyBytes {
		return c.failModeDecision(fmt.Errorf("guardrails: response exceeds %d byte limit", c.maxBodyBytes)), nil
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return c.failModeDecision(fmt.Errorf("guardrails: server returned status %d", resp.StatusCode)), nil
	}

	status, content, reason, err := c.provider.ParseCheckResponse(respBody)
	if err != nil {
		return c.failModeDecision(fmt.Errorf("guardrails: malformed response: %w", err)), nil
	}

	return &Decision{Status: status, Content: content, Reason: reason}, nil
}

// failModeDecision resolves a transport failure or unparseable response
// into a Decision per the configured failMode, keeping cause so callers can
// tell a failMode fallback apart from a real guardrails verdict.
func (c *nemoChecker) failModeDecision(cause error) *Decision {
	if c.failMode == FailModeAllow {
		return &Decision{Status: StatusAllowed, Err: cause}
	}
	return &Decision{Status: StatusBlocked, Reason: "guardrails check failed", Err: cause}
}

// mergeConfigIDs lists global config IDs first, then per-server ones, so
// gateway-wide policies evaluate before server-specific ones, deduplicating
// any overlap between the two.
func mergeConfigIDs(global, perServer []string) []string {
	if len(global) == 0 {
		return dedup(perServer)
	}
	if len(perServer) == 0 {
		return dedup(global)
	}
	merged := make([]string, 0, len(global)+len(perServer))
	merged = append(merged, global...)
	merged = append(merged, perServer...)
	return dedup(merged)
}

// dedup removes duplicates while preserving first-occurrence order. Returns
// the input unmodified (including nil) when there's nothing to dedup.
func dedup(ids []string) []string {
	if len(ids) < 2 {
		return ids
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeFailMode(failMode string) string {
	if failMode == "" {
		return FailModeDeny
	}
	return failMode
}
