# SEP-991 Impact Assessment

## Background

SEP-991 introduces OAuth Client ID Metadata Documents (CIMD) as the preferred client registration mechanism for MCP and changes Dynamic Client Registration (DCR) from a SHOULD to a MAY.

The purpose of this document is to assess the impact of SEP-991 on the current MCP Gateway authentication design, examples, and documentation.

## Current MCP Gateway Assumptions

### Auth Phase 1

The current authentication design references Dynamic Client Registration (DCR) as part of the MCP OAuth flow and expects the identity provider to support DCR.

### Authentication Guide

The authentication guide currently describes:

* Dynamic Client Registration support
* Automatic client registration
* Anonymous client registration for development environments
* OAuth flows that include dynamic client registration

### Auth Phase 2

The phase 2 design currently assumes OpenID Connect clients are created through Dynamic Client Registration.

### Authentication Sequence Diagrams

Current authentication sequence diagrams include explicit registration steps between the MCP client and the authorization server.

## SEP-991 Summary

SEP-991 introduces OAuth Client ID Metadata Documents (CIMD) as an additional client registration mechanism.

Key changes include:

* Client IDs may be HTTPS URLs.
* Client metadata is hosted at the client_id URL.
* Authorization servers can validate metadata without maintaining dynamic registration state.
* Dynamic Client Registration remains supported but is no longer the preferred default approach.

## Impact Analysis

### Protected Resource Metadata

No immediate impact has been identified for the MCP Gateway Protected Resource Metadata endpoint.

The existing resource metadata discovery flow appears compatible with SEP-991 because it is independent of the client registration mechanism.

### Authentication Flows

Current examples and diagrams are DCR-centric and may require updates to reflect CIMD-based flows.

### Documentation

The following documentation should be reviewed:

* docs/guides/authentication.md
* docs/design/auth-phase-1.md
* docs/design/auth-phase-2.md
* docs/design/flows.md

## Open Questions

### Keycloak Compatibility

Investigation required:

* Does Keycloak support OAuth Client ID Metadata Documents?
* Is support native or does it require extensions/customization?
* Should MCP Gateway examples continue to prefer DCR when using Keycloak?

### Recommended Direction

Potential approaches:

1. Continue documenting DCR-based examples where required by provider limitations.
2. Add CIMD-based examples where supported.
3. Document both approaches and explain fallback behavior.

## Status

Investigation in progress.
