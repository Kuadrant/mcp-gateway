# Performance Testing

Local test harness for reproducing and investigating broker-router scalability under concurrent MCP sessions. Uses [k6](https://k6.io/) with [xk6-infobip-mcp](https://github.com/infobip/xk6-infobip-mcp) for MCP protocol support.

## Prerequisites

A Kind cluster with the gateway running:

```bash
make local-env-setup
make reload
```

## Setup

```bash
make perf-setup      # deploy mock server + MCPServerRegistration + pprof service
make perf-build-k6   # build k6 with MCP extension (one-time)
```

## Running tests

```bash
# steady-state at fixed concurrency
make perf-run-steady PERF_USERS=64 PERF_DURATION=5m

# ramp-up to find failure point (captures pprof profiles + resource usage)
make perf-run-ramp PERF_MAX_USERS=4096 PERF_RAMP_RATE=8 PERF_HOLD_DURATION=2m
```

Results are written to `out/perf/<timestamp>/` with an HTML report, CSV metrics, broker logs, and pprof profiles.

## Comparing runs

```bash
go run ./tests/perf/cmd/report \
  -csv out/perf/<new-run>/k6-ramp.csv \
  -baseline out/perf/<old-run>/k6-ramp.csv \
  -resources out/perf/<new-run>/resources.csv \
  -title "description" \
  -out comparison.html
```

Generates a comparison report with baseline overlay on charts and a delta table.

## Profiling

The broker exposes pprof on port 6060. The Makefile targets use `kubectl port-forward` to access it.

```bash
# one-off snapshot
make perf-profile

# interactive analysis
go tool pprof -http :9090 out/perf/<run>/profiles/snap-3-*-cpu.pb.gz
```

The `perf-run-ramp` target captures interval snapshots (goroutine, heap, CPU) every 90 seconds during the test, so you get profiles at different concurrency levels.

## Components

| Path | Purpose |
|-|-|
| `k6/concurrency-levels.js` | steady-state test at fixed VU count |
| `k6/ramp-up.js` | ramp from 0 to N users to find failure point |
| `k6/ci-benchmark.js` | short CI-optimised scenario for regression tracking |
| `cmd/report/` | HTML report generator with comparison support |
| `mock-server/` | 10-tool zero-latency MCP server for isolating gateway overhead |
| `manifests/` | Kind deployment for mock server + MCPServerRegistration |
| `manifests/k6-benchmark-job.yaml` | Kubernetes Job that runs k6 in-cluster |
| `scripts/collect-resources.sh` | polls broker CPU/memory/goroutines during tests |
| `scripts/capture-profiles.sh` | captures pprof snapshots at intervals |
| `scripts/convert-k6-to-benchmark.sh` | converts k6 summary JSON to benchmark-action format |

## Makefile targets

| Target | Description |
|-|-|
| `perf-setup` | deploy mock server and pprof service into Kind |
| `perf-teardown` | remove perf test resources |
| `perf-build-k6` | build k6 binary with xk6-infobip-mcp |
| `perf-run-steady` | run steady-state concurrency test |
| `perf-run-ramp` | run ramp-up test with profiling |
| `perf-profile` | capture a one-off pprof snapshot |
| `perf-build-k6-image` | build the `k6-mcp` Docker image |
| `perf-push-k6-image` | push the `k6-mcp` image to ghcr.io |
| `perf-ci-setup` | load images + deploy mock server + create k6 ConfigMap |
| `perf-ci-run` | apply Job, wait, extract results, convert to benchmark format |
| `perf-ci-clean` | remove CI benchmark Job and ConfigMap |

---

## CI Benchmarking (Issue #305 / Sub-issue #807)

The CI benchmark suite tracks gateway performance from release to release using a
k6 load generator deployed as a Kubernetes Job **inside** the cluster. Running
k6 in-cluster eliminates external network jitter and gives accurate baseline
metrics for regression detection.

### How it works

```
GitHub Actions
  └─ kind create cluster
  └─ make ci-setup         # Istio, gateway, controller, test servers
  └─ make perf-ci-setup    # loads k6-mcp image + deploys mock server + ConfigMap
  └─ kubectl apply k6-benchmark-job.yaml
  └─ kubectl wait --for=condition=complete job/mcp-gateway-benchmark
  └─ kubectl cp <pod>:/results/summary.json
  └─ convert-k6-to-benchmark.sh → benchmark-results.json
  └─ benchmark-action       # stores history + comments on PR + blocks on regression
```

### Triggering the workflow

**Manually** (from the Actions tab — fulfils sub-issue #807):
1. Navigate to **Actions → Performance Benchmark**.
2. Click **Run workflow** and select the branch.
3. The workflow provisions a fresh Kind cluster and posts results as a PR comment.

**Automatically**: the workflow runs on every PR that touches `internal/`,
`cmd/`, or `tests/perf/`.

### Regression gating

`benchmark-action` stores historical metrics on the `gh-pages` branch and
compares each run against the stored baseline. If any metric degrades by more
than **30 %** (threshold: `130%`), the workflow fails and blocks the PR from
merging. A summary comment is posted on every run showing the trend.

Metrics tracked (all smaller-is-better):

| Metric | Description |
|-|-|
| `p95_tool_call_ms` | 95th-percentile MCP tool-call latency |
| `p99_tool_call_ms` | 99th-percentile MCP tool-call latency |
| `avg_tool_call_ms` | average MCP tool-call latency |
| `tool_error_rate` | percentage of failed tool calls |
| `session_fail_rate` | percentage of failed MCP session opens |

### Running the CI benchmark locally

Requires a running Kind cluster with the gateway deployed:

```bash
# One-time: build the k6-mcp image (already done if using the published image)
make perf-build-k6-image

# Setup: load images + deploy mock server + create ConfigMap
make ci-setup
make perf-ci-setup

# Run: apply Job, wait, extract, and convert results
make perf-ci-run
# Results written to out/perf/benchmark-results.json

# Cleanup
make perf-ci-clean
```

### Pre-requisites for CI

* The `k6-mcp` image must be published to
  `ghcr.io/kuadrant/mcp-gateway/k6-mcp:latest` before the first CI run:
  ```bash
  make perf-build-k6-image
  make perf-push-k6-image
  ```
* GitHub Pages must be enabled on the repository (`gh-pages` branch) for
  `benchmark-action` to persist historical data.
