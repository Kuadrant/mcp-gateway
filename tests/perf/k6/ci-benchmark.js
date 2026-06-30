// CI benchmark scenario for the MCP gateway.
// Designed to run as an in-cluster Kubernetes Job and produce stable,
// comparable metrics suitable for regression tracking via benchmark-action.
//
// Key differences from the local development scenarios:
//   - Short fixed duration (30 s) and low VU count suited to CI runners
//   - Targets the gateway via in-cluster DNS (configurable via TARGET_URL)
//   - Writes a --summary-export JSON used by the convert-k6-to-benchmark.sh
//     converter before being fed to benchmark-action
//
// Usage (in-cluster Job):
//   k6 run /scenarios/ci-benchmark.js \
//       --summary-export /results/summary.json
//
// Usage (local, against a running Kind cluster):
//   TARGET_URL=http://localhost:8001/mcp PREFIX=mock_ \
//     ./bin/k6 run tests/perf/k6/ci-benchmark.js \
//       --summary-export out/ci-summary.json

import mcp from 'k6/x/infobip_mcp';
import { check, sleep } from 'k6';
import { Counter, Trend, Rate } from 'k6/metrics';

// ─── configuration ────────────────────────────────────────────────────────────
// Default URL targets the Istio gateway service via in-cluster DNS.
// Override with TARGET_URL env var when running locally.
const TARGET_URL = __ENV.TARGET_URL
    || 'http://mcp-gateway-istio.gateway-system.svc.cluster.local:8080/mcp';
const PREFIX     = __ENV.PREFIX   || 'mock_';
const VUS        = parseInt(__ENV.VUS      || '10');
const DURATION   = __ENV.DURATION || '30s';
const TIMEOUT    = parseInt(__ENV.TIMEOUT  || '10');

// ─── custom metrics ───────────────────────────────────────────────────────────
const mcpSessionsOpened   = new Counter('mcp_sessions_opened');
const mcpSessionOpenFail  = new Rate('mcp_session_open_fail');
const mcpSessionDuration  = new Trend('mcp_session_duration', true);

const mcpToolCalls        = new Counter('mcp_tool_calls');
const mcpToolCallDuration = new Trend('mcp_tool_call_duration', true);
const mcpToolCallFails    = new Counter('mcp_tool_call_fails');
const mcpToolCallFailRate = new Rate('mcp_tool_call_fail_rate');

// ─── scenario options ─────────────────────────────────────────────────────────
export const options = {
    scenarios: {
        ci_steady: {
            executor: 'constant-vus',
            vus:      VUS,
            duration: DURATION,
        },
    },
    thresholds: {
        // CI gates: fail the Job (and therefore the workflow) if these are breached
        mcp_tool_call_fail_rate: ['rate<0.01'],          // < 1 % errors
        mcp_tool_call_duration:  ['p(95)<200'],           // p95 < 200 ms
        mcp_session_open_fail:   ['rate<0.01'],           // < 1 % session failures
    },
};

// Tools exposed by the perf-mock-server (10 zero-latency tools)
const TOOLS = [
    'alpha', 'bravo', 'charlie', 'delta', 'echo',
    'foxtrot', 'golf', 'hotel', 'india', 'juliet',
];

// ─── default function (one VU iteration) ─────────────────────────────────────
export default function () {
    const sessionStart = Date.now();

    // Open an MCP session
    let client;
    try {
        client = mcp.NewClient({
            endpoint: TARGET_URL,
            timeout:  TIMEOUT,
            isSSE:    false,
        });
        mcpSessionsOpened.add(1);
        mcpSessionOpenFail.add(false);
    } catch (e) {
        mcpSessionOpenFail.add(true);
        sleep(1);
        return;
    }

    // Exercise a fixed set of tool calls per iteration (5 calls, ~0.5 s each)
    try {
        for (let i = 0; i < 5; i++) {
            const toolName = `${PREFIX}${TOOLS[i % TOOLS.length]}`;
            const start    = Date.now();
            try {
                const result  = client.callTool(toolName, { input: 'bench' });
                const elapsed = Date.now() - start;
                mcpToolCalls.add(1);
                mcpToolCallDuration.add(elapsed);
                const ok = result.length > 0;
                mcpToolCallFailRate.add(!ok);
                if (!ok) {
                    mcpToolCallFails.add(1);
                }
                check(result, { 'tool call returned data': (r) => r.length > 0 });
            } catch (e) {
                mcpToolCalls.add(1);
                mcpToolCallFailRate.add(true);
                mcpToolCallFails.add(1);
                mcpToolCallDuration.add(Date.now() - start);
            }
            sleep(0.1 + Math.random() * 0.2);
        }
    } finally {
        mcpSessionDuration.add(Date.now() - sessionStart);
        client.closeConnection();
    }
}
