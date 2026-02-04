# MCP Gateway

An Envoy-based gateway for Model Context Protocol (MCP) servers, enabling aggregation and routing of multiple MCP servers behind a single endpoint.

## Vision

See [VISION.md](./VISION.md) for project vision and design principles.

## Architecture

See [docs/design/overview.md](./docs/design/overview.md) for technical architecture.

## Quick Install

### Minimal Installation (bring your own infrastructure)

If you already have a Kubernetes cluster with [Gateway API](https://gateway-api.sigs.k8s.io/guides/) installed, install just the MCP Gateway components:

```bash
# Install Gateway API CRDs
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
# Install MCP Gateway (quotes required for zsh)
kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=main'
```

This installs:
- MCP gateway CRDs
- Broker/router deployment
- Controller deployment
- RBAC resources

See [config/install/README.md](./config/install/README.md) for details and prerequisites.

### Development Environment

For a complete local environment with all dependencies (Istio, Gateway API, Keycloak, MCP test servers):

```bash
make local-env-setup
```

This sets up:
- a `kind` cluster
- Istio as a Gateway API provider
- MCP Gateway components (Broker / Router / Controller)
- Test MCP servers
- example configurations

#### Adding e2e tests

If you are adding an e2e test, please consider using the claude slash command provided : [e2e command](./.claude/commands/e2e-tests.md). You can of course still add them manually if you prefer.

## Quick start with MCP Inspector

Set up a local kind cluster with the Broker, Router & Controller running.
These components are built during the make target into a single image and loaded into the cluster.
Also sets up an Istio Gateway API Gateway, and HTTPRoutes for test mcp servers, which are added to the broker/router.

```bash
make local-env-setup

# Or with custom ports (defaults: 8001->30080->8080 for MCP Broker/Gateway, 8002->30089->8002 for Keycloak)
KIND_HOST_PORT_MCP_GATEWAY=8090 KIND_HOST_PORT_KEYCLOAK=8453 make local-env-setup
```

> **Note**: If you change these ports, be mindful that some examples or YAML resources may need to be updated manually to use the updated port. You should check for anything that connects to `mcp.127-0-0-1.sslip.io` or `keycloak.127-0-0-1.sslip.io` and update the port numbers accordingly.
>
> **Keycloak Port Requirement**: The host port for Keycloak needs to match the internal listener port (8002 in the default configuration) because there is a `hostAlias` in Authorino in the local dev environment that ensures Authorino calls back to Keycloak at the correct IP/port to validate tokens. If you change the host port (e.g., to 9999), you must also change the listener in the gateway to 9999, otherwise Authorino cannot reach Keycloak over the internal Kubernetes network.

Run the MCP Inspector and connect to the gateway

```bash
make inspect-gateway
```

This will start MCP Inspector and automatically open it with the correct URL for the gateway.

## Example OAuth setup: Keycloak as an ACL Server

After running the Quick start above, configure OAuth authentication with a single command:

```bash
make auth-example-setup
```

This will:
- Install Keycloak
- Set up a Keycloak realm with user/groups/client scopes, including group mappings for tool permissions
- Configure the mcp-broker with OAuth environment variables
- Apply AuthPolicy for token validation/exchange on the /mcp endpoint, including tool authorization via keycloak group mappings (both via Keycloak)
- Apply additional OAuth configurations

The mcp-broker now serves OAuth discovery information at `/.well-known/oauth-protected-resource`.

Finally, open MCP Inspector at http://localhost:6274/?transport=streamable-http&serverUrl=http://mcp.127-0-0-1.sslip.io:8001/mcp

When you click connect with MCP Inspector, you should be redirected to Keycloak. There you will need to login as the MCP user with password mcp. You now should only be able to access tools based on the ACL configuration.

You can modify tool authorization permissions by signing in to keycloak at https://keycloak.127-0-0-1.sslip.io:8002/ as the `admin` user with password `admin`, and modifying the 'Role Mappings' in the 'accounting' Group under the 'mcp' realm.
Each MCP Server is represented as a 'Client', with each tool represented as a 'Role'.

## Running Modes

### Standalone Mode (File-based)
Uses a YAML configuration file to define MCP servers:

```bash
make run
# Or directly:
./bin/mcp-broker-router --mcp-gateway-config ./config/mcp-system/config.yaml
```

The broker watches the config file for changes and hot-reloads configuration automatically.

### Controller Mode (Kubernetes)
Discovers MCP servers dynamically from Kubernetes Gateway API `HTTPRoute` resources:

```bash
make run-controller
# Or directly:
./bin/mcp-broker-router --controller
```

In controller mode:
- Watches `MCPServer` custom resources
- Discovers servers via `HTTPRoute` references
- Generates aggregated configuration in `ConfigMap`, for use by the broker/router
- Exposes health endpoints on `:8081` and metrics on `:8082`

## Configuration

### Standalone Configuration
Edit `config/mcp-system/config.yaml`:

```yaml
servers:
  - name: weather-service
    url: http://weather.example.com:8080
    hostname: weather.example.com
    enabled: true
    toolPrefix: "weather_"
  - name: calendar-service
    url: http://calendar.example.com:8080
    hostname: calendar.example.com
    enabled: true
    toolPrefix: "cal_"
```

### Kubernetes Configuration

#### MCPServerRegistration Resource

The `MCPServer` is a Kubernetes Custom Resource that defines an MCP (Model Context Protocol) server to be aggregated by the gateway. It enables discovery and federation of tools from backend MCP servers through Gateway API `HTTPRoute` references.

Each `MCPServer` resource:
- References a single HTTPRoute that points to a backend MCP service
- Configures a tool prefix to avoid naming conflicts when federating tools
- Enables the controller to automatically discover and configure the broker with available MCP servers
- Maintains status conditions to indicate whether the server is successfully discovered, valid and ready

Create `MCPServer` resources that reference HTTPRoutes:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: weather-tools
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: weather-route
  toolPrefix: weather_
---
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: calendar-tools
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: calendar-route
  toolPrefix: cal_
```

## Command Line Flags

```bash
--mcp-router-address            # gRPC ext_proc address (default: 0.0.0.0:50051)
--mcp-broker-public-address     # HTTP broker address (default: 0.0.0.0:8080)
--mcp-gateway-config            # Config file path (default: ./config/mcp-system/config.yaml)
--controller                    # Enable Kubernetes controller mode
```

### OAuth Configuration

The mcp-broker supports configurable OAuth protected resource discovery through environment variables. When configured, the broker serves OAuth discovery information at `/.well-known/oauth-protected-resource`.

| Environment Variable | Description | Default | Example |
|---------------------|-------------|---------|---------|
| `OAUTH_RESOURCE_NAME` | Human-readable name for the protected resource | `"MCP Server"` | `"My MCP Gateway"` |
| `OAUTH_RESOURCE` | URL of the protected MCP endpoint | `"/mcp"` | `"http://mcp.example.com/mcp"` |
| `OAUTH_AUTHORIZATION_SERVERS` | Comma-separated list of authorization server URLs | `[]` (empty) | `"http://keycloak.example.com/realms/mcp,http://auth.example.com"` |
| `OAUTH_BEARER_METHODS_SUPPORTED` | Comma-separated list of bearer token methods | `["header"]` | `"header,query"` |
| `OAUTH_SCOPES_SUPPORTED` | Comma-separated list of supported scopes | `["basic"]` | `"basic,read,write"` |

**Example configuration:**

```bash
export OAUTH_RESOURCE_NAME="Production MCP Server"
export OAUTH_RESOURCE="https://mcp.example.com/mcp"
export OAUTH_AUTHORIZATION_SERVERS="https://keycloak.example.com/realms/mcp"
export OAUTH_BEARER_METHODS_SUPPORTED="header"
export OAUTH_SCOPES_SUPPORTED="basic,read,write,groups"
```

**Response Format:**

The endpoint returns a JSON response following the OAuth Protected Resource discovery specification:

```json
{
  "resource_name": "Production MCP Server",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": [
    "https://keycloak.example.com/realms/mcp"
  ],
  "bearer_methods_supported": ["header"],
  "scopes_supported": ["basic", "read", "write"]
}
```

## Deployment to OpenShift

A script is available to deploy the MCP Gateway and dependent services to an OpenShift cluster. Utilize the steps described [here](./config/openshift/README.md) to facilitate the deployment.
