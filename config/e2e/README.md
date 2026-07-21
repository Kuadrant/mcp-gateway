# E2E Gateway Configuration

This directory contains template files for e2e test gateways that support both KIND and OpenShift deployments.

## Files

- `*.yaml.template` - Template files with `${E2E_DOMAIN}` and `${GATEWAY_CLASS_NAME}` placeholders
- `kustomization-kind.yaml` - Kustomization for KIND (includes NodePort services)
- `kustomization-openshift.yaml` - Kustomization for OpenShift (no NodePort needed)
- `namespace.yaml` - Gateway namespace definition
- `nodeport.yaml` - NodePort services for KIND local access

**Generated files (gitignored):**
- `gateway-1.yaml`, `gateway-2.yaml`, `gateway-elicitation.yaml`, `gateway-shared.yaml` - Generated from templates
- `kustomization.yaml` - Copied from platform-specific kustomization file

## Usage

### KIND (default)
```bash
make generate-e2e-config
make deploy-e2e-gateways
```

### OpenShift
```bash
# Set your cluster domain
make deploy-e2e-gateways-openshift E2E_DOMAIN=apps.my-cluster.example.com

# Or manually:
make generate-e2e-config E2E_DOMAIN=apps.my-cluster.example.com GATEWAY_CLASS_NAME=openshift-default E2E_PLATFORM=openshift
make deploy-e2e-gateways
```

### Custom configuration
```bash
make generate-e2e-config \
  E2E_DOMAIN=custom.domain.com \
  GATEWAY_CLASS_NAME=my-gateway-class \
  E2E_PLATFORM=kind
```

## Environment Variables for Tests

The e2e tests (in `tests/e2e/`) also use environment variables for client connections:

```bash
# Match the domain and gateway class used for gateway deployment
export E2E_DOMAIN=apps.my-cluster.example.com
export GATEWAY_CLASS_NAME=openshift-default  # default: istio
export E2E_SCHEME=https  # if using HTTPS
```

```bash
export E2E_TIMEOUT=90m # It might take longer for e2e to finish on OpenShift compared to KIND
export MCP_GATEWAY_NAMESPACE=kuadrant-system # If MCP Gateway was not deployed into mcp-system namespace
```

See `tests/e2e/commons.go` for all available environment variables.

## Run tests
```bash
make test-e2e
```
