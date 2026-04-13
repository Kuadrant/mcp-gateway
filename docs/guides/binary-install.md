# Standalone Installation (Advanced)

**Status**: Work in Progress - Not Fully Supported

This method runs MCP Gateway broker and router components as standalone binaries with file-based configuration. This is an advanced deployment option for non-Kubernetes environments.

## Important Caveats

- **Limited Support**: This method is not fully supported and requires manual configuration
- **Envoy Required**: You must configure Envoy as a proxy (Istio is not needed)
- **Manual Configuration**: No controller automation - all server configuration is manual
- **Guide Compatibility**: Other guides in this documentation use Kubernetes CRDs and kubectl commands and are **not applicable** to binary installations
- **No Dynamic Discovery**: Server changes require configuration file updates and restarts
- **Production Readiness**: Not recommended for production use without additional operational tooling

## When to Use This Method

- **Non-Kubernetes Deployments**: Running on VMs, bare metal, or other environments
- **Development/Testing**: Local development or proof-of-concept scenarios
- **Minimal Dependencies**: When you want to avoid Kubernetes complexity
- **Learning**: Understanding MCP Gateway internals without Kubernetes abstractions

## Prerequisites

- [Go 1.25+](https://golang.org/doc/install) installed (for building from source)
- [Git](https://git-scm.com/downloads) installed
- [Envoy proxy](https://www.envoyproxy.io/docs/envoy/latest/start/install) installed and configured
- Access to MCP servers you want to aggregate

## Step 1: Build from Source

```bash
# Clone the repository
git clone https://github.com/Kuadrant/mcp-gateway.git
cd mcp-gateway

# Build the combined broker-router binary
go build -o bin/mcp-broker-router ./cmd/mcp-broker-router
```

**Note**: Pre-built binaries are not currently distributed. You must build from source.

## Step 2: Create Configuration File

Create a YAML configuration file defining your MCP servers:

```yaml
servers:
  - name: weather-service
    url: http://weather.example.com:8080/mcp
    hostname: weather.example.com
    enabled: true
    toolPrefix: "weather_"

  - name: calendar-service
    url: http://calendar.example.com:8080/mcp
    hostname: calendar.example.com
    enabled: true
    toolPrefix: "cal_"
```

**Configuration Fields**:
- `name`: Unique identifier for the server
- `url`: Full URL to the MCP server endpoint (including path)
- `hostname`: Hostname used for routing decisions
- `enabled`: Set to `false` to temporarily disable a server
- `toolPrefix`: Prefix added to all tools from this server (helps avoid naming conflicts)

Save this as `config/servers.yaml` or any location you prefer.

## Step 3: Start the Gateway

```bash
# Run with your configuration
./bin/mcp-broker-router \
  --mcp-gateway-config=config/servers.yaml \
  --mcp-gateway-public-host=your-hostname.example.com \
  --log-level=-4
```

**Command Options**:
- `--mcp-gateway-config`: Path to your YAML configuration file
- `--mcp-gateway-public-host`: **Required** - Public hostname for MCP Gateway (must match your Gateway listener hostname)
- `--mcp-router-address`: Address for gRPC router (default: `0.0.0.0:50051`)
- `--log-level`: Logging verbosity
  - `-4`: Debug (verbose)
  - `0`: Info (default)
  - `4`: Errors only

The gateway starts two components:
- **HTTP Broker**: Listens on `0.0.0.0:8080` (MCP protocol endpoint)
- **gRPC Router**: Listens on `0.0.0.0:50051` (internal routing, requires Envoy)

## Step 4: Configure Envoy Proxy

You need Envoy to route traffic through the external processor (router). Create an Envoy configuration file:

```yaml
# envoy.yaml - Minimal example
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
            - name: mcp_backend
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: mcp_broker
          http_filters:
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              grpc_service:
                envoy_grpc:
                  cluster_name: mcp_router
              processing_mode:
                request_header_mode: SEND
                response_header_mode: SEND
                request_body_mode: BUFFERED
                response_body_mode: NONE
          - name: envoy.filters.http.router

  clusters:
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
                address: 127.0.0.1
                port_value: 8080

  - name: mcp_router
    connect_timeout: 5s
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    http2_protocol_options: {}
    load_assignment:
      cluster_name: mcp_router
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: 127.0.0.1
                port_value: 50051
```

Start Envoy:
```bash
envoy -c envoy.yaml
```

## Step 5: Verify Installation

```bash
# Check broker status (direct to broker)
curl http://localhost:8080/status

# Test through Envoy proxy
curl http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test", "version": "1.0"}}}'

# List available tools
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

## Troubleshooting

### Gateway Won't Start

```bash
# Check if ports are in use
lsof -i :8080  # Broker
lsof -i :50051 # Router

# Verify configuration syntax
cat config/servers.yaml

# Run with debug logging
./bin/mcp-broker-router --mcp-gateway-config=config/servers.yaml --log-level=-4
```

### Tools Not Appearing

```bash
# Test backend MCP server directly
curl -X POST http://weather.example.com:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'

# Check gateway logs for errors
# Restart gateway after configuration changes
```

### Envoy Connection Issues

```bash
# Check Envoy is running
ps aux | grep envoy

# Verify Envoy can reach broker
curl http://localhost:8080/status

# Check Envoy logs for ext_proc errors
# Ensure gRPC router (port 50051) is accessible from Envoy
```

### Configuration Not Reloading

**Note**: Configuration changes require a restart:
```bash
# Stop the gateway (Ctrl+C)
# Edit config/servers.yaml
# Restart
./bin/mcp-broker-router --config=config/servers.yaml
```

## Limitations

- **No Authentication/Authorization**: OAuth and policy enforcement require additional proxy configuration
- **No Virtual Servers**: Virtual server filtering requires controller integration
- **No Credential Management**: External server credentials must be handled manually in configuration or environment variables
- **No Automatic Updates**: Server changes require manual config edits and restarts
- **No High Availability**: Single instance only; HA requires external load balancing and session management

## Next Steps

For full-featured deployments with authentication, authorization, dynamic configuration, and operational tooling, consider using the [Kubernetes installation method](./how-to-install-and-configure.md).
