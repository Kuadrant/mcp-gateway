# SEP-991 Impact Assessment

## Background

SEP-991 introduces OAuth Client ID Metadata Documents (CIMD) as the preferred client registration mechanism for the Model Context Protocol (MCP). Under this proposal, OAuth clients use an HTTPS URL as their `client_id`, where the URL resolves to a JSON metadata document describing the client. Dynamic Client Registration (DCR) remains supported but is no longer the preferred registration mechanism for deployments where clients and authorization servers have no pre-existing relationship.

The purpose of this document is to assess the impact of SEP-991 on the current MCP Gateway authentication design, documentation, and examples.

---

## Current MCP Gateway Assumptions

### Auth Phase 1

The current authentication design references Dynamic Client Registration (DCR) as part of the recommended OAuth flow and assumes the authorization server supports DCR.

### Authentication Guide

The authentication guide currently describes:

- Dynamic Client Registration support
- Automatic client registration
- Anonymous client registration for development environments
- OAuth authorization flows that include Dynamic Client Registration

### Auth Phase 2

The Phase 2 authentication design assumes OpenID Connect clients are created through Dynamic Client Registration.

### Authentication Sequence Diagrams

Current authentication sequence diagrams model Dynamic Client Registration as the primary client registration mechanism between the MCP client and the authorization server.

---

## SEP-991 Summary

SEP-991 introduces OAuth Client ID Metadata Documents (CIMD) as the preferred client registration mechanism for MCP while retaining Dynamic Client Registration as an optional interoperability mechanism.

Key changes include:

- `client_id` values may be HTTPS URLs.
- Client metadata is published at the `client_id` URL.
- Authorization servers retrieve and validate client metadata instead of maintaining dynamic registration state.
- Dynamic Client Registration remains available as a compatibility mechanism rather than the preferred registration model.
- Authorization servers advertise support for Client ID Metadata Documents through OAuth Authorization Server Metadata.

---

## Impact Analysis

### Protected Resource Metadata

Based on the current MCP Gateway design, Protected Resource Metadata discovery remains independent of the client registration mechanism. The existing resource metadata discovery flow is compatible with both Dynamic Client Registration and Client ID Metadata Documents, and no architectural changes have been identified for this component.

### Authentication Flows

Current authentication examples and sequence diagrams are centered around Dynamic Client Registration. Under SEP-991, these examples should be updated to present Client ID Metadata Documents as the preferred registration mechanism while continuing to document Dynamic Client Registration as a compatibility option for authorization servers that support it.

### Documentation

The following documentation should be reviewed and updated:

- `docs/guides/authentication.md`
- `docs/design/auth-phase-1.md`
- `docs/design/auth-phase-2.md`
- `docs/design/flows.md`

The primary documentation changes are:

- Introduce Client ID Metadata Documents as the preferred client registration mechanism.
- Update authentication sequence diagrams to reflect the CIMD-based flow.
- Retain Dynamic Client Registration examples as a compatibility path for deployments that continue to rely on DCR.
- Clarify that Protected Resource Metadata discovery is independent of the client registration mechanism.

---

## Keycloak Compatibility

Current Keycloak documentation primarily documents OpenID Connect Dynamic Client Registration (RFC 7591 and RFC 7592).

Recent development activity within the Keycloak project indicates ongoing work to add native support for OAuth Client ID Metadata Documents (CIMD), including implementation work and MCP compatibility improvements. Until that work reaches general availability, MCP Gateway documentation should avoid assuming universal CIMD support across Keycloak deployments.

---

## Recommended Direction

SEP-991 changes the preferred client registration mechanism without changing the overall OAuth authorization architecture used by MCP Gateway.

Based on this assessment, the recommended direction is:

- Continue documenting Dynamic Client Registration as a compatibility mechanism for existing identity providers.
- Update authentication documentation and sequence diagrams to present Client ID Metadata Documents as the preferred registration mechanism where supported.
- Document Dynamic Client Registration as a fallback when authorization servers do not yet implement CIMD.
- Preserve the existing Protected Resource Metadata discovery flow, as it remains independent of the client registration mechanism.

---

## Conclusion

SEP-991 primarily changes how OAuth clients are identified and registered rather than changing the overall OAuth authorization flow used by MCP Gateway.

Based on the current design documentation, no architectural changes have been identified for the gateway's Protected Resource Metadata implementation. The primary impact of SEP-991 is on authentication documentation, client registration guidance, and sequence diagrams, which should be updated to present Client ID Metadata Documents as the preferred registration mechanism while retaining Dynamic Client Registration as a compatibility path.

---

## References

### Specifications

- SEP-991: OAuth Client ID Metadata Documents for MCP
- OAuth 2.0 Dynamic Client Registration Protocol (RFC 7591)
- OAuth 2.0 Dynamic Client Registration Management Protocol (RFC 7592)
- OAuth 2.0 Authorization Server Metadata (RFC 8414)
- OAuth 2.0 Protected Resource Metadata (RFC 9728)

### MCP Gateway Documentation Reviewed

- `docs/guides/authentication.md`
- `docs/design/auth-phase-1.md`
- `docs/design/auth-phase-2.md`
- `docs/design/flows.md`