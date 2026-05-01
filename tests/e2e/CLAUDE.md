# E2E Tests

## E2E Test Reliability
- Tests use broker `/status` endpoint for reliable server registration checks (not log parsing)
- Port-forwards target deployments directly: `deployment/mcp-gateway`
- Tests clean up existing resources before creating to avoid conflicts
- Structured JSON responses provide better debugging when tests fail

## Conformance Tests
MCP conformance tests verify that the gateway correctly implements the Model Context Protocol specification. These tests are sourced from the official `@modelcontextprotocol/conformance` npm package maintained by Anthropic.

## Useful test servers for inspecting responses

Server1 and Server2 both offer tools for inspecting headers, which is useful for validating what was passed through to the backend MCP.

**Test scenarios currently run in CI** (`.github/workflows/conformance.yaml`):
- `server-initialize`: Server initialization handshake
- `tools-list`: Tool listing and discovery
- `tools-call-simple-text`: Simple text tool responses
- `tools-call-image`: Image content in tool responses
- `tools-call-audio`: Audio content in tool responses
- `tools-call-embedded-resource`: Embedded resource handling
- `tools-call-mixed-content`: Mixed content type responses
- `tools-call-error`: Error handling and propagation
- `tools-call-with-progress`: Progress notification support

**Running conformance tests locally**:
```bash
make deploy-conformance-server  # Deploy test server to Kind cluster

# Run specific scenario
npx @modelcontextprotocol/conformance server \
  --url http://mcp.127-0-0-1.sslip.io:8001/mcp \
  --scenario server-initialize

# Run all active scenarios
npx @modelcontextprotocol/conformance server \
  --url http://mcp.127-0-0-1.sslip.io:8001/mcp
```

**Updating CI test scenarios**:
1. Check available scenarios: `npx @modelcontextprotocol/conformance list`
2. Add new scenario blocks to `.github/workflows/conformance.yaml` under the "Run MCP conformance tests" step
3. Each scenario runs as a separate `npx @modelcontextprotocol/conformance server --url ... --scenario <name>` command

## Known Issues: Flaky E2E Tests
**Problem**: Tests timeout waiting for broker to register servers due to:
- ConfigMap volume mount sync delays (60-120s in Kubernetes)
- Log-based checks becoming unreliable

**Solution**: Use broker `/status` API endpoint instead of log parsing for all server state checks.
