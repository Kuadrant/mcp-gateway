## Routing

### Problem

In order to expose MCP Servers, the MCP Gateway aggregates the available tools from the registered backend MCP servers and presents them as a single tool set to agent applications. As there is only one endpoint expected by the agent: `https://{host}/mcp` , the gateway needs to be able to accept MCP protocol requests at that endpoint and then be able to make a routing decision about which backend MCP server should receive the call.


### Solution

> Note: In describing the solution [Gateway API](https://gateway-api.sigs.k8s.io/) and Kubernetes are used as the basis for defining and deploying routes and gateways. It is worth mentioning that the result of all these APIs is config that is eventually consumed by Envoy and as such could be done without the need for the Gateway API resources.

MCP Gateway router component as ext_proc intercept all requests hitting the /mcp endpoint before the Envoy router. Based on its configuration the router decides what to set the `:authority` header to in order to define the routing decision for Envoy. The router component owns the `:authority` header for all requests hitting the listener it is programmed to route on (the MCP Gateway listener). It will ensure that the `:authority` header is always set to the correct value for the requests it receives. If there is a failure or the router is not running, envoy will not proceed with the request as the `failure_mode_allow` will be set to false. The correct value for all none tools calls is the external host the gateway is exposed on. The correct value in the case of a `tool/call` is the host set in the HTTPRoute targeted by the `MCPServer` resource. This HTTPRoute host, can be any value, it is purely a value to tell the router what to set the `:authority` header to. 

![](./images/mcp-gateway-routing.jpg)


### The Router

The router component is configured to know about the different backend MCP Servers that have been registered. This MCPSever configuration is managed by the MCP Gateway Controller component. The router should also be configured with the public listener hostname via `required` flag `--mcp-gateway-public-host`. The router intercepts all requests to the gateway MCP listener before routing has happened and based on its configuration decides whether the request should be processed by the MCP Broker or configure the routing to ensure the request is sent to the correct MCP Server. The router only re-routes `tools/calls` but validates all calls hitting the MCP gateway listener to ensure clients cannot explicitly bypass the broker (note they can never bypass the router).

#### The MCPServerRegistration resource

The MCP Server resource is a Kubernetes CRD used to register and configure an MCP server to be exposed via the Gateway. It targets a HTTPRoute and sets some additional configuration. This resource is reconciled by the MCP Gateway Controller into configuration that the router can consume to make routing decisions.

### The Gateway Listeners

Although there are many ways to configure the gateway. What is shown here is an example that we use for testing and validation. It clearly separates the MCP Gateway from all other HTTP traffic.

```yaml
listeners:
    - name: mcp-gateway # gateway listener
      hostname: 'mcp.127-0-0-1.sslip.io' #public accessibly MCP host one used by client
      port: 8080 # this is a port dedicated for use with the MCP Gateway. This way the ext_proc can be configured to only intercept request to this listener port and expect them to be JSONRPC based MCP requests validate them and ensure the :authority header is set correctly based on the type of request
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: All
    - name: mcp-servers # no host name required as the host is set by the HTTRoute
      port: 8080
      protocol: HTTP
      hostname: '*.mcp.local' # this hostname can be any wildcard hostname. It is here to ensure the MCP Server routes attach only to this listener and can be protected by the correct AuthPolicy. 
      allowedRoutes:
        namespaces:
          from: All #any namespace can register an MCP server route
```


So here we have dedicated port 8080 to MCP requests.  It is strongly recommended to use another port for other workloads so that the router doesn't intercept these requests. In our example gateway we configure a new listener and port for keycloak for example:


```yaml
    - name: keycloak
      hostname: 'keycloak.127-0-0-1.sslip.io'
      port: 8002 #notice this is a different port to avoid the ext_proc router receiving these requests
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              kubernetes.io/metadata.name: keycloak

```
#### The Routes

To configure the MCP Gateway we have two different routes with distinct routing responsibilities, both of these are required:

- **The MCP Gateway Route:** This route is how agents interact with the Gateway and is intended to be the route exposed for use (for example via a DNS resolvable hostname). The default backend for this route must be the MCP Broker component. From a client perspective this endpoint acts as an MCP Server.[Example](../../config/mcp-system/httproute.yaml). Although this route will also receive tools/calls it does not actually send tools/calls to the broker backend. These are intercepted and re-routed.

- **Individual MCP Server Routes:** These are intended to route to individual MCP Servers that can handle distinct tools/calls from a client. There can be many of these routes but there is expected to be a 1:1 relationship between a route and a MCP Backend. Each MCP Server route should have some form of hostname set and it should match the gateway listener (in the above example `*.mcp.local`). The hostname used, is not hugely important as it is not expected to be DNS resolvable. In our examples we use `{ServerName}.mcp.local` in the HTTPRoute. Each route should have a single rule that points at the MCP backend. [Example](../../config/test-servers/server1-httproute.yaml).

### MCPServerRegistration Config Routing

When the controller reconciles an MCPServerRegistration, it must decide which MCPGatewayExtension namespaces should receive the server's config in their `mcp-gateway-config` secret. The listener port (derived from the MCPGatewayExtension's `sectionName`) is the binding key for this filtering.

#### Flow

1. **Resolve the HTTPRoute**: The controller fetches the HTTPRoute referenced by the MCPServerRegistration's `targetRef`.
2. **Find accepted Gateways**: From the HTTPRoute's status, it collects all parent Gateways that have accepted the route (`Accepted=True`).
3. **Find MCPGatewayExtensions**: For each accepted Gateway, it finds all valid MCPGatewayExtensions targeting that Gateway.
4. **Filter by listener port**: For each MCPGatewayExtension, the controller looks up the listener port from the extension's `sectionName`. It then checks whether the HTTPRoute attaches to a listener on that same port.

```
MCPServerRegistration
    │
    ▼
HTTPRoute (from targetRef)
    │
    ▼
Accepted Gateway(s) (from HTTPRoute status)
    │
    ▼
MCPGatewayExtension(s) targeting Gateway
    │
    ▼
Filter: does HTTPRoute attach to a listener
        on the same port as the extension's sectionName?
    │
    ├── parentRef.sectionName set → listener name must be on same port
    └── no sectionName → hostname match on same port (wildcard supported)
    │
    ▼
Write config to matching extension namespace(s)
```

#### Attachment check

The `httpRouteAttachesToListener` function checks whether an HTTPRoute is attached to a listener on the same port as the MCPGatewayExtension's target listener. It operates in two modes:

- **Explicit sectionName in HTTPRoute parentRef**: The parentRef's `sectionName` must resolve to a listener on the same port as the extension's target listener.
- **No sectionName in HTTPRoute parentRef**: The HTTPRoute's hostnames are matched against hostnames of all listeners on the same port, with wildcard support (e.g. `server1.team-a.mcp.local` matches `*.team-a.mcp.local`).

This is what enables team isolation on a shared gateway. If team-a's listeners are on port 8080 and team-b's on port 8081, each team's broker only receives config for MCP servers whose HTTPRoutes attach to their respective port.
