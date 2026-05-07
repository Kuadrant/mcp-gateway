// report generates an HTML performance report from k6 CSV output.
// reads k6 CSV, aggregates MCP metrics into time-series buckets,
// and outputs a self-contained HTML file with interactive charts.
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type sample struct {
	metric string
	ts     int64
	value  float64
}

type bucket struct {
	sum   float64
	count int
	max   float64
	vals  []float64
}

func (b *bucket) avg() float64 {
	if b.count == 0 {
		return 0
	}
	return b.sum / float64(b.count)
}

func (b *bucket) percentile(p float64) float64 {
	if len(b.vals) == 0 {
		return 0
	}
	sort.Float64s(b.vals)
	idx := int(math.Ceil(p/100*float64(len(b.vals)))) - 1
	if idx < 0 {
		idx = 0
	}
	return b.vals[idx]
}

func main() {
	csvPath := flag.String("csv", "", "k6 CSV output file")
	baselinePath := flag.String("baseline", "", "baseline k6 CSV for comparison (optional)")
	resourcePath := flag.String("resources", "", "resource usage CSV (from collect-resources.sh)")
	outPath := flag.String("out", "", "output HTML file (default: stdout)")
	title := flag.String("title", "MCP Gateway Performance Report", "report title")
	flag.Parse()

	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "usage: report -csv <k6.csv> [-baseline <baseline-k6.csv>] [-resources r.csv] [-out report.html]")
		os.Exit(1)
	}

	samples, err := readCSV(*csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading CSV: %v\n", err)
		os.Exit(1)
	}

	var resources []resourceSample
	if *resourcePath != "" {
		resources, err = readResourceCSV(*resourcePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read resources CSV: %v\n", err)
		}
	}

	var baselineSamples []sample
	if *baselinePath != "" {
		baselineSamples, err = readCSV(*baselinePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read baseline CSV: %v\n", err)
		}
	}

	report := buildReport(samples, resources, baselineSamples, *title)

	var w io.Writer = os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating output: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	_, _ = fmt.Fprint(w, report)
}

func readCSV(path string) ([]sample, error) {
	f, err := os.Open(path) //nolint:gosec // path from CLI flag
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}

	nameIdx, tsIdx, valIdx := -1, -1, -1
	for i, h := range header {
		switch h {
		case "metric_name":
			nameIdx = i
		case "timestamp":
			tsIdx = i
		case "metric_value":
			valIdx = i
		}
	}
	if nameIdx < 0 || tsIdx < 0 || valIdx < 0 {
		return nil, fmt.Errorf("missing required columns")
	}

	var samples []sample
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue
		}
		ts, err := strconv.ParseInt(row[tsIdx], 10, 64)
		if err != nil {
			continue
		}
		val, err := strconv.ParseFloat(row[valIdx], 64)
		if err != nil {
			continue
		}
		samples = append(samples, sample{metric: row[nameIdx], ts: ts, value: val})
	}
	return samples, nil
}

type chartData struct {
	Labels []string    `json:"labels"`
	Series [][]float64 `json:"series"`
	Names  []string    `json:"names"`
}

func aggregate(samples []sample, metric string) map[int64]*bucket {
	buckets := map[int64]*bucket{}
	for _, s := range samples {
		if s.metric != metric {
			continue
		}
		b, ok := buckets[s.ts]
		if !ok {
			b = &bucket{}
			buckets[s.ts] = b
		}
		b.sum += s.value
		b.count++
		if s.value > b.max {
			b.max = s.value
		}
		b.vals = append(b.vals, s.value)
	}
	return buckets
}

func timeRange(samples []sample) (int64, int64) {
	minTS, maxTS := int64(math.MaxInt64), int64(0)
	for _, s := range samples {
		if s.ts < minTS {
			minTS = s.ts
		}
		if s.ts > maxTS {
			maxTS = s.ts
		}
	}
	return minTS, maxTS
}

func buildTimeSeries(samples []sample, metric string, agg string, minTS, maxTS int64) ([]string, []float64) {
	buckets := aggregate(samples, metric)
	var labels []string
	var values []float64

	for ts := minTS; ts <= maxTS; ts++ {
		t := time.Unix(ts, 0)
		labels = append(labels, t.Format("15:04:05"))
		b, ok := buckets[ts]
		if !ok {
			values = append(values, 0)
			continue
		}
		switch agg {
		case "avg":
			values = append(values, b.avg())
		case "count":
			values = append(values, float64(b.count))
		case "sum":
			values = append(values, b.sum)
		case "rate":
			values = append(values, b.sum/float64(b.count))
		case "p95":
			values = append(values, b.percentile(95))
		case "p99":
			values = append(values, b.percentile(99))
		case "max":
			values = append(values, b.max)
		}
	}
	return labels, values
}

type summary struct {
	Total      int     `json:"total"`
	Successes  int     `json:"successes"`
	Fails      int     `json:"fails"`
	Rate       float64 `json:"rate"`
	AvgMs      float64 `json:"avgMs"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
	MaxMs      float64 `json:"maxMs"`
	SuccAvgMs  float64 `json:"succAvgMs"`
	SuccP95Ms  float64 `json:"succP95Ms"`
	SuccP99Ms  float64 `json:"succP99Ms"`
	RPS        float64 `json:"rps"`
	SuccessRPS float64 `json:"successRps"`
	Elapsed    float64 `json:"elapsed"`
}

func buildSummary(samples []sample) summary {
	minTS, maxTS := timeRange(samples)
	elapsed := float64(maxTS - minTS)
	if elapsed == 0 {
		elapsed = 1
	}

	toolDurations := aggregate(samples, "mcp_tool_call_duration")
	toolFails := aggregate(samples, "mcp_tool_call_fail_rate")

	var allDurations []float64
	totalCalls := 0
	for _, b := range toolDurations {
		allDurations = append(allDurations, b.vals...)
		totalCalls += b.count
	}

	failCount := 0
	totalFailSamples := 0
	for _, b := range toolFails {
		for _, v := range b.vals {
			totalFailSamples++
			if v > 0 {
				failCount++
			}
		}
	}

	sort.Float64s(allDurations)
	successCount := totalCalls - failCount
	if successCount < 0 {
		successCount = 0
	}

	s := summary{
		Total:     totalCalls,
		Successes: successCount,
		Fails:     failCount,
		Elapsed:   elapsed,
		RPS:       float64(totalCalls) / elapsed,
	}
	if len(allDurations) > 0 {
		sum := 0.0
		for _, v := range allDurations {
			sum += v
			if v > s.MaxMs {
				s.MaxMs = v
			}
		}
		s.AvgMs = sum / float64(len(allDurations))
		s.P50Ms = percentileSlice(allDurations, 50)
		s.P95Ms = percentileSlice(allDurations, 95)
		s.P99Ms = percentileSlice(allDurations, 99)
		s.SuccAvgMs = s.AvgMs
		s.SuccP95Ms = s.P95Ms
		s.SuccP99Ms = s.P99Ms
	}
	s.SuccessRPS = float64(successCount) / elapsed
	if s.Total > 0 {
		s.Rate = float64(s.Fails) / float64(s.Total) * 100
	}
	return s
}

type resourceSample struct {
	ts       int64
	cpuMC    float64
	rssMB    float64
	vmsMB    float64
	goroutns float64
	threads  float64
}

func readResourceCSV(path string) ([]resourceSample, error) {
	f, err := os.Open(path) //nolint:gosec // path from CLI flag
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	var samples []resourceSample
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(row[0], 10, 64)
		cpu, _ := strconv.ParseFloat(row[1], 64)
		rss, _ := strconv.ParseFloat(row[2], 64)
		vms, _ := strconv.ParseFloat(row[3], 64)
		gor, _ := strconv.ParseFloat(row[4], 64)
		thr, _ := strconv.ParseFloat(row[5], 64)
		samples = append(samples, resourceSample{ts, cpu, rss, vms, gor, thr})
	}
	return samples, nil
}

func buildResourceCharts(resources []resourceSample) ([]byte, []byte) {
	if len(resources) == 0 {
		empty, _ := json.Marshal(chartData{})
		return empty, empty
	}

	var labels []string
	var cpuVals, rssVals, gorVals []float64
	for _, r := range resources {
		t := time.Unix(r.ts, 0)
		labels = append(labels, t.Format("15:04:05"))
		cpuVals = append(cpuVals, r.cpuMC)
		rssVals = append(rssVals, r.rssMB)
		gorVals = append(gorVals, r.goroutns)
	}

	cpuMemChart := chartData{
		Labels: labels,
		Series: [][]float64{cpuVals, rssVals},
		Names:  []string{"CPU (millicores)", "RSS (MB)"},
	}
	gorChart := chartData{
		Labels: labels,
		Series: [][]float64{gorVals},
		Names:  []string{"Goroutines/Threads"},
	}

	cpuJSON, _ := json.Marshal(cpuMemChart)
	gorJSON, _ := json.Marshal(gorChart)
	return cpuJSON, gorJSON
}

func percentileSlice(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// normaliseToElapsed converts absolute timestamps to elapsed seconds from start
func normaliseToElapsed(labels []string, _ int64) []string {
	out := make([]string, len(labels))
	for i := range labels {
		out[i] = fmt.Sprintf("%ds", int64(i))
	}
	return out
}

func buildCharts(samples []sample) (latency, throughput, fail, httpLatency chartData) {
	minTS, maxTS := timeRange(samples)
	labels, avgVals := buildTimeSeries(samples, "mcp_tool_call_duration", "avg", minTS, maxTS)
	_, p95Vals := buildTimeSeries(samples, "mcp_tool_call_duration", "p95", minTS, maxTS)

	_, toolRPS := buildTimeSeries(samples, "mcp_tool_calls", "count", minTS, maxTS)
	_, vus := buildTimeSeries(samples, "vus", "max", minTS, maxTS)

	_, toolFails := buildTimeSeries(samples, "mcp_tool_call_fail_rate", "rate", minTS, maxTS)
	_, httpFails := buildTimeSeries(samples, "http_req_failed", "rate", minTS, maxTS)

	_, httpAvg := buildTimeSeries(samples, "http_req_duration", "avg", minTS, maxTS)
	_, httpP95 := buildTimeSeries(samples, "http_req_duration", "p95", minTS, maxTS)

	elapsed := normaliseToElapsed(labels, minTS)

	latency = chartData{Labels: elapsed, Series: [][]float64{avgVals, p95Vals}, Names: []string{"avg", "p95"}}
	throughput = chartData{Labels: elapsed, Series: [][]float64{toolRPS, vus}, Names: []string{"Tool calls/s", "VUs"}}
	fail = chartData{Labels: elapsed, Series: [][]float64{toolFails, httpFails}, Names: []string{"Tool call fail rate", "HTTP fail rate"}}
	httpLatency = chartData{Labels: elapsed, Series: [][]float64{httpAvg, httpP95}, Names: []string{"HTTP avg", "HTTP p95"}}
	return
}

func mergeBaseline(current, baseline chartData, label string) chartData {
	if len(baseline.Series) == 0 {
		return current
	}
	// add the baseline's first series as a dashed comparison line
	baseVals := baseline.Series[0]
	// pad or truncate to match current length
	if len(baseVals) > len(current.Labels) {
		baseVals = baseVals[:len(current.Labels)]
	}
	for len(baseVals) < len(current.Labels) {
		baseVals = append(baseVals, 0)
	}
	current.Series = append(current.Series, baseVals)
	current.Names = append(current.Names, label)
	return current
}

func buildReport(samples []sample, resources []resourceSample, baselineSamples []sample, title string) string {
	latency, throughput, fail, httpLatency := buildCharts(samples)

	hasBaseline := len(baselineSamples) > 0
	if hasBaseline {
		bLatency, bThroughput, bFail, bHTTPLatency := buildCharts(baselineSamples)
		latency = mergeBaseline(latency, bLatency, "baseline avg")
		throughput = mergeBaseline(throughput, bThroughput, "baseline tool calls/s")
		fail = mergeBaseline(fail, bFail, "baseline fail rate")
		httpLatency = mergeBaseline(httpLatency, bHTTPLatency, "baseline HTTP avg")
	}

	s := buildSummary(samples)
	var bs summary
	if hasBaseline {
		bs = buildSummary(baselineSamples)
	}

	latencyJSON, _ := json.Marshal(latency)
	throughputJSON, _ := json.Marshal(throughput)
	failJSON, _ := json.Marshal(fail)
	httpLatencyJSON, _ := json.Marshal(httpLatency)
	summaryJSON, _ := json.Marshal(s)
	baselineSummaryJSON, _ := json.Marshal(bs)
	cpuMemJSON, gorJSON := buildResourceCharts(resources)

	minTS, _ := timeRange(samples)
	ts := time.Unix(minTS, 0).Format("2006-01-02 15:04:05")

	hasResourcesStr := "false"
	if len(resources) > 0 {
		hasResourcesStr = "true"
	}
	hasBaselineStr := "false"
	if hasBaseline {
		hasBaselineStr = "true"
	}

	replacer := strings.NewReplacer(
		"{{TITLE}}", title,
		"{{TIMESTAMP}}", ts,
		"{{LATENCY_DATA}}", string(latencyJSON),
		"{{THROUGHPUT_DATA}}", string(throughputJSON),
		"{{FAIL_DATA}}", string(failJSON),
		"{{HTTP_LATENCY_DATA}}", string(httpLatencyJSON),
		"{{SUMMARY_DATA}}", string(summaryJSON),
		"{{BASELINE_SUMMARY_DATA}}", string(baselineSummaryJSON),
		"{{CPUMEM_DATA}}", string(cpuMemJSON),
		"{{GOROUTINE_DATA}}", string(gorJSON),
		"{{HAS_RESOURCES}}", hasResourcesStr,
		"{{HAS_BASELINE}}", hasBaselineStr,
	)
	return replacer.Replace(htmlTemplate)
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{TITLE}}</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js"></script>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0d1117; color: #c9d1d9; padding: 24px; }
  h1 { font-size: 20px; font-weight: 600; margin-bottom: 4px; }
  .subtitle { color: #8b949e; font-size: 13px; margin-bottom: 24px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 24px; }
  .card { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 16px; }
  .card h3 { font-size: 13px; color: #8b949e; font-weight: 500; margin-bottom: 12px; text-transform: uppercase; letter-spacing: 0.5px; }
  .stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 24px; }
  .stat { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 14px; }
  .stat .label { font-size: 11px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.5px; }
  .stat .value { font-size: 22px; font-weight: 600; margin-top: 4px; }
  .stat .delta { font-size: 12px; margin-top: 2px; }
  .stat .value.good, .stat .delta.good { color: #3fb950; }
  .stat .value.warn, .stat .delta.warn { color: #d29922; }
  .stat .value.bad, .stat .delta.bad { color: #f85149; }
  .stat .delta.neutral { color: #8b949e; }
  table { width: 100%; border-collapse: collapse; margin-bottom: 24px; font-size: 13px; }
  th, td { padding: 8px 12px; text-align: right; border-bottom: 1px solid #21262d; }
  th { color: #8b949e; font-weight: 500; text-transform: uppercase; font-size: 11px; letter-spacing: 0.5px; }
  td:first-child, th:first-child { text-align: left; }
  td.better { color: #3fb950; }
  td.worse { color: #f85149; }
  canvas { width: 100% !important; }
  @media (max-width: 768px) { .grid { grid-template-columns: 1fr; } .stats { grid-template-columns: repeat(2, 1fr); } }
</style>
</head>
<body>
<h1>{{TITLE}}</h1>
<p class="subtitle">{{TIMESTAMP}}</p>

<div class="stats" id="stats"></div>
<div id="comparison" style="display:none"></div>
<div class="grid">
  <div class="card"><h3>Tool Call Latency (ms)</h3><canvas id="latency"></canvas></div>
  <div class="card"><h3>Throughput</h3><canvas id="throughput"></canvas></div>
  <div class="card"><h3>Failure Rates</h3><canvas id="failures"></canvas></div>
  <div class="card"><h3>MCP vs HTTP Latency (ms)</h3><canvas id="httpLatency"></canvas></div>
</div>
<div class="grid" id="resourceGrid" style="display:none">
  <div class="card"><h3>CPU and Memory</h3><canvas id="cpumem"></canvas></div>
  <div class="card"><h3>Goroutines</h3><canvas id="goroutines"></canvas></div>
</div>

<script>
const latencyData = {{LATENCY_DATA}};
const throughputData = {{THROUGHPUT_DATA}};
const failData = {{FAIL_DATA}};
const httpLatencyData = {{HTTP_LATENCY_DATA}};
const summary = {{SUMMARY_DATA}};
const baseline = {{BASELINE_SUMMARY_DATA}};
const cpumemData = {{CPUMEM_DATA}};
const goroutineData = {{GOROUTINE_DATA}};
const hasResources = {{HAS_RESOURCES}};
const hasBaseline = {{HAS_BASELINE}};

const colors = ['#58a6ff','#f0883e','#f85149','#3fb950','#bc8cff','#d29922'];
const baselineColor = '#484f58';

function cls(v, lo, hi) { return v <= lo ? 'good' : v <= hi ? 'warn' : 'bad'; }

function delta(curr, base, lowerBetter) {
  if (!base) return { text: '', cls: 'neutral' };
  const pct = ((curr - base) / base * 100);
  const sign = pct > 0 ? '+' : '';
  const c = lowerBetter ? (pct < -5 ? 'good' : pct > 5 ? 'bad' : 'neutral') : (pct > 5 ? 'good' : pct < -5 ? 'bad' : 'neutral');
  return { text: sign + pct.toFixed(1) + '%', cls: c };
}

function renderStats() {
  const el = document.getElementById('stats');
  const b = hasBaseline ? baseline : null;
  const items = [
    { label: 'Successful Calls', value: summary.successes.toLocaleString(), c: '', d: delta(summary.successes, b?.successes, false) },
    { label: 'Fail Rate', value: summary.rate.toFixed(2) + '%', c: cls(summary.rate, 0, 1), d: delta(summary.rate, b?.rate, true) },
    { label: 'Success Avg', value: summary.succAvgMs.toFixed(1) + 'ms', c: cls(summary.succAvgMs, 10, 50), d: delta(summary.succAvgMs, b?.succAvgMs, true) },
    { label: 'Success p95', value: summary.succP95Ms.toFixed(1) + 'ms', c: cls(summary.succP95Ms, 20, 100), d: delta(summary.succP95Ms, b?.succP95Ms, true) },
    { label: 'Success p99', value: summary.succP99Ms.toFixed(1) + 'ms', c: cls(summary.succP99Ms, 50, 200), d: delta(summary.succP99Ms, b?.succP99Ms, true) },
    { label: 'Max Latency', value: summary.maxMs.toFixed(1) + 'ms', c: cls(summary.maxMs, 100, 500), d: delta(summary.maxMs, b?.maxMs, true) },
    { label: 'Success Rate', value: summary.successRps.toFixed(1) + '/s', c: '', d: delta(summary.successRps, b?.successRps, false) },
    { label: 'Duration', value: summary.elapsed.toFixed(0) + 's', c: '', d: { text: '', cls: 'neutral' } },
  ];
  el.innerHTML = items.map(i => {
    const deltaHtml = i.d.text ? '<div class="delta ' + i.d.cls + '">vs baseline: ' + i.d.text + '</div>' : '';
    return '<div class="stat"><div class="label">' + i.label + '</div><div class="value ' + i.c + '">' + i.value + '</div>' + deltaHtml + '</div>';
  }).join('');
}

function renderComparison() {
  if (!hasBaseline) return;
  const el = document.getElementById('comparison');
  el.style.display = 'block';
  const rows = [
    ['Fail Rate', summary.rate.toFixed(2) + '%', baseline.rate.toFixed(2) + '%', true],
    ['Successful Calls', summary.successes.toLocaleString(), baseline.successes.toLocaleString(), false],
    ['Success Avg Latency', summary.succAvgMs.toFixed(1) + 'ms', baseline.succAvgMs.toFixed(1) + 'ms', true],
    ['Success p95 Latency', summary.succP95Ms.toFixed(1) + 'ms', baseline.succP95Ms.toFixed(1) + 'ms', true],
    ['Success p99 Latency', summary.succP99Ms.toFixed(1) + 'ms', baseline.succP99Ms.toFixed(1) + 'ms', true],
    ['Success Throughput', summary.successRps.toFixed(1) + '/s', baseline.successRps.toFixed(1) + '/s', false],
    ['Total Throughput', summary.rps.toFixed(1) + '/s', baseline.rps.toFixed(1) + '/s', false],
  ];
  let html = '<div class="card"><h3>COMPARISON: CURRENT vs BASELINE</h3><table><tr><th>Metric</th><th>Current</th><th>Baseline</th><th>Change</th></tr>';
  rows.forEach(([name, curr, base, lowerBetter]) => {
    const cv = parseFloat(curr.replace(/,/g, '')), bv = parseFloat(base.replace(/,/g, ''));
    const pct = bv ? ((cv - bv) / bv * 100) : 0;
    const sign = pct > 0 ? '+' : '';
    const cls = lowerBetter ? (pct < -5 ? 'better' : pct > 5 ? 'worse' : '') : (pct > 5 ? 'better' : pct < -5 ? 'worse' : '');
    html += '<tr><td>' + name + '</td><td>' + curr + '</td><td>' + base + '</td><td class="' + cls + '">' + sign + pct.toFixed(1) + '%</td></tr>';
  });
  html += '</table></div>';
  el.innerHTML = html;
}

function makeChart(id, data, opts) {
  const ctx = document.getElementById(id);
  new Chart(ctx, {
    type: 'line',
    data: {
      labels: data.labels,
      datasets: data.series.map((s, i) => {
        const isBaseline = hasBaseline && data.names[i].startsWith('baseline');
        return {
          label: data.names[i],
          data: s,
          borderColor: isBaseline ? baselineColor : colors[i % colors.length],
          backgroundColor: 'transparent',
          borderWidth: isBaseline ? 1 : (i === 0 ? 2 : 1.5),
          borderDash: isBaseline ? [6, 3] : [],
          pointRadius: 0,
          tension: 0.3,
          yAxisID: opts && opts.dualAxis && i === data.series.length - 1 ? 'y1' : 'y',
        };
      }),
    },
    options: {
      responsive: true,
      interaction: { intersect: false, mode: 'index' },
      plugins: { legend: { labels: { color: '#8b949e', font: { size: 11 } } } },
      scales: {
        x: { ticks: { color: '#484f58', maxRotation: 0, autoSkip: true, maxTicksLimit: 15 }, grid: { color: '#21262d' }, title: { display: true, text: 'elapsed', color: '#484f58' } },
        y: { ticks: { color: '#484f58' }, grid: { color: '#21262d' }, title: { display: !!opts?.yLabel, text: opts?.yLabel || '', color: '#8b949e' } },
        ...(opts && opts.dualAxis ? { y1: { position: 'right', ticks: { color: '#484f58' }, grid: { drawOnChartArea: false }, title: { display: true, text: opts.y1Label || '', color: '#8b949e' } } } : {}),
      },
    },
  });
}

renderStats();
renderComparison();
makeChart('latency', latencyData, { yLabel: 'ms' });
makeChart('throughput', throughputData, { yLabel: 'req/s', dualAxis: true, y1Label: 'VUs' });
makeChart('failures', failData, { yLabel: 'rate' });
makeChart('httpLatency', httpLatencyData, { yLabel: 'ms' });

if (hasResources && cpumemData.labels && cpumemData.labels.length > 0) {
  document.getElementById('resourceGrid').style.display = 'grid';
  makeChart('cpumem', cpumemData, { yLabel: 'millicores', dualAxis: true, y1Label: 'MB' });
  makeChart('goroutines', goroutineData, { yLabel: 'count' });
}
</script>
</body>
</html>`
