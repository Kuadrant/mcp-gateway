## Documentation Plan

### User Guide (`docs/guides/auditing.md`)

### When I want to understand what's in the audit log

When a platform engineer deploys the MCP Gateway and wants to know what audit data is available, they want a reference of the JSON access log format, field descriptions, and where logs appear, so that they can start querying without reading the design doc.

**Cover:**
- Log location (gateway pod stdout)
- JSON format reference with field descriptions
- The three correlation levels (agent workflow, MCP session, individual request)

### When I want to trace a tool call across systems

When a platform engineer receives an alert about a failed or suspicious tool call, they want to use `traceparent`, `mcp_session_id`, and baggage to find the broader context across agent framework logs, gateway audit logs, and backend server logs, so that they can investigate without searching disconnected systems.

**Cover:**
- How correlation IDs connect the three log sources
- Setting `baggage` and `traceparent` in agent frameworks
- Example queries for Loki/Grafana and CloudWatch

### When I want auth decisions in the audit log

When a platform engineer wants to see who was allowed or denied access alongside tool call data, they want to configure AuthPolicy to surface auth decisions in the same audit log, so that they have a single place to query for compliance.

**Cover:**
- AuthPolicy response header configuration to inject auth decisions
- Correlating Authorino decision logs with gateway audit logs via `x-request-id`
- Example AuthPolicy YAML

### When I want to log tool call arguments

When a compliance officer needs to verify what data was sent to tools during an incident, they want to enable parameter logging and understand the sensitivity implications, so that they can assess data exposure.

**Cover:**
- `spec.audit.parameterLogging` CRD field configuration
- Truncation behaviour (1KB)
- Sensitivity considerations and deferred redaction

### When I want to customise the log format

When a platform engineer has specific log format requirements for their SIEM, they want to override the default access log format, so that the audit trail fits their existing log pipeline.

**Cover:**
- Overriding the default format via EnvoyFilter (`MERGE` or `REPLACE`)
- Shipping audit logs to external systems
- Treating audit logs as sensitive data
