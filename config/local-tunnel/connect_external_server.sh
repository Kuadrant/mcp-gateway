#!/bin/bash
set -e

EXTERNAL_DOMAIN="$1"

if [ -z "$EXTERNAL_DOMAIN" ]; then
  read -p "Enter your external domain (e.g., my-tunnel.ngrok-free.dev or api.myserver.com): " EXTERNAL_DOMAIN
fi

if [ -z "$EXTERNAL_DOMAIN" ]; then
  echo "Error: External domain is required."
  exit 1
fi

echo "Configuring connection to external MCP server: $EXTERNAL_DOMAIN"

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

# Generate ServiceEntry
# This tells the mesh about the external service and maps port 80 traffic to it.
cat <<EOF > "$SCRIPT_DIR/service-entry.yaml"
apiVersion: networking.istio.io/v1beta1
kind: ServiceEntry
metadata:
  name: local-tunnel-entry
  namespace: gateway-system
spec:
  exportTo:
  - "*"
  hosts:
  - $EXTERNAL_DOMAIN
  location: MESH_EXTERNAL
  ports:
  - name: https
    number: 443
    protocol: HTTPS
  - name: http
    number: 80
    protocol: HTTP
  resolution: DNS
  endpoints:
  - address: $EXTERNAL_DOMAIN
    ports:
      http: 443 # Map HTTP port 80 traffic to the HTTPS endpoint port 443
EOF

# Generate DestinationRule
# This enforces TLS (HTTPS) when talking to the external domain.
cat <<EOF > "$SCRIPT_DIR/destination-rule.yaml"
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: local-tunnel-tls
  namespace: gateway-system
spec:
  exportTo:
  - "*"
  host: $EXTERNAL_DOMAIN
  trafficPolicy:
    tls:
      mode: SIMPLE
      sni: $EXTERNAL_DOMAIN
EOF

# Generate Local MCP Server Resources
# Service: A ClusterIP target for internal routing.
# HTTPRoute: Rewrites the host and path for the external server.
# MCPServer: Registers the tool with the Gateway.
cat <<EOF > "$SCRIPT_DIR/local-mcp-server.yaml"
apiVersion: v1
kind: Service
metadata:
  name: local-tunnel-service
  namespace: mcp-system
spec:
  type: ExternalName
  externalName: $EXTERNAL_DOMAIN
  ports:
  - name: https
    port: 443
    targetPort: 443
    protocol: TCP
    appProtocol: https
  - name: http-fallback
    port: 80
    targetPort: 443
    protocol: TCP
    appProtocol: https

---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: local-tunnel-route
  namespace: mcp-system
spec:
  parentRefs:
  - name: mcp-gateway
    namespace: gateway-system
  hostnames:
  - 'local-dev.mcp.local'
  - '$EXTERNAL_DOMAIN'
  rules:
  - filters:
    - type: RequestHeaderModifier
      requestHeaderModifier:
        add:
          # Harmless for non-ngrok servers, but required for ngrok free tier
          - name: ngrok-skip-browser-warning
            value: "true"
    - type: URLRewrite
      urlRewrite:
        hostname: $EXTERNAL_DOMAIN
    backendRefs:
    - name: local-tunnel-service
      port: 443

---
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServer
metadata:
  name: local-dev-server
  namespace: mcp-system
spec:
  toolPrefix: "local_"
  path: "/mcp"
  targetRef:
    group: "gateway.networking.k8s.io"
    kind: "HTTPRoute"
    name: "local-tunnel-route"
EOF

echo "Applying resources..."
kubectl apply -f "$SCRIPT_DIR/"

echo "Configuration applied successfully!"
echo "Your external MCP server ($EXTERNAL_DOMAIN) is now connected."
