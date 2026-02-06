     helm upgrade -i team-b-nodeport ./charts/mcp-gateway \
    --namespace gateway-system \
    --set controller.enabled=false \
    --set broker.create=false \
    --set gateway.create=false \
    --set gateway.name=team-b-gateway \
    --set gateway.namespace=gateway-system \
    --set gateway.nodePort.create=true \
    --set gateway.nodePort.mcpPort=30471 \
    --set httpRoute.create=false \
    --set mcpGatewayExtension.create=false \
    --set envoyFilter.create=false


      helm upgrade -i team-a-nodeport ./charts/mcp-gateway \
    --namespace gateway-system \
    --set controller.enabled=false \
    --set broker.create=false \
    --set gateway.create=false \
    --set gateway.name=team-a-gateway \
    --set gateway.namespace=gateway-system \
    --set gateway.nodePort.create=true \
    --set gateway.nodePort.mcpPort=30080 \
    --set httpRoute.create=false \
    --set mcpGatewayExtension.create=false \
    --set envoyFilter.create=false
