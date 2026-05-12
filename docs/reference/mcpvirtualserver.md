# The MCPVirtualServer Custom Resource Definition (CRD)

- [MCPVirtualServer](#mcpvirtualserver)
- [MCPVirtualServerSpec](#mcpvirtualserverspec)

## MCPVirtualServer

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `spec` | [MCPVirtualServerSpec](#mcpvirtualserverspec) | Yes | The specification for MCPVirtualServer custom resource |

## MCPVirtualServerSpec

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `description` | String | No | Human-readable description of this virtual server's purpose |
| `tools` | []String | Yes | List of tool names to expose through this virtual server. Must contain at least one tool. Tools must be available from the underlying MCP servers configured in the system |
| `prompts` | []String | No | List of prompt names to expose through this virtual server. When omitted, all prompts are exposed. Prompts must be available from the underlying MCP servers configured in the system |
