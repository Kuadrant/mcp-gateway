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
| `cmd/report/` | HTML report generator with comparison support |
| `mock-server/` | 10-tool zero-latency MCP server for isolating gateway overhead |
| `manifests/` | Kind deployment for mock server + MCPServerRegistration |
| `scripts/collect-resources.sh` | polls broker CPU/memory/goroutines during tests |
| `scripts/capture-profiles.sh` | captures pprof snapshots at intervals |

## Makefile targets

| Target | Description |
|-|-|
| `perf-setup` | deploy mock server and pprof service into Kind |
| `perf-teardown` | remove perf test resources |
| `perf-build-k6` | build k6 binary with xk6-infobip-mcp |
| `perf-run-steady` | run steady-state concurrency test |
| `perf-run-ramp` | run ramp-up test with profiling |
| `perf-profile` | capture a one-off pprof snapshot |
