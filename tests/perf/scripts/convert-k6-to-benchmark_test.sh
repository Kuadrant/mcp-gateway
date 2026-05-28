#!/usr/bin/env bash
# tests for convert-k6-to-benchmark.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONVERT="${SCRIPT_DIR}/convert-k6-to-benchmark.sh"
TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_TEST"' EXIT

pass=0
fail=0

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    # numeric comparison: strip trailing zeros for consistent matching
    local e a
    e="$(echo "$expected" | awk '{printf "%g", $0}')"
    a="$(echo "$actual" | awk '{printf "%g", $0}')"
    if [[ "$e" == "$a" ]]; then
        echo "  PASS: $desc"
        pass=$((pass + 1))
    else
        echo "  FAIL: $desc"
        echo "    expected: $expected ($e)"
        echo "    actual:   $actual ($a)"
        fail=$((fail + 1))
    fi
}

# ── test: correct values extraction ──────────────────────────────────────────

echo "=== correct values extraction ==="

cat > "$TMPDIR_TEST/summary.json" <<'EOF'
{
  "metrics": {
    "mcp_tool_call_duration": {
      "type": "trend",
      "contains": "time",
      "values": { "avg": 18.4, "p(95)": 42.1, "p(99)": 95.0 }
    },
    "mcp_tool_call_fail_rate": {
      "type": "rate",
      "contains": "default",
      "values": { "rate": 0.001, "passes": 999, "fails": 1 }
    },
    "mcp_session_open_fail": {
      "type": "rate",
      "contains": "default",
      "values": { "rate": 0.0, "passes": 100, "fails": 0 }
    }
  }
}
EOF

out="$("$CONVERT" "$TMPDIR_TEST/summary.json")"

p95="$(echo "$out" | jq -r '.[] | select(.name == "p95_tool_call_ms") | .value')"
assert_eq "p95 value" "42.1" "$p95"

p99="$(echo "$out" | jq -r '.[] | select(.name == "p99_tool_call_ms") | .value')"
assert_eq "p99 value" "95" "$p99"

avg="$(echo "$out" | jq -r '.[] | select(.name == "avg_tool_call_ms") | .value')"
assert_eq "avg value" "18.4" "$avg"

err="$(echo "$out" | jq -r '.[] | select(.name == "tool_error_rate") | .value')"
assert_eq "error rate (0.001 * 100)" "0.1" "$err"

sess="$(echo "$out" | jq -r '.[] | select(.name == "session_fail_rate") | .value')"
assert_eq "session fail rate" "0" "$sess"

count="$(echo "$out" | jq 'length')"
assert_eq "entry count" "5" "$count"

unit="$(echo "$out" | jq -r '.[] | select(.name == "p95_tool_call_ms") | .unit')"
assert_eq "p95 unit" "ms" "$unit"

# ── test: missing metrics default to zero ────────────────────────────────────

echo "=== missing metrics default to zero ==="

cat > "$TMPDIR_TEST/empty.json" <<'EOF'
{ "metrics": {} }
EOF

out="$("$CONVERT" "$TMPDIR_TEST/empty.json")"

p95="$(echo "$out" | jq -r '.[] | select(.name == "p95_tool_call_ms") | .value')"
assert_eq "missing p95 defaults to 0" "0" "$p95"

avg="$(echo "$out" | jq -r '.[] | select(.name == "avg_tool_call_ms") | .value')"
assert_eq "missing avg defaults to 0" "0" "$avg"

err="$(echo "$out" | jq -r '.[] | select(.name == "tool_error_rate") | .value')"
assert_eq "missing error rate defaults to 0" "0" "$err"

# ── test: zero values ────────────────────────────────────────────────────────

echo "=== zero values ==="

cat > "$TMPDIR_TEST/zeros.json" <<'EOF'
{
  "metrics": {
    "mcp_tool_call_duration": {
      "type": "trend",
      "contains": "time",
      "values": { "avg": 0, "p(95)": 0, "p(99)": 0 }
    },
    "mcp_tool_call_fail_rate": {
      "type": "rate",
      "contains": "default",
      "values": { "rate": 0 }
    },
    "mcp_session_open_fail": {
      "type": "rate",
      "contains": "default",
      "values": { "rate": 0 }
    }
  }
}
EOF

out="$("$CONVERT" "$TMPDIR_TEST/zeros.json")"

p95="$(echo "$out" | jq -r '.[] | select(.name == "p95_tool_call_ms") | .value')"
assert_eq "zero p95" "0" "$p95"

avg="$(echo "$out" | jq -r '.[] | select(.name == "avg_tool_call_ms") | .value')"
assert_eq "zero avg" "0" "$avg"

# ── test: missing file exits non-zero ────────────────────────────────────────

echo "=== error handling ==="

if "$CONVERT" "$TMPDIR_TEST/nonexistent.json" >/dev/null 2>&1; then
    echo "  FAIL: should exit non-zero for missing file"
    ((fail++))
else
    echo "  PASS: exits non-zero for missing file"
    ((pass++))
fi

if "$CONVERT" >/dev/null 2>&1; then
    echo "  FAIL: should exit non-zero with no args"
    ((fail++))
else
    echo "  PASS: exits non-zero with no args"
    ((pass++))
fi

# ── summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== $pass passed, $fail failed ==="
exit "$fail"
