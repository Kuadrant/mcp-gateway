# MCP Gateway Auditing Guide

This guide covers enabling, configuring, and collecting audit logs for the Model Context Protocol (MCP) Gateway.

An audit trail provides a persistent, queryable record of all MCP tool and prompt interactions flowing through the gateway. This record is essential for security compliance, caller attribution, and operational troubleshooting.

## Why Auditing is Useful

In an agentic system, a single developer prompt to an AI agent can trigger an orchestrator to make multiple backend tool invocations across different MCP servers. Auditing allows platform engineers and security teams to:
* **Attribute Activity**: Trace which user or agent invoked which specific tools on which backend servers.
* **Correlate Requests**: Map tool execution flows across three logical levels: the overall agent workflow (via W3C Trace IDs), the specific MCP session (via Session IDs), and the individual HTTP request (via Request IDs).
* **Ensure Compliance**: Log and archive all tool invocations for compliance, security reviews, and forensic auditing.
* **Observe Operations**: Identify slow, failing, or irregular tool calls to improve performance and reliability.

## What the Audit Trail Captures

When enabled, the gateway captures key MCP metadata from the traffic flow and logs it as part of Envoy's network-level access logging. The audit trail records:
* **Core HTTP Metadata**: Timestamp, request ID, HTTP method, request path, response code, duration (latency), bytes sent/received, and upstream host.
* **MCP Metadata**: MCP method (e.g., `tools/call`, `tools/list`), backend server name, and the specific tool name.
* **Caller Identity**: User and agent identifiers resolved from client baggage or fallback authentication headers.
* **Correlation IDs**: `traceparent` (trace ID) for agent workflow correlation and `mcp-session-id` for session tracking.
* **Optional Parameters**: Tool call arguments (opt-in configuration).

## Prerequisites

Before configuring auditing, ensure you have:
* The MCP Gateway installed and running.
* An MCP client or agent framework capable of connecting to the gateway.
* (Optional) An AuthPolicy configured if you want validated caller identity fallbacks.
* (Optional) A log collection pipeline (e.g., Loki, Elastic, Splunk) to forward container stdout logs.

---

## Step 1: Enable Auditing on the Gateway

Auditing is configured via the `spec.audit` field on the `MCPGatewayExtension` resource. By default, auditing is disabled. Creating or updating an `MCPGatewayExtension` with the `spec.audit` field present triggers the gateway operator to configure structured JSON access logs and inject the required audit configurations.

To enable auditing with default settings, apply the following configuration to your `MCPGatewayExtension`:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  audit: {}
```

### Advanced Configuration

You can configure the `AuditConfig` with additional options to control tool parameter logging and customize fallback identity headers:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  audit:
    parameterLogging: Enabled
    identityHeaders:
      - x-forwarded-email
      - x-auth-user
```

Apply the configuration using `kubectl`:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  audit:
    parameterLogging: Enabled
    identityHeaders:
      - x-forwarded-email
      - x-auth-user
EOF
```

### Verification

Verify that the `MCPGatewayExtension` status is updated and shows `Ready`:

```bash
kubectl get mcpgatewayextension mcp-gateway -n mcp-system -o yaml
```

Once applied, the operator injects the audit configuration into the gateway's Envoy proxy and configures structured access logging.

---

## Step 2: Configure Envoy Access Logs using Dynamic Metadata

When auditing is enabled, the MCP Gateway operator adds an access log configuration patch to the target Gateway's EnvoyFilter. This patch configures the Envoy HTTP Connection Manager to emit access logs using ext_proc dynamic metadata.

### Dynamic Metadata Fields

The MCP Router processor sets audited MCP properties within the `mcp-audit` dynamic metadata namespace. These properties can be extracted in the Envoy access log format using `%DYNAMIC_METADATA(mcp-audit:<field>)%` format strings:

| Field | Description | Format String |
|-------|-------------|---------------|
| `method` | The MCP method being called (e.g., `tools/call`) | `%DYNAMIC_METADATA(mcp-audit:method)%` |
| `tool_name` | The name of the tool (after prefix stripping) | `%DYNAMIC_METADATA(mcp-audit:tool_name)%` |
| `server_name` | The target backend MCP server | `%DYNAMIC_METADATA(mcp-audit:server_name)%` |
| `session_id` | The unique persistent MCP session ID | `%DYNAMIC_METADATA(mcp-audit:session_id)%` |
| `user_id` | The user identity (resolved from baggage/fallback) | `%DYNAMIC_METADATA(mcp-audit:user_id)%` |
| `agent_id` | The agent identity (resolved from baggage) | `%DYNAMIC_METADATA(mcp-audit:agent_id)%` |
| `tool_params` | Tool call parameters (only if parameter logging is enabled) | `%DYNAMIC_METADATA(mcp-audit:tool_params)%` |

### Default JSON Access Log Format

By default, the gateway operator configures the Envoy proxy to emit structured JSON logs to stdout in the following format:

```json
{
  "timestamp": "%START_TIME%",
  "method": "%REQ(:METHOD)%",
  "path": "%REQ(:PATH)%",
  "response_code": "%RESPONSE_CODE%",
  "request_id": "%REQ(X-REQUEST-ID)%",
  "traceparent": "%REQ(TRACEPARENT)%",
  "mcp_method": "%DYNAMIC_METADATA(mcp-audit:method)%",
  "mcp_tool_name": "%DYNAMIC_METADATA(mcp-audit:tool_name)%",
  "mcp_server_name": "%DYNAMIC_METADATA(mcp-audit:server_name)%",
  "mcp_session_id": "%DYNAMIC_METADATA(mcp-audit:session_id)%",
  "mcp_user_id": "%DYNAMIC_METADATA(mcp-audit:user_id)%",
  "mcp_agent_id": "%DYNAMIC_METADATA(mcp-audit:agent_id)%",
  "mcp_tool_params": "%DYNAMIC_METADATA(mcp-audit:tool_params)%",
  "duration_ms": "%DURATION%",
  "upstream_host": "%UPSTREAM_HOST%",
  "bytes_sent": "%BYTES_SENT%",
  "bytes_received": "%BYTES_RECEIVED%"
}
```

> **Note:** If you are using header-based log parsing in legacy environments, these properties are also set as HTTP headers on request forwarding and can be retrieved using `%REQ(X-MCP-<FIELD>)%` (e.g., `%REQ(X-MCP-TOOLNAME)%`). Both mechanisms are fully compatible.

---

## Step 3: Configure Optional Parameter Logging

By default, tool call parameters are excluded from the audit trail to prevent accidental exposure of sensitive information (`parameterLogging` defaults to `Disabled`). You can explicitly opt-in to logging tool call arguments by setting `parameterLogging: Enabled` in the `spec.audit` block.

When enabled:
1. The MCP Router parses the JSON-RPC request body for `tools/call` requests.
2. It extracts the arguments from `params.arguments`.
3. The arguments are serialized into a JSON string and populated in the `tool_params` dynamic metadata namespace.

### Parameter Truncation

To protect gateway performance and prevent excessively large log lines, tool parameters are automatically truncated to **1KB (1024 bytes)** before being added to the metadata and logged.

---

## Step 4: Configure AuthPolicy Integration

Surfacing authorization decisions (allowed/denied) alongside tool usage logs is critical for security compliance. Kuadrant's `AuthPolicy` (backed by Authorino) can be configured to validate user tokens and inject authoritative identity claims into request headers. These headers can then be captured by the gateway's audit fallback mechanisms.

To correlate authorization decisions and identity with your MCP audit logs:

1. Configure your `AuthPolicy` to evaluate user authorization.
2. Use the `response` section of the `AuthPolicy` to inject user identity information as headers (e.g., `x-auth-user`).
3. Set the `identityHeaders` field in your `MCPGatewayExtension` to check these headers.

### Example AuthPolicy Configuration

The following policy validates incoming JWTs and injects `x-auth-user` and `x-auth-decision` headers:

```yaml
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-gateway-auth
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  defaults:
    rules:
      authentication:
        jwt-keycloak:
          jwt:
            issuerUrl: https://keycloak.mcp.local/realms/mcp
      response:
        headers:
          x-auth-user:
            valueFrom:
              authJson: auth.identity.metadata.username
          x-auth-decision:
            value: "allowed"
```

Because `x-auth-user` is listed in `spec.audit.identityHeaders`, the gateway will automatically fall back to using its value for `mcp_user_id` when the client does not send W3C Baggage headers.

Additionally, both the Authorino decision logs and the MCP Gateway access logs contain the unique `%REQ(X-REQUEST-ID)%` value, making it trivial to join the decision logs with the tool invocation logs in a SIEM.

---

## Annotated JSON Audit Log Examples

Below are representative structured JSON logs emitted by the gateway pod to stdout:

### Example 1: Successful Tool Call with Full Correlation Context

This entry represents a successful `tools/call` invocation where the client propagated W3C Trace Context and Baggage headers, and parameter logging was enabled:

```json
{
  "timestamp": "2026-05-18T14:23:01.123Z",
  "method": "POST",
  "path": "/mcp",
  "response_code": 200,
  "request_id": "abc-111",                                            // Unique Envoy request ID
  "traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa-01",  // W3C Trace ID linking this request to the agent workflow
  "mcp_method": "tools/call",                                         // MCP method being invoked
  "mcp_tool_name": "create_issue",                                    // Tool name (after prefix stripping)
  "mcp_server_name": "jira",                                          // Target backend MCP server
  "mcp_session_id": "sess-7a3b",                                      // Unique persistent MCP session ID
  "mcp_user_id": "dev-jane",                                          // User ID resolved from W3C baggage user.id
  "mcp_agent_id": "coding-agent-v2",                                  // Agent ID resolved from W3C baggage agent.id
  "mcp_tool_params": "{\"project\":\"PROJ\",\"summary\":\"Bug\"}",    // Tool call arguments (JSON string, max 1KB)
  "duration_ms": 342,                                                 // Processing latency in milliseconds
  "upstream_host": "10.0.1.5:8080",                                   // Host address of the upstream MCP server
  "bytes_sent": 1024,
  "bytes_received": 512
}
```

### Example 2: Graceful Degradation (Legacy Client)

This entry shows a tool call made by a client that does not propagate baggage or traceparent headers. Key operational data is still fully captured:

```json
{
  "timestamp": "2026-05-18T14:25:01.123Z",
  "method": "POST",
  "path": "/mcp",
  "response_code": 200,
  "request_id": "def-444",
  "traceparent": "-",                                                 // No trace context provided by client
  "mcp_method": "tools/call",
  "mcp_tool_name": "get_weather",
  "mcp_server_name": "weather-service",
  "mcp_session_id": "sess-9x2f",
  "mcp_user_id": "-",                                                 // No user identity resolved
  "mcp_agent_id": "-",                                                // No agent identity resolved
  "mcp_tool_params": "-",                                             // Parameter logging disabled
  "duration_ms": 305,
  "upstream_host": "10.0.2.3:8080",
  "bytes_sent": 890,
  "bytes_received": 2048
}
```

### Example 3: Auth-Layer Identity Fallback

This entry shows a request where the client did not send W3C Baggage, but the gateway successfully resolved the user identity from the configured `x-forwarded-email` fallback header:

```json
{
  "timestamp": "2026-05-18T14:27:02.456Z",
  "method": "POST",
  "path": "/mcp",
  "response_code": 200,
  "request_id": "ghi-777",
  "traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-0e8b2a1c-01",
  "mcp_method": "tools/call",
  "mcp_tool_name": "search_prs",
  "mcp_server_name": "github",
  "mcp_session_id": "sess-7a3b",
  "mcp_user_id": "operator@acme.org",                                 // Sourced from fallback x-forwarded-email header
  "mcp_agent_id": "-",
  "mcp_tool_params": "-",
  "duration_ms": 218,
  "upstream_host": "10.0.3.4:8080",
  "bytes_sent": 768,
  "bytes_received": 1200
}
```

---

## Log Shipping and Aggregation (SIEM)

Gateway audit logs are written directly to stdout of the gateway pods. In standard Kubernetes production environments, these logs are captured by container runtimes and forwarded to centralized SIEM platforms.

### Tracing Workflows Across Systems

To reconstruct the full path of an agentic interaction:
1. **Trace ID**: Extract the Trace ID from the `traceparent` field. Use this ID to query logs in your frontend agent framework, orchestrator, and backend servers.
2. **Session ID**: Use the `mcp_session_id` to group all tool calls, listings, and connections made during a single persistent client session.
3. **Request ID**: Use the `request_id` (`x-request-id`) to correlate specific gateway access log entries with downstream backend MCP server logs or upstream auth proxy (Authorino) logs.

### Loki / Grafana Configuration

When using Grafana Loki with Promtail, you can extract the structured JSON log fields to enable rich querying.

**Promtail Pipeline Configuration:**

```yaml
scrape_configs:
  - job_name: kubernetes-pods
    pipeline_stages:
      - json:
          expressions:
            mcp_method: mcp_method
            mcp_tool_name: mcp_tool_name
            mcp_server_name: mcp_server_name
            mcp_user_id: mcp_user_id
            traceparent: traceparent
      - labels:
          mcp_method:
          mcp_tool_name:
          mcp_server_name:
          mcp_user_id:
```

**LogQL Query Examples:**

*   **Find all tool calls made by a specific user:**
    ```logql
    {namespace="mcp-system"} | json | mcp_user_id = "dev-jane" and mcp_method = "tools/call"
    ```

*   **Trace a specific workflow via Trace ID:**
    ```logql
    {namespace="mcp-system"} | json | traceparent =~ ".*4bf92f3577b34da6a3ce929d0e0e4736.*"
    ```

### Elasticsearch / Logstash / Kibana (ELK)

In the ELK stack, configure Logstash to parse the JSON output from your filebeat logs.

**Logstash Filter Configuration:**

```ruby
filter {
  if [kubernetes][container][name] == "mcp-gateway" {
    json {
      source => "message"
      target => "mcp_audit"
    }
    mutate {
      rename => { "[mcp_audit][traceparent]" => "trace_id" }
    }
  }
}
```

### Splunk

For Splunk, ensure the source type is configured to auto-extract JSON fields (`KV_MODE = json` in `props.conf`).

**Splunk Search Queries:**

*   **List all distinct tools invoked by an agent:**
    ```splunk
    sourcetype="kube:container:mcp-gateway" mcp_agent_id="coding-agent-v2" mcp_method="tools/call"
    | stats count by mcp_tool_name, mcp_server_name
    ```

*   **Find failed tool invocations (error status):**
    ```splunk
    sourcetype="kube:container:mcp-gateway" mcp_method="tools/call" response_code!=200
    | table timestamp, mcp_user_id, mcp_server_name, mcp_tool_name, response_code
    ```

---

## Security Considerations

Audit logs containing operational data must be treated with appropriate security controls to ensure compliance and data safety.

### Sensitive Data Exposure in Tool Parameters

> **Warning:** Enabling parameter logging (`parameterLogging: Enabled`) carries significant security risks. Tool call arguments often contain highly sensitive data, including:
> * User credentials, API tokens, or secrets passed to tools (e.g., personal access tokens).
> * Personally Identifiable Information (PII) like names, emails, addresses, or phone numbers.
> * Sensitive business data or proprietary source code processed by the AI agent.
>
> If you enable parameter logging, you must ensure that access to the audit logs is strictly restricted, and that logs are shipped to a secure, access-controlled SIEM system.

### Caller Identity Validation

The `user_id` and `agent_id` dynamic metadata and headers are populated in two ways:
1. **W3C Baggage Header**: Sourced directly from client-provided headers. Since these headers are client-controlled, they should be treated as **informational, not authoritative**.
2. **Auth-Layer Fallbacks**: Sourced when baggage is empty. By configuring `identityHeaders` (e.g., `x-forwarded-email`, `x-auth-user`), the identity is resolved from headers set by an authentication proxy or AuthPolicy (e.g., Authorino). This identity is **authoritative** because it is set by the gateway after validation.

### Log Injection Mitigation

To prevent malicious clients from injecting control characters (like newlines) into the audit logs via client-controlled headers like `baggage`, the gateway automatically sanitizes these values. Any control characters (CR, LF, null bytes) are stripped before being added to the dynamic metadata and log entry.

---

## Next Steps

With auditing configured, you can proceed to:
* **[Authentication Configuration](./authentication.md)** - Secure the gateway with OIDC/OAuth 2.1 authentication.
* **[Authorization Configuration](./authorization.md)** - Control tool-level access for users and groups.
* **[Troubleshooting](./troubleshooting.md)** - Guidelines for debugging gateway deployment issues.
