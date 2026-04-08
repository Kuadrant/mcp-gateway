##@ Auth Examples

.PHONY: auth-example-setup
auth-example-setup: cert-manager-install kuadrant-install keycloak-install ## Setup auth example of OAuth2 authentication using Kuadrant, tool list filtering based on RBAC permissions configured in Keycloak, OAuth2 Token Exchange, and integration with Vault (requires: make local-env-setup)
	@echo "========================================="
	@echo "Setting up OAuth Example"
	@echo "========================================="
	@echo ""
	@echo "This setup activates configuration in the MCP Gateway to test the following use cases:"
	@echo "  - OAuth2 authentication using Keycloak as Identity Provider"
	@echo "  - Tool list filtering based on RBAC permissions configured in Keycloak"
	@echo "  - OAuth2 Token Exchange to obtain access tokens for backend services"
	@echo "  - Integration with HashiCorp Vault (alternative to OAuth2 Token Exchange)"
	@echo ""
	@echo "Prerequisites: make local-env-setup should be completed"
	@echo ""
	@echo "Step 1/6: Configuring OAuth environment variables..."
	@kubectl set env deployment/mcp-gateway \
		OAUTH_RESOURCE_NAME="MCP Server" \
		OAUTH_RESOURCE="http://mcp.127-0-0-1.sslip.io:8001/mcp" \
		OAUTH_AUTHORIZATION_SERVERS="https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp" \
		OAUTH_BEARER_METHODS_SUPPORTED="header" \
		OAUTH_SCOPES_SUPPORTED="basic,groups,roles,profile" \
		-n mcp-system
	@echo "✅ OAuth environment variables configured"
	@echo ""
	@echo "Step 2/6: Installing Vault..."
	@bin/kustomize build config/vault | bin/yq 'select(.kind == "Deployment").spec.template.spec.containers[0].args += ["-dev-root-token-id=root"] | .' | kubectl apply -f -
	@echo "✅ Vault installed"
	@echo ""
	@echo "Step 3/6: Applying AuthPolicy configurations..."
	@kubectl apply -k ./config/samples/oauth-token-exchange/
	@kubectl patch mcpgatewayextension mcp-gateway-extension -n mcp-system --type='merge' \
		-p='{"spec":{"trustedHeadersKey":{"secretName":"trusted-headers-public-key"}}}'
	@echo "✅ AuthPolicy configurations applied"
	@echo ""
	@echo "Step 4/6: Configuring CORS rules for the OpenID Connect Client Registration endpoint..."
	@kubectl apply -f ./config/keycloak/preflight_envoyfilter.yaml
	@echo "✅ CORS configured"
	@echo ""
	@echo "Step 5/6: Patch Authorino deployment to be able to connect to Keycloak..."
	@./utils/patch-authorino-to-keycloak.sh
	@echo "✅ Authorino deployment patched"
	@echo ""
	@echo "Step 6/6: Deploying test MCP servers..."
	@"$(MAKE)" deploy-test-servers
	@"$(MAKE)" deploy-example
	@echo "✅ Test MCP servers deployed and configured"
	@echo ""
	@echo "🎉 OAuth example setup complete!"
	@echo ""
	@echo "The mcp-broker now serves OAuth discovery information at:"
	@echo "  /.well-known/oauth-protected-resource"
	@echo ""
	@echo "Next step: Open MCP Inspector with 'make inspect-gateway'"
	@echo "and go through the OAuth flow with credentials: mcp/mcp"
