# E2E Tests

End-to-end tests for MCP Gateway using Ginkgo/Gomega.

## Running Tests

### Quick Start (assumes cluster exists)
```bash
make test-e2e-local
```

### Full Setup + Run
```bash
make test-e2e
```

### CI Mode (fail fast, no setup)
```bash
make test-e2e-ci
```

### Watch Mode (for development)
```bash
make test-e2e-watch
```

## Configuration

Tests can be configured via environment variables (see `suite_test.go` for full list):

```bash
# Namespace configuration (defaults shown)
MCP_GATEWAY_NAMESPACE=mcp-system      # MCP system components namespace
GATEWAY_NAMESPACE=gateway-system      # Gateway namespace
TEST_SERVER_NAMESPACE=mcp-test        # Test servers namespace

# Domain configuration
E2E_DOMAIN=127-0-0-1.sslip.io        # Base domain for hostnames
E2E_SCHEME=http                       # http or https
GATEWAY_CLASS_NAME=istio              # Gateway class to use
```

Example for OpenShift:
```bash
MCP_GATEWAY_NAMESPACE=kuadrant-system \
GATEWAY_NAMESPACE=istio-gateway \
E2E_DOMAIN=apps.my-cluster.example.com \
GATEWAY_CLASS_NAME=openshift-default \
make test-e2e
```

## Troubleshooting

If tests fail, check (adjust namespace if using custom `MCP_GATEWAY_NAMESPACE` or `TEST_SERVER_NAMESPACE`):
```bash
# Controller logs (default namespace: mcp-system)
kubectl logs -n ${MCP_GATEWAY_NAMESPACE:-mcp-system} deployment/mcp-gateway-controller

# Broker logs (default namespace: mcp-system)
kubectl logs -n ${MCP_GATEWAY_NAMESPACE:-mcp-system} deployment/mcp-gateway

# Test server status (default namespace: mcp-test)
kubectl get pods -n ${TEST_SERVER_NAMESPACE:-mcp-test}

# MCPServerRegistration status
kubectl get mcpsrs -A
```
