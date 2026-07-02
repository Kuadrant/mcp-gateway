# Inspection & URLs

open := $(shell { which xdg-open || which open; } 2>/dev/null)

INSPECT_LOCAL_PORT ?= 9999

# generic template for inspecting MCP servers via port-forward
# args: $(1) = server name, $(2) = service name, $(3) = namespace, $(4) = service port, $(5) = mcp path, $(6) = extra notes
define inspect-server-template
	@set -eu; \
		echo "Port-forwarding to $(1) ($(2).$(3):$(4))..."; \
		kubectl port-forward -n $(3) svc/$(2) $(INSPECT_LOCAL_PORT):$(4) & \
		PF_PID=$$!; \
		trap "echo 'Cleaning up...'; kill $$PF_PID 2>/dev/null || true; exit" EXIT INT TERM; \
		sleep 2; \
		$(if $(6),echo "$(6)";) \
		INSPECTOR_URL="http://localhost:6274/?transport=streamable-http&serverUrl=http://localhost:$(INSPECT_LOCAL_PORT)$(5)"; \
		echo "MCP server: http://localhost:$(INSPECT_LOCAL_PORT)$(5)"; \
		echo "Inspector:  $$INSPECTOR_URL"; \
		echo ""; \
		MCP_AUTO_OPEN_ENABLED=false DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@0.21.1 & \
		sleep 2; \
		$(open) "$$INSPECTOR_URL" 2>/dev/null || echo "(Could not open browser automatically)"; \
		echo "Press Ctrl+C to stop and cleanup"; \
		wait
endef

# URLs for services
urls-impl:
	@echo "=== MCP Gateway URLs ==="
	@echo "Gateway: $(GATEWAY_SCHEME)://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)"
	@echo "Keycloak: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)"

##@ Inspection

.PHONY: inspect-server1
inspect-server1: ## Open MCP Inspector for test server 1
	$(call inspect-server-template,test server 1,mcp-test-server1,mcp-test,9090,/mcp)

.PHONY: inspect-server2
inspect-server2: ## Open MCP Inspector for test server 2
	$(call inspect-server-template,test server 2,mcp-test-server2,mcp-test,9090,/mcp)

.PHONY: inspect-server3
inspect-server3: ## Open MCP Inspector for test server 3
	$(call inspect-server-template,test server 3,mcp-test-server3,mcp-test,9090,/mcp)

.PHONY: inspect-api-key-server
inspect-api-key-server: ## Open MCP Inspector for API key test server (requires auth)
	$(call inspect-server-template,API key test server,mcp-api-key-server,mcp-test,9090,/mcp,NOTE: This server requires Bearer token authentication)

.PHONY: inspect-oidc-server
inspect-oidc-server: ## Open MCP Inspector for OpenID Connect test server (requires auth)
	$(call inspect-server-template,OIDC test server,mcp-oidc-server,mcp-test,9090,/mcp,NOTE: This server requires OIDC Bearer token authentication)

.PHONY: inspect-everything-server
inspect-everything-server: ## Open MCP Inspector for test everything server
	$(call inspect-server-template,test everything server,everything-server,mcp-test,9090,/mcp)

# Open MCP Inspector for gateway (broker via gateway)
.PHONY: inspect-gateway
inspect-gateway: ## Open MCP Inspector for the gateway
	@echo "Opening MCP Inspector for gateway"; \
	echo "URL: $(GATEWAY_SCHEME)://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)/mcp"; \
	echo ""; \
	MCP_AUTO_OPEN_ENABLED=false DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@0.21.1 & \
	sleep 2; \
	$(open) "http://localhost:6274/?transport=streamable-http&serverUrl=$(GATEWAY_SCHEME)://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)/mcp"; \
	echo "Press Ctrl+C to stop and cleanup"; \
	wait

# Show status of all MCP components implementation
status-impl:
	@echo "=== Cluster Components ==="
	@kubectl get pods -n istio-system | grep -E "(istiod|sail)" || echo "Istio: Not found"
	@kubectl get pods -n gateway-system | grep gateway || echo "Gateway: Not found"
	@kubectl get pods -n mcp-system 2>/dev/null || echo "MCP System: No pods"
	@kubectl get pods -n mcp-server 2>/dev/null || echo "Mock MCP: No pods"
	@echo ""
	@echo "=== Local Processes ==="
	@lsof -i :8080 | grep LISTEN | head -1 || echo "Broker: Not running (port 8080)"
	@lsof -i :9002 | grep LISTEN | head -1 || echo "Router: Not running (port 9002)"
	@echo ""
	@echo "=== Port Forwards ==="
	@ps aux | grep -E "kubectl.*port-forward" | grep -v grep || echo "No active port-forwards"
