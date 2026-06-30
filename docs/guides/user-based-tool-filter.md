# User-Based Tool Filtering

This guide shows how to filter `tools/list` responses based on the authenticated user's allowed tools. The MCP Gateway broker verifies a signed `x-mcp-authorized` header and only returns the tools listed in that header.

## Prerequisites

- MCP Gateway is installed and working
- [Authentication](./authentication.md) is configured
- At least one MCP server is registered with the gateway
- Kuadrant `AuthPolicy` is available in your cluster
- You know the name and namespace of your `MCPGatewayExtension`
- Your identity provider includes per-server tool permissions in the authenticated user's token claims

In the examples below:

- The gateway namespace is `gateway-system`
- The broker namespace is `mcp-system`
- The Authorino namespace is `kuadrant-system`
- The MCPGatewayExtension name is `mcp-gateway-extension`
- The AuthPolicy name is `mcp-auth-policy`

Replace these values if your installation uses different names.

## How It Works

1. An upstream authorization system validates the user's identity
2. It creates a signed JWT containing the user's allowed capabilities in an `allowed-capabilities` claim
3. This JWT is passed to the broker via the `x-mcp-authorized` header
4. The broker validates the JWT signature and filters `tools/list` responses accordingly

The `allowed-capabilities` claim is a JSON-encoded string containing a capabilities map. The top-level key is the capability type, and the `tools` entry maps server names to allowed tool names:

```json
{
  "allowed-capabilities": "{\"tools\":{\"mcp-test/server1-route\":[\"greet\",\"time\"],\"mcp-test/server2-route\":[\"hello_world\"]}}",
  "exp": 1760004918,
  "iat": 1760004618
}
```

The value of `allowed-capabilities` is a string, not a nested JSON object. The broker deserializes this string to extract the `tools` map for filtering.

## Step 1: Generate a signing key pair

Generate an ECDSA P-256 key pair. Authorino uses the private key to sign the `x-mcp-authorized` wristband, and the broker uses the public key to verify it.

```bash
openssl ecparam -name prime256v1 -genkey -noout -out private-key.pem
openssl ec -in private-key.pem -pubout -out public-key.pem
```

Verify that both files were created:

```bash
ls -l private-key.pem public-key.pem
```

## Step 2: Create the Kubernetes secrets

Create one secret for the broker's public key and one for Authorino's private key.

The public-key secret must be created in the same namespace as the `MCPGatewayExtension`. In this example, that namespace is `mcp-system`.

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

Verify that both secrets exist:

```bash
kubectl get secret trusted-headers-public-key -n mcp-system
kubectl get secret trusted-headers-private-key -n kuadrant-system
```

## Step 3: Configure the MCPGatewayExtension

Configure the MCPGatewayExtension to inject the public key into the broker by referencing the secret from `spec.trustedHeadersKey.secretName`:

```bash
kubectl patch mcpgatewayextension mcp-gateway-extension -n mcp-system --type='merge' \
  -p='{"spec":{"trustedHeadersKey":{"secretName":"trusted-headers-public-key"}}}'
```

Wait for the broker deployment to roll out:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system --timeout=120s
```

Verify the extension now references the trusted header key:

```bash
kubectl get mcpgatewayextension mcp-gateway-extension -n mcp-system \
  -o jsonpath='{.spec.trustedHeadersKey.secretName}{"\n"}'
```

Expected output:

```text
trusted-headers-public-key
```

## Step 4: Apply an AuthPolicy that generates `x-mcp-authorized`

Apply an AuthPolicy that:

- authenticates the user with your identity provider
- allows `tools/list`, `initialize`, and `notifications/initialized`
- extracts the user's allowed tools from the identity claims
- returns the `x-mcp-authorized` wristband header signed with the private key

If you already created an authentication-only `mcp-auth-policy`, delete it first. This guide uses `spec.rules`, while the authentication guide uses `spec.defaults.rules`. Replacing the object avoids ending up with both shapes merged together.

Update the `issuerUrl` and `resource_metadata` values to match your environment before applying:

```bash
kubectl delete authpolicy mcp-auth-policy -n gateway-system --ignore-not-found

kubectl apply -f - <<EOF
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
      keycloak:
        jwt:
          issuerUrl: https://keycloak.example.com/realms/mcp
    authorization:
      allow-mcp-method:
        patternMatching:
          patterns:
            - predicate: |
                !request.headers.exists(h, h == 'x-mcp-method') || (request.headers['x-mcp-method'] in ["tools/list","initialize","notifications/initialized"])
      authorized-capabilities:
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
              }
            }
          allValues: true
    response:
      success:
        headers:
          x-mcp-authorized:
            wristband:
              issuer: authorino
              customClaims:
                allowed-capabilities:
                  selector: auth.authorization.authorized-capabilities.capabilities.@tostr
              tokenDuration: 300
              signingKeyRefs:
                - name: trusted-headers-private-key
                  algorithm: ES256
      unauthenticated:
        code: 401
        headers:
          WWW-Authenticate:
            value: Bearer resource_metadata=https://mcp.example.com/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "Access denied: Authentication required."
            }
      unauthorized:
        code: 403
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "Access denied."
            }
EOF
```

Verify that the policy is enforced:

```bash
kubectl get authpolicy mcp-auth-policy -n gateway-system \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}{"\n"}'
```

Expected output:

```text
True
```

> **Note:** The `authorized-capabilities` Rego expects the authenticated user's tool permissions to be present in `resource_access`, keyed by MCP server name such as `mcp-test/server1-route`. Tool roles must be prefixed with `tool:`, such as `tool:greet`.

## Step 5: Verify that `tools/list` is filtered

Open MCP Inspector and sign in as a user who should only see a subset of tools:

```bash
npx @modelcontextprotocol/inspector@0.21.1
```

Connect the inspector to your gateway's MCP endpoint using the authenticated flow from the [authentication guide](./authentication.md).

After login:

1. Open **Tools**
2. Run **List Tools**
3. Confirm that only the tools allowed for that user are shown

For example, if the signed header only allows:

```json
{
  "tools": {
    "mcp-test/server1-route": ["greet", "time"],
    "mcp-test/server2-route": ["hello_world"]
  }
}
```

Then the broker should only return the prefixed tools for those entries, such as `test1_greet`, `test1_time`, and `test2_hello_world`.

To verify that filtering is user-specific, sign out and authenticate as a different user with a different set of tool roles. The `tools/list` response should change to match that user's permissions.

## Cleanup

If you only created the local key files for testing, remove them from your workstation:

```bash
rm -f private-key.pem public-key.pem
```

## Next Steps

- [Authorization](./authorization.md)
- [Tool Revocation](./tool-revocation.md)
- [Troubleshooting](./troubleshooting.md)
