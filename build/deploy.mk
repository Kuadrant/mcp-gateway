# Deploy

.PHONY: deploy-gateway
deploy-gateway: $(KUSTOMIZE) # Deploy the MCP gateway with Istio
	$(KUSTOMIZE) build config/istio/gateway | kubectl apply -f -

.PHONY: undeploy-gateway
undeploy-gateway: $(KUSTOMIZE) # Remove the MCP gateway
	- $(KUSTOMIZE) build config/istio/gateway | kubectl delete -f -

.PHONY: deploy-namespaces
deploy-namespaces: # Create MCP namespaces
	kubectl apply -f config/mcp-system/namespace.yaml
	kubectl apply -f config/istio/gateway/namespace.yaml
