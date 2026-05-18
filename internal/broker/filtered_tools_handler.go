package broker

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var authorizedCapabilitiesHeader = http.CanonicalHeaderKey("x-mcp-authorized")
var virtualMCPHeader = http.CanonicalHeaderKey("x-mcp-virtualserver")

const allowedCapabilitiesClaimKey = "allowed-capabilities"

// FilterTools reduces the tool set based on authorization headers.
// Priority: x-mcp-authorized JWT filtering, then x-mcp-virtualserver filtering.
func (broker *mcpBrokerImpl) FilterTools(ctx context.Context, _ any, mcpReq *mcp.ListToolsRequest, mcpRes *mcp.ListToolsResult) {
	attrs := []attribute.KeyValue{brokerComponentAttr}
	if sid := sessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", sid))
	}
	ctx, span := brokerTracer().Start(ctx, "mcp-broker.tools-list", trace.WithAttributes(attrs...))
	defer span.End()

	broker.logger.DebugContext(ctx, "FilterTools called", "input_tools_count", len(mcpRes.Tools))
	// mcp-go builds a fresh Tools slice per ListTools response, so assigning
	// here does not alias shared state. individual Tool.Meta pointers are
	// shared though -- see removeGatewayMeta for the copy-on-write handling.
	tools := mcpRes.Tools
	emptyTools := []mcp.Tool{}
	if len(mcpRes.Tools) == 0 {
		mcpRes.Tools = emptyTools
		return
	}

	// step 1: apply x-mcp-authorized filtering (JWT-based)
	tools = broker.applyAuthorizedCapabilitiesFilter(mcpReq.Header, tools)
	broker.logger.DebugContext(ctx, "FilterTools authorized capabilities result", "output_tools_count", len(tools))

	// step 2: apply virtual server filtering
	tools = broker.applyVirtualServerFilter(mcpReq.Header, tools)

	// step 3: apply session scope filtering (discovery feature)
	if broker.discovery.enabled {
		tools = broker.applyScopeFilter(ctx, tools)
	}

	// filter out any gateway specific meta data we are storing internally before sending to clients
	tools = broker.removeGatewayMeta(tools)
	broker.logger.DebugContext(ctx, "FilterTools final result", "output_tools_count", len(tools))

	span.SetAttributes(attribute.Int("mcp.tools.count", len(tools)))

	// ensure we never return nil (would serialize as null instead of [])
	if tools == nil {
		tools = emptyTools
	}
	mcpRes.Tools = tools
}

func (broker *mcpBrokerImpl) removeGatewayMeta(tools []mcp.Tool) []mcp.Tool {
	broker.logger.Debug("removing gateway specific meta")
	// the tools slice is unique per mcpResponse (mcp-go builds a fresh slice for
	// each ListTools call), so indexing into it is safe. however, the Tool.Meta
	// pointers inside are shared with the server's internal tool map. mutating
	// AdditionalFields in-place would be a data race when concurrent ListTools
	// calls run the AfterListTools hook, so we copy Meta before modifying.
	for i, t := range tools {
		if t.Meta == nil || len(t.Meta.AdditionalFields) == 0 {
			continue
		}
		cleaned := make(map[string]any, len(t.Meta.AdditionalFields))
		for k, v := range t.Meta.AdditionalFields {
			if k == "kuadrant/id" || k == brokerToolMetaKey {
				continue
			}
			cleaned[k] = v
		}
		cp := *t.Meta
		cp.AdditionalFields = cleaned
		tools[i].Meta = &cp
	}
	return tools
}

// applyAuthorizedCapabilitiesFilter filters tools based on x-mcp-authorized JWT header.
// Returns original tools if header not present and enforcement is off.
// Returns empty slice if header validation fails or enforcement is on without header.
func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilter(headers http.Header, tools []mcp.Tool) []mcp.Tool {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		broker.logger.Debug("no x-mcp-authorized header", "enforced", broker.enforceCapabilityFilter)
		if broker.enforceCapabilityFilter {
			return []mcp.Tool{}
		}
		return tools
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-mcp-authorized header", "error", err)
		return []mcp.Tool{}
	}

	allowedTools, hasTools := capabilities["tools"]
	if !hasTools {
		broker.logger.Debug("no tools key in capabilities")
		if broker.enforceCapabilityFilter {
			return []mcp.Tool{}
		}
		return tools
	}

	return broker.filterToolsByServerMap(allowedTools)
}

// parseAuthorizedCapabilitiesJWT validates and extracts allowed capabilities from the JWT header.
func (broker *mcpBrokerImpl) parseAuthorizedCapabilitiesJWT(headerValues []string) (map[string]map[string][]string, error) {
	if len(headerValues) != 1 {
		return nil, fmt.Errorf("expected exactly 1 header value, got %d", len(headerValues))
	}

	jwtValue := headerValues[0]
	if jwtValue == "" {
		return nil, fmt.Errorf("empty header value")
	}

	if broker.trustedHeadersPublicKey == "" {
		return nil, fmt.Errorf("no public key configured to validate JWT")
	}

	token, err := validateJWTHeader(jwtValue, broker.trustedHeadersPublicKey)
	if err != nil {
		return nil, fmt.Errorf("JWT validation failed: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("failed to extract claims from JWT")
	}

	capabilitiesClaim, ok := claims[allowedCapabilitiesClaimKey]
	if !ok {
		return nil, fmt.Errorf("missing %s claim in JWT", allowedCapabilitiesClaimKey)
	}

	capabilitiesJSON, ok := capabilitiesClaim.(string)
	if !ok {
		return nil, fmt.Errorf("%s claim is not a string", allowedCapabilitiesClaimKey)
	}

	var capabilities map[string]map[string][]string
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
		return nil, fmt.Errorf("failed to unmarshal allowed-capabilities JSON: %w", err)
	}

	broker.logger.Debug("parsed authorized capabilities", "capabilities", capabilities)
	return capabilities, nil
}

func (broker *mcpBrokerImpl) findServerByName(name string) upstream.ActiveMCPServer {
	for _, upstream := range broker.mcpServers {
		if upstream.MCPName() == name {
			return upstream
		}
	}
	return nil
}

// filterToolsByServerMap filters tools based on a map of server name to allowed tool names.
func (broker *mcpBrokerImpl) filterToolsByServerMap(allowedTools map[string][]string) []mcp.Tool {
	var filtered []mcp.Tool

	for serverName, toolNames := range allowedTools {
		upstream := broker.findServerByName(serverName)
		if upstream == nil {
			broker.logger.Error("upstream not found", "server", serverName)
			continue
		}
		tools := upstream.GetManagedTools()
		if tools == nil {
			broker.logger.Debug("no tools registered for upstream server", "server", upstream.MCPName)
			continue
		}

		for _, tool := range tools {
			broker.logger.Debug("checking access", "tool", tool.Name, "against", toolNames)
			if slices.Contains(toolNames, tool.Name) {
				broker.logger.Debug("access granted", "tool", tool.Name)
				tool.Name = fmt.Sprintf("%s%s", upstream.Config().Prefix, tool.Name)
				filtered = append(filtered, tool)
			}
		}
	}

	return filtered
}

// applyVirtualServerFilter filters tools to only those specified in the virtual server.
func (broker *mcpBrokerImpl) applyVirtualServerFilter(headers http.Header, tools []mcp.Tool) []mcp.Tool {
	headerValues, ok := headers[virtualMCPHeader]
	if !ok || len(headerValues) != 1 {
		return tools
	}

	virtualServerID := headerValues[0]
	broker.logger.Debug("applying virtual server filter", "virtualServer", virtualServerID)

	vs, err := broker.GetVirtualSeverByHeader(virtualServerID)
	if err != nil {
		broker.logger.Error("failed to get virtual server", "error", err)
		return tools
	}

	// build a set of allowed tool names for O(1) lookup
	filteredSet := make(map[string]struct{}, len(vs.Tools))
	for _, name := range vs.Tools {
		filteredSet[name] = struct{}{}
	}

	var filtered []mcp.Tool
	for _, tool := range tools {
		if _, inFilter := filteredSet[tool.Name]; inFilter {
			filtered = append(filtered, tool)
		}
	}

	return filtered
}

// validateJWTHeader validates the JWT header using ES256 algorithm.
func validateJWTHeader(token string, publicKey string) (*jwt.Token, error) {
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	return jwt.Parse(token, func(_ *jwt.Token) (any, error) {
		pubkey, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := pubkey.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("expected *ecdsa.PublicKey, got %T", pubkey)
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}))
}
