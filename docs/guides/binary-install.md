# Standalone Installation (Advanced)

**Status**: Work in Progress - Not Fully Supported

This method runs MCP Gateway broker and router components as standalone binaries with file-based configuration. This is an advanced deployment option for non-Kubernetes environments.

## Important Caveats

- **Limited Support**: This method is not fully supported and requires manual configuration
- **Envoy Required**: You must configure Envoy as a proxy (Istio is not needed)
- **Manual Configuration**: No controller automation - all server configuration is manual
- **Guide Compatibility**: Other guides in this documentation use Kubernetes CRDs and kubectl commands and are **not applicable** to binary installations
- **No Dynamic Discovery**: Server changes require configuration file updates (hot-reload is supported, no restart needed)
- **Production Readiness**: Not recommended for production use without additional operational tooling

## When to Use This Method

- **Non-Kubernetes Deployments**: Running on VMs, bare metal, or other environments
- **Development/Testing**: Local development or proof-of-concept scenarios
- **Minimal Dependencies**: When you want to avoid Kubernetes complexity
- **Learning**: Understanding MCP Gateway internals without Kubernetes abstractions

## Prerequisites

- [Go 1.26+](https://golang.org/doc/install) installed (for building from source)
- [Git](https://git-scm.com/downloads) installed
- [Envoy proxy](https://www.envoyproxy.io/docs/envoy/latest/start/install) installed (or Docker to run it as a container)
- Access to MCP servers you want to aggregate

## Step 1: Build from Source

```bash
# Clone the repository
git clone https://github.com/Kuadrant/mcp-gateway.git
cd mcp-gateway

# Build the combined broker-router binary
go build -o bin/mcp-broker-router ./cmd/mcp-broker-router
```

> **Note:** Pre-built binaries are not currently distributed. You must build from source.

## Step 2: Create Configuration File

Create a YAML configuration file defining your MCP servers. This example connects to a single MCP server running on localhost:

```yaml
servers:
  - name: my-mcp-server
    url: http://localhost:3001/mcp
    hostname: my-mcp-server.local
    prefix: "myserver_"
    state: Enabled
```

**Configuration Fields**:
- `name`: Unique identifier for the server
- `url`: Full URL to the MCP server endpoint (including path)
- `hostname`: Routing hostname for this server. The router uses this to direct tool calls through Envoy to the correct backend. Each server needs a unique hostname that matches a virtual host in the Envoy configuration (see Step 3).
- `prefix`: Prefix added to all tools from this server (avoids naming conflicts when aggregating multiple servers)
- `state`: Set to `Enabled` or omit (defaults to `Enabled`)

Save this as `config.yaml`.

## Step 3: Configure Envoy Proxy

Envoy sits in front of the broker and routes traffic through the external processor (router). The router uses Envoy's ext_proc filter to inspect requests and control routing:

- **Non-tool requests** (initialize, tools/list): the router sets `:authority` to the gateway's public host, so Envoy routes them to the broker.
- **Tool calls** (tools/call): the router sets `:authority` to the upstream server's `hostname` from the config, so Envoy routes them directly to that backend.

This means the Envoy config needs a virtual host for the gateway and one for each upstream server.

Create an `envoy.yaml`:

```yaml
static_resources:
  listeners:
  - name: mcp_listener
    address:
      socket_address:
        address: 0.0.0.0
        port_value: 8888
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress_http
          codec_type: AUTO
          route_config:
            name: local_route
            virtual_hosts:
            # Gateway host: handles initialize, tools/list, and other broker requests
            - name: mcp_gateway
              domains: ["localhost", "localhost:8888"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: mcp_broker
            # Upstream server: handles tools/call routed by the ext_proc router.
            # The domain must match the hostname in config.yaml.
            # Add one virtual_host per upstream MCP server.
            - name: my_mcp_server
              domains: ["my-mcp-server.local"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: my_mcp_server
          http_filters:
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              failure_mode_allow: false
              allow_mode_override: true
              mutation_rules:
                allow_all_routing: true
              message_timeout: 10s
              processing_mode:
                request_header_mode: SEND
                response_header_mode: SEND
                request_body_mode: BUFFERED
                response_body_mode: NONE
                request_trailer_mode: SKIP
                response_trailer_mode: SKIP
              grpc_service:
                envoy_grpc:
                  cluster_name: mcp_router
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router

  clusters:
  # Broker: handles MCP protocol (initialize, tools/list, etc.)
  - name: mcp_broker
    connect_timeout: 5s
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: mcp_broker
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: host.docker.internal
                port_value: 8080

  # Router: ext_proc gRPC service
  - name: mcp_router
    connect_timeout: 5s
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
        explicit_http_config:
          http2_protocol_options: {}
    load_assignment:
      cluster_name: mcp_router
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: host.docker.internal
                port_value: 50051

  # Upstream MCP server: add one cluster per server in config.yaml
  - name: my_mcp_server
    connect_timeout: 5s
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: my_mcp_server
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: host.docker.internal
                port_value: 3001
```

> **Note:** This example uses `host.docker.internal` for cluster addresses, which works when running Envoy in Docker while the other components run on the host. If running Envoy natively, change these to `127.0.0.1`.

Start Envoy (using Docker):

```bash
docker run --rm -p 8888:8888 \
  -v $(pwd)/envoy.yaml:/etc/envoy/envoy.yaml:ro \
  envoyproxy/envoy:v1.32-latest \
  -c /etc/envoy/envoy.yaml
```

Or if Envoy is installed locally:

```bash
envoy -c envoy.yaml
```

## Step 4: Start the Gateway

The gateway requires a signing key for session management. Generate one and start the binary:

```bash
# Generate a signing key (do this once, reuse across restarts)
export GATEWAY_SIGNING_KEY=$(openssl rand -hex 32)

# Start the gateway
./bin/mcp-broker-router \
  --mcp-gateway-config=config.yaml \
  --mcp-gateway-public-host=localhost \
  --mcp-gateway-private-host=localhost:8888 \
  --log-level=-4
```

**Required flags/env vars**:
- `GATEWAY_SIGNING_KEY` (env var): Key for JWT session signing. The binary will not start without it.
- `--mcp-gateway-public-host`: Public hostname clients use to reach the gateway. Must match the gateway domain in the Envoy virtual host configuration.
- `--mcp-gateway-private-host`: Address the router uses for hairpin requests (lazy backend initialization). Point this at Envoy's listener.
- `--mcp-gateway-config`: Path to your YAML configuration file.

**Optional flags**:
- `--mcp-broker-public-address`: Broker listen address (default: `0.0.0.0:8080`)
- `--mcp-router-address`: gRPC router listen address (default: `0.0.0.0:50051`)
- `--log-level`: `-4` debug, `0` info (default), `4` warn, `8` error
- `--log-format`: `txt` (default) or `json`
- `--session-length`: Session duration in minutes (default: 1440 / 24h)

The gateway starts two components:
- **HTTP Broker** on `0.0.0.0:8080`: connects to upstream MCP servers, federates tools
- **gRPC Router** on `0.0.0.0:50051`: Envoy ext_proc service, handles request routing and session management

## Step 5: Verify Installation

```bash
# Initialize a session through Envoy
curl -s -D - http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test", "version": "1.0"}}}'

# List available tools (use the mcp-session-id from the initialize response headers)
curl -s http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: <session-id-from-above>" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'
```

Tools from your configured servers should appear with their configured prefix.

## Troubleshooting

### Gateway Panics on Startup

If you see `GATEWAY_SIGNING_KEY (or JWT_SESSION_SIGNING_KEY) is required but not set`:

```bash
export GATEWAY_SIGNING_KEY=$(openssl rand -hex 32)
```

If you see `--mcp-gateway-public-host cannot be empty`:

```bash
# Add the required flag
./bin/mcp-broker-router --mcp-gateway-public-host=localhost ...
```

### Tools Not Appearing

```bash
# Verify the upstream MCP server is reachable directly
curl -s http://localhost:3001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'

# Check gateway debug logs for connection errors
# Run with --log-level=-4 for verbose output
```

### Hairpin / Initialization Failures

If tools/list returns empty or you see initialization errors in logs, the router cannot reach the broker through Envoy for lazy backend init:

```bash
# Ensure --mcp-gateway-private-host points at Envoy's listener
--mcp-gateway-private-host=localhost:8888
```

### Tool Calls Fail with "unknown tool"

The router rewrites `:authority` to the upstream server's `hostname` from the config, which Envoy uses to route the request to the correct backend cluster. Check that:

1. The `hostname` in `config.yaml` matches a `domains` entry in the Envoy virtual host
2. That virtual host routes to a cluster pointing at the upstream server
3. The upstream cluster address and port are correct

### Envoy Connection Issues

```bash
# Verify Envoy is running and listening
curl -s http://localhost:8888/

# Check that broker is reachable from Envoy's network
# If Envoy runs in Docker, use host.docker.internal in cluster addresses
# If Envoy runs natively, use 127.0.0.1
```

### Configuration Changes

The gateway watches the configuration file for changes and hot-reloads automatically. No restart is needed after editing the config file.

## Limitations

- **No Authentication/Authorization**: OAuth and policy enforcement require additional proxy configuration
- **No Virtual Servers**: Virtual server filtering requires controller integration
- **No Credential Management**: External server credentials must be handled manually in configuration
- **No High Availability**: Single instance only; HA requires external load balancing and Redis for session storage
- **No Automatic Discovery**: Servers must be manually added to the configuration file

## Next Steps

For full-featured deployments with authentication, authorization, dynamic configuration, and operational tooling, consider using the [Kubernetes installation method](./how-to-install-and-configure.md).
