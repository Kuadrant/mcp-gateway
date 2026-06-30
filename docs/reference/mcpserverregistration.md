# The MCPServerRegistration Custom Resource Definition (CRD)

- [MCPServerRegistration](#mcpserverregistration)
- [MCPServerRegistrationSpec](#mcpserverregistrationspec)
- [TargetReference](#targetreference)
- [SecretReference](#secretreference)
- [CACertSecretReference](#cacertsecretreference)
- [TokenURLElicitationConfig](#tokenurelicitationconfig)
- [MCPServerRegistrationStatus](#mcpserverregistrationstatus)

## MCPServerRegistration

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `spec` | [MCPServerRegistrationSpec](#mcpserverregistrationspec) | Yes | The specification for MCPServerRegistration custom resource |
| `status` | [MCPServerRegistrationStatus](#mcpserverregistrationstatus) | No | The status for the custom resource |

## MCPServerRegistrationSpec

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `targetRef` | [TargetReference](#targetreference) | Yes | An HTTPRoute that points to a backend MCP server. The controller discovers the backend service from this HTTPRoute and configures the broker to federate its tools |
| `prefix` | String | No | Prefix added to all federated tools from referenced servers. Avoids naming conflicts when aggregating tools from multiple sources (e.g. `server1_search` and `server2_search`). Must match `^[a-z0-9][a-z0-9_]*$`. Immutable once set |
| `path` | String | No | URL path where the MCP server endpoint is exposed. Default: `/mcp` |
| `credentialRef` | [SecretReference](#secretreference) | No | Reference to a Secret containing authentication credentials used exclusively by the broker for tool discovery and session management. Never injected into client `tools/call` requests. The secret must have the label `mcp.kuadrant.io/secret=true` |
| `state` | String | No | Desired operational state of the server. Enum: `Enabled` (default), `Disabled`. When set to `Disabled`, the broker stops connecting to the server and removes its tools from the gateway. The server can be re-enabled at any time by setting this field back to `Enabled` |
| `caCertSecretRef` | [CACertSecretReference](#cacertsecretreference) | No | Reference to a Secret containing a PEM-encoded CA certificate bundle. The broker uses this CA to verify TLS connections to the upstream MCP server. The secret must have the label `mcp.kuadrant.io/secret=true`. CA cert data must not exceed 64 KiB |
| `tokenURLElicitation` | [TokenURLElicitationConfig](#tokenurlelicitationconfig) | No | Enables per-user token collection via URL elicitation (-32042 flow). When set, the router collects tokens from elicitation-capable clients at tool-call time. See [URL Elicitation guide](../guides/url-elicitation.md) |
| `userSpecificList` | String (`Enabled` / `Disabled`) | No | When `Enabled`, the broker fetches tools from this server per-user using their session headers instead of caching the service account's tool list. When `Enabled`, the `prefix` field is required (enforced by CEL validation). Default: `Disabled` |
| `category` | []String | No | One or more categories for tool discovery filtering. Used by `discover_tools` to let agents filter servers by category. Default: `["uncategorised"]`. Max 3 items, max 128 chars each |
| `hint` | String | No | Short description of what this MCP server offers. Returned by `discover_tools` to help agents decide which tools to select. Max 256 chars |
| `tags` | []String | No | Arbitrary labels for this MCP server. Used to filter and discover tools via the `list_tags` and `filter_tools_by_tags` broker tools. Max 10 items, 1-128 chars each |

## TargetReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `group` | String | No | Group of the target resource. Default: `gateway.networking.k8s.io` |
| `kind` | String | No | Kind of the target resource. Default: `HTTPRoute` |
| `name` | String | Yes | Name of the target HTTPRoute |
| `namespace` | String | No | Namespace of the target resource. Defaults to same namespace |

## SecretReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `name` | String | Yes | Name of the Secret resource |
| `key` | String | No | Key within the Secret that contains the credential value. Default: `token` |

## CACertSecretReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `name` | String | Yes | Name of the Secret resource containing the CA certificate |
| `key` | String | No | Key within the Secret that contains the PEM-encoded CA certificate. Default: `ca.crt` |

## TokenURLElicitationConfig

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `url` | String | No | Overrides the default broker token page URL. When set, users are directed to this external URL instead of the built-in page. The gateway appends `?elicitation_id=<id>` to the URL |

`tokenURLElicitation` and `credentialRef` serve different purposes: `credentialRef` provides the broker with credentials for tool discovery, while `tokenURLElicitation` collects per-user tokens at tool-call time.

**Examples:**

```yaml
# Minimal: uses the built-in broker token page
spec:
  credentialRef:
    name: my-server-cred
  tokenURLElicitation: {}
```

```yaml
# External URL: directs users to a custom credential page
spec:
  credentialRef:
    name: my-server-cred
  tokenURLElicitation:
    url: "https://vault.example.com/ui/tokens"
```

### Custom CA Certificate

To connect to an upstream MCP server that uses a private CA (e.g. OpenShift service-serving CA, cert-manager, self-signed):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-server-ca
  labels:
    mcp.kuadrant.io/secret: "true"
type: Opaque
data:
  ca.crt: <base64-encoded-PEM-CA-bundle>
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-server
spec:
  targetRef:
    name: my-server-route
  caCertSecretRef:
    name: my-server-ca
```

## MCPServerRegistrationStatus

| **Field** | **Type** | **Description** |
|-----------|----------|-----------------|
| `conditions` | [][Kubernetes meta/v1.Condition](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#Condition) | List of conditions that define the status of the resource |
