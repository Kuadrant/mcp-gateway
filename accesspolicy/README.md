# XAccessPolicy Controller

A Kubernetes controller that translates `XAccessPolicy` custom resources into Kuadrant `AuthPolicy` objects, enabling declarative, tool-level access control for MCP (Model Context Protocol) servers running behind [kuadrant/mcp-gateway](https://github.com/kuadrant/mcp-gateway).

## Description

The XAccessPolicy controller bridges the gap between high-level, gateway-agnostic MCP authorization intent and the concrete enforcement mechanisms provided by Kuadrant's Authorino. It watches `XAccessPolicy` resources that target `Gateway` objects and performs two key tasks:

1. **CEL Translation** — Converts domain-specific variables like `request.mcp.tool_name` into the data-plane equivalents (`request.headers['x-mcp-toolname']`) that Authorino can evaluate at runtime.
2. **Policy Aggregation** — Combines multiple `XAccessPolicy` rules targeting the same Gateway into a single Kuadrant `AuthPolicy`, satisfying Kuadrant's 1:1 policy-to-target constraint.

### Architecture

```
┌──────────────┐     ┌────────────────────────┐     ┌────────────────┐
│ XAccessPolicy│────▶│ AccessPolicy Controller│────▶│ AuthPolicy     │
│ (user-facing)│     │  • CEL translation     │     │ (Kuadrant CRD) │
└──────────────┘     │  • Policy aggregation  │     └───────┬────────┘
                     └────────────────────────┘             │
                                                            ▼
                                                    ┌──────────────┐
                                                    │  Authorino   │
                                                    │ (enforcement)│
                                                    └──────────────┘
```

### Example XAccessPolicy

```yaml
apiVersion: agentic.networking.x-k8s.io/v1alpha1
kind: XAccessPolicy
metadata:
  name: web-search-policy
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: prod-mcp-gateway
  rules:
    - name: allow-search-web-only
      authorization:
        type: CEL
        cel:
          expression: "request.mcp.tool_name == 'search_web'"
```

The controller translates `request.mcp.tool_name` → `request.headers['x-mcp-toolname']` and produces an `AuthPolicy` with pattern-matching predicates that Authorino evaluates at the data plane.

### Status Conditions

The controller reports progress through standard Kubernetes conditions on each `XAccessPolicy`:

| Condition | Meaning |
|-----------|---------|
| `Accepted` | The policy's CEL rules compiled successfully |
| `ResolvedRefs` | The target Gateway was found in the cluster |
| `Programmed` | The resulting AuthPolicy was successfully applied |
## Quickstart

The fastest way to see the controller in action is the one-command quickstart. It spins up a local Kind cluster with everything pre-configured — including Kuadrant, the MCP Gateway, and a sample MCP server. We then use the **official MCP Inspector** to interact with the tools and see access policies enforced in real time.

### Prerequisites

- [kind](https://kind.sigs.k8s.io/), [kubectl](https://kubernetes.io/docs/tasks/tools/), [Docker](https://docs.docker.com/get-docker/), [Go](https://go.dev/dl/), [Helm](https://helm.sh/docs/intro/install/), [Node.js / npx](https://nodejs.org/)

### Run it

```sh
make quickstart
```

This will:
1. Create a Kind cluster (`accesspolicy-demo`)
2. Install Gateway API CRDs, the Kuadrant operator, and MCP Gateway
3. Build & deploy the accesspolicy-controller
4. Deploy an MCP server with sample tools (`get-sum`, `echo`, `get-tiny-image`, etc.)
5. Apply an `XAccessPolicy` that allows only `get-sum` and `echo`
6. Port-forward the Envoy Gateway to `localhost:8080`

### Try it

Open a new terminal and run the official MCP Inspector to connect to the Gateway:

```sh
npx -y @modelcontextprotocol/inspector http://localhost:8080/sse
```

In the Inspector UI, try calling the tools:

| Tool Used | Expected Result |
|-----------|-----------------|
| `get-sum` | ✅ Allowed |
| `echo` | ✅ Allowed |
| `get-tiny-image` | ❌ Blocked |

### Dynamic policy updates

Swap `echo` → `get-tiny-image` in the allow list with a single command:

```sh
kubectl apply -f quickstart/policy/updated-policy.yaml
```

Now `get-tiny-image` is ✅ allowed and `echo` is ❌ blocked — no restarts needed.

### Cleanup

```sh
make quickstart-clean
```

## Multi-Policy Aggregation Demo

The AccessPolicy controller allows multiple `XAccessPolicy` custom resources to target the same Gateway. It aggregates all these policies into a single Kuadrant `AuthPolicy`.

To see this in action:

```sh
make demo-multi
```

This demo deploys the same MCP infrastructure as the quickstart, but applies two independent `XAccessPolicy` resources created by different teams:
- Team A's policy allows `get-sum`.
- Team B's policy allows `echo`.

In the MCP Inspector UI, verify that both tools are ✅ Allowed, while other tools remain ❌ Blocked.

Cleanup:
```sh
make demo-multi-clean
```

## Getting Started

### Prerequisites
- Go v1.24.6+
- Docker v17.03+
- kubectl v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster
- [Gateway API](https://gateway-api.sigs.k8s.io/) CRDs installed
- [Kuadrant Operator](https://docs.kuadrant.io/) deployed (provides `AuthPolicy` CRD and Authorino)

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/accesspolicy:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don't work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/accesspolicy:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Running Locally

For development, you can run the controller against your current kubeconfig context:

```sh
# Install CRDs
make install

# Run the controller locally
make run
```

Then apply an `XAccessPolicy` in another terminal:

```sh
kubectl apply -f config/samples/agentic_v1alpha1_xaccesspolicy.yaml
```

## Testing

Run all unit and integration tests (uses envtest for a real K8s API + etcd):

```sh
make test
```

Run only the translator unit tests:

```sh
go test ./internal/translator/...
```

Run the linter:

```sh
make lint
```

Run the conformance tests (spins up a local Kind cluster, deploys the controller, and runs the official `kube-agentic-networking` conformance suite):

```sh
make test-conformance
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/accesspolicy:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/vibhor-5/accesspolicy-controller/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Project Layout

```
├── api/v1alpha1/               # XAccessPolicy CRD types and deepcopy
├── cmd/main.go                 # Manager entrypoint
├── config/
│   ├── crd/bases/              # Generated CRD manifests (do not edit)
│   ├── rbac/                   # Generated RBAC (do not edit)
│   └── samples/                # Example XAccessPolicy CRs
├── internal/
│   ├── controller/             # XAccessPolicy reconciler
│   └── translator/             # CEL macro translation and validation
├── quickstart/                 # One-command demo environment
│   ├── run-quickstart.sh       # Orchestration script (make quickstart)
│   ├── kind-config.yaml        # Kind cluster config
│   ├── agent/                  # ADK-based AI agent with web UI
│   ├── mcpserver/              # MCP "everything" server
│   └── policy/                 # Sample Gateway + XAccessPolicy resources
└── docs/                       # Project documentation
    ├── user_guide.md           # How to use XAccessPolicy and write CEL rules
    ├── design.md               # Architecture and design decisions
    ├── tasks.md                # Implementation task breakdown
    ├── implementation_guide.md # Step-by-step implementation guide
    └── demo.md                 # End-to-end demo walkthrough
```
