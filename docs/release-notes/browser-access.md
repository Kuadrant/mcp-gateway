# Browser access for MCP gateways

MCP gateways now allow non-credentialed cross-origin browser requests from any
origin. The external processor answers browser preflights and adds the required
response headers for both managed and custom HTTPRoutes.

The controller also publishes the resolved MCP URL in
`status.mcpEndpoint`, including a non-default listener port.

Native MCP clients are unaffected.
