# Debugging commands (most are accessed via main Makefile)

# Enable debug logging for Envoy
debug-envoy-impl:
	@echo "Enabling debug logging for all Istio gateways..."
	@PODS=$$(kubectl get pods -A -l gateway.istio.io/managed=istio.io-gateway-controller -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} {end}' 2>/dev/null); \
	if [ -z "$$PODS" ]; then \
		echo "Error: No Istio gateway pods found"; \
		exit 1; \
	fi; \
	for POD_INFO in $$PODS; do \
		NS=$$(echo $$POD_INFO | cut -d'/' -f1); \
		POD=$$(echo $$POD_INFO | cut -d'/' -f2); \
		echo "Enabling debug on pod: $$POD in namespace: $$NS"; \
		kubectl exec -n $$NS $$POD -- curl -s -X POST http://localhost:15000/logging?level=debug > /dev/null; \
	done
	@echo "Debug logging enabled on all gateways. Use 'make debug-envoy-off' to disable."

# Disable debug logging for Envoy
debug-envoy-off-impl:
	@echo "Setting Istio gateway logging to info level on all gateways..."
	@PODS=$$(kubectl get pods -A -l istio=ingressgateway -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} {end}' 2>/dev/null); \
	if [ -z "$$PODS" ]; then \
		echo "Error: No Istio gateway pods found"; \
		exit 1; \
	fi; \
	for POD_INFO in $$PODS; do \
		NS=$$(echo $$POD_INFO | cut -d'/' -f1); \
		POD=$$(echo $$POD_INFO | cut -d'/' -f2); \
		echo "Disabling debug on pod: $$POD in namespace: $$NS"; \
		kubectl exec -n $$NS $$POD -- curl -s -X POST http://localhost:15000/logging?level=info > /dev/null; \
	done
	@echo "Debug logging disabled on all gateways."

# Show Envoy configuration
.PHONY: debug-envoy-config
debug-envoy-config: # Show Envoy configuration dump
	@echo "Fetching Envoy configuration..."
	kubectl exec $$(kubectl get pods -l istio=ingressgateway -n gateway-system -o name) -n gateway-system -- \
		curl -s http://localhost:15000/config_dump | jq .	

# Show Envoy clusters
.PHONY: debug-envoy-clusters
debug-envoy-clusters: # Show Envoy cluster status
	@echo "Fetching Envoy clusters..."
	kubectl exec $$(kubectl get pods -l istio=ingressgateway -n gateway-system -o name) -n gateway-system -- \
		curl -s http://localhost:15000/clusters

# Show Envoy listeners
.PHONY: debug-envoy-listeners
debug-envoy-listeners: # Show Envoy listeners
	@echo "Fetching Envoy listeners..."
	kubectl exec $$(kubectl get pods -l istio=ingressgateway -n gateway-system -o name) -n gateway-system -- \
		curl -s http://localhost:15000/listeners

# Access Envoy admin interface
.PHONY: debug-envoy-admin
debug-envoy-admin: # Port forward Envoy admin interface to localhost:15000
	@echo "Forwarding Envoy admin interface to http://localhost:15000"
	@echo "You can access:"
	@echo "  - Config dump: http://localhost:15000/config_dump"
	@echo "  - Stats: http://localhost:15000/stats"
	@echo "  - Clusters: http://localhost:15000/clusters"
	@echo "  - Logging: http://localhost:15000/logging"
	kubectl port-forward -n gateway-system $$(kubectl get pods -l istio=ingressgateway -n gateway-system -o name) 15000:15000

# Watch gateway logs
debug-logs-gateway-impl:
	@GATEWAY_NS=$$(kubectl get pods -A -l istio=ingressgateway -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null); \
	if [ -z "$$GATEWAY_NS" ]; then \
		echo "Error: No Istio gateway pod found"; \
		exit 1; \
	fi; \
	echo "Watching logs for Istio gateway in namespace: $$GATEWAY_NS"; \
	kubectl logs -f -n $$GATEWAY_NS -l istio=ingressgateway

# Watch specific component logs
.PHONY: logs-mock
logs-mock: # Tail mock MCP server logs
	kubectl logs -f -n mcp-server -l app=mcp-test

.PHONY: logs-istiod
logs-istiod: # Tail Istiod control plane logs
	kubectl logs -f -n istio-system -l app=istiod

.PHONY: logs-all
logs-all: # Show recent logs from all MCP-related components
	@echo "=== Recent Istio Gateway logs ==="
	@kubectl logs -n gateway-system -l istio=ingressgateway --tail=20 2>/dev/null || echo "No gateway logs"
	@echo ""
	@echo "=== Recent Mock MCP logs ==="
	@kubectl logs -n mcp-server -l app=mcp-test --tail=20 2>/dev/null || echo "No mock MCP logs"
	@echo ""
	@echo "=== Recent Istiod logs ==="
	@kubectl logs -n istio-system -l app=istiod --tail=10 2>/dev/null || echo "No istiod logs"

# Enable Envoy ext_proc debug logging
.PHONY: debug-ext-proc
debug-ext-proc: # Enable debug logging for ext_proc filter
	@echo "Enabling debug logging for ext_proc filter..."
	kubectl exec $$(kubectl get pods -l istio=ingressgateway -n gateway-system -o name) -n gateway-system -- \
		curl -X POST "http://localhost:15000/logging?ext_proc=debug"
	@echo "ext_proc debug logging enabled."
