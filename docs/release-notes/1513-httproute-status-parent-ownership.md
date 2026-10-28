# Fix HTTPRoute Status Ownership and MCPServerRegistration Deletion

MCP Gateway now writes its HTTPRoute `Programmed` condition into a status parent
entry owned by `mcp.kuadrant.io/mcp-gateway`. This fixes MCPServerRegistration
deletions getting stuck after Kuadrant policies are removed.

## What's Changed

- Each accepted gateway parent that attaches to a listener targeted by an
  MCPGatewayExtension has a separate MCP-owned entry with `Programmed=True`.
  Other parents get no MCP entry. Other controllers' conditions are preserved.
- Legacy `Programmed` conditions previously written by MCP into other controllers'
  entries are removed. Entries left without conditions are removed entirely.
- MCP's `Programmed` condition is removed when an MCPServerRegistration is deleted
  or stops being ready, for example when its MCPGatewayExtension is removed or its
  prefix conflicts. Previously it stayed until deletion. It is kept while another
  registration on the same HTTPRoute is still Ready or Disabled.

## Migration

No manifest changes are required.

- Update automation that selects `status.parents[]` by position or expects MCP's
  `Programmed` condition on the gateway controller's entry. Select by
  `controllerName: mcp.kuadrant.io/mcp-gateway` instead.
- Legacy condition cleanup happens on each MCPServerRegistration's first reconcile
  after upgrade. A controller restart triggers reconciliation; cleanup is not
  instantaneous at upgrade.
- A route that no gateway has accepted now receives no MCP `Programmed` condition.
- On a shared route, MCP's `Programmed` condition stays while any registration on
  it is Ready or Disabled. It is removed once the last of them is deleted or stops
  being ready.
