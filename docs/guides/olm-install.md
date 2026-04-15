# Installing MCP Gateway via OLM

This guide covers installing the MCP Gateway controller using [Operator Lifecycle Manager (OLM)](https://olm.operatorframework.io/).

## Prerequisites

OpenShift clusters include OLM by default. For non-OpenShift Kubernetes clusters, install OLM first:

```bash
make olm-install
```

## Install

Deploy from a release tag using kustomize with a remote ref:

```bash
export MCP_GATEWAY_VERSION=0.6.0-rc2
kubectl apply -k "https://github.com/Kuadrant/mcp-gateway/config/deploy/olm?ref=v${MCP_GATEWAY_VERSION}"
```

Wait for the controller to be ready. The CSV may take a moment to appear while OLM processes the subscription:

```bash
kubectl wait csv -n mcp-system -l operators.coreos.com/mcp-gateway.mcp-system="" \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=5m
```

If the command returns "no matching resources found", wait a few seconds and retry -- the CSV has not been created yet.

## Next Steps

Installing via OLM deploys the operator only. To deploy the MCP Gateway itself, create an `MCPGatewayExtension` resource. See [Manual Resource Creation](./isolated-gateway-deployment.md#manual-resource-creation) for details.

## Uninstall

```bash
make undeploy-olm
```

## Local Development (Kind)

The default `local-env-setup` target uses kustomize:

```bash
make local-env-setup
```

To use the OLM-based deployment instead:

```bash
make local-env-setup INSTALL_OLM=true
```

This builds the bundle and catalog images locally, loads them into the Kind cluster, and sets `imagePullPolicy: Never` on the CatalogSource so no remote registry is needed.

## Available Make Targets

| Target | Description |
|--------|-------------|
| `make bundle` | Generate OLM bundle manifests |
| `make bundle-build` | Build the OLM bundle image |
| `make bundle-push` | Push the OLM bundle image |
| `make catalog-build` | Build the OLM catalog image |
| `make catalog-push` | Push the OLM catalog image |
| `make olm-install` | Install OLM on the cluster |
| `make olm-uninstall` | Uninstall OLM from the cluster |
| `make deploy-olm` | Deploy controller via OLM on local cluster |
| `make undeploy-olm` | Remove OLM-deployed controller |
