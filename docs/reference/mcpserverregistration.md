# The MCPServerRegistration Custom Resource Definition (CRD)

- [MCPServerRegistration](#mcpserverregistration)
- [MCPServerRegistrationSpec](#mcpserverregistrationspec)
- [TargetReference](#targetreference)
- [SecretReference](#secretreference)
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
| `toolPrefix` | String | No | Prefix added to all federated tools from referenced servers. Avoids naming conflicts when aggregating tools from multiple sources (e.g. `server1_search` and `server2_search`). Immutable once set |
| `path` | String | No | URL path where the MCP server endpoint is exposed. Default: `/mcp` |
| `credentialRef` | [SecretReference](#secretreference) | No | Reference to a Secret containing authentication credentials. The secret must have the label `mcp.kuadrant.io/secret=true`. Credentials are made available to the broker via `KAGENTI_{NAME}_CRED` env vars |

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

## MCPServerRegistrationStatus

| **Field** | **Type** | **Description** |
|-----------|----------|-----------------|
| `conditions` | [][Kubernetes meta/v1.Condition](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#Condition) | List of conditions that define the status of the resource |
| `discoveredTools` | Integer | Number of tools discovered from this MCPServerRegistration |
