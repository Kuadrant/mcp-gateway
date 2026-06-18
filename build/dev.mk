# Local Development

# Update EnvoyFilter to point to local router
.PHONY: dev-envoyfilter
dev-envoyfilter: # Configure EnvoyFilter to use local MCP router
	@echo "Updating EnvoyFilter to use host.docker.internal:50051..."
	kubectl apply -f config/dev/envoyfilter.yaml
	kubectl apply -f config/dev/serviceentry.yaml

# Create a service that points to host for broker
.PHONY: dev-broker-service
dev-broker-service: # Create service pointing to local MCP broker
	@echo "Swapping broker service to point to host.docker.internal:8080..."
	kubectl delete -f config/mcp-system/broker-service.yaml
	kubectl apply -f config/dev/broker-service.yaml

# Create a service that points to host for broker
.PHONY: dev-broker-service-in-cluster
dev-broker-service-in-cluster: # Create service pointing to local MCP broker
	@echo "Swapping service to point to 8080 in cluster..."
	kubectl delete -f config/dev/broker-service.yaml
	kubectl apply -f config/mcp-system/broker-service.yaml

# Setup for local development
.PHONY: dev-setup
dev-setup: dev-envoyfilter dev-broker-service # Configure cluster to use local services
	@echo "Cluster configured for local development!"
	@echo "Now run:"
	@echo "  make run-router   # In one terminal"
	@echo "  make run-broker   # In another terminal"

# Reset to in-cluster configuration
.PHONY: dev-reset
dev-reset: # Reset to in-cluster service configuration
	kubectl apply -f config/istio/envoyfilter.yaml
	@echo "Reset to in-cluster configuration"

# Watch logs from the gateway
.PHONY: dev-logs-gateway
dev-logs-gateway: # Watch logs from the Istio gateway
	kubectl -n gateway-system logs -f deployment/mcp-gateway-istio

# Test the MCP flow
.PHONY: dev-test
dev-test: # Test MCP request through the gateway
	@echo "Testing MCP request through gateway..."
	curl -k -X POST \
		-H "Content-Type: application/json" \
		-d '{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}' \
		$(GATEWAY_SCHEME)://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)/mcp

# Clean up port forwards
.PHONY: dev-stop-forward
dev-stop-forward: # Stop any kubectl port-forward processes
	@echo "Stopping kubectl port-forward processes..."
	-pkill -f "kubectl.*port-forward" || true
	@echo "Port forwards stopped"

# Stop all local development processes
.PHONY: dev-stop
dev-stop: dev-stop-forward # Stop all local dev processes (port-forwards, router, broker)
	@echo "Stopping local mcp-router and mcp-broker..."
	-pkill -f "mcp-router" || true
	-pkill -f "mcp-broker" || true
	@echo "All local development processes stopped"

# Run broker locally with port-forwarded upstreams
.PHONY: dev-broker-local
dev-broker-local: ## Run broker locally with port-forwarded MCP servers
	@echo "Setting up local broker with port-forwarded upstreams..."
	@# Extract config from ConfigMap
	@kubectl -n mcp-system get configmap mcp-gateway-config -o jsonpath='{.data.config\.yaml}' > /tmp/mcp-broker-config.yaml 2>/dev/null || (echo "Error: ConfigMap not found. Run 'make deploy-example-gateway' first." && exit 1)
	@# Start port-forwards in background
	@echo "Starting port-forwards for MCP servers..."
	@kubectl -n mcp-test port-forward svc/mcp-test-server1 9091:9090 > /dev/null 2>&1 &
	@kubectl -n mcp-test port-forward svc/mcp-test-server2 9092:9090 > /dev/null 2>&1 &
	@kubectl -n mcp-test port-forward svc/mcp-test-server3 9093:9090 > /dev/null 2>&1 &
	@sleep 2
	@# Rewrite config to use localhost ports
	@sed -e 's|http://mcp-test-server1.mcp-test.svc.cluster.local:9090|http://localhost:9091|g' \
	     -e 's|http://mcp-test-server2.mcp-test.svc.cluster.local:9090|http://localhost:9092|g' \
	     -e 's|http://mcp-test-server3.mcp-test.svc.cluster.local:9090|http://localhost:9093|g' \
	     /tmp/mcp-broker-config.yaml > /tmp/mcp-broker-config-local.yaml
	@echo "Config rewritten for local ports:"
	@echo "  server1: localhost:9091"
	@echo "  server2: localhost:9092"
	@echo "  server3: localhost:9093"
	@echo ""
	@echo "Starting broker locally..."
	./bin/mcp-broker-router --mcp-gateway-config=/tmp/mcp-broker-config-local.yaml --mcp-broker-public-address=0.0.0.0:8080 --mcp-router-address=0.0.0.0:50051

# Stop local broker and port-forwards
.PHONY: dev-broker-stop
dev-broker-stop: ## Stop local broker and port-forwards
	@echo "Stopping broker and port-forwards..."
	-pkill -f "kubectl.*port-forward.*mcp-test-server" || true
	-pkill -f "mcp-broker-router" || true
	@echo "Local broker and port-forwards stopped"
