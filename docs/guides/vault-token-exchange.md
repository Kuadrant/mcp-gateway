# GitHub MCP Server with Vault Token Exchange

This guide demonstrates connecting to the GitHub MCP server through MCP Gateway, using HashiCorp Vault to manage per-user GitHub Personal Access Tokens (PATs).

## Overview

When a user makes an MCP request:

1. AuthPolicy validates the user's OIDC token
2. Authorino authenticates to Vault using the user's access token
3. Authorino fetches the user's GitHub PAT from Vault, keyed by their OIDC subject
4. The PAT is injected into the `Authorization` header for the upstream MCP server

## Prerequisites

- MCP Gateway installed and configured
- [External GitHub MCP server](./external-mcp-server.md) set up (Steps 1-5)
- HashiCorp Vault deployed and accessible from the cluster -- see [Vault installation docs](https://developer.hashicorp.com/vault/docs/install)
- Vault KV v2 secrets engine enabled at `secret/`
- OIDC provider configured -- see [Authentication](./authentication.md)
- Kuadrant with Authorino installed

## Step 1: Configure Vault for Authorino access

Configure Vault's JWT auth method so Authorino can verify user tokens against your OIDC provider. For instructions on enabling JWT authentication, see [Vault JWT auth documentation](https://developer.hashicorp.com/vault/api-docs/auth/jwt#configure). For a simpler development setup using Vault's root token, see the [Vault integration guide](./vault-integration.md#using-a-vault-root-token).

Create a Vault policy granting read access to MCP Gateway secrets. In production, use a templated path so each user's Vault token can only read their own secret:

```sh
vault policy write authorino - <<EOF
path "secret/data/mcp-gateway/{{identity.entity.aliases.AUTH_JWT_ACCESSOR.name}}" {
  capabilities = ["read"]
}
EOF
```

Replace `AUTH_JWT_ACCESSOR` with the accessor value from `vault auth list` for the JWT auth method. For development, a wildcard path is simpler:

```sh
vault policy write authorino - <<EOF
path "secret/data/mcp-gateway/*" {
  capabilities = ["read"]
}
EOF
```

Create a Vault role that accepts user JWTs:

```sh
vault write auth/jwt/role/authorino - <<EOF
{
  "role_type": "jwt",
  "bound_audiences": ["<your-client-id>"],
  "user_claim": "sub",
  "policies": ["authorino"],
  "ttl": "1h"
}
EOF
```

> **Note:** `bound_audiences` must match an audience (`aud`) claim present in the user's access token. Vault requires at least one entry if the token contains an `aud` claim.

Verify the policy and role were created:

```sh
vault policy read authorino
vault read auth/jwt/role/authorino
```

## Step 2: Store a GitHub PAT in Vault

Store each user's GitHub PAT in Vault, keyed by their OIDC subject claim (`sub`):

```sh
vault kv put secret/mcp-gateway/<user-sub> github_pat="ghp_YOUR_GITHUB_TOKEN"
```

Replace `<user-sub>` with the user's OIDC subject identifier (e.g., a username or UUID, depending on your identity provider).

> **Note:** The `sub` claim must be present in the access token for the Vault path lookup to work. Some identity providers (e.g., Keycloak 26+) use lightweight access tokens that omit `sub` by default. Configure your IdP to include it, or use a different claim in the Vault URL expression.

The PAT needs at minimum `read:user` scope. Adjust scopes based on which GitHub MCP tools your users need access to.

Verify the secret was stored:

```sh
vault kv get secret/mcp-gateway/<user-sub>
```

## Step 3: Create the AuthPolicy

This AuthPolicy uses the user's own access token to authenticate to Vault and fetch their GitHub PAT. All requests require a valid JWT.

```sh
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: github-vault-policy
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  rules:
    authentication:
      "mcp-clients":
        jwt:
          # issuerUrl must be reachable by Authorino from within the cluster.
          # use jwksUrl with an in-cluster URL to bypass OIDC discovery if needed.
          issuerUrl: <your-oidc-issuer-url>
    metadata:
      "vault-login":
        priority: 0
        http:
          url: http://vault.vault.svc.cluster.local:8200/v1/auth/jwt/login
          method: POST
          body:
            expression: |
              "{\"role\": \"authorino\", \"jwt\": \"" + request.headers["authorization"].split("Bearer ")[1] + "\"}"
        cache:
          key:
            expression: auth.identity.sub
          ttl: 300
      "vault":
        priority: 1
        when:
        - predicate: auth.metadata.exists(p, p == "vault-login") && has(auth.metadata["vault-login"].auth) && has(auth.metadata["vault-login"].auth.client_token)
        http:
          urlExpression: |
            "http://vault.vault.svc.cluster.local:8200/v1/secret/data/mcp-gateway/" + auth.identity.sub
          method: GET
          headers:
            "X-Vault-Token":
              expression: auth.metadata["vault-login"].auth.client_token
    authorization:
      "found-vault-secret":
        patternMatching:
          patterns:
          - predicate: |
              has(auth.metadata.vault.data) && has(auth.metadata.vault.data.data) && has(auth.metadata.vault.data.data.github_pat) && type(auth.metadata.vault.data.data.github_pat) == string
    response:
      success:
        headers:
          "authorization":
            plain:
              expression: |
                "Bearer " + auth.metadata.vault.data.data.github_pat
EOF
```

The `vault-login` step authenticates to Vault using the user's JWT (cached per user for 5 minutes). The `vault` step fetches the secret at `secret/data/mcp-gateway/<sub>`. The authorization rule verifies the secret contains a `github_pat` field, and the response injects it into the request headers.

> **Note:** The MCPServerRegistration `credentialRef` provides a static PAT for broker tool discovery. The AuthPolicy above injects the per-user PAT at request time. Both are needed -- see the [external MCP server tutorial](../tutorials/external-mcp-server.md#create-secret).

If requests return 403 after applying this policy, check Authorino logs for vault-login errors (audience mismatch, expired tokens, unreachable Vault).

## Step 4: Verify

Check that the AuthPolicy is accepted:

```sh
kubectl get authpolicy github-vault-policy -n mcp-test
```

Connect to the gateway using the [MCP Inspector](https://modelcontextprotocol.io/docs/tools/inspector) or your MCP client. Log in with your OIDC credentials. Under Tools > List Tools, you should see GitHub tools with prefix `github_`. Calling `github_get_me` should return the GitHub user profile associated with the PAT stored in Vault for the authenticated user.

> [!NOTE]
> This example uses Keycloak as the OIDC provider. If you're testing locally with self-signed certificates, you may need to accept the Keycloak certificate in your browser first. Navigate to your Keycloak URL directly and accept the certificate warning before connecting.

## Cleanup

```sh
kubectl delete authpolicy github-vault-policy -n mcp-test
```

To remove the GitHub MCP server resources, see the [cleanup section](../tutorials/external-mcp-server.md#cleanup) in the external MCP server guide. You may also want to remove the Vault policy, role, and stored secrets created in Steps 1 and 2.

## Next Steps

- [Vault Integration](./vault-integration.md) -- general Vault integration patterns including root token setup for development
- [Authorization](./authorization.md) -- add tool-level access control on top of Vault credentials
- [External MCP Servers](./external-mcp-server.md) -- connect to other external MCP servers
