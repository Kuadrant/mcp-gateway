# Browser access for MCP gateways

MCP gateways now allow cross-origin browser requests without cookie-based
credentials from any origin. The external processor answers browser preflights
and adds the required response headers for both managed and custom HTTPRoutes.

Only preflight requests bypass authentication. Actual MCP requests still pass
through `AuthPolicy`. Protect the gateway with authentication even on a VPN or
private network; otherwise, a website visited by a network-connected user could
invoke tools and read their responses. See
[Browser access (CORS)](../guides/configure-mcp-gateway-listener-and-router.md#browser-access-cors).

The controller also publishes the resolved MCP URL in
`status.mcpEndpoint`, including a non-default listener port.

Native MCP clients are unaffected.
