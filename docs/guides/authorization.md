# Authorization Configuration

This guide covers configuring fine-grained authorization and access control for MCP Gateway, building on the authentication setup.

## Overview

Authorization in MCP Gateway controls which authenticated users can access specific MCP tools. This guide demonstrates using Kuadrant's AuthPolicy with Common Expression Language (CEL) to implement role-based access control.

Key concepts:
- **Tool-Level Authorization**: Control access to individual MCP tools
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
      "roles": ["add", "sum", "multiply", "divide"] // roles representing the allowed tools
    },
    "mcp-ns/geometry-mcp-server": {
      "roles": ["area", "distance", "volume"]
    }
  }
}
```

> **Note:** The test Keycloak instance deployed in the [authentication guide](./authentication.md) is already configured to include these claims based on user group membership. The `mcp` user is part of both `accounting` group, which map to different tool permissions.

## Step 2: Configure Tool-Level Authorization

Apply an AuthPolicy that enforces tool-level access control:

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
          issuerUrl: http://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
    authorization:
      'tool-access-check':
        patternMatching:
          patterns:
            - predicate: |
                request.headers['x-mcp-toolname'] in (has(auth.identity.resource_access) && auth.identity.resource_access.exists(p, p == request.headers['x-mcp-servername']) ? auth.identity.resource_access[request.headers['x-mcp-servername']].roles : [])
    response:
      unauthenticated:
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "MCP Tool Access denied: Authentication required."
            }
      unauthorized:
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "MCP Tool Access denied: Insufficient permissions for this tool."
            }
EOF
```

**Key Configuration Explained:**

- **Authentication**: Validates the JWT token using the configured issuer URL
- **Authorization Logic**: CEL expression checks if user's roles allow access to the requested tool
- **CEL Breakdown**:
  - `request.headers['x-mcp-toolname']`: The name of the requested MCP tool (stripped from prefix)
  - `request.headers['x-mcp-servername']`: The namespaced name of the MCP server matching the MCPServerRegistration resource
  - `auth.identity.resource_access`: The JWT claim containing all roles representing each allowed tool the user can access, grouped by MCP server
- **Response Handling**: Custom 401 and 403 responses for unauthenticated and unauthorized access attempts

## Step 3: Test Authorization

**Note**: The authentication guide already created the `accounting` group, added the `mcp` user to it, and configured group claims in JWT tokens. No additional Keycloak configuration is needed.

Test that authorization now controls tool access by setting up the MCP Inspector:

```bash
# Start MCP Inspector (requires Node.js/npm)
npx @modelcontextprotocol/inspector@latest &
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

1. **Login as mcp/mcp** (has both `accounting` and `developers` groups)
2. **Try allowed tools**:
   - `test1_greet`
   - `test2_headers`
   - `test3_add`
3. **Try restricted tools**:
   - `apikey_hello_world` - Should return 403 Forbidden

## Alternative Authorization Mechanisms

While this guide uses Kuadrant AuthPolicy, MCP Gateway supports various authorization approaches including other policy engines, built-in Istio authorization, and Gateway API policy extensions.

## Monitoring and Observability

Monitor authorization decisions:

```bash
# Check AuthPolicy status
kubectl get authpolicy -A

# View authorization logs
kubectl logs -n kuadrant-system -l app=authorino
```

## Next Steps

With authorization configured, you can:
- **[External MCP Servers](./external-mcp-server.md)** - Apply auth to external services
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Compose auth across multiple servers
- **[Troubleshooting](./troubleshooting.md)** - Debug auth and authz issues
