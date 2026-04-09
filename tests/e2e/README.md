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

## Troubleshooting

If tests fail, check:
```bash
# Controller logs
kubectl logs -n mcp-system deployment/mcp-gateway-controller

# Broker logs  
kubectl logs -n mcp-system deployment/mcp-broker-router

# Test server status
kubectl get pods -n mcp-test

# MCPServerRegistration status
kubectl get mcpsrs -A
```
