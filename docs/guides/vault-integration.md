# Integrating Vault with MCP Gateway

<br/>
<br/>

## Overview

The Kuadrant MCP Gateway provides a centralized way to connect AI agents to tools with the Model Context Protocol (MCP). Many backend MCP servers require sensitive credentials such as API keys or Personal Access Tokens (PATs) to access external APIs (for example, GitHub and Slack).

### Using Vault

You can use HashiCorp Vault to securely store these credentials and a Kuadrant AuthPolicy to retrieve and inject those credentials into the request flow. Authorino is used by Kuadrant to add authorization and authentication to APIs that do not have credential checks built-in. The essentials of the workflow include the following elements:

- MCP Gateway: Acts as the entry point for AI clients (e.g., Claude Code, VS Code).
- Authorino: The external authorization service used by Kuadrant to validate identities and fetch external metadata.
- HashiCorp Vault: The source of truth for secrets.
- AuthPolicy: The Kuadrant resource that defines how to authenticate the user and fetch their specific secret from Vault.

### Using an existing setup

If you already have a Kubernetes cluster, a central authorization tool that uses standard protocols like OpenID Connect or OAuth 2.0, and an MCP server ready to connect, use the AuthPolicy examples that follow to create your own object and apply it.

Adjust the commands and configuration in the following examples according to your use case. The goal of this documentation is to guide you on testing an AuthPolicy that speaks with an OIDC server on one side and with a Vault instance on the other.

The sample procedure presented below enforces an AuthPolicy that integrates MCP Gateway with Vault for requests targeting a specific MCP server. For enforcing the Vault integration policy on multiple servers, you can consider targeting a Gateway or individual gateway Listener instead. However, those details are beyond the scope of this documentation.

## Enable JWT authentication in your Vault server

For instructions on how to configure JWT authentication in Vault, see [Vault's documentation](https://developer.hashicorp.com/vault/api-docs/auth/jwt#configure). This method is for use cases where you do not want root access, which is more secure.

Make sure to create a Vault policy and Vault role that grants access for Authorino to read secrets at the `secret/data/mcp-gateway/*` path, or whatever path you decided to namespace the MCP server secrets.

<details>
<summary>Example Vault policy and role for Authorino</summary>

In general, you can create a Vault policy and Vault role for Authorino by running the following commands:
```sh
curl -H "X-Vault-Token: $VAULT_TOKEN" -H 'Content-Type: application/json' -X POST \
  --data '{
    "policy": "path \"secret/data/mcp-gateway/*\" {\n  capabilities = [\"read\", \"list\"]\n}"
  }' \
  http://localhost:8200/v1/sys/policies/acl/authorino
```
```sh
curl -H "X-Vault-Token: $VAULT_TOKEN" -H 'Content-Type: application/json' -X POST \
  --data '{
    "role_type": "jwt",
    "bound_audiences": ["authorino"],
    "user_claim": "sub",
    "policies": ["authorino"],
    "ttl": "1h"
  }'
```
</details>

### Create the AuthPolicy

Create an AuthPolicy to connect an external OIDC Identity Provider (IdP) with Vault to get a Vault token on behalf of the user or service that needs access to the MCP server data.

```sh
kubectl apply -f -<<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: vault-integration-policy
  namespace: mcp-test
spec:
  # Change it to target your MCP server route that requires fetching credentials from Vault or
  # the entire MCP Gateway listener used to route internal MCP traffic if all routes require Vault integration
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: mcp-server2-route
  rules:
    authentication:
      "mcp-clients":
        jwt:
          # Change it to the issuer URL of your OpenId Connect SSO provider
          # You can also use jwksUrl instead of issuerUrl for authentication servers that do not implement OIDC Discovery
          issuerUrl: <insert_issuer_URL_here>
    metadata:
      "oauth-token":
        priority: 0
        http:
         # Change it to the issuer URL of your OAuth provider
          url: <insert_oauth-token-issuer_URL_here>
          method: POST
          credentials:
            authorizationHeader:
              prefix: Basic
          sharedSecretRef:
            name: authorino-oauth-client
            key: client_secret
          bodyParameters:
            grant_type:
              value: client_credentials
            scope:
              value: openid
        cache:
          key:
            value: 'singleton'
          ttl: 1800 # 30 minutes
      "vault-login":
        priority: 1
        when:
        - predicate: auth.metadata.exists(p, p == "oauth-token") && has(auth.metadata["oauth-token"].access_token)
        http:
          # Change it to your Vault server URL.
          url: http://vault.vault.svc.cluster.local:8200/v1/auth/jwt/login
          method: POST
          body:
            expression: |
              "{\"role\": \"authorino\", \"jwt\": \"" + auth.metadata["oauth-token"].access_token + "\"}"
        cache:
          key:
            value: 'singleton'
          ttl: 3600 # 1 hour
      "vault":
        priority: 2
        when:
        - predicate: auth.metadata.exists(p, p == "vault-login") && has(auth.metadata["vault-login"].auth ) && has(auth.metadata["vault-login"].auth.client_token)
        http:
          # Change it to your Vault server URL and secret path. Adapt the auth-identity according to
          # how your claims uniquely identify an MCP Gateway user
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
              has(auth.metadata.vault.data) && has(auth.metadata.vault.data) && has(auth.metadata.vault.data.data.test_server2_pat) && type(auth.metadata.vault.data.data.test_server2_pat) == string
            # ‘test_server2_pat’ is hard-coded here as the entry inside of the Vault secret that contains
            # the user's PAT to authenticate with the targeted MCP server
            # For more than one MCP server in the scope of the AuthPolicy, you can make this entry
            # fetch the name of the server dynamically, depending on what you are targeting
    response:
      success:
        headers:
          "Authorization":
            plain:
              expression: |
                "Bearer " + auth.metadata.vault.data.data.test_server2_pat
      ---
apiVersion: v1
kind: Secret
metadata:
  name: authorino-oauth-client
  namespace: kuadrant-system
stringData:
  client_secret: <secret-value>
type: Opaque
EOF
```
- `oauth-token`: the policy makes a call to the OAuth provider.
- `vault-login`: the step that performs the JWT authentication against Vault.
- `vault`: fetches the secret from Vault.

<br/>
<br/>

## Using a Vault root token

This option is easier to use, but much less secure than using tokens configured with policies. Using the Vault root token for Authorino to authenticate to Vault gives Authorino full access to read and write any secret stored in Vault. Use a root token only for initial setup or in development environments.

The following `AuthPolicy` is an example. Update it with the specifications that apply to your use case.

### Create the AuthPolicy

```sh
kubectl apply -f -<<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: vault-integration-policy
  namespace: mcp-test
spec:
  # Change it to target your MCP server route that requires fetching credentials from Vault or
  # the entire MCP Gateway listener used to route internal MCP traffic if all routes require Vault integration
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: <mcp-server2-route>
  rules:
    authentication:
      "mcp-clients":
        jwt:
          issuerUrl: <your-issuer-url>
          # Use the issuer URL of your OpenId Connect SSO provider
          # Or an jwksUrl instead for authentication servers that do not implement OIDC Discovery
    metadata:
      "vault":
        http:
          # Use your Vault server URL and secret path
          urlExpression: |
            "https://vault.vault.svc.cluster.local:8200/v1/secret/data/mcp-gateway/" + auth.identity.sub
          method: GET
          credentials:
            customHeader:
              name: X-Vault-Token
          sharedSecretRef:
            name: vault-secret
            key: root-token
    authorization:
      "found-vault-secret":
        patternMatching:
          patterns:
          - predicate: |
              has(auth.metadata.vault.data) && has(auth.metadata.vault.data.data) && has(auth.metadata.vault.data.data.test_server2_pat) && type(auth.metadata.vault.data.data.test_server2_pat) == string
    response:
      success:
        headers:
          "Authorization":
            plain:
              expression: |
                "Bearer " + auth.metadata.vault.data.data.test_server2_pat
---
apiVersion: v1
kind: Secret
metadata:
  name: vault-secret
  namespace: kuadrant-system
stringData:
  root-token: root
type: Opaque
EOF
```

## Testing MCP Gateway integration with Vault

You can test your MCP Gateway integration by using the general steps that follow. Example commands are available in the details lists for reference.

### 1. Store a secret in Vault

<details>
<summary>Example curl command to store a vault token</summary>

```sh
curl -s -H "X-Vault-Token: $VAULT_TOKEN" -H 'Content-Type: application/json' -X POST \
  --data '{"data":{"test_server2_pat":"s3cr3t"}}' \
  http://localhost:8200/v1/secret/data/mcp-gateway/<sub>
```
</details>

### 2. Get an access token

<details>
<summary>Example access token request</summary>

```sh
ACCESS_TOKEN=$(curl <replace-with-your-issuer-url> -s -d 'grant_type=client_credentials' -d 'client_id=<mcp-client-id>' -d 'client_secret=<mcp-client-secret>' -d 'scope=openid profile groups roles' | jq -r .access_token)
```
</details>

### 3. Start a session with the MCP Gateway

You can initialize a session according to your own development environment's set up.

### 4. Send a request to the MCP server route that requires fetching credentials from Vault

You can send a request to the MCP server according to your own development environment's set up.

### Example output

The expected output shows that the request was successful and the `Authorization:` header was set using the secret fetched from Vault:

```jsonc
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "Authorization: [Bearer s3cr3t]"
      },
      …
    ],
    …
  }
}
```
