# MCP Gateway Vision

## Problem Statement

Platform teams want to expose MCP servers to agents safely, but today this requires bespoke routing, auth, policy, and observability per server. There is no standard entrypoint, no shared policy model, and no production-grade way to operate MCP at scale.

## Value Proposition

MCP Gateway reduces the cost and risk of adopting MCP at scale by centralising routing, security, and policy enforcement on proven infrastructure.  
It enables platform teams to offer MCP as a shared capability, while preserving flexibility to adopt upstream standards as they mature.

## Solution

MCP Gateway is a shared MCP entrypoint that allows platform teams to operate many MCP servers with consistent routing, policy, and observability, while tracking upstream standards instead of inventing new ones.
MCP Gateway composes MCP support out of Envoy’s routing and extension mechanisms, rather than introducing a separate MCP-specific proxy layer.

## Who is this for?

* Platform / infra teams running MCP servers at scale
* Teams standardising agent-to-tool access across orgs
* Not intended as a lightweight dev-only proxy

## Non-Goals

* Not replacing Envoy AI Gateway or its broader AI gateway capabilities
* Not transforming or masking tool inputs/outputs — the gateway federates and aggregates tools as they are; reshaping tool schemas, filtering fields, or creating derived tools from existing ones is the responsibility of the MCP server itself or dedicated tooling (e.g. camel, toolhive proxy, custom MCP servers)
* Not automatically mapping non-MCP APIs (OpenAPI, REST, gRPC) to MCP tools — converting existing APIs into MCP tool definitions is out of scope; use purpose-built tooling at the MCP server layer for that

## Approach

**Move pragmatically, align with standards.** MCP Gateway implements functionality early while upstream projects refine standards. As those standards mature, we adopt them.
It exists to de-risk early adoption.

We actively engage and/or integrate with:

- **[Gateway API](https://gateway-api.sigs.k8s.io/)** - declarative routing and infrastructure
- **[AI Gateway Working Group](https://github.com/kubernetes-sigs/wg-ai-gateway)** - payload processing, Backend resources
- **[Kube Agentic Networking](https://github.com/kubernetes-sigs/kube-agentic-networking)** - AccessPolicy, agent-to-tool communication standards
- **[Kuadrant](https://kuadrant.io/)** - auth and rate limiting policies; we explore agentic-specific policy patterns here first
- **[Envoy](https://www.envoyproxy.io/)** - routing and payload processing; upstream work on [JSON-RPC/MCP support](https://github.com/envoyproxy/envoy/issues/39174) may replace our ext_proc
- **[Istio](https://istio.io/)** - Gateway API provider with evolving agent-oriented features
- **[Agentic AI Foundation](https://www.linuxfoundation.org/press/linux-foundation-announces-the-formation-of-the-agentic-ai-foundation)** - an open foundation to ensure agentic AI evolves transparently and collaboratively

## Principles

- **Single Entrypoint** - a single shared MCP entrypoint to many MCP servers
- **Envoy first** - the core router and broker work directly with Envoy; no Kubernetes required
- **Kubernetes adds convenience** - Gateway API and CRDs provide declarative management on top of the Envoy foundation
- **Bring your own policies** - expose metadata for external policy engines
- **Defense in Depth** - we treat AI agents as adversarial: agents today rely on Large Language Models (LLMs) to generate instructions which the agents execute. These LLMs have known limitations and can make significant mistakes. Some tools will enable destructive or security-sensitive actions, and as such the MCP Gateway and the underlying MCP Servers form a critical line of defense against agent mistakes. We incorporate a development philosophy that our policies around MCP traffic should follow principle-of-least-privilege, and provide facilities to vet the actions of agents (e.g. MCP elicitation).

## Outcome

As standards stabilise, MCP Gateway adopts them or steps aside. Until then, teams get usable, production-grade infrastructure today.
Success is measured by reduced duplication across MCP servers, consistent policy enforcement, and the ability to onboard new MCP servers without bespoke gateway work.

### What does this mean in practice?

Note, these are just examples at the time of writing, and are not intended to be goals.

*Example 1:* The MCP Gateway project will provide a Kubernetes CRD, like MCPServerRegistration, that represents a running MCP Server somewhere.
This CRD is expected to converge with or be replaced by a 'Backend' resource that is aligned with the outcome of the ai-gateway working group or agentic-networking sub-project. This Backend resource may get implemented in the Gateway API provider, Istio, in time.

*Example 2:* The MCP Gateway project has some very specific Kuadrant AuthPolicy examples around tool permissions based on integrating with Keycloak. The MCP Gateway project will provide a KeycloakToolRoleMappingPolicy that wraps an AuthPolicy, abstracting the detailed rules configuration required to parse and iterate tools, checking against Keycloak role mappings.

*Example 3:* The MCP Gateway project includes an ext-proc component that parses MCP requests, hoisting information like the tool being called into headers. It also provides MCP server multiplexing by way of tool prefixing. In time, this multiplexing may be a feature available from Envoy proxy. We would look to leverage that feature in Envoy proxy instead of our own ext-proc at that time.

## Why Envoy?

- **Battle-tested foundation**: Envoy is a graduated CNCF project ([since 2018](https://www.cncf.io/announcements/2018/11/28/cncf-announces-envoy-graduation/)) with [proven production use](https://theirstack.com/en/technology/envoy/us) at scale
- **Rich extension model**: External Processor (ext_proc), WASM, Lua filters enable custom protocol support

## Why not use...

### ...standalone MCP Servers? (aka. the case for an MCP Gateway, in general)

- Each MCP server must independently implement auth, policy, observability, and security hardening  
- Agents must integrate with multiple endpoints instead of a single, stable entrypoint  
- Limitations around multi-tenancy and shared governance across teams  
- Scaling the number of servers scales operational complexity linearly

### ...Solo AgentGateway?

- Introduces a new MCP-aware proxy with its own data plane and lifecycle
- MCP routing, policy, and semantics are implemented outside native Envoy mechanisms like routing and filters.
- Long-term evolution tied to AgentGateway and kgateway roadmaps

### ...Envoy AI Gateway?

- Typically requires adopting Envoy AI Gateway *and* Envoy Gateway as the Gateway API provider
- MCP support is implemented via a Go-based MCP proxy, limiting reuse by downstream extensions (for example, auth and policy)
- Running the MCP data plane standalone requires static configuration and introduces coupling to AI Gateway–specific MCP semantics

### ...mcp-context-forge?

- Not Gateway API–native; does not integrate with standard Kubernetes ingress/routing primitives
- Not Kubernetes-native; operates outside cluster-level traffic and policy models
- Introduces a separate control and data plane rather than composing with Envoy
- Implemented in Python, making it less aligned with Envoy and Kubernetes native gateway ecosystems
