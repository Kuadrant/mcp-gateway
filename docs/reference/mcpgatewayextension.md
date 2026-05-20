# The MCPGatewayExtension Custom Resource Definition (CRD)

- [MCPGatewayExtension](#mcpgatewayextension)
- [MCPGatewayExtensionSpec](#mcpgatewayextensionspec)
- [MCPGatewayExtensionTargetReference](#mcpgatewayextensiontargetreference)
- [TrustedHeadersKey](#trustedheaderskey)
- [SessionStore](#sessionstore)
- [AuditConfig](#auditconfig)
- [MCPGatewayExtensionStatus](#mcpgatewayextensionstatus)

## MCPGatewayExtension

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `spec` | [MCPGatewayExtensionSpec](#mcpgatewayextensionspec) | Yes | The specification for MCPGatewayExtension custom resource |
| `status` | [MCPGatewayExtensionStatus](#mcpgatewayextensionstatus) | No | The status for the custom resource |

## MCPGatewayExtensionSpec

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `targetRef` | [MCPGatewayExtensionTargetReference](#mcpgatewayextensiontargetreference) | Yes | The Gateway listener to extend with MCP protocol support |
| `publicHost` | String | No | Overrides the public host derived from the listener hostname. Use when the listener has a wildcard and you need a specific host |
| `privateHost` | String | No | Overrides the internal host used for hair-pinning requests back through the gateway. Defaults to `<gateway>-istio.<ns>.svc.cluster.local:<port>`, with an `https://` scheme prefix when the targeted Gateway listener uses the HTTPS protocol. The supplied value is honoured verbatim, so an operator can include a scheme (e.g. `https://my-gw:443`) or pin to a different port. |
| `backendPingIntervalSeconds` | Integer | No | How often (in seconds) the broker pings upstream MCP servers. Min: 10, Max: 7200, Default: 60 |
| `trustedHeadersKey` | [TrustedHeadersKey](#trustedheaderskey) | No | Configures trusted-header key pair for JWT-based tool filtering. When set, the public key secret is injected into the broker deployment via the `TRUSTED_HEADER_PUBLIC_KEY` env var |
| `httpRouteManagement` | String | No | Controls whether the operator manages the gateway HTTPRoute. `Enabled` (default): creates and manages the HTTPRoute. `Disabled`: does not create an HTTPRoute. Disabling does not delete a previously created route |
| `sessionStore` | [SessionStore](#sessionstore) | No | References a secret for redis-based session storage. When not set, in-memory session storage is used |
| `urlElicitation` | String | No | Controls URL-based token elicitation. `Enabled`: creates a separate `/tokens` HTTPRoute and passes `--enable-url-elicitation` to the broker. `Disabled` (default): no `/tokens` route is created |
| `audit` | [AuditConfig](#auditconfig) | No | Configures the MCP audit trail via Envoy access logs. When set, the operator adds an access log to the gateway and injects audit env vars into the router deployment. When not set, no audit access log is added |

## MCPGatewayExtensionTargetReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `group` | String | Yes | Group of the target resource. Default: `gateway.networking.k8s.io` |
| `kind` | String | Yes | Kind of the target resource. Default: `Gateway` |
| `name` | String | Yes | Name of the target Gateway |
| `namespace` | String | No | Namespace of the target Gateway. Defaults to the MCPGatewayExtension namespace. Cross-namespace references require a ReferenceGrant |
| `sectionName` | String | Yes | Name of a listener on the target Gateway. The controller reads the listener's port and hostname to configure the MCP Gateway instance |

## TrustedHeadersKey

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `secretName` | String | Yes | Name of the secret containing the PEM-encoded public key used by the broker to verify trusted-header JWTs. The secret must have a data entry with key `key`. When `generate` is `Enabled`, the operator creates this secret |
| `generate` | String | No | Controls whether the operator generates an ECDSA P-256 key pair. `Enabled`: creates `<secretName>` (public key) and `<secretName>-private` (private key) with owner references. `Disabled` (default): the secret must already exist. Changing this field requires deleting the existing secrets first to ensure the keys are a matching pair |

## SessionStore

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `secretName` | String | Yes | Name of the secret containing a `CACHE_CONNECTION_STRING` data entry. The value should be a redis connection string (`redis://<user>:<pass>@<host>:<port>/<db>`). The secret must exist in the MCPGatewayExtension namespace and must have the label `mcp.kuadrant.io/secret: "true"`. Injected as `CACHE_CONNECTION_STRING` env var into the broker-router deployment |

## AuditConfig

Configuration options for configuring the model context protocol (MCP) audit trail via Envoy access logs.

> [!NOTE]
> The configuration fields in `AuditConfig` depend on the auditing infrastructure introduced in issue #992 and may not be available in older versions of the `MCPGatewayExtension` CRD.

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `parameterLogging` | String | No | Controls whether tool call parameters are included in the audit trail. <br/>- **Allowed Values**: `Enabled`, `Disabled`<br/>- **Default**: `Disabled`<br/>- **Behavior**: When `Enabled`, the arguments (`params.arguments`) from `tools/call` requests are parsed, serialized to JSON, and logged (truncated to a maximum of 1KB to protect log performance and storage). |
| `identityHeaders` | []String | No | An ordered list of HTTP header names to inspect for the caller's identity when the W3C Baggage `user.id` field is absent or empty. <br/>- **Default**: `["x-forwarded-email", "x-auth-user"]`<br/>- **Behavior**: The gateway checks these headers in the specified order and uses the first non-empty value. |

### Example Schema

```yaml
audit:
  parameterLogging: Enabled
  identityHeaders:
    - x-forwarded-email
    - x-auth-user
    - x-authenticated-userid
```

## MCPGatewayExtensionStatus

| **Field** | **Type** | **Description** |
|-----------|----------|-----------------|
| `conditions` | [][Kubernetes meta/v1.Condition](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#Condition) | List of conditions that define the status of the resource |

### Conditions

| **Type** | **Description** |
|----------|-----------------|
| `Ready` | Indicates whether the MCPGatewayExtension is fully configured: the broker-router deployment is running, the EnvoyFilter has been applied, and trusted headers (if configured) are valid |

### Condition Reasons

| **Reason** | **Description** |
|------------|-----------------|
| `ValidMCPGatewayExtension` | The MCPGatewayExtension is valid and ready |
| `InvalidMCPGatewayExtension` | Invalid configuration detected |
| `ReferenceGrantRequired` | A ReferenceGrant is missing for a cross-namespace Gateway reference |
| `DeploymentNotReady` | The broker-router deployment is not ready |
| `SecretNotFound` | A referenced secret is missing (trusted headers or session store) |
| `SecretInvalid` | A referenced secret lacks the required data entry (`key` for trusted headers, `CACHE_CONNECTION_STRING` for session store) |
