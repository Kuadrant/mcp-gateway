# Kind Cluster Setup Guide

This guide walks through setting up a local Kind cluster with the essential prerequisites for MCP Gateway: Gateway API CRDs and Istio as the Gateway API provider.

## Prerequisites

- [Docker](https://docs.docker.com/engine/install/) or [Podman](https://podman.io/docs/installation) installed and running
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Helm](https://helm.sh/docs/intro/install/) installed
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) installed

## Step 1: Create Kind Cluster

```bash
# Create a Kind cluster with port mappings for Gateway API
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 8080
    protocol: TCP
  - containerPort: 443
    hostPort: 8443
    protocol: TCP
EOF
```

**Why these port mappings**: Allows access to the Istio gateway from your local machine on ports 8080 (HTTP) and 8443 (HTTPS).

## Step 2: Install Gateway API CRDs

```bash
# Install Gateway API standard CRDs
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml

# Verify installation
kubectl get crd gateways.gateway.networking.k8s.io
```

## Step 3: Install Istio as Gateway API Provider

```bash
# Add Istio Helm repository
helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update

# Install Istio base components
helm install istio-base istio/base -n istio-system --create-namespace --wait

# Install Istio control plane
helm install istiod istio/istiod -n istio-system --wait

# Verify Istio installation
kubectl get pods -n istio-system
```

## Step 4: Verify Cluster Readiness

```bash
# Check that all components are ready
kubectl get pods -n istio-system
kubectl get crd | grep gateway

# Verify Kind cluster is accessible
kubectl cluster-info --context kind-kind
```

Your Kind cluster is now ready with Gateway API and Istio installed!

## Next Steps

Now that you have a cluster with the prerequisites, choose your installation method:

- **[Install MCP Gateway via Helm](./how-to-install-and-configure.md#method-1-helm-recommended)** - Recommended approach
- **[Install MCP Gateway via Kustomize](./how-to-install-and-configure.md#method-2-kustomize)** - Alternative method

After installation, continue with configuration:

- **[Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md)** - Set up traffic routing
- **[Configure MCP Servers](./register-mcp-servers.md)** - Connect your MCP servers
- **[Authentication Setup](./authentication.md)** - Optional: Add OAuth authentication
- **[Authorization Setup](./authorization.md)** - Optional: Add access control

## Cleanup

When you're done testing:

```bash
# Delete the Kind cluster (uses default name "kind")
kind delete cluster
```
