##@ Auth Examples

.PHONY: auth-example-setup-no-vault
auth-example-setup-no-vault: cert-manager-install kuadrant-install keycloak-install ## Setup auth example without Vault or token exchange (skips Vault install and AuthPolicy/token-exchange configs)
	@echo "========================================="
	@echo "Setting up OAuth Example (no Vault)"
	@echo "========================================="
	@echo ""
	@echo "Step 1/4: Configuring OAuth environment variables..."
	@kubectl set env deployment/mcp-gateway \
		OAUTH_RESOURCE_NAME="MCP Server" \
		OAUTH_RESOURCE="http://mcp.127-0-0-1.sslip.io:8001/mcp" \
		OAUTH_AUTHORIZATION_SERVERS="https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp" \
		OAUTH_BEARER_METHODS_SUPPORTED="header" \
		OAUTH_SCOPES_SUPPORTED="basic,groups,roles,profile,offline_access" \
		-n mcp-system
	@echo "✅ OAuth environment variables configured"
	@echo ""
	@echo "Step 2/4: Configuring CORS rules for the OpenID Connect Client Registration endpoint..."
	@kubectl apply -f ./config/keycloak/preflight_envoyfilter.yaml
	@echo "✅ CORS configured"
	@echo ""
	@echo "Step 3/4: Patch Authorino deployment to be able to connect to Keycloak..."
	@$(detect-kuadrant-ns); \
	./utils/patch-authorino-to-keycloak.sh $$KUADRANT_NS
	@echo "✅ Authorino deployment patched"
	@echo ""
	@echo "Step 4/4: Deploying test MCP servers..."
	@"$(MAKE)" deploy-test-servers
	@"$(MAKE)" deploy-example
	@echo "✅ Test MCP servers deployed and configured"
	@echo ""
	@echo "🎉 OAuth example setup complete (no Vault)!"
	@echo ""

.PHONY: auth-example-setup
auth-example-setup: auth-example-setup-no-vault ## Setup auth example with Vault and token exchange (requires: make local-env-setup or local-env-setup-olm)
	@echo ""
	@echo "Installing Vault..."
	@bin/kustomize build config/vault | bin/yq 'select(.kind == "Deployment").spec.template.spec.containers[0].args += ["-dev-root-token-id=root"] | .' | kubectl apply -f -
	@echo "✅ Vault installed"
	@echo ""
	@echo "Applying AuthPolicy configurations..."
	@kubectl apply -k ./config/samples/oauth-token-exchange/
	@$(detect-kuadrant-ns); \
	kubectl apply -f ./config/samples/oauth-token-exchange/trusted-headers-private-key.yaml -n $$KUADRANT_NS; \
	kubectl apply -f ./config/samples/oauth-token-exchange/token-exchange-secret.yaml -n $$KUADRANT_NS
	@kubectl patch mcpgatewayextension mcp-gateway-extension -n mcp-system --type='merge' \
		-p='{"spec":{"trustedHeadersKey":{"secretName":"trusted-headers-public-key"}}}'
	@echo "✅ AuthPolicy configurations applied"
	@echo ""
	@echo "🎉 Full OAuth example setup complete (with Vault)!"
