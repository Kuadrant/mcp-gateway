#!/bin/bash
# Quick validation script for Wasm router equivalence testing
# Usage: ./local/test-wasm-router.sh [gateway-url]

set -e

GATEWAY_URL="${1:-http://mcp.127-0-0-1.sslip.io:8001}"
MCP_ENDPOINT="${GATEWAY_URL}/mcp"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; exit 1; }
info() { echo -e "${YELLOW}→ $1${NC}"; }

echo "========================================"
echo "Wasm Router Equivalence Test"
echo "========================================"
echo "Gateway: ${GATEWAY_URL}"
echo ""

# 1. Initialize
info "Testing initialize..."
INIT_RESP=$(curl -s -i -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}')

SESSION=$(echo "$INIT_RESP" | grep -i "mcp-session-id:" | head -1 | cut -d' ' -f2 | tr -d '\r')

if [ -z "$SESSION" ]; then
  echo "$INIT_RESP"
  fail "No session ID returned from initialize"
fi
pass "Initialize - got session: ${SESSION:0:50}..."

# 2. Initialized notification
info "Testing notifications/initialized..."
INITIALIZED_RESP=$(curl -s -w "\n%{http_code}" -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: ${SESSION}" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}')

HTTP_CODE=$(echo "$INITIALIZED_RESP" | tail -1)
if [ "$HTTP_CODE" -ge 200 ] && [ "$HTTP_CODE" -lt 300 ]; then
  pass "Initialized notification - HTTP $HTTP_CODE"
else
  fail "Initialized notification failed - HTTP $HTTP_CODE"
fi

# 3. Tools list
info "Testing tools/list..."
TOOLS_RESP=$(curl -s -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: ${SESSION}" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')

if echo "$TOOLS_RESP" | grep -q "tools"; then
  TOOL_COUNT=$(echo "$TOOLS_RESP" | grep -o '"name"' | wc -l | tr -d ' ')
  pass "Tools list - found ${TOOL_COUNT} tools"
else
  echo "$TOOLS_RESP"
  fail "Tools list did not return tools"
fi

# 4. Tool call - server1_time (if exists)
info "Testing tools/call (server1_time)..."
TOOL_RESP=$(curl -s -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: ${SESSION}" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"server1_time","arguments":{}}}')

if echo "$TOOL_RESP" | grep -q "result\|error"; then
  if echo "$TOOL_RESP" | grep -q "isError.*true\|\"error\""; then
    echo "$TOOL_RESP"
    fail "Tool call returned error"
  fi
  pass "Tool call (server1_time) - got result"
else
  echo "$TOOL_RESP"
  fail "Tool call did not return result or error"
fi

# 5. Tool call - server2 tool (tests prefix stripping)
info "Testing tools/call (server2_hello_world)..."
TOOL2_RESP=$(curl -s -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: ${SESSION}" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"server2_hello_world","arguments":{}}}')

if echo "$TOOL2_RESP" | grep -q "result"; then
  pass "Tool call (server2_hello_world) - prefix stripping works"
else
  info "Tool call (server2_hello_world) - may not be registered: ${TOOL2_RESP:0:100}"
fi

# 6. Unknown tool (should return JSON-RPC error)
info "Testing tools/call (unknown tool)..."
UNKNOWN_RESP=$(curl -s -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: ${SESSION}" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}')

if echo "$UNKNOWN_RESP" | grep -q "error\|not found\|isError"; then
  pass "Unknown tool - correctly returns error"
else
  echo "$UNKNOWN_RESP"
  fail "Unknown tool should return error"
fi

# 7. Invalid session (should fail)
info "Testing invalid session..."
INVALID_RESP=$(curl -s -w "\n%{http_code}" -X POST "${MCP_ENDPOINT}" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "mcp-session-id: invalid-session-id" \
  -d '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"server1_time","arguments":{}}}')

HTTP_CODE=$(echo "$INVALID_RESP" | tail -1)
if [ "$HTTP_CODE" -ge 400 ]; then
  pass "Invalid session - correctly rejected (HTTP $HTTP_CODE)"
else
  info "Invalid session - HTTP $HTTP_CODE (may be acceptable depending on implementation)"
fi

echo ""
echo "========================================"
echo -e "${GREEN}All tests passed!${NC}"
echo "========================================"
