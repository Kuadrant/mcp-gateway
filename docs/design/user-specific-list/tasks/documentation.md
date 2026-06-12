## Documentation Plan

### User Guide (`docs/guides/user-specific-tools.md`)

### When I want tools scoped per user from a shared MCP server

When a platform engineer fronts a shared MCP server (e.g. GitHub MCP) that returns different tools based on the requesting user's credentials, they want to configure the gateway to fetch tools per user instead of caching the service account's list, so that each user sees only their own tools.

**Cover:**
- What `userSpecificList` does and when to use it
- Example MCPServerRegistration YAML with `userSpecificList: Enabled`
- Auth configuration prerequisites (user headers must reach the upstream)
- Behavior when the upstream server is down (graceful degradation)
- Performance characteristics: each `tools/list` triggers a full MCP handshake (initialize + list) per userSpecificList server. Advise operators to expect added latency proportional to upstream response times and to only enable for servers that genuinely return different tools per user.

### When I want to understand how user-specific tools interact with existing filters

When a platform engineer already uses `x-mcp-authorized` JWT filtering or MCPVirtualServer to scope tool visibility, they want to understand how user-specific tools interact with these mechanisms, so that they can combine them without surprises.

**Cover:**
- Filter ordering: user-specific fetch → JWT filter → virtual server filter
- User-specific tools are subject to the same filters as cached tools
- Example combining `userSpecificList` with a virtual server

### API Reference (`docs/reference/mcpserverregistration.md`)

### When I need to know the CRD field details

When a platform engineer is writing an MCPServerRegistration manifest, they want a reference for the `userSpecificList` field, so that they know the type, default, and behavior.

**Cover:**
- Field name, type, default value
- Behavior when true vs false
- Relationship to `credentialRef` (service account credential still used for health checks, not for user-specific tool fetches)
