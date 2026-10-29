# Demo Walkthrough: XAccessPolicy with MCP Inspector

## Objective
Demonstrate how the standalone controller converts an `XAccessPolicy` targeting a `Gateway` into a Kuadrant `AuthPolicy`, mapping native CEL expressions directly to dynamically enforce tool-level access on `mcp-gateway`. We will use the **MCP Inspector** to visually verify the allowed and blocked tool calls.

## 1. Local Cluster Setup
Spin up a local `kind` cluster and install the necessary dependencies:

```bash
# Create cluster
kind create cluster --name accesspolicy-demo

# Install Gateway API
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/experimental-install.yaml

# Install Istio
helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update
helm install istio-base istio/base -n istio-system --create-namespace --wait
helm install istiod istio/istiod -n istio-system --wait

# Install Kuadrant Operator (provides AuthPolicy CRD and Authorino)
helm repo add kuadrant https://kuadrant.io/helm-charts
helm install kuadrant-operator kuadrant/kuadrant-operator --namespace kuadrant-system --create-namespace

cat <<EOF | kubectl apply -f -
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
EOF

# Deploy MCP Gateway Early Preview
kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/crd?ref=main'
sleep 2
kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=main'
kubectl patch clusterrole mcp-controller --type='json' -p='[{"op": "add", "path": "/rules/-", "value": {"apiGroups": ["apps"], "resources": ["deployments"], "verbs": ["get", "list", "watch", "update", "patch"]}}]'
```

## 2. Deploy the AccessPolicy Controller
Build and load the controller image:

```bash
make install # Installs the XAccessPolicy CRDs
make docker-build IMG=accesspolicy-controller:demo
kind load docker-image accesspolicy-controller:demo --name accesspolicy-demo
make deploy IMG=accesspolicy-controller:demo
```

## 3. Deploy an MCP Server
Deploy a sample MCP server that exposes tools like `get-sum` and `echo`.
*(We assume you have built an `accesspolicy-mcp-server:demo` image loaded into Kind.)*

```bash
kubectl create namespace quickstart-ns
kubectl apply -f quickstart/mcpserver/deployment.yaml
```

## 4. Apply the Infrastructure (Gateway & HTTPRoute)
We will define a Gateway that uses Istio as its class, and an `HTTPRoute` routing to the MCP server.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: demo-gateway
  namespace: quickstart-ns
spec:
  gatewayClassName: istio
  listeners:
    - name: http
      protocol: HTTP
      port: 8080
      allowedRoutes:
        namespaces:
          from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: mcp-route
  namespace: quickstart-ns
spec:
  parentRefs:
    - name: demo-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: mcp-server
          port: 3001
EOF
```

## 5. Apply the XAccessPolicy
Apply an `XAccessPolicy` that allows only `get-sum` using a CEL expression.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: agentic.networking.x-k8s.io/v1alpha1
kind: XAccessPolicy
metadata:
  name: demo-access-policy
  namespace: quickstart-ns
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: demo-gateway
  rules:
    - name: allow-get-sum
      authorization:
        type: CEL
        cel:
          expression: "request.mcp.tool_name == 'get-sum'"
EOF
```

## 6. Observe the Generated AuthPolicy
The controller will intercept the `XAccessPolicy` and generate a Kuadrant `AuthPolicy`, transferring the CEL expression directly to a pattern matching rule.

```bash
kubectl get authpolicy demo-gateway-auth -n quickstart-ns -o yaml
```
*Notice how the CEL expression maps exactly to `spec.authScheme.authorization["combined-rules"].patternMatching.patterns[0].predicate`.*

## 7. Verification with MCP Inspector
Port-forward the Envoy Gateway proxy and run the official MCP Inspector.

```bash
# Port-forward the gateway
kubectl port-forward svc/demo-gateway-istio 8080:8080 -n quickstart-ns &

# Launch MCP Inspector connecting to the Gateway via SSE
npx -y @modelcontextprotocol/inspector --transport sse --server-url http://localhost:8080/sse
```

### In the Inspector UI:
1. **Try `get-sum`**: Select the `get-sum` tool and execute it.
   - **Result**: ✅ The request succeeds, and you get a response back because Authorino allowed the request based on the generated `AuthPolicy`.
2. **Try `get-tiny-image`**: Select a tool NOT in your `XAccessPolicy`.
   - **Result**: ❌ The request instantly fails with a `403 Forbidden` error. Authorino drops it at the Gateway before it ever reaches the backend MCP server!

## 8. Dynamic Updates
To prove the controller updates Authorino instantly:
1. Edit the `XAccessPolicy` in Kubernetes to add `request.mcp.tool_name == 'get-tiny-image'`.
2. Go back to the Inspector UI (no need to restart) and click `get-tiny-image` again.
3. ✅ It now succeeds!

---

# Demo Walkthrough: Multi-Policy Aggregation

## Objective
Demonstrate how the standalone controller converts multiple `XAccessPolicy` resources targeting the same `Gateway` into a single, combined Kuadrant `AuthPolicy`.

## 1. Run the Multi-Policy Demo
Instead of manually applying resources step-by-step, we've provided an automated script that deploys a complete environment with two independent policies:

```bash
make demo-multi
```

## 2. Observe the Resources
This demo deploys a Gateway and two distinct `XAccessPolicies`:
- **Team A Policy**: Allows the `get-sum` tool.
- **Team B Policy**: Allows the `echo` tool.

```bash
kubectl get xaccesspolicies -n quickstart-ns
```

## 3. Verify Aggregation
The controller aggregates these policies into a single `AuthPolicy` applied to the Gateway:

```bash
kubectl get authpolicy demo-gateway-auth -n quickstart-ns -o yaml
```

You will see that the CEL expressions from both Team A and Team B's policies are combined into the generated `AuthPolicy`.

## 4. Test Enforcement
Connect to the Gateway using the MCP Inspector as before:
```bash
npx -y @modelcontextprotocol/inspector --transport sse --server-url http://localhost:8080/sse
```

Test the following tools:
1. **`get-sum`**: ✅ Allowed (granted by Team A's policy)
2. **`echo`**: ✅ Allowed (granted by Team B's policy)
3. **`get-tiny-image`**: ❌ Blocked (not granted by any policy)
