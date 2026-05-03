# Keycloak IdP for development

KEYCLOAK_NAMESPACE = keycloak
KEYCLOAK_ADMIN_USER = admin
KEYCLOAK_ADMIN_PASSWORD = admin

keycloak-install-impl:
	@echo "Installing Keycloak (dev mode using official image)..."
	@echo "Note: Using kubectl deployment due to Helm chart issues"
	@# Create namespace if it doesn't exist
	@kubectl create namespace $(KEYCLOAK_NAMESPACE) 2>/dev/null || echo "Namespace $(KEYCLOAK_NAMESPACE) already exists"
	@kubectl apply -f tests/e2e/assets/keycloak/realm-import.yaml
	@kubectl apply -f tests/e2e/assets/keycloak/deployment.yaml
	@echo "Waiting for Keycloak to be ready..."
	@kubectl wait --for=condition=ready pod -l app=keycloak -n $(KEYCLOAK_NAMESPACE) --timeout=120s || true
	@echo ""
	@echo "Patching Gateway for the Keycloak route..."
	@kubectl patch gateway mcp-gateway -n gateway-system --type json -p "$$(cat tests/e2e/assets/keycloak/patch-gateway.json)"
	@echo ""
	@echo "Creating HTTPRoute"
	@kubectl apply -f tests/e2e/assets/keycloak/httproute.yaml
	@echo ""
	@echo "Requesting a certificate for Keycloak..."
	@kubectl apply -f tests/e2e/assets/keycloak/certificate.yaml
	@for i in $$(seq 1 30); do kubectl get secret mcp-gateway-keycloak-cert -n gateway-system >/dev/null 2>&1 && break; echo "Waiting for TLS cert secret..."; sleep 2; done; \
		kubectl get secret mcp-gateway-keycloak-cert -n gateway-system >/dev/null 2>&1 || { echo "ERROR: TLS cert secret not created after 60s"; exit 1; }
	@echo ""
	@echo "Reconfiguring the Kubernetes API server to trust the Keycloak server for authentication..."
	@mkdir -p out/certs
	@kubectl get secret mcp-gateway-keycloak-cert -n gateway-system -o jsonpath='{.data.ca\.crt}' | base64 -d > out/certs/ca.crt
	@echo "Keycloak TLS certificate extracted to out/certs/ca.crt (bind-mounted to API server)"
	@GATEWAY_IP=$$(kubectl get gateway/mcp-gateway -n gateway-system -o jsonpath='{.status.addresses[0].value}' 2>/dev/null || echo '127.0.0.1'); \
		echo "Setting Keycloak hostname to resolve to the MCP Gateway IP: $$GATEWAY_IP"; \
		docker exec mcp-gateway-control-plane bash -c "grep -q 'keycloak.127-0-0-1.sslip.io' /etc/hosts || echo '$$GATEWAY_IP keycloak.127-0-0-1.sslip.io' >> /etc/hosts" || \
		podman exec mcp-gateway-control-plane bash -c "grep -q 'keycloak.127-0-0-1.sslip.io' /etc/hosts || echo '$$GATEWAY_IP keycloak.127-0-0-1.sslip.io' >> /etc/hosts"
	@echo "Keycloak hostname set to resolve to the MCP Gateway IP"
	@echo "Restarting Kubernetes API server to pick up new configs..."
	@docker exec mcp-gateway-control-plane pkill -f kube-apiserver || \
		podman exec mcp-gateway-control-plane pkill -f kube-apiserver
	@echo "Waiting for API server to restart..."
	@sleep 5
	@echo "Waiting for API server to be ready..."
	@for i in $$(seq 1 30); do \
		if kubectl get --raw /healthz >/dev/null 2>&1; then \
			echo "Kubernetes API server updated with new configs"; \
			break; \
		fi; \
		sleep 2; \
	done
	@echo ""
	@echo "🎉 Keycloak installed with bootstrapped MCP realm!"
	@echo ""
	@echo "Run 'make keycloak-forward' to access at http://localhost:8095"
	@echo "Admin credentials: $(KEYCLOAK_ADMIN_USER) / $(KEYCLOAK_ADMIN_PASSWORD)"
	@echo ""
	@echo "Realm: mcp"
	@echo "User: mcp / mcp"
	@echo "Email: mcp@example.com"
	@echo "Group: accounting (with mcp user as member)"
	@echo "Client Scope: groups (optional, with Group Membership mapper)"
	@echo "Session idle timeout: 15 minutes"
	@echo ""

.PHONY: keycloak-uninstall
keycloak-uninstall: # Uninstall Keycloak
	@idx=$$(kubectl get gateway mcp-gateway -n gateway-system -o json | jq -r '.spec.listeners | map(.name=="keycloak") | index(true)') && \
		kubectl patch gateway mcp-gateway -n gateway-system --type='json' -p="[{\"op\":\"remove\",\"path\":\"/spec/listeners/$${idx}\"}]"
	@kubectl delete -f tests/e2e/assets/keycloak/httproute.yaml 2>/dev/null || true
	@kubectl delete -f tests/e2e/assets/keycloak/deployment.yaml 2>/dev/null || true
	@kubectl delete -f tests/e2e/assets/keycloak/realm-import.yaml 2>/dev/null || true
	@kubectl delete namespace $(KEYCLOAK_NAMESPACE) 2>/dev/null || true

keycloak-status-impl:
	@if kubectl get svc -n $(KEYCLOAK_NAMESPACE) keycloak >/dev/null 2>&1; then \
		echo "========================================"; \
		echo "Keycloak Status"; \
		echo "========================================"; \
		echo ""; \
		echo "Status: Installed"; \
		echo ""; \
		echo "Admin Console:"; \
		echo "  URL: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)"; \
		echo "  Username: $(KEYCLOAK_ADMIN_USER)"; \
		echo "  Password: $(KEYCLOAK_ADMIN_PASSWORD)"; \
		echo ""; \
		echo "OIDC Endpoints:"; \
		echo "  Discovery: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/.well-known/openid-configuration"; \
		echo "  Token:     https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/protocol/openid-connect/token"; \
		echo "  Authorize: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/protocol/openid-connect/auth"; \
		echo "  UserInfo:  https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/protocol/openid-connect/userinfo"; \
		echo "  JWKS:      https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/protocol/openid-connect/certs"; \
		echo ""; \
		echo "Test Client Configuration:"; \
		echo "  Client ID: mcp-gateway"; \
		echo "  Root URL: http://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)"; \
		echo "  Valid Redirect URIs: http://mcp.127-0-0-1.sslip.io:$(KIND_HOST_PORT_MCP_GATEWAY)/*"; \
		echo "  Web Origins: +"; \
		echo ""; \
		echo "========================================"; \
	else \
		echo "Keycloak is not installed. Run: make keycloak-install"; \
	fi

.PHONY: keycloak-url
keycloak-url: # Get Keycloak URLs
	@echo "=== Keycloak URLs ==="
	@echo "Admin Console: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)"
	@echo "OIDC Discovery: https://keycloak.127-0-0-1.sslip.io:$(KIND_HOST_PORT_KEYCLOAK)/realms/master/.well-known/openid-configuration"
	@echo ""
	@echo "Credentials: $(KEYCLOAK_ADMIN_USER) / $(KEYCLOAK_ADMIN_PASSWORD)"
