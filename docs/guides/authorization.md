# Authorization Configuration

This guide covers configuring fine-grained authorization and access control for MCP Gateway, building on the authentication setup.

## Overview

Authorization in MCP Gateway controls which authenticated users can access specific MCP tools, prompts, and resources. This guide demonstrates using Kuadrant's AuthPolicy with Common Expression Language (CEL) to implement role-based access control.

Key concepts:
- **Tool, Prompt, and Resource-Level Authorization**: Control access to individual MCP tools, prompts, and resources
- **Role-Based Access**: Use Keycloak client roles and group bindings for permission decisions
- **Self-contained ACL**: Access control lists stored in the signed JWT tokens
- **CEL Expressions**: Define complex authorization logic using Common Expression Language

## Prerequisites

- [Authentication Configuration](./authentication.md) completed
- Identity provider configured to include group/role claims in tokens
- [Node.js and npm](https://nodejs.org/en/download/) installed (for MCP Inspector testing)

**Note**: This guide demonstrates authorization using Kuadrant's AuthPolicy, but MCP Gateway supports any Istio/Gateway API compatible authorization mechanism.

## Understanding the Authorization Flow

1. **Authentication**: User authenticates and receives JWT token with permissions
2. **Tool Request**: Client makes MCP tool call (e.g., `tools/call`)
3. **Request Identity Check**: AuthPolicy verifies JWT token and extracts authorization claims
4. **Authorization Check**: CEL expression evaluates requested tool against user's permissions extracted from the JWT
5. **Access Decision**: Allow or deny based on evaluation result

## Step 1: Customise token issuance to include ACL information

Ensure your identity provider (e.g., Keycloak) includes necessary group/role claims in the issued JWT tokens.

The issued OAuth token should include claims similar to:

```jsonc
{
  "resource_access": {
    "mcp-ns/arithmetic-mcp-server": { // matches the namespaced name of the MCPServerRegistration CR
      "roles": ["tool:add", "tool:sum", "tool:multiply", "tool:divide", "prompt:math_tutor"] // roles prefixed with capability type
    },
    "mcp-ns/geometry-mcp-server": {
      "roles": ["tool:area", "tool:distance", "tool:volume", "prompt:calculate_area", "resource:ui://widget.html"] // resource roles use the full unprefixed URI (prefix stripped, scheme kept)
    }
  }
}
```

> **Note:** The test Keycloak instance deployed in the [authentication guide](./authentication.md) is already configured to include these claims based on user group membership. The `mcp` user is part of the `accounting` group, which maps to specific tool permissions.

## Step 2: Configure Tool, Prompt, and Resource Authorization

Apply an AuthPolicy that enforces access control for tools, prompts, and resources:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-tool-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcps  # Targets the MCP server listener
  rules:
    authentication:
      'sso-server':
        jwt:
          issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
    authorization:
      'tool-access-check':
        when:
          - predicate: "request.headers.exists(h, h == 'x-mcp-toolname')"
        patternMatching:
          patterns:
            - predicate: |
                ('tool:' + request.headers['x-mcp-toolname']) in (has(auth.identity.resource_access) && auth.identity.resource_access.exists(p, p == request.headers['x-mcp-servername']) ? auth.identity.resource_access[request.headers['x-mcp-servername']].roles : [])
      'prompt-access-check':
        when:
          - predicate: "request.headers.exists(h, h == 'x-mcp-promptname')"
        patternMatching:
          patterns:
            - predicate: |
                ('prompt:' + request.headers['x-mcp-promptname']) in (has(auth.identity.resource_access) && auth.identity.resource_access.exists(p, p == request.headers['x-mcp-servername']) ? auth.identity.resource_access[request.headers['x-mcp-servername']].roles : [])
      'resource-access-check':
        when:
          - predicate: "request.headers.exists(h, h == 'x-mcp-resourceuri')"
        patternMatching:
          patterns:
            - predicate: |
                ('resource:' + request.headers['x-mcp-resourceuri']) in (has(auth.identity.resource_access) && auth.identity.resource_access.exists(p, p == request.headers['x-mcp-servername']) ? auth.identity.resource_access[request.headers['x-mcp-servername']].roles : [])
    response:
      unauthenticated:
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "MCP Access denied: Authentication required."
            }
      unauthorized:
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "MCP Access denied: Insufficient permissions."
            }
EOF
```

**Key Configuration Explained:**

- **Authentication**: Validates the JWT token using the configured issuer URL
- **Authorization Logic**: CEL expressions check if the user's roles allow access to the requested tool, prompt, or resource. The appropriate check is triggered based on the presence of the `x-mcp-toolname`, `x-mcp-promptname`, or `x-mcp-resourceuri` header.
- **CEL Breakdown**:
  - `request.headers['x-mcp-toolname']` / `x-mcp-promptname` / `x-mcp-resourceuri`: The name (or, for resources, the full unprefixed `ui://` URI) of the requested capability
  - `request.headers['x-mcp-servername']`: The namespaced name of the MCP server matching the MCPServerRegistration resource
  - `auth.identity.resource_access`: The JWT claim containing roles prefixed by capability type (e.g. `tool:greet`, `prompt:math_tutor`, `resource:ui://widget.html`), grouped by MCP server
- **Response Handling**: Custom 401 and 403 responses for unauthenticated and unauthorized access attempts

> **Note:** `resource-access-check` governs whether a specific resource can be fetched via `resources/read`. It does not control whether that resource appears in `resources/list`; that's a separate, list-time filter driven by the `resources` claim inside the `allowed-capabilities` JWT claim (see below), keyed per-server by resource authority rather than by role name. The two checks are independent: a client can be authorized to read a resource that's hidden from its `resources/list` response, or the reverse.

For more advanced policy examples covering token exchange and `tools/list` filtering, see the [Vault Token Exchange guide](./vault-token-exchange.md).

You can also apply a policy that filters available tools and prompts during the `initialize` flow using an OPA rule:

```yaml
authorization:
  'authorized-capabilities':
    opa:
      rego: |
        allow = true
        capabilities = {
          "tools": { server: tools |
            server := object.keys(input.auth.identity.resource_access)[_]
            tools := [substring(r, count("tool:"), -1) |
              r := input.auth.identity.resource_access[server].roles[_]
              startswith(r, "tool:")
            ]
          },
          "prompts": { server: prompts |
            server := object.keys(input.auth.identity.resource_access)[_]
            prompts := [substring(r, count("prompt:"), -1) |
              r := input.auth.identity.resource_access[server].roles[_]
              startswith(r, "prompt:")
            ]
          }
        }
      allValues: true
```

> **Note:** Resource visibility in `resources/list` is filtered separately from tools/prompts above, and expects a different claim shape: a per-server map of allowed resource authorities (the unprefixed `ui://` host, not a role-prefixed name), e.g. `{"resources": {"mcp-ns/geometry-mcp-server": ["widget.html"]}}`. How you derive that shape from your identity provider's roles depends on your setup; there's no single `role-prefix` convention for it the way `tool:`/`prompt:` works above. If the token has no `resources` claim at all, all resources are visible (unless the broker enforces capability filtering, in which case none are). Once the claim is present, though, a server missing from its map, or listed with an empty array, has all of its resources excluded with no fallback.

## Step 3: Test Authorization

**Note**: The authentication guide already created the `accounting` group, added the `mcp` user to it, and configured group claims in JWT tokens. No additional Keycloak configuration is needed.

Test that authorization now controls tool access by setting up the MCP Inspector:

```bash
# Start MCP Inspector (requires Node.js/npm)
npx @modelcontextprotocol/inspector@0.21.1 &
INSPECTOR_PID=$!

# Wait for services to start
sleep 3

# Open MCP Inspector with the gateway URL
open "http://localhost:6274/?transport=streamable-http&serverUrl=http://mcp.127-0-0-1.sslip.io:8001/mcp"
```

**What this accomplishes:**
- **Gateway Access**: Makes the MCP Gateway accessible through your local browser
- **Authentication Testing**: Allows you to test the complete OAuth + authorization flow
- **Tool Verification**: Lets you verify which tools are accessible based on user groups

**Test Scenarios:**

1. **Login as mcp/mcp** (has `accounting` and `engineering` groups)
2. **Try allowed tools**:
   - `test1_greet`
   - `test2_headers`
3. **Try restricted tools**:
   - `test1_time` - Should return 403 Forbidden (accounting group only has the `tool:greet` role for test-server1)

## Alternative Authorization Mechanisms

While this guide uses Kuadrant AuthPolicy, MCP Gateway supports various authorization approaches including other policy engines, built-in Istio authorization, and Gateway API policy extensions.

## Monitoring and Observability

Monitor authorization decisions:

```bash
# Check AuthPolicy status
kubectl get authpolicy -A

# View authorization logs
kubectl logs -n kuadrant-system -l authorino-resource=authorino
```

## Next Steps

With authorization configured, you can:
- **[Tool Revocation](./tool-revocation.md)** - Revoke tool access and monitor enforcement
- **[External MCP Servers](./external-mcp-server.md)** - Apply auth to external services
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Compose auth across multiple servers
- **[Troubleshooting](./troubleshooting.md)** - Debug auth and authz issues
