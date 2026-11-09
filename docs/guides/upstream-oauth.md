# Authenticating the broker to upstream MCP servers

Use OAuth 2.0 client credentials when an upstream MCP server requires an access token for tool discovery. The broker obtains and renews its own tokens, so you do not need to replace an expired bearer token in a Secret manually.

This configures **broker-to-upstream authentication only**. It does not authenticate clients to the gateway or supply credentials for client `tools/call` requests. Configure [client authentication](./authentication.md) and upstream tool-call authorization separately. This is a client credentials grant, not an exchange of a user token.

## Prerequisites

- MCP Gateway is installed with CRDs, controller, and broker-router versions that support `oauth2ClientCredentials`.
- An `MCPGatewayExtension` and an `HTTPRoute` to the upstream server are configured. See [registering MCP servers](./register-mcp-servers.md) or [connecting external MCP servers](./external-mcp-server.md).
- Your authorization server has a confidential client with the client credentials grant enabled and permission to discover tools on the upstream server.
- You have the client ID, client secret, HTTPS token endpoint URL, and any required scopes.
- The broker can reach both the token endpoint and the upstream MCP endpoint. Their TLS certificates are trusted by the broker.
- You have `kubectl`, `curl`, and `jq`, and permission to manage registrations and Secrets in the registration namespace.

Check that the installed CRD exposes the configuration:

```bash
kubectl explain mcpserverregistration.spec.oauth2ClientCredentials --api-version=mcp.kuadrant.io/v1
```

If the field is absent, upgrade the CRDs and gateway components before continuing. This setting is separate from `MCPGatewayExtension.spec.oauthProtectedResource`, which advertises authorization servers to MCP clients.

The examples use `mcp-test` for the registration namespace, `my-mcp-server-route` for the existing HTTPRoute, and `mcp-system` for the MCPGatewayExtension namespace. Replace these values to match your deployment.

## Step 1: Creating the credentials Secret

Create the Secret in the **same namespace as the MCPServerRegistration**. The following Bash command prompts for the client secret without echoing it, writes it briefly to a private temporary directory, and removes that directory when the command exits. Replace `<client-id>` with the client ID from your authorization server. Use your approved secret manager instead if one is available.

```bash
bash <<'EOF'
set +x
set -eu
umask 077
secret_dir=$(mktemp -d)
trap 'rm -rf "$secret_dir"' EXIT

printf '%s' '<client-id>' > "$secret_dir/clientID"
read -r -s -p 'OAuth client secret: ' client_secret </dev/tty
printf '\n'
if [ -z "$client_secret" ]; then
  printf 'The OAuth client secret cannot be empty.\n' >&2
  exit 1
fi
printf '%s' "$client_secret" > "$secret_dir/clientSecret"
unset client_secret

kubectl create secret generic my-mcp-server-oauth \
  --namespace=mcp-test \
  --from-file=clientID="$secret_dir/clientID" \
  --from-file=clientSecret="$secret_dir/clientSecret" \
  --dry-run=client -o yaml > "$secret_dir/secret.yaml"
kubectl apply -f "$secret_dir/secret.yaml"
kubectl label secret my-mcp-server-oauth -n mcp-test \
  mcp.kuadrant.io/secret=true --overwrite
EOF
```

The command applies the `mcp.kuadrant.io/secret=true` label required by the controller. Both `clientID` and `clientSecret` must be nonempty; these key names are fixed. Supply the raw client secret, not an access token or a value prefixed with `Bearer`. The script does not place the secret in the command text or shell history. Do not enable shell tracing while entering credentials.

Verify the Secret exists and contains the expected keys without printing their values:

```bash
kubectl get secret my-mcp-server-oauth -n mcp-test \
  -o go-template='{{range $key, $value := .data}}{{printf "%s\n" $key}}{{end}}'
```

The output must include `clientID` and `clientSecret`.

## Step 2: Configuring the registration

Set `oauth2ClientCredentials` on the registration. Replace `tokenURL` with the authorization server token endpoint, not its issuer URL or authorization endpoint. Replace `tools:read` with a scope supported by your provider, or omit `scopes` if none are required.

For a new registration, apply the manifest below. For an existing registration that uses `credentialRef`, use the migration patch later in this step instead.

```bash
kubectl apply -f - <<'EOF'
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-mcp-server
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-mcp-server-route
  prefix: myserver_
  path: /mcp
  oauth2ClientCredentials:
    tokenURL: https://auth.example.com/realms/mcp/protocol/openid-connect/token
    secretRef:
      name: my-mcp-server-oauth
    scopes:
      - tools:read
EOF
```

`tokenURL` must use HTTPS. Token requests have a 10-second timeout and do not follow redirects; use the final endpoint URL. Restrict registration write access and broker network egress to trusted administrators and destinations: the broker sends the referenced client credentials to this endpoint.

`credentialRef` and `oauth2ClientCredentials` are mutually exclusive. To migrate an existing registration from a static credential, remove `credentialRef` and add the OAuth configuration in the same update. For example, this merge patch leaves the existing route and prefix unchanged:

```bash
kubectl patch mcpsr my-mcp-server -n mcp-test --type=merge -p '{
  "spec": {
    "credentialRef": null,
    "oauth2ClientCredentials": {
      "tokenURL": "https://auth.example.com/realms/mcp/protocol/openid-connect/token",
      "secretRef": {"name": "my-mcp-server-oauth"},
      "scopes": ["tools:read"]
    }
  }
}'
```

For a private certificate authority (CA), configure [custom CA certificates](./custom-ca-certificates.md). Token requests use the same additive trust pool as the upstream connection: system roots, the gateway `caCertBundleRef`, and the registration `caCertSecretRef`. Include the token endpoint CA as well as the upstream CA if they differ. If only the token endpoint needs a private CA and the MCP server uses plain HTTP, use the gateway bundle: setting `caCertSecretRef` also switches the MCP endpoint to HTTPS.

Wait for the controller to accept the registration:

```bash
kubectl wait --for=condition=Ready mcpsr/my-mcp-server -n mcp-test --timeout=120s
```

> **Note:** The registration `Ready` condition confirms that the controller wrote the configuration. It does not confirm that the broker obtained a token or discovered tools.

## Step 3: Verifying authenticated discovery

Forward the broker service port from the MCPGatewayExtension namespace:

```bash
kubectl port-forward -n mcp-system service/mcp-gateway 8080:8080
```

In another terminal, inspect the upstream entry in the broker status response:

```bash
curl --fail --silent --show-error http://localhost:8080/status \
  | jq '.servers[] | select(.name == "mcp-test/my-mcp-server") | {name, ready, totalTools, message}'
```

Wait for `ready: true` and verify that `totalTools` matches the tools exposed to the configured service account. An absent entry means the broker has not loaded this registration; check the registration conditions and that you selected the correct gateway service. A `ready: false` entry contains a diagnostic `message`.

Connect your MCP client to the gateway using its normal client credentials and verify that the upstream tools are available with the `myserver_` prefix. Successful discovery does not prove that client tool calls are authorized: the broker does not attach its OAuth token to those calls.

Stop the port-forward with Ctrl+C when finished.

## Token renewal and credential rotation

The broker caches access tokens and obtains a new token through the client credentials grant when a request needs one near or after expiry. No refresh token or manual bearer-token replacement is required. An upstream HTTP 401 response discards the cached token so a subsequent request can obtain a new one; it does not guarantee that the failed request succeeds immediately.

To rotate credentials, update `clientID` or `clientSecret` in the same labeled Secret using your Secret management workflow, or reapply the Secret from Step 1 with the new values. The controller reconciles registrations that reference the Secret, and the broker reloads the configuration and reconnects using the updated credentials without a pod restart. Repeat Step 3 to verify recovery after propagation.

## Troubleshooting authentication failures

Inspect controller conditions first:

```bash
kubectl describe mcpsr my-mcp-server -n mcp-test
```

For broker-side failures, check `/status` and the broker-router logs:

```bash
kubectl logs -n mcp-system deployment/mcp-gateway --since=10m
```

| Symptom | Check |
|---------|-------|
| The API rejects the registration | Use an HTTPS `tokenURL`, a nonempty `secretRef.name`, and at most 10 nonempty scope entries. Remove `credentialRef` when configuring OAuth. |
| The registration is not Ready | Verify that the Secret exists in the registration namespace, has the managed-secret label, and contains nonempty `clientID` and `clientSecret` keys. Also check the HTTPRoute and MCPGatewayExtension conditions. |
| Broker status reports `failed to obtain access token` | Check the broker logs for `token request failed`. Token endpoint HTTP status and OAuth error codes such as `invalid_client` or `invalid_scope` help distinguish bad credentials from invalid scopes. Check DNS, network egress, TLS trust, the endpoint URL, and the 10-second timeout. Redirects are rejected. |
| Token acquisition succeeds but the upstream returns 401 or 403 | Verify that the upstream accepts tokens issued to this client, with the required audience, scopes, and permissions. There is no dedicated audience parameter in `oauth2ClientCredentials`; configure the provider to issue a token acceptable to the upstream. |
| Discovery works but a client tool call fails | Configure authentication and authorization for that client-to-upstream request. Broker credentials are not used for client tool calls. |

Token acquisition errors in `/status` are sanitized. Do not dump credential Secrets or access tokens when collecting diagnostics.

## Next steps

- [MCPServerRegistration reference](../reference/mcpserverregistration.md#oauth2clientcredentialsconfig)
- [Authenticating MCP clients](./authentication.md)
- [Authorizing tool access](./authorization.md)
- [Collecting per-user upstream tokens](./url-elicitation.md)
