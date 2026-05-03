# MCP Gateway

An Envoy-based gateway for Model Context Protocol (MCP) servers, enabling aggregation and routing of multiple MCP servers behind a single endpoint.

## Vision
See [VISION.md](./VISION.md) for project vision and design principles.

## Architecture
See [docs/design/overview.md](./docs/design/overview.md) for technical architecture.

## Installation

The project supports two primary deployment methods. Ensure all [prerequisites](./docs/guides/how-to-install-and-configure.md#prerequisites) (Gateway API, Istio, etc.) are met before proceeding.

### 1. OLM (Recommended for OpenShift)
Managed operator deployment via Operator Lifecycle Manager.
- **[OLM Installation Guide](./docs/guides/olm-install.md)**

### 2. Kustomize / Helm
Standard Kubernetes deployment for the MCP Gateway operator.
- **[Standard Installation Guide](./docs/guides/how-to-install-and-configure.md)**

## Local Development (Quick Start)

For rapid evaluation and local development, we provide an automated script that sets up a `kind` cluster with all dependencies (Istio, Gateway API, test servers):

```bash
make local-env-setup
```

This sets up:
- A `kind` cluster
- Istio as a Gateway API provider
- MCP Gateway components (Broker / Router / Controller)
- The everything test server and example configurations

See the **[Quick Start Guide](./docs/guides/quick-start.md)** for more details.

## Quick Start with MCP Inspector

Once the local environment is set up, you can inspect the gateway using the MCP Inspector UI:

```bash
make inspect-gateway
```

## Documentation
- [How to Install and Configure](./docs/guides/how-to-install-and-configure.md)
- [Register MCP Servers](./docs/guides/register-mcp-servers.md)
- [External MCP Servers](./docs/guides/external-mcp-server.md)
- [Authentication and Authorization](./docs/guides/authentication.md)

## Contributing
See [CONTRIBUTING.md](./CONTRIBUTING.md) for details on how to get involved.

## License
Apache License 2.0 - See [LICENSE](./LICENSE) for details.
