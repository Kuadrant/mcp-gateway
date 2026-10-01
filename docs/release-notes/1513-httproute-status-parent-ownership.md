# Fix HTTPRoute Status Ownership and MCPServerRegistration Deletion

MCP Gateway now writes its HTTPRoute `Programmed` condition into a status parent
entry owned by `mcp.kuadrant.io/mcp-gateway`. This fixes MCPServerRegistration
deletions getting stuck after Kuadrant policies are removed.

## What's Changed

- Each accepted gateway parent has a separate MCP-owned entry with
  `Programmed=True`. Other controllers' conditions are preserved.
- Legacy `Programmed` conditions previously written by MCP into other controllers'
  entries are removed. Entries left without conditions are removed entirely.
- Deleting an MCPServerRegistration removes MCP's `Programmed` condition only when
  no other active registration references the same HTTPRoute.

## Migration

No manifest changes are required.

- Update automation that selects `status.parents[]` by position or expects MCP's
  `Programmed` condition on the gateway controller's entry. Select by
  `controllerName: mcp.kuadrant.io/mcp-gateway` instead.
- Legacy condition cleanup happens on each MCPServerRegistration's first reconcile
  after upgrade. A controller restart triggers reconciliation; cleanup is not
  instantaneous at upgrade.
- A route that no gateway has accepted now receives no MCP `Programmed` condition.
- Deleting one of several registrations on a shared route leaves MCP's
  `Programmed` condition in place. Deleting the last active registration removes it.
