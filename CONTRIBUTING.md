# Contributing to this project

Greetings! We are grateful for your interest in joining the Kuadrant community and making a positive impact. Whether you're raising issues, enhancing documentation, fixing bugs, or developing new features, your contributions are essential to our success.

To get started, kindly read through this document and familiarize yourself with our code of conduct. 

We can't wait to collaborate with you!

## Issues

Please feel free to report an issue for bugs, and any other problems you
encounter with the MCP Gateway project.

### Process

We have some light process around issue management that contributors should
be aware of: We organize via [GitHub milestones], which are collected into
a roadmap. We use [GitHub project boards] to track the milestones and issues.
Milestones are sequential.

We provide some labels to indicate individual issue priorities based on
impact and time-sensitivity:

* `priority/low` - Low-impact and little time-sensitivity. Can be worked on
  after other issues have been accounted for.
* `priority/normal` - Standard, default priority for issues. Can be worked on
  after higher priority items are finished, and before any low priority items.
* `priority/high` - High-impact, may affect many other issues, and may be very
  time-sensitive. Should accounted for before non-critical issues.
* `priority/critical` - Indicates an extremely time-sensitive and/or extremely
  high impact critical issues. Interrupts other work until it is accounted for,
  and should be used sparingly.

[GitHub milestones]:https://github.com/Kuadrant/mcp-gateway/milestones
[GitHub project boards]:https://github.com/orgs/Kuadrant/projects/27/views/1

### Triage

We have some light automation and process around triaging issues. This process
is centered around milestones:

1. When issues don't have a milestone: they are considered "in need of triage",
   so the maintainers can review and decide whether they will be accepted, and
   if so which milestone they belong to.
2. When issues have a milestone: they are considered `triage/accepted`, and
   need a priority. The priority will default to `priority/normal`.

### Code of conduct

Participation in the Kuadrant community is governed by the [Kuadrant Community Code of Conduct](https://github.com/Kuadrant/governance/blob/main/CODE_OF_CONDUCT.md).

## Development Environment

The project uses a top-level `Makefile` with additional include files in `build/*.mk`. Run `make help` for a categorised summary of the most common targets.

> **Note:** `make help` only lists targets documented with `## ` (double-hash). Targets in `build/*.mk` that use `# ` (single-hash) are valid but intentionally hidden from the help output to keep the quick-reference list focused. The most useful of those are documented below, grouped by purpose.

### Debugging Envoy

Use these targets when you need to inspect or adjust the Istio gateway's Envoy proxy. `make debug-envoy` and `make debug-envoy-off` are visible in `make help`; the remaining targets are in `build/debug.mk` and hidden from help.

| Target | Purpose |
|---|---|
| `make debug-envoy` | Enable debug-level logging on every running Istio gateway. |
| `make debug-envoy-off` | Restore the gateway's log level to info. |
| `make debug-envoy-config` | Dump the full Envoy configuration for the gateway as JSON. |
| `make debug-envoy-clusters` | Show the status of all upstream clusters registered in Envoy. |
| `make debug-envoy-listeners` | List all listeners configured in Envoy. |
| `make debug-envoy-admin` | Port-forward the Envoy admin interface to `http://localhost:15000`. |
| `make debug-ext-proc` | Enable debug-level logging for the `ext_proc` filter only (scoped to the external processor, not the full gateway). |

### Inspecting Istio configuration

Use these targets to query or validate the Istio control-plane configuration of the gateway. `make istio-clusters` and `make istio-config` are visible in `make help`; the remaining targets are in `build/istio-debug.mk` and hidden from help.

| Target | Purpose |
|---|---|
| `make istio-clusters` | Show all upstream clusters registered in the gateway's Envoy proxy. |
| `make istio-listeners` | List all listeners in the gateway's Envoy proxy. |
| `make istio-routes` | List all routes in the gateway's Envoy proxy. |
| `make istio-endpoints` | Show all endpoints visible to the gateway. |
| `make istio-config` | Print all proxy configurations at once (listeners, routes, clusters, endpoints). |
| `make istio-analyze` | Run `istioctl analyze` against the cluster to surface common misconfiguration issues. |
| `make istio-dashboard` | Open the Kiali dashboard (requires Kiali to be deployed). |
| `make istio-external` | Show ServiceEntry and DestinationRule resources used for external MCP server routing. |

### Tailing logs

These targets are in `build/debug.mk`. In addition to `make logs` (gateway logs, visible in `make help`), the following targets cover specific components.

| Target | Purpose |
|---|---|
| `make logs-mock` | Tail logs from the mock MCP test servers. |
| `make logs-istiod` | Tail logs from the Istiod control-plane pod. |
| `make logs-all` | Print recent logs from every MCP-related component in one view. |

### Local dev loop

These targets are in `build/dev.mk` and support running the broker and router as host processes while the cluster handles everything else. Start with `make dev` (visible in `make help`) to set up the cluster side, then use the targets below to manage the local processes.

| Target | Purpose |
|---|---|
| `make dev-setup` | Configure the cluster to route traffic to locally running services (EnvoyFilter and Service resources). |
| `make dev-reset` | Revert the cluster to use the in-cluster broker and router deployments. |
| `make dev-envoyfilter` | Apply the EnvoyFilter that points the gateway's ext_proc at the locally running router. |
| `make dev-broker-service` | Create a Kubernetes Service that forwards to the locally running broker. |
| `make dev-test` | Send a test MCP request through the gateway to verify end-to-end connectivity. |
| `make dev-logs-gateway` | Tail logs from the Istio gateway (alias scoped to the dev workflow). |
| `make dev-stop` | Stop all local dev processes (port-forwards, router, broker). |
| `make dev-stop-forward` | Stop any `kubectl port-forward` processes started by the dev workflow. |
