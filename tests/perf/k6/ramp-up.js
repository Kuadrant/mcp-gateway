// ramp-up test to find the gateway's saturation/crash point.
// matches the PSAP "ramp-up under load" methodology:
// 0 to N users at a steady ramp rate, hold at peak.
//
// usage:
//   TARGET_URL=http://localhost:8001/mcp TOOL_PREFIX=mock_ MAX_USERS=4096 RAMP_RATE=8 \
//     ./bin/k6 run tests/perf/k6/ramp-up.js

import mcp from 'k6/x/infobip_mcp';
import { check, sleep } from 'k6';
import { Counter, Trend, Rate, Gauge } from 'k6/metrics';

const TARGET_URL = __ENV.TARGET_URL || 'http://localhost:8001/mcp';
const TOOL_PREFIX = __ENV.TOOL_PREFIX || 'mock_';
const MAX_USERS = parseInt(__ENV.MAX_USERS || '4096');
const RAMP_RATE = parseInt(__ENV.RAMP_RATE || '8');
const HOLD_DURATION = __ENV.HOLD_DURATION || '5m';
const TIMEOUT = parseInt(__ENV.TIMEOUT || '10');

const rampSeconds = Math.ceil(MAX_USERS / RAMP_RATE);
const rampDuration = `${rampSeconds}s`;

const mcpToolCalls = new Counter('mcp_tool_calls');
const mcpToolCallDuration = new Trend('mcp_tool_call_duration', true);
const mcpToolCallFailRate = new Rate('mcp_tool_call_fail_rate');
const mcpSessionsOpened = new Counter('mcp_sessions_opened');
const mcpSessionOpenFail = new Rate('mcp_session_open_fail');
const mcpSessionDuration = new Trend('mcp_session_duration', true);

export const options = {
    scenarios: {
        ramp_to_crash: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: rampDuration, target: MAX_USERS },
                { duration: HOLD_DURATION, target: MAX_USERS },
                { duration: '30s', target: 0 },
            ],
            gracefulStop: '10s',
        },
    },
    thresholds: {
        mcp_tool_call_fail_rate: [{ threshold: 'rate<0.05', abortOnFail: false }],
        mcp_tool_call_duration: [{ threshold: 'p(95)<1000', abortOnFail: false }],
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
        while (true) {
            for (let i = 0; i < TOOLS.length; i++) {
                const toolName = `${TOOL_PREFIX}${TOOLS[i]}`;
                const start = Date.now();
                try {
                    const result = client.callTool(toolName, { input: 'test' });
                    const elapsed = Date.now() - start;
                    mcpToolCalls.add(1);
                    mcpToolCallDuration.add(elapsed);
                    const ok = result.length > 0;
                    mcpToolCallFailRate.add(!ok);
                    check(result, { 'tool call ok': (r) => r.length > 0 });
                } catch (e) {
                    mcpToolCalls.add(1);
                    mcpToolCallFailRate.add(true);
                    mcpToolCallDuration.add(Date.now() - start);
                }
                sleep(0.1 + Math.random() * 0.4);
            }
        }
    } finally {
        mcpSessionDuration.add(Date.now() - sessionStart);
        client.closeConnection();
    }
}
