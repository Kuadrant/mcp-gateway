// steady-state concurrency test for mcp gateway overhead measurement.
// matches the PSAP "concurrency level test" methodology:
// all VUs spawn at once, run for a fixed duration, measure latency overhead.
//
// usage:
//   TARGET_URL=http://localhost:8001/mcp TOOL_PREFIX=mock_ USERS=512 DURATION=5m \
//     ./bin/k6 run tests/perf/k6/concurrency-levels.js

import mcp from 'k6/x/infobip_mcp';
import { check, sleep } from 'k6';
import { Counter, Trend, Rate, Gauge } from 'k6/metrics';

const TARGET_URL = __ENV.TARGET_URL || 'http://localhost:8001/mcp';
const TOOL_PREFIX = __ENV.TOOL_PREFIX || 'mock_';
const USERS = parseInt(__ENV.USERS || '64');
const DURATION = __ENV.DURATION || '5m';
const TIMEOUT = parseInt(__ENV.TIMEOUT || '10');

// mcp session metrics
const mcpSessionsOpened = new Counter('mcp_sessions_opened');
const mcpSessionOpenFail = new Rate('mcp_session_open_fail');
const mcpSessionDuration = new Trend('mcp_session_duration', true);

// mcp tool call metrics
const mcpToolCalls = new Counter('mcp_tool_calls');
const mcpToolCallDuration = new Trend('mcp_tool_call_duration', true);
const mcpToolCallFails = new Counter('mcp_tool_call_fails');
const mcpToolCallFailRate = new Rate('mcp_tool_call_fail_rate');
const mcpToolCallRate = new Rate('mcp_tool_call_success_rate');

// per-tool breakdown
const mcpToolDurationByName = {};

export const options = {
    scenarios: {
        steady_state: {
            executor: 'constant-vus',
            vus: USERS,
            duration: DURATION,
        },
    },
    thresholds: {
        mcp_tool_call_fail_rate: ['rate<0.01'],
        mcp_tool_call_duration: ['p(95)<50'],
        mcp_session_open_fail: ['rate<0.01'],
    },
};

const TOOLS = [
    'alpha', 'bravo', 'charlie', 'delta', 'echo',
    'foxtrot', 'golf', 'hotel', 'india', 'juliet',
];

export default function () {
    const sessionStart = Date.now();

    let client;
    try {
        client = mcp.NewClient({
            endpoint: TARGET_URL,
            timeout: TIMEOUT,
            isSSE: false,
        });
        mcpSessionsOpened.add(1);
        mcpSessionOpenFail.add(false);
    } catch (e) {
        mcpSessionOpenFail.add(true);
        sleep(1);
        return;
    }

    try {
        for (let i = 0; i < 100; i++) {
            const toolName = `${TOOL_PREFIX}${TOOLS[i % TOOLS.length]}`;
            const start = Date.now();
            try {
                const result = client.callTool(toolName, { input: 'test' });
                const elapsed = Date.now() - start;
                mcpToolCalls.add(1);
                mcpToolCallDuration.add(elapsed);
                const ok = result.length > 0;
                mcpToolCallFailRate.add(!ok);
                mcpToolCallRate.add(ok);
                if (!ok) {
                    mcpToolCallFails.add(1);
                }
                check(result, { 'tool call returned data': (r) => r.length > 0 });
            } catch (e) {
                mcpToolCalls.add(1);
                mcpToolCallFailRate.add(true);
                mcpToolCallDuration.add(Date.now() - start);
            }
            sleep(0.1 + Math.random() * 0.4);
        }
    } finally {
        mcpSessionDuration.add(Date.now() - sessionStart);
        client.closeConnection();
    }
}
