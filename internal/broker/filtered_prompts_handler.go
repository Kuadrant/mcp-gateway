package broker

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FilterPrompts reduces the prompt set based on authorization headers.
func (broker *mcpBrokerImpl) FilterPrompts(ctx context.Context, headers http.Header, mcpRes *mcp.ListPromptsResult) {
	attrs := []attribute.KeyValue{brokerComponentAttr}
	ctx, span := brokerTracer().Start(ctx, "mcp-broker.prompts-list", trace.WithAttributes(attrs...))
	defer span.End()

	broker.logger.DebugContext(ctx, "FilterPrompts called", "input_prompts_count", len(mcpRes.Prompts))
	prompts := mcpRes.Prompts
	if len(mcpRes.Prompts) == 0 {
		mcpRes.Prompts = []*mcp.Prompt{}
		return
	}

	prompts = broker.applyAuthorizedCapabilitiesFilterForPrompts(headers, prompts)
	prompts = broker.applyVirtualServerFilterForPrompts(headers, prompts)
	prompts = broker.removeGatewayMetaFromPrompts(prompts)

	span.SetAttributes(attribute.Int("mcp.prompts.count", len(prompts)))

	if prompts == nil {
		prompts = []*mcp.Prompt{}
	}
	mcpRes.Prompts = prompts
}

func (broker *mcpBrokerImpl) removeGatewayMetaFromPrompts(prompts []*mcp.Prompt) []*mcp.Prompt {
	// list results share Prompt pointers with the server's stored prompts;
	// copy the struct before mutating (see removeGatewayMeta).
	out := make([]*mcp.Prompt, 0, len(prompts))
	for _, p := range prompts {
		if len(p.Meta) == 0 {
			out = append(out, p)
			continue
		}
		cleaned := make(map[string]any, len(p.Meta))
		for k, v := range p.Meta {
			if k == "kuadrant/id" {
				continue
			}
			cleaned[k] = v
		}
		if len(cleaned) == 0 {
			cleaned = nil
		}
		cp := *p
		cp.Meta = mcp.Meta(cleaned)
		out = append(out, &cp)
	}
	return out
}

func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilterForPrompts(headers http.Header, prompts []*mcp.Prompt) []*mcp.Prompt {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		if broker.enforceCapabilityFilter {
			return []*mcp.Prompt{}
		}
		return prompts
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-mcp-authorized header for prompts", "error", err)
		return []*mcp.Prompt{}
	}

	allowedPrompts, hasPrompts := capabilities["prompts"]
	if !hasPrompts {
		if broker.enforceCapabilityFilter {
			return []*mcp.Prompt{}
		}
		return prompts
	}

	return broker.filterPromptsByServerMap(allowedPrompts)
}

func (broker *mcpBrokerImpl) filterPromptsByServerMap(allowedPrompts map[string][]string) []*mcp.Prompt {
	var filtered []*mcp.Prompt

	for serverName, promptNames := range allowedPrompts {
		upstream := broker.findServerByName(serverName)
		if upstream == nil {
			broker.logger.Error("upstream not found for prompt filtering", "server", serverName)
			continue
		}
		prompts := upstream.GetManagedPrompts()
		if prompts == nil {
			continue
		}

		for _, prompt := range prompts {
			if slices.Contains(promptNames, prompt.Name) {
				p := prompt
				p.Name = fmt.Sprintf("%s%s", upstream.Config().Prefix, p.Name)
				filtered = append(filtered, &p)
			}
		}
	}

	return filtered
}

func (broker *mcpBrokerImpl) applyVirtualServerFilterForPrompts(headers http.Header, prompts []*mcp.Prompt) []*mcp.Prompt {
	headerValues, ok := headers[virtualMCPHeader]
	if !ok || len(headerValues) != 1 {
		return prompts
	}

	virtualServerID := headerValues[0]
	vs, err := broker.GetVirtualServerByHeader(virtualServerID)
	if err != nil {
		broker.logger.Error("failed to get virtual server for prompt filtering", "error", err)
		return prompts
	}

	if len(vs.Prompts) == 0 {
		return prompts
	}

	filteredSet := make(map[string]struct{}, len(vs.Prompts))
	for _, name := range vs.Prompts {
		filteredSet[name] = struct{}{}
	}

	var filtered []*mcp.Prompt
	for _, prompt := range prompts {
		if _, inFilter := filteredSet[prompt.Name]; inFilter {
			filtered = append(filtered, prompt)
		}
	}

	return filtered
}
