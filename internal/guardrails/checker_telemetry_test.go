package guardrails

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type telemetry struct {
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
}

// setupTelemetry installs tracer and meter providers. Call before
// building the checker so its instruments bind to the test meter.
func setupTelemetry(t *testing.T) *telemetry {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	prevTP, prevMP := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return &telemetry{spans: spans, reader: reader}
}

func (tel *telemetry) checkSpan(t *testing.T) tracetest.SpanStub {
	t.Helper()
	var found []tracetest.SpanStub
	for _, s := range tel.spans.GetSpans() {
		if s.Name == checkSpanName {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "expected exactly one %s span", checkSpanName)
	return found[0]
}

func (tel *telemetry) metrics(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, tel.reader.Collect(context.Background(), &rm))
	return rm
}

func spanAttrs(s tracetest.SpanStub) map[attribute.Key]attribute.Value {
	m := make(map[attribute.Key]attribute.Value, len(s.Attributes))
	for _, kv := range s.Attributes {
		m[kv.Key] = kv.Value
	}
	return m
}

func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

func labelsEqual(set attribute.Set, labels map[string]string) bool {
	if set.Len() != len(labels) {
		return false
	}
	for k, v := range labels {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

// counterValue returns the sum for the data point matching labels exactly,
// or 0 when absent.
func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string, labels map[string]string) int64 {
	t.Helper()
	m, ok := findMetric(rm, name)
	if !ok {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "%s is not an int64 counter", name)
	for _, dp := range sum.DataPoints {
		if labelsEqual(dp.Attributes, labels) {
			return dp.Value
		}
	}
	return 0
}

func histogramCount(t *testing.T, rm metricdata.ResourceMetrics, name string, labels map[string]string) uint64 {
	t.Helper()
	m, ok := findMetric(rm, name)
	require.True(t, ok, "%s not recorded", name)
	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "%s is not a float64 histogram", name)
	for _, dp := range hist.DataPoints {
		if labelsEqual(dp.Attributes, labels) {
			return dp.Count
		}
	}
	return 0
}

// assertCheckTelemetry asserts the span and metrics of exactly one check.
// failureType is empty for checks that returned a verdict.
func assertCheckTelemetry(t *testing.T, tel *telemetry, phase, outcome, failureType string, httpCode int64) {
	t.Helper()

	s := tel.checkSpan(t)
	attrs := spanAttrs(s)
	require.Equal(t, trace.SpanKindClient, s.SpanKind)
	require.Equal(t, phase, attrs[phaseKey].AsString())
	require.Equal(t, outcome, attrs[outcomeKey].AsString())
	if httpCode != 0 {
		require.Equal(t, httpCode, attrs["http.response.status_code"].AsInt64())
	} else {
		require.NotContains(t, attrs, attribute.Key("http.response.status_code"))
	}

	rm := tel.metrics(t)
	require.Equal(t, int64(1), counterValue(t, rm, "mcp_guardrails_checks",
		map[string]string{"phase": phase, "outcome": outcome}))
	require.Equal(t, uint64(1), histogramCount(t, rm, "mcp_guardrails_check_duration_seconds",
		map[string]string{"phase": phase}))

	if failureType == "" {
		require.NotEqual(t, codes.Error, s.Status.Code)
		require.NotContains(t, attrs, attribute.Key("error.type"))
		_, recorded := findMetric(rm, "mcp_guardrails_check_failures")
		require.False(t, recorded, "successful checks must not record failures")
		return
	}
	require.Equal(t, codes.Error, s.Status.Code)
	require.Equal(t, failureType, attrs["error.type"].AsString())
	require.Equal(t, int64(1), counterValue(t, rm, "mcp_guardrails_check_failures",
		map[string]string{"phase": phase, "failure_type": failureType}))
}

func writeVerdict(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func writeStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func TestNeMoChecker_CheckTelemetry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		handler     http.HandlerFunc
		failMode    string
		maxBody     int64
		response    bool
		emptyTool   bool
		outcome     string
		failureType string
		httpCode    int64
	}{
		{name: "allowed request", handler: writeVerdict(`{"status":"passed","content":"ok"}`), outcome: "allowed", httpCode: 200},
		{name: "blocked request", handler: writeVerdict(`{"status":"blocked","content":"no","rail":"r"}`), outcome: "blocked", httpCode: 200},
		{name: "modified response", handler: writeVerdict(`{"status":"modified","content":"[x]"}`), response: true, outcome: "modified", httpCode: 200},
		{name: "translation failure", handler: writeVerdict(`{}`), emptyTool: true, outcome: "error", failureType: "translation"},
		{name: "http status failure", handler: writeStatus(http.StatusBadGateway), outcome: "error", failureType: "http_status", httpCode: 502},
		{
			name:    "body limit failure",
			handler: writeVerdict(`{"status":"passed","content":"` + strings.Repeat("a", 32) + `"}`),
			maxBody: 8, outcome: "error", failureType: "body_limit", httpCode: 200,
		},
		{name: "parse failure", handler: writeVerdict(`not json`), outcome: "error", failureType: "parse", httpCode: 200},
		{
			name:     "fail-open http failure is still a telemetry failure",
			handler:  writeStatus(http.StatusServiceUnavailable),
			failMode: FailModeAllow, outcome: "error", failureType: "http_status", httpCode: 503,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tel := setupTelemetry(t)
			failMode := tc.failMode
			if failMode == "" {
				failMode = FailModeDeny
			}
			checker := newTestCheckerWithMaxBodyBytes(t, tc.handler, failMode, tc.maxBody)

			toolName := "execute_sql"
			if tc.emptyTool {
				toolName = ""
			}
			phase := "request"
			if tc.response {
				phase = "response"
				_, _ = checker.CheckResponse(context.Background(), toolName, []byte("text"), nil)
			} else {
				_, _ = checker.CheckRequest(context.Background(), toolName, json.RawMessage(`{}`), nil)
			}

			assertCheckTelemetry(t, tel, phase, tc.outcome, tc.failureType, tc.httpCode)
		})
	}

	t.Run("fail-open transport failure is a telemetry failure with an allowed decision", func(t *testing.T) {
		tel := setupTelemetry(t)
		checker := mustNewChecker(t, &Config{URL: "http://127.0.0.1:1", Model: "m", FailMode: FailModeAllow}, 0)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusAllowed, decision.Status)

		assertCheckTelemetry(t, tel, "request", "error", "transport", 0)
	})

	t.Run("deadline expiry is recorded as timeout", func(t *testing.T) {
		tel := setupTelemetry(t)
		checker := newTestChecker(t, func(_ http.ResponseWriter, r *http.Request) {
			// the server only detects client disconnect once the body is consumed
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}, FailModeDeny)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		decision, err := checker.CheckRequest(ctx, "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)

		assertCheckTelemetry(t, tel, "request", "error", "timeout", 0)
	})

	t.Run("caller cancellation is recorded as canceled, not transport", func(t *testing.T) {
		tel := setupTelemetry(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		checker := newTestChecker(t, func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			cancel()
			<-r.Context().Done()
		}, FailModeDeny)

		decision, err := checker.CheckRequest(ctx, "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)

		assertCheckTelemetry(t, tel, "request", "error", "canceled", 0)
	})

	t.Run("span is a child of the caller span", func(t *testing.T) {
		tel := setupTelemetry(t)
		checker := newTestChecker(t, writeVerdict(`{"status":"passed","content":"ok"}`), FailModeDeny)

		ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
		_, _ = checker.CheckRequest(ctx, "execute_sql", json.RawMessage(`{}`), nil)
		parent.End()

		require.Equal(t, parent.SpanContext().SpanID(), tel.checkSpan(t).Parent.SpanID())
	})
}

// TestNeMoChecker_TelemetryRecordsNoContent checks that no tool name,
// argument, content, config ID, or server output reaches spans or metric
// labels, on verdict and failure paths for both phases.
func TestNeMoChecker_TelemetryRecordsNoContent(t *testing.T) {
	forbidden := []string{
		"execute_sql", "hunter2", "password", "alice@example.com",
		"global-1", "server-config-id", "secret-redacted", "pii-rail", "server-leak",
	}
	allowedLabels := map[attribute.Key]bool{"phase": true, "outcome": true, "failure_type": true}

	for _, tc := range []struct {
		name     string
		handler  http.HandlerFunc
		response bool
	}{
		{name: "request verdict", handler: writeVerdict(`{"status":"modified","content":"secret-redacted","rail":"pii-rail"}`)},
		{name: "response verdict", handler: writeVerdict(`{"status":"blocked","content":"secret-redacted","rail":"pii-rail"}`), response: true},
		{name: "request parse failure", handler: writeVerdict(`{"status":"server-leak"}`)},
		{
			name: "response http failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("server-leak"))
			},
			response: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tel := setupTelemetry(t)
			checker := newTestChecker(t, tc.handler, FailModeDeny)
			if tc.response {
				_, _ = checker.CheckResponse(context.Background(), "execute_sql", []byte("alice@example.com"), []string{"server-config-id"})
			} else {
				_, _ = checker.CheckRequest(context.Background(), "execute_sql",
					json.RawMessage(`{"password":"hunter2"}`), []string{"server-config-id"})
			}

			s := tel.checkSpan(t)
			texts := []string{s.Status.Description}
			for _, kv := range s.Attributes {
				texts = append(texts, kv.Value.String())
			}
			for _, ev := range s.Events {
				texts = append(texts, ev.Name)
				for _, kv := range ev.Attributes {
					texts = append(texts, kv.Value.String())
				}
			}

			rm := tel.metrics(t)
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					var sets []attribute.Set
					switch data := m.Data.(type) {
					case metricdata.Sum[int64]:
						for _, dp := range data.DataPoints {
							sets = append(sets, dp.Attributes)
						}
					case metricdata.Histogram[float64]:
						for _, dp := range data.DataPoints {
							sets = append(sets, dp.Attributes)
						}
					}
					for _, set := range sets {
						for _, kv := range set.ToSlice() {
							require.True(t, allowedLabels[kv.Key], "unexpected label %q on %s", kv.Key, m.Name)
							texts = append(texts, kv.Value.String())
						}
					}
				}
			}

			for _, text := range texts {
				for _, f := range forbidden {
					require.NotContains(t, text, f)
				}
			}
		})
	}
}
