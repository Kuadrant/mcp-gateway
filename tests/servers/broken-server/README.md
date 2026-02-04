# MCP Test Server 5 (Validation Test Server)

Server4 is an intentionally "bad" MCP server designed to test validation failures in the MCP Gateway. It can simulate different types of failures to verify that the gateway properly detects and handles problematic servers.

## Failure Modes

The server supports three different failure modes controlled by the `FAILURE_MODE` environment variable or `--failure-mode` flag:

### 1. Protocol Version Failure (default)

```bash
FAILURE_MODE=protocol ./mcp-test-server --http 0.0.0.0:9090
```

- Returns an old/unsupported protocol version (2024-11-05)
- Uses manual JSON responses instead of proper MCP protocol
- Should fail protocol validation

### 2. No Tools Capability

```bash
FAILURE_MODE=no-tools ./mcp-test-server --http 0.0.0.0:9090
```

- Creates a valid MCP server but with no tools
- Should fail capabilities validation (missing required tools capability)

### 3. Tool Name Conflicts

```bash
FAILURE_MODE=tool-conflicts ./mcp-test-server --http 0.0.0.0:9090
```

- Provides tools with names that conflict with other servers: `time`, `slow`, `headers`
- Should be detected by tool conflict validation
- Tools are functional but have conflicting names

### 4. Connection Failure (Scaling-based Testing)

Connection failures are tested by scaling the deployment rather than code simulation:

```bash
# Scale down to simulate connection failure
kubectl scale deployment mcp-test-Broken-server -n mcp-test --replicas=0

# Scale back up to restore connection
kubectl scale deployment mcp-test-Broken-server -n mcp-test --replicas=1
```

- Scaling to 0 replicas simulates the server being unreachable
- Should fail connection validation (connection refused)

## Usage

### Command Line

```bash
# Default protocol failure mode
./mcp-test-server --http 0.0.0.0:9090

# Specific failure mode
./mcp-test-server --failure-mode no-tools --http 0.0.0.0:9090

# Using environment variable
FAILURE_MODE=tool-conflicts ./mcp-test-server --http 0.0.0.0:9090
```

### Docker

```bash
# Protocol failure (default)
docker run -p 9090:9090 ghcr.io/kuadrant/mcp-gateway/test-broken-server:latest

# No tools capability
docker run -p 9090:9090 -e FAILURE_MODE=no-tools ghcr.io/kuadrant/mcp-gateway/test-broken-server:latest

# Tool conflicts
docker run -p 9090:9090 -e FAILURE_MODE=tool-conflicts ghcr.io/kuadrant/mcp-gateway/test-broken-server:latest
```

## Testing with MCP Gateway

Once server4 is running, test the gateway's validation:

```bash
# Check validation status for all servers
curl http://gateway:8080/status

# Check validation status for Broken-server specifically
curl http://gateway:8080/status/mcp-Broken-server-route

# Force refresh validation
curl -X POST http://gateway:8080/status

# Expected results based on failure mode:
# - protocol: Protocol version invalid (2024-11-05 vs 2025-06-18)
# - no-tools: No capabilities available (missing tools capability)
# - tool-conflicts: Tool name conflicts detected
# - connection (scaled down): Connection refused
```

## Integration

Broken-server is automatically deployed as part of the local development environment:

```bash
make local-env-setup  # Deploys Broken-server with other test servers
```

In Kubernetes, Broken-server is deployed with `FAILURE_MODE=protocol` by default, but can be reconfigured by updating the deployment environment variables.
