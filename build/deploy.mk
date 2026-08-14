# Deploy

# Deploy single gateway for local demo (mcp-gateway in gateway-system)
# For e2e tests with multiple gateways, use deploy-e2e-gateways instead
.PHONY: deploy-gateway
deploy-gateway: $(KUSTOMIZE) ## Deploy single MCP gateway for local demo
	$(KUSTOMIZE) build config/istio/gateway | kubectl apply -f -

.PHONY: undeploy-gateway
undeploy-gateway: $(KUSTOMIZE) ## Remove the MCP gateway
	- $(KUSTOMIZE) build config/istio/gateway | kubectl delete -f -

.PHONY: deploy-namespaces
deploy-namespaces: # Create MCP namespaces
	kubectl apply -f config/mcp-system/namespace.yaml
	kubectl apply -f config/istio/gateway/namespace.yaml

# Deploy only the controller, using the images built and kind-loaded by
# build-and-load-image (tagged 'latest') instead of the pinned release version
.PHONY: deploy-controller-local
deploy-controller-local: install-crd ## Deploy only the controller, using locally-built images
	kubectl apply -k config/mcp-gateway/overlays/mcp-system-local/
	@echo "Waiting for controller to be ready..."
	@kubectl wait --for=condition=Available deployment/mcp-gateway-controller -n mcp-system --timeout=$(WAIT_TIME)
	@echo "Waiting for MCPGatewayExtension to be ready..."
	@kubectl wait --for=condition=Ready mcpgatewayextension/mcp-gateway-extension -n mcp-system --timeout=$(WAIT_TIME)
	@echo "Controller and broker-router are ready"

.PHONY: deploy-local
deploy-local: install-crd deploy-namespaces deploy-controller-local ## Deploy controller to mcp-system namespace using locally-built images
