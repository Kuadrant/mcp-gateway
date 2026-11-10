// Package guardrails checks tools/call requests and responses against an
// external guardrails server. The NeMo-backed Checker owns HTTP transport,
// timeout, TLS, fail mode, config ID merging, and provider translation.
package guardrails

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Kuadrant/mcp-gateway/internal/guardrails/api"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails/external/nemo"
)

// Config is the resolved guardrails server config (alias of api.Config).
type Config = api.Config

// Checker runs guardrails checks against tools/call requests and responses
// (alias of api.Checker).
type Checker = api.Checker

// Decision is the outcome of a single guardrails check (alias of api.Decision).
type Decision = api.Decision

// Status is the outcome kind of a Decision (alias of api.Status).
type Status = api.Status

// Status values a Decision can carry.
const (
	StatusAllowed  = api.StatusAllowed
	StatusBlocked  = api.StatusBlocked
	StatusModified = api.StatusModified
)

// Fail modes applied when the guardrails server is unreachable or errors.
const (
	FailModeDeny  = api.FailModeDeny
	FailModeAllow = api.FailModeAllow
)

// checksPath is the guardrails server endpoint all checks are sent to.
const checksPath = "/v1/checks"

// checkTimeout bounds a single guardrails HTTP round trip, comfortably
// inside the 10s ext_proc message_timeout (config/istio/envoyfilter.yaml,
// mcpgatewayextension_controller.go). 3s was too tight in practice: a small
// self-hosted judge model (e.g. phi3) can take ~3.3s for a single
// self-check LLM call under light load, causing "guardrails check
// unavailable" (fail-closed) on otherwise-benign requests. 8s leaves ~2s of
// headroom within the ext_proc window for HTTP/TLS overhead on top of the
// LLM call itself.
const checkTimeout = 8 * time.Second

// dialTimeout bounds DNS/TCP connection setup so an unreachable guardrails
// server fails fast rather than eating the full checkTimeout on dial alone.
const dialTimeout = 1 * time.Second

// defaultMaxIdleConnsPerHost is used when the caller doesn't specify a
// concurrency hint.
const defaultMaxIdleConnsPerHost = 100

// instrumentationName names the tracer and meter which matches the router.
const instrumentationName = "mcp-router"

const checkSpanName = "mcp-guardrails.nemo.check"

// phase is the tools/call side being checked.
type phase int

const (
	phaseRequest phase = iota
	phaseResponse
	phaseCount
)

var phaseNames = [phaseCount]string{"request", "response"}

func (p phase) String() string { return phaseNames[p] }

// attributes for the guardrails check span.
const (
	phaseKey   = attribute.Key("guardrails.phase")
	outcomeKey = attribute.Key("guardrails.outcome")
)

var (
	phaseAttrs       = [phaseCount]attribute.KeyValue{phaseKey.String("request"), phaseKey.String("response")}
	outcomeErrorAttr = outcomeKey.String(outcomeError)
)

// errorKind classifies a check failure for telemetry.
type errorKind string

const (
	errKindTranslation errorKind = "translation"
	errKindTimeout     errorKind = "timeout"
	errKindCanceled    errorKind = "canceled"
	errKindTransport   errorKind = "transport"
	errKindHTTP        errorKind = "http_status"
	errKindBodyLimit   errorKind = "body_limit"
	errKindParse       errorKind = "parse"
)

// defaultMaxBodyBytes bounds the guardrails server's check response when the
// caller doesn't specify a limit, matching the MCPGatewayExtension
// maxBodyBytes default (1 MiB).
const defaultMaxBodyBytes = 1 << 20

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
	*nemo.Transformer
}

func (p *nemoProvider) ParseCheckResponse(body []byte) (Status, string, string, error) {
	resp, err := p.Transformer.ParseCheckResponse(body)
	if err != nil {
		return "", "", "", err
	}
	switch resp.Status {
	case nemo.StatusPassed:
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
	metrics         *checkMetrics
}

var _ Checker = (*nemoChecker)(nil)

// Close releases idle connections held by the checker's HTTP transport.
func (c *nemoChecker) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// NewChecker constructs a Checker for the given resolved guardrails config.
// maxBodyBytes bounds the guardrails server's check response; non-positive
// values fall back to defaultMaxBodyBytes.
func NewChecker(cfg *Config, tlsConfig *tls.Config, maxIdleConnsPerHost int, maxBodyBytes int64) (Checker, error) {
	if maxIdleConnsPerHost <= 0 {
		maxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}

	metrics, err := newCheckMetrics()
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: dialTimeout}
	transport.DialContext = dialer.DialContext
	transport.TLSClientConfig = tlsConfig
	transport.MaxIdleConnsPerHost = maxIdleConnsPerHost

	return &nemoChecker{
		// no Client.Timeout: each call sets its own context deadline instead
		httpClient:      &http.Client{Transport: transport},
		baseURL:         strings.TrimSuffix(cfg.URL, "/"),
		globalConfigIDs: cfg.ConfigIDs,
		failMode:        normalizeFailMode(cfg.FailMode),
		maxBodyBytes:    maxBodyBytes,
		provider:        &nemoProvider{Transformer: nemo.NewTransformer(cfg.Model)},
		metrics:         metrics,
	}, nil
}

// CheckRequest translates and checks a tools/call request. A translation
// failure is always a hard deny regardless of failMode; a transport failure
// or an unparseable guardrails response falls back to failMode instead.
func (c *nemoChecker) CheckRequest(ctx context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*Decision, error) {
	return c.check(ctx, phaseRequest, func() ([]byte, error) {
		return c.provider.TransformRequest(toolName, arguments, mergeConfigIDs(c.globalConfigIDs, configIDs))
	})
}

// CheckResponse translates and checks a tools/call response's text content.
// Same failure semantics as CheckRequest.
func (c *nemoChecker) CheckResponse(ctx context.Context, toolName string, content []byte, configIDs []string) (*Decision, error) {
	return c.check(ctx, phaseResponse, func() ([]byte, error) {
		return c.provider.TransformResponse(toolName, content, mergeConfigIDs(c.globalConfigIDs, configIDs))
	})
}

// check translates, performs the guardrails HTTP round trip,
// and maps the outcome to a Decision, recording one span and one set of
// metrics. Non-2xx, transport errors, oversized bodies, and unparseable
// responses all fall back to failMode rather than propagating an error —
// only a translation failure skips failMode entirely. A failMode fallback
// is a telemetry failure even when it allows.
func (c *nemoChecker) check(ctx context.Context, p phase, translate func() ([]byte, error)) (*Decision, error) {
	start := time.Now()
	ctx, span := otel.Tracer(instrumentationName).Start(ctx, checkSpanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(phaseAttrs[p]),
	)
	defer span.End()

	body, err := translate()
	if err != nil {
		c.recordFailure(ctx, span, p, errKindTranslation, start)
		return nil, fmt.Errorf("guardrails: %s translation failed: %w", p, err)
	}

	decision, kind, err := c.checkResponse(ctx, span, body)
	if err != nil {
		c.recordFailure(ctx, span, p, kind, start)
		return c.failModeDecision(err), nil
	}

	span.SetAttributes(outcomeKey.String(string(decision.Status)))
	c.metrics.record(ctx, p, string(decision.Status), start)
	return decision, nil
}

// recordFailure marks span failed with a fixed message and counts the
// failure; raw error text may echo server output or URLs, so it is not
// recorded.
func (c *nemoChecker) recordFailure(ctx context.Context, span trace.Span, p phase, kind errorKind, start time.Time) {
	span.SetAttributes(outcomeErrorAttr, semconv.ErrorTypeKey.String(string(kind)))
	span.SetStatus(codes.Error, "guardrails check failed: "+string(kind))
	c.metrics.recordFailure(ctx, p, kind, start)
}

// transportErrorKind separates timeouts (checkTimeout, dialTimeout, or the
// caller's deadline) and caller cancellation, e.g. a client disconnect, from
// guardrails server transport failures.
func transportErrorKind(err error) errorKind {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return errKindTimeout
	case errors.Is(err, context.Canceled):
		return errKindCanceled
	default:
		return errKindTransport
	}
}

func (c *nemoChecker) checkResponse(ctx context.Context, span trace.Span, body []byte) (*Decision, errorKind, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checksPath, bytes.NewReader(body))
	if err != nil {
		return nil, errKindTransport, fmt.Errorf("guardrails: failed to build check request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, transportErrorKind(err), fmt.Errorf("guardrails: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close, response already consumed
	span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxBodyBytes+1))
		return nil, errKindHTTP, fmt.Errorf("guardrails: server returned status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBodyBytes+1))
	if err != nil {
		return nil, transportErrorKind(err), fmt.Errorf("guardrails: failed to read response: %w", err)
	}
	if int64(len(respBody)) > c.maxBodyBytes {
		return nil, errKindBodyLimit, fmt.Errorf("guardrails: response exceeds %d byte limit", c.maxBodyBytes)
	}

	status, content, reason, err := c.provider.ParseCheckResponse(respBody)
	if err != nil {
		return nil, errKindParse, fmt.Errorf("guardrails: malformed response: %w", err)
	}

	return &Decision{Status: status, Content: content, Reason: reason}, "", nil
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
