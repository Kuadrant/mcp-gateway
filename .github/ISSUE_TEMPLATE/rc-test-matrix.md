---
name: RC Test Matrix
about: Test matrix checklist for a release candidate
title: "RC Test Matrix: vX.Y.Z-rcN"
labels: release-testing
---

## Release Candidate

- **Version**: vX.Y.Z-rcN
- **Release**: https://github.com/Kuadrant/mcp-gateway/releases/tag/vX.Y.Z-rcN
- **Branch**: release-X.Y.Z

## Installation

### Kind (primary)

- [ ] Fresh install via Helm on Kind cluster
- [ ] Fresh install via OLM on Kind cluster

### OpenShift (secondary, upstream components only)

- [ ] Fresh install via Helm on OpenShift
- [ ] Fresh install via OLM on OpenShift

## E2E tests (`make test-e2e`)

Run against Kind environment with RC images.

## Gevals integration tests

Run against Kind environment with RC images. See [evals/README.md](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/evals/README.md) for configuration and usage. Ref: [#601](https://github.com/Kuadrant/mcp-gateway/issues/601).

## MCP Conformance tests

Run against Kind environment with RC images. See [conformance workflow](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/.github/workflows/conformance.yaml) for scenarios.

## Documentation Guides (automated TBD)

Verify key guides work end-to-end with RC version. Report both guide accuracy issues and product bugs.

- [ ] [Quick Start](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/quick-start.md)
- [ ] [How to Install and Configure](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/how-to-install-and-configure.md)
- [ ] [Register MCP Servers](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/register-mcp-servers.md)
- [ ] [Authentication](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/authentication.md)
- [ ] [Authorization](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/authorization.md)
- [ ] [External MCP Server](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/external-mcp-server.md)
- [ ] [Virtual MCP Servers](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/virtual-mcp-servers.md)
- [ ] [OLM Install](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/olm-install.md)
- [ ] [OpenTelemetry](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/opentelemetry.md)
- [ ] [Scaling](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/scaling.md)
- [ ] [Isolated Gateway Deployment](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/isolated-gateway-deployment.md)
- [ ] [User-Based Tool Filter](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/user-based-tool-filter.md)
- [ ] [Tool Revocation](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/tool-revocation.md)
- [ ] [Configure MCP Gateway Listener and Router](https://github.com/Kuadrant/mcp-gateway/blob/release-X.Y.Z/docs/guides/configure-mcp-gateway-listener-and-router.md)

## Won't Be Tested

- Previous Kubernetes/OpenShift versions
- Previous Gateway API versions
- AWS/GCP/ARO/ROSA clusters
- Performance/load testing
- Upgrade/migration testing
- AuthN/AuthZ scenarios beyond documented guides (e.g. different IdPs, RBAC-based auth)

## Issues Found

Link any issues found during testing