# The MCPGatewayExtension Custom Resource Definition (CRD)

- [MCPGatewayExtension](#mcpgatewayextension)
- [MCPGatewayExtensionSpec](#mcpgatewayextensionspec)
- [MCPGatewayExtensionTargetReference](#mcpgatewayextensiontargetreference)
- [CACertBundleReference](#cacertbundlereference)
- [TrustedHeadersKey](#trustedheaderskey)
- [SessionStore](#sessionstore)
- [OAuthProtectedResource](#oauthprotectedresource)
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
| `privateHost` | String | No | Overrides the internal host used for hair-pinning requests back through the gateway. Defaults to `<gateway>-<gatewayClassName>.<ns>.svc.cluster.local:<port>`, with an `https://` scheme prefix when the targeted Gateway listener uses the HTTPS protocol. The supplied value is honoured verbatim, so an operator can include a scheme (e.g. `https://my-gw:443`) or pin to a different port. |
| `backendPingIntervalSeconds` | Integer | No | How often (in seconds) the broker pings upstream MCP servers. Min: 10, Max: 7200, Default: 60 |
| `trustedHeadersKey` | [TrustedHeadersKey](#trustedheaderskey) | No | Configures trusted-header key pair for JWT-based tool filtering. When set, the public key secret is injected into the broker deployment via the `TRUSTED_HEADER_PUBLIC_KEY` env var |
| `httpRouteManagement` | String | No | Controls whether the operator manages the gateway HTTPRoute. `Enabled` (default): creates and manages the HTTPRoute. `Disabled`: does not create an HTTPRoute. Disabling does not delete a previously created route |
| `logLevel` | String | No | Controls the broker-router log verbosity. Maps to the broker's `--log-level` flag: `debug=-4`, `info=0`, `warn=4`, `error=8`. When unset, the operator-wide `BROKER_ROUTER_LOG_LEVEL` default is used if configured. Accepted values: `debug`, `info`, `warn`, `error` |
| `sessionStore` | [SessionStore](#sessionstore) | No | References a secret for redis-based session storage. When not set, in-memory session storage is used |
| `urlElicitation` | String | No | Controls URL-based token elicitation. `Enabled`: creates a separate `/tokens` HTTPRoute and passes `--enable-url-elicitation` to the broker. `Disabled` (default): no `/tokens` route is created |
| `caCertBundleRef` | [CACertBundleReference](#cacertbundlereference) | No | References a Secret containing a PEM-encoded CA certificate bundle used as the base trust pool for all upstream MCP server connections. Per-server `caCertSecretRef` on MCPServerRegistration appends to this pool. The Secret must have the label `mcp.kuadrant.io/secret: "true"` and must not exceed 256 KiB |
| `oauthProtectedResource` | [OAuthProtectedResource](#oauthprotectedresource) | No | Configures the OAuth protected resource metadata served at `/.well-known/oauth-protected-resource`. When set, the controller injects `OAUTH_*` env vars into the broker-router deployment |

## MCPGatewayExtensionTargetReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `group` | String | No | Group of the target resource. Default: `gateway.networking.k8s.io` |
| `kind` | String | No | Kind of the target resource. Default: `Gateway` |
| `name` | String | Yes | Name of the target Gateway |
| `namespace` | String | No | Namespace of the target Gateway. Defaults to the MCPGatewayExtension namespace. Cross-namespace references require a ReferenceGrant |
| `sectionName` | String | Yes | Name of a listener on the target Gateway. The controller reads the listener's port and hostname to configure the MCP Gateway instance |

## CACertBundleReference

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `name` | String | Yes | Name of the Secret containing the PEM-encoded CA certificate bundle. The Secret must exist in the MCPGatewayExtension namespace and must have the label `mcp.kuadrant.io/secret: "true"` |
| `key` | String | No | Key within the Secret data that contains the PEM data. Default: `ca.crt` |

The gateway CA bundle is written once to the config Secret as `gatewayCACertPEM`, replacing N per-server copies with a single entry. The broker loads it as the base trust pool for all upstream connections. Per-server `caCertSecretRef` on MCPServerRegistration appends additional CAs for backends with unique certificates.

Trust pool hierarchy: system roots, then gateway CA bundle (if set), then per-server CA (if set). Each layer is additive.

## TrustedHeadersKey

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `secretName` | String | Yes | Name of the secret containing the PEM-encoded public key used by the broker to verify trusted-header JWTs. The secret must have a data entry with key `key`. When `generate` is `Enabled`, the operator creates this secret |
| `generate` | String | No | Controls whether the operator generates an ECDSA P-256 key pair. `Enabled`: creates `<secretName>` (public key) and `<secretName>-private` (private key) with owner references. `Disabled` (default): the secret must already exist. Changing this field requires deleting the existing secrets first to ensure the keys are a matching pair |

## SessionStore

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `secretName` | String | Yes | Name of the secret containing a `CACHE_CONNECTION_STRING` data entry. The value should be a redis connection string (`redis://<user>:<pass>@<host>:<port>/<db>`). The secret must exist in the MCPGatewayExtension namespace and must have the label `mcp.kuadrant.io/secret: "true"`. Injected as `CACHE_CONNECTION_STRING` env var into the broker-router deployment |

## OAuthProtectedResource

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `authorizationServers` | []String | Yes | OAuth authorization server URLs. Injected as `OAUTH_AUTHORIZATION_SERVERS` (comma-separated) |
| `resourceName` | String | No | Human-readable name for this resource. Defaults to `"MCP Server"`. Injected as `OAUTH_RESOURCE_NAME` |
| `resource` | String | No | URI of the protected resource. Defaults to `https://<publicHost>/mcp`. Injected as `OAUTH_RESOURCE` |
| `bearerMethodsSupported` | []String | No | Supported bearer token methods. Defaults to `["header"]`. Injected as `OAUTH_BEARER_METHODS_SUPPORTED` (comma-separated) |
| `scopesSupported` | []String | No | Supported OAuth scopes. Defaults to `["basic"]`. Injected as `OAUTH_SCOPES_SUPPORTED` (comma-separated) |

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
| `SecretNotFound` | A referenced secret is missing (trusted headers, session store, or CA cert bundle) |
| `SecretInvalid` | A referenced secret lacks the required data entry (`key` for trusted headers, `CACHE_CONNECTION_STRING` for session store) or contains invalid PEM data (CA cert bundle) |
