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
kubectl apply -k https://github.com/Kuadrant/mcp-gateway/config/deploy/olm?ref=v0.5.1
```

Replace `v0.5.1` with the desired release tag.

Wait for the controller to be ready:

```bash
kubectl wait csv -n mcp-system -l operators.coreos.com/mcp-gateway.mcp-system="" --for=jsonpath='{.status.phase}'=Succeeded --timeout=5m
```

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

## Architecture

Only the controller is installed via OLM. The broker-router is deployed dynamically by the controller when it reconciles an MCPGatewayExtension CR, using the `RELATED_IMAGE_ROUTER_BROKER` environment variable to reference the broker-router image.
