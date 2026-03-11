# The MCPGatewayExtension Custom Resource Definition (CRD)

- [MCPGatewayExtension](#mcpgatewayextension)
- [MCPGatewayExtensionSpec](#mcpgatewayextensionspec)
- [MCPGatewayExtensionTargetReference](#mcpgatewayextensiontargetreference)
- [TrustedHeadersKey](#trustedheaderskey)
- [SessionStore](#sessionstore)
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
| `privateHost` | String | No | Overrides the internal host used for hair-pinning requests back through the gateway. Defaults to `<gateway>-istio.<ns>.svc.cluster.local:<port>` |
| `backendPingIntervalSeconds` | Integer | No | How often (in seconds) the broker pings upstream MCP servers. Min: 10, Max: 7200, Default: 60 |
| `trustedHeadersKey` | [TrustedHeadersKey](#trustedheaderskey) | No | Configures trusted-header key pair for JWT-based tool filtering. When set, the public key secret is injected into the broker deployment via the `TRUSTED_HEADER_PUBLIC_KEY` env var |
| `httpRouteManagement` | String | No | Controls whether the operator manages the gateway HTTPRoute. `Enabled` (default): creates and manages the HTTPRoute. `Disabled`: does not create an HTTPRoute. Disabling does not delete a previously created route |
| `sessionStore` | [SessionStore](#sessionstore) | No | References a secret for redis-based session storage. When not set, in-memory session storage is used |

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
| `secretName` | String | Yes | Name of the secret containing a `REDIS_URL` data entry. The value should be a redis connection string (`redis://<user>:<pass>@<host>:<port>/<db>`). The secret must exist in the MCPGatewayExtension namespace. Injected as `CACHE_CONNECTION_STRING` env var into the broker-router deployment |

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
| `SecretInvalid` | A referenced secret lacks the required data entry (`key` for trusted headers, `REDIS_URL` for session store) |
