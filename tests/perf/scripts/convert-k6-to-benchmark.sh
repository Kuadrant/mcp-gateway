#!/usr/bin/env bash
# convert-k6-to-benchmark.sh
#
# Converts a k6 --summary-export JSON file into the customSmallerIsBetter
# format expected by benchmark-action/github-action-benchmark.
#
# Usage:
#   ./tests/perf/scripts/convert-k6-to-benchmark.sh <summary.json> > benchmark-results.json
#
# Input format (k6 --summary-export):
#   {
#     "metrics": {
#       "http_req_duration": { "values": { "avg": 20.1, "p(95)": 45.2, "p(99)": 98.3 } },
#       "mcp_tool_call_duration": { "values": { "avg": 18.4, "p(95)": 42.1, "p(99)": 95.0 } },
#       "mcp_tool_call_fail_rate": { "values": { "rate": 0.001 } },
#       "mcp_session_open_fail":   { "values": { "rate": 0.000 } },
#       ...
#     }
#   }
#
# Output format (customSmallerIsBetter):
#   [
#     { "name": "p95_tool_call_ms",  "unit": "ms",      "value": 42.1 },
#     { "name": "p99_tool_call_ms",  "unit": "ms",      "value": 95.0 },
#     { "name": "avg_tool_call_ms",  "unit": "ms",      "value": 18.4 },
#     { "name": "tool_error_rate",   "unit": "percent", "value": 0.1  },
#     { "name": "session_fail_rate", "unit": "percent", "value": 0.0  }
#   ]

set -euo pipefail

SUMMARY_FILE="${1:-}"

if [[ -z "$SUMMARY_FILE" ]]; then
    echo "Usage: $0 <k6-summary.json>" >&2
    exit 1
fi

if [[ ! -f "$SUMMARY_FILE" ]]; then
    echo "Error: file not found: $SUMMARY_FILE" >&2
    exit 1
fi

if ! command -v jq &>/dev/null; then
    echo "Error: jq is required but not installed." >&2
    exit 1
fi

jq '
[
  # MCP tool call p95 latency (primary regression signal)
  {
    name:  "p95_tool_call_ms",
    unit:  "ms",
    value: (.metrics.mcp_tool_call_duration.values["p(95)"] // 0)
  },
  # MCP tool call p99 latency
  {
    name:  "p99_tool_call_ms",
    unit:  "ms",
    value: (.metrics.mcp_tool_call_duration.values["p(99)"] // 0)
  },
  # MCP tool call average latency
  {
    name:  "avg_tool_call_ms",
    unit:  "ms",
    value: (.metrics.mcp_tool_call_duration.values.avg // 0)
  },
  # MCP tool call error rate (as a percentage)
  {
    name:  "tool_error_rate",
    unit:  "percent",
    value: ((.metrics.mcp_tool_call_fail_rate.values.rate // 0) * 100)
  },
  # MCP session open failure rate (as a percentage)
  {
    name:  "session_fail_rate",
    unit:  "percent",
    value: ((.metrics.mcp_session_open_fail.values.rate // 0) * 100)
  }
]
' "$SUMMARY_FILE"
