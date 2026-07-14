# E2E Tests

## Functional Suites

Specs use Ginkgo `Label()` for suite membership. See [docs/ci.md](../../docs/ci.md) for the full suite reference, Make targets, CI commands, and labelling instructions.

## Test Patterns
- Tests use broker `/status` endpoint for reliable server registration checks (not log parsing)
- Port-forwards target deployments directly: `deployment/mcp-gateway`
- Tests clean up existing resources before creating to avoid conflicts
- Structured JSON responses provide better debugging when tests fail
- Test servers live in `tests/servers/` -- create new ones for specific scenarios
- Test server images are built and pushed in `.github/workflows/test-images.yaml`

## Conformance Tests

MCP conformance tests verify the gateway correctly implements the Model Context Protocol specification. Tests are sourced from the official `@modelcontextprotocol/conformance` npm package maintained by Anthropic.

**Test scenarios currently run in CI** (`.github/workflows/conformance.yaml`):
- `server-initialize`: Server initialisation handshake
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

## Parallel test isolation via dedicated listeners

Tests that register MCPServerRegistrations on the shared `mcp-gateway` gateway interfere with
each other when run in parallel (config reloads, session invalidation, tool list changes).

To isolate a test suite for parallel execution:

1. **Add a wildcard HTTP listener** to `config/istio/gateway/gateway.yaml` on port 8080:
   ```yaml
   - name: my-test-suite
     hostname: '*.my-test-suite.127-0-0-1.sslip.io'
     port: 8080
     protocol: HTTP
     allowedRoutes:
       namespaces:
         from: All
   ```

2. **Create a dedicated namespace and MCPGatewayExtension** in `BeforeAll` targeting that listener
   with `WithSectionName("my-test-suite")`.

3. **All MCPServerRegistrations must set both**:
   - `WithSectionName("my-test-suite")` -- so the HTTPRoute parentRef targets the correct listener
   - `WithHostname("server.my-test-suite.127-0-0-1.sslip.io")` -- so the HTTPRoute hostname
     matches the listener's wildcard pattern; the gateway rejects routes whose hostname doesn't
     match the listener

4. **Gateway URL** uses the same wildcard domain on the Kind nodeport:
   `http://mcp.my-test-suite.127-0-0-1.sslip.io:8001/mcp` (port 8001 maps to gateway port 8080
   via nodeport 30080).

Both the sectionName and hostname are required -- the controller uses `httpRouteAttachesToListener`
which checks sectionName against listeners on the same port, and the gateway itself only accepts
HTTPRoutes whose hostname matches the listener's hostname pattern. Missing either causes "no valid
gateways for httproute" or "no valid mcpgatewayextensions configured" errors.

See `tool_discovery_test.go` for a working example of this pattern.

## Parallel safety rules

Tests run with `--procs` (multiple Ginkgo processes). Every new or modified test must follow these
rules to avoid cross-test interference.

### Mark tests `Serial` when they mutate shared infrastructure

Any test that does one of the following MUST have the `Serial` decorator on the `It`:

- Scales, restarts, or patches the shared `mcp-gateway` deployment
- Scales, restarts, or patches a shared test server deployment (e.g. `mcp-test-server3`)
- Modifies the shared `MCPGatewayExtension` (e.g. `SetupTrustedHeadersAuth`, session store changes)
- Disables or re-enables an `MCPServerRegistration` that other tests depend on
- Creates an `MCPGatewayExtension` in a namespace that already has one

### Never use shared bools for async notification callbacks

Notification handlers run in a separate goroutine. Reading a `bool` in `Eventually` while a
callback writes it is a data race (fails under `-race`). Use a buffered channel and
`Eventually(ch).Should(Receive())` instead. See `happy_path_test.go` notification tests for the
pattern.

### Use unique prefixes and per-test cleanup

- Every `MCPServerRegistration` must use a unique prefix (e.g. `WithPrefix("mytest_")`)
- Assertions must check only the test's own prefix via `WaitForToolsWithPrefix` or
  `verifyMCPServerRegistrationToolsPresent` -- never assert exact tool counts on the shared gateway
- Clean up resources per-test: append created objects to the container's `testResources` slice, deleted in `AfterEach` -- not per-suite

### Make helper functions idempotent

Helpers that patch deployments (add volumes, flags, listeners) must check whether the patch has
already been applied before re-applying. A crashed prior run may leave state behind. See
`PatchBrokerCA` and `AddGatewayHTTPSListener` for the pattern.

### Prefer isolated listeners over the shared gateway

Tests that would conflict with parallel registrations on the shared gateway should use the
dedicated listener pattern (see "Parallel test isolation via dedicated listeners" above).
Only register on the shared gateway when testing shared-gateway-specific behaviour.

## Useful Test Servers

Server1 and Server2 both offer tools for inspecting headers, which is useful for validating what was passed through to the backend MCP.

## Known Issues: Flaky E2E Tests
**Problem**: Tests timeout waiting for broker to register servers due to:
- ConfigMap volume mount sync delays (60-120s in Kubernetes)
- Log-based checks becoming unreliable

**Solution**: Use broker `/status` API endpoint instead of log parsing for all server state checks.
