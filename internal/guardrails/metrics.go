package guardrails

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	phaseLabel       = attribute.Key("phase")
	outcomeLabel     = attribute.Key("outcome")
	failureTypeLabel = attribute.Key("failure_type")
)

const outcomeError = "error"

var checkOutcomes = [...]string{string(StatusAllowed), string(StatusBlocked), string(StatusModified), outcomeError}

var failureKinds = [...]errorKind{
	errKindTranslation, errKindTimeout, errKindCanceled, errKindTransport, errKindHTTP, errKindBodyLimit, errKindParse,
}

// durationBuckets spans fast verdicts up to checkTimeout; the sdk defaults
// are sized for milliseconds.
var durationBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, checkTimeout.Seconds(), 10}

// checkMetrics keeps track of the check instruments and per-label measurement options.
type checkMetrics struct {
	checks   metric.Int64Counter
	duration metric.Float64Histogram
	failures metric.Int64Counter

	checksOpts   [phaseCount]map[string]metric.AddOption
	durationOpts [phaseCount]metric.RecordOption
	failureOpts  [phaseCount]map[errorKind]metric.AddOption
}

func newCheckMetrics() (*checkMetrics, error) {
	meter := otel.GetMeterProvider().Meter(instrumentationName)

	checks, err := meter.Int64Counter("mcp_guardrails_checks",
		metric.WithDescription("number of guardrails checks by phase and outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp_guardrails_checks: %w", err)
	}

	duration, err := meter.Float64Histogram("mcp_guardrails_check_duration_seconds",
		metric.WithDescription("time taken for guardrails checks, including translation and parsing"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp_guardrails_check_duration_seconds: %w", err)
	}

	failures, err := meter.Int64Counter("mcp_guardrails_check_failures",
		metric.WithDescription("number of failed guardrails checks by phase and failure type"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp_guardrails_check_failures: %w", err)
	}

	m := &checkMetrics{checks: checks, duration: duration, failures: failures}
	for p := range phaseCount {
		phaseAttr := phaseLabel.String(p.String())
		m.durationOpts[p] = metric.WithAttributeSet(attribute.NewSet(phaseAttr))
		m.checksOpts[p] = make(map[string]metric.AddOption, len(checkOutcomes))
		for _, o := range checkOutcomes {
			m.checksOpts[p][o] = metric.WithAttributeSet(attribute.NewSet(phaseAttr, outcomeLabel.String(o)))
		}
		m.failureOpts[p] = make(map[errorKind]metric.AddOption, len(failureKinds))
		for _, k := range failureKinds {
			m.failureOpts[p][k] = metric.WithAttributeSet(attribute.NewSet(phaseAttr, failureTypeLabel.String(string(k))))
		}
	}
	return m, nil
}

// record counts a completed check and its duration since start.
func (m *checkMetrics) record(ctx context.Context, p phase, outcome string, start time.Time) {
	m.duration.Record(ctx, time.Since(start).Seconds(), m.durationOpts[p])
	if opt, ok := m.checksOpts[p][outcome]; ok {
		m.checks.Add(ctx, 1, opt)
	}
}

// recordFailure counts a failed check under outcome error and its kind.
func (m *checkMetrics) recordFailure(ctx context.Context, p phase, kind errorKind, start time.Time) {
	m.record(ctx, p, outcomeError, start)
	if opt, ok := m.failureOpts[p][kind]; ok {
		m.failures.Add(ctx, 1, opt)
	}
}
