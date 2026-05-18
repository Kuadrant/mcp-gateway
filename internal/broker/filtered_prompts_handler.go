package broker

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
)

// FilterPrompts reduces the prompt set based on authorization headers.
func (broker *mcpBrokerImpl) FilterPrompts(_ context.Context, _ any, mcpReq *mcp.ListPromptsRequest, mcpRes *mcp.ListPromptsResult) {
	broker.logger.Debug("FilterPrompts called", "input_prompts_count", len(mcpRes.Prompts))
	prompts := mcpRes.Prompts
	emptyPrompts := []mcp.Prompt{}
	if len(mcpRes.Prompts) == 0 {
		mcpRes.Prompts = emptyPrompts
		return
	}

	prompts = broker.applyAuthorizedCapabilitiesFilterForPrompts(mcpReq.Header, prompts)
	prompts = broker.applyVirtualServerFilterForPrompts(mcpReq.Header, prompts)
	prompts = broker.removeGatewayMetaFromPrompts(prompts)

	if prompts == nil {
		prompts = emptyPrompts
	}
	mcpRes.Prompts = prompts
}

func (broker *mcpBrokerImpl) removeGatewayMetaFromPrompts(prompts []mcp.Prompt) []mcp.Prompt {
	for i := range prompts {
		if prompts[i].Meta != nil {
			delete(prompts[i].Meta.AdditionalFields, "kuadrant/id")
			if len(prompts[i].Meta.AdditionalFields) == 0 {
				prompts[i].Meta = nil
			}
		}
	}
	return prompts
}

func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilterForPrompts(headers http.Header, prompts []mcp.Prompt) []mcp.Prompt {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		if broker.enforceCapabilityFilter {
			return []mcp.Prompt{}
		}
		return prompts
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-mcp-authorized header for prompts", "error", err)
		return []mcp.Prompt{}
	}

	allowedPrompts, hasPrompts := capabilities["prompts"]
	if !hasPrompts {
		if broker.enforceCapabilityFilter {
			return []mcp.Prompt{}
		}
		return prompts
	}

	return broker.filterPromptsByServerMap(allowedPrompts)
}

func (broker *mcpBrokerImpl) filterPromptsByServerMap(allowedPrompts map[string][]string) []mcp.Prompt {
	var filtered []mcp.Prompt

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
				prompt.Name = fmt.Sprintf("%s%s", upstream.Config().Prefix, prompt.Name)
				filtered = append(filtered, prompt)
			}
		}
	}

	return filtered
}

func (broker *mcpBrokerImpl) applyVirtualServerFilterForPrompts(headers http.Header, prompts []mcp.Prompt) []mcp.Prompt {
	headerValues, ok := headers[virtualMCPHeader]
	if !ok || len(headerValues) != 1 {
		return prompts
	}

	virtualServerID := headerValues[0]
	vs, err := broker.GetVirtualSeverByHeader(virtualServerID)
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

	var filtered []mcp.Prompt
	for _, prompt := range prompts {
		if _, inFilter := filteredSet[prompt.Name]; inFilter {
			filtered = append(filtered, prompt)
		}
	}

	return filtered
}
