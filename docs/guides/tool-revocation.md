# Tool Revocation

This guide covers revoking access to MCP tools for specific users or groups, and monitoring revocation enforcement.

## Overview

Tool revocation prevents a user or group from calling specific MCP tools. It builds on the authorization setup where tool access is controlled by roles in the identity provider's JWT tokens. Revoking a tool means removing the corresponding role from a user or group, so their next token no longer grants access.

Two enforcement points apply:

- **`tools/call`**: The AuthPolicy's CEL expression checks the `x-mcp-toolname` header against the user's `resource_access` roles. A revoked tool returns 403 Forbidden.
- **`tools/list`**: The broker filters the tools list using the signed `x-authorized-tools` header. A revoked tool no longer appears in the list.

## Prerequisites

- [Authentication](./authentication.md) configured
- [Authorization](./authorization.md) configured with tool-level AuthPolicy

## Step 1: Revoke Tool Access

Remove the tool role from the user or group in your identity provider.

In Keycloak, this is done by removing a client role mapping. The client name corresponds to the namespaced MCPServerRegistration (e.g., `mcp-test/server1-route`), and each role represents a tool name (e.g., `greet`, `headers`).

To revoke a tool for a group:
1. Go to **Groups** > select the group (e.g., `accounting`)
2. Go to **Role mapping** > remove the tool role from the relevant client

To revoke a tool for a single user:
1. Go to **Users** > select the user
2. Go to **Role mapping** > remove the tool role from the relevant client

## Step 2: Verify tools/call Denial

After revoking a tool, verify that the user can no longer call it. Log out of any existing MCP Inspector session and log back in as the affected user to get a fresh token.

Open MCP Inspector and connect to your gateway's `/mcp` endpoint. Authenticate through the OAuth flow.

Under **Tools > List Tools**, the revoked tool will still appear (this is expected — `tools/list` filtering is configured in Step 4). Try calling the revoked tool — the request should return 403 Forbidden.

## Step 3: Understand When Revocation Takes Effect

Revocation is not instantaneous. Access is governed by the JWT token, and tokens are valid until they expire.

- **New sessions**: Users who authenticate after revocation receive a token without the revoked tool. They are denied immediately.
- **Existing sessions**: Users with an active token retain access until the token expires. The token lifetime is configured in your identity provider (e.g., Keycloak's **Access Token Lifespan** setting).
- **In-flight requests**: A `tools/call` that is already being processed completes normally. The authorization check occurs before the request reaches the backend, so only new requests are affected.

To force faster revocation, reduce the access token lifespan in your identity provider. Shorter lifetimes mean tokens are refreshed more frequently, picking up role changes sooner. This is a trade-off between revocation latency and token refresh overhead.

> **Note:** There is no mechanism to revoke a specific in-flight token. Revocation relies on token expiry and re-issuance.

## Step 4: Enable tools/list Filtering

By default, revoking a tool only blocks `tools/call` requests (returning 403). The revoked tool still appears in `tools/list` responses. To filter revoked tools from the list, the broker needs a signed header that carries the user's authorized tools.

This step configures Authorino to generate that header using a wristband JWT signed with an ECDSA key pair.

### Generate an ECDSA key pair

```bash
openssl ecparam -name prime256v1 -genkey -noout -out private-key.pem
openssl ec -in private-key.pem -pubout -out public-key.pem
```

### Create Kubernetes secrets

The public key goes in the broker's namespace; the private key goes in Authorino's namespace:

```bash
kubectl create secret generic trusted-headers-public-key \
  --from-file=key=public-key.pem \
  -n mcp-system \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic trusted-headers-private-key \
  --from-file=key.pem=private-key.pem \
  -n kuadrant-system \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Update the AuthPolicy to generate the x-authorized-tools header

Delete the existing `mcp-auth-policy` and create a new version that adds authorization rules and a wristband response. The policy must be deleted first because the original uses `defaults.rules` while this version uses `rules`, and `kubectl apply` would merge both instead of replacing:

```bash
kubectl delete authpolicy mcp-auth-policy -n gateway-system --ignore-not-found
kubectl create -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  when:
    - predicate: "!request.path.contains('/.well-known')"
  rules:
    authentication:
      'keycloak':
        jwt:
          issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
    authorization:
      'allow-mcp-method':
        patternMatching:
          patterns:
          - predicate: |
              !request.headers.exists(h, h == 'x-mcp-method') || (request.headers['x-mcp-method'] in ["tools/list","initialize","notifications/initialized"])
      'authorized-tools':
        opa:
          rego: |
            allow = true
            tools = { server: roles | server := object.keys(input.auth.identity.resource_access)[_]; roles := object.get(input.auth.identity.resource_access, server, {}).roles }
          allValues: true
    response:
      success:
        headers:
          x-authorized-tools:
            wristband:
              issuer: 'authorino'
              customClaims:
                'allowed-tools':
                  selector: auth.authorization.authorized-tools.tools.@tostr
              tokenDuration: 300
              signingKeyRefs:
                - name: trusted-headers-private-key
                  algorithm: ES256
      unauthenticated:
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "Access denied: Authentication required."
            }
      unauthorized:
        code: 401
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "Access denied: Unsupported method. New authentication required (401)."
            }
EOF
```

### Configure the broker to validate the signed header

The patch triggers an automatic broker redeployment that loads the public key from the secret created earlier:

```bash
kubectl patch mcpgatewayextension mcp-gateway-extension -n mcp-system --type='merge' \
  -p='{"spec":{"trustedHeadersKey":{"secretName":"trusted-headers-public-key"}}}'

kubectl rollout status deployment/mcp-gateway -n mcp-system --timeout=60s
```

Verify the AuthPolicy is enforced:

```bash
kubectl get authpolicy mcp-auth-policy -n gateway-system -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# Expected: True
```

## Step 5: Verify tools/list Filtering

Log out and log back in to get a fresh token. Under **Tools > List Tools**, the revoked tool should no longer appear in the list. Only tools matching the user's `resource_access` roles are returned.

## Step 6: Monitor Revocation Enforcement

If [OpenTelemetry](./opentelemetry.md) is enabled, denied requests appear as trace spans with `http.status_code: 403`. Query your trace backend to find them:

- Filter by `http.status_code = 403`
- Use `gen_ai.tool.name` to identify which tool was denied
- Use `mcp.session.id` to correlate with the client session

If the [observability stack](./observability.md) is deployed, query gateway logs for revocation-related activity:

```logql
{namespace="mcp-system"} |= `x-mcp-toolname` | json | line_format "{{.msg}}"
```

The router logs the `x-mcp-toolname` and `x-mcp-servername` headers for every `tools/call` request. Combined with the 403 status, this gives visibility into which users are attempting to call revoked tools.

## Next Steps

- **[OpenTelemetry](./opentelemetry.md)** - Enable distributed tracing
- **[Troubleshooting](./troubleshooting.md)** - Debug authorization issues
