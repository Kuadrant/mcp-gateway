package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// entityRegistry consolidates the duplicated tool/prompt logic into a single
// generic struct. The type-specific bits (how to convert an entity to its
// server representation, how to interact with the gateway, how to fetch from
// upstream) are injected via function pointers at construction time.
//
// Callers are responsible for holding the appropriate lock around mutations.
type entityRegistry[E any, S any] struct {
	// items holds the original entities from the upstream server (no prefix)
	items []E
	// byName maps unprefixed name → entity pointer for fast lookup
	byName map[string]*E
	// byServedName maps prefixed name → entity pointer for fast lookup
	byServedName map[string]*E
	// serverItems holds the gateway-facing representations (with prefixed names)
	serverItems []S

	// getName returns the unprefixed name of an entity
	getName func(E) string
	// getServerName returns the prefixed name of a server-side entity
	getServerName func(S) string
	// toServer converts an upstream entity to its gateway-facing representation.
	// This replaces toolToServerTool / promptToServerPrompt.
	toServer func(E) S
	// getMetaFields extracts the Meta map from a server-side entity
	getMetaFields func(S) map[string]any

	// addToGateway registers server items with the gateway
	addToGateway func(...S)
	// deleteFromGateway removes named items from the gateway
	deleteFromGateway func(...string)
	// listFromGateway returns all items currently registered with the gateway
	listFromGateway func() map[string]*S
	// listFromMCP fetches the current items from the upstream MCP server
	listFromMCP func(context.Context) ([]E, error)

	logger   *slog.Logger
	serverID string
	// entityTag is used in log and error messages e.g. "tool" or "prompt"
	entityTag string
	// prefix is the gateway-facing name prefix applied to served entities
	prefix string
}

// newEntityRegistry constructs an entityRegistry with all function pointers injected.
func newEntityRegistry[E any, S any](
	getName func(E) string,
	getServerName func(S) string,
	toServer func(E) S,
	getMetaFields func(S) map[string]any,
	addToGateway func(...S),
	deleteFromGateway func(...string),
	listFromGateway func() map[string]*S,
	listFromMCP func(context.Context) ([]E, error),
	logger *slog.Logger,
	serverID string,
	entityTag string,
	prefix string,
) *entityRegistry[E, S] {
	return &entityRegistry[E, S]{
		items:             []E{},
		byName:            make(map[string]*E),
		byServedName:      make(map[string]*E),
		serverItems:       []S{},
		getName:           getName,
		getServerName:     getServerName,
		toServer:          toServer,
		getMetaFields:     getMetaFields,
		addToGateway:      addToGateway,
		deleteFromGateway: deleteFromGateway,
		listFromGateway:   listFromGateway,
		listFromMCP:       listFromMCP,
		logger:            logger,
		serverID:          serverID,
		entityTag:         entityTag,
		prefix:            prefix,
	}
}

// diff computes what needs to be added and removed when transitioning from
// oldItems to newItems. Replaces diffTools / diffPrompts.
func (r *entityRegistry[E, S]) diff(oldItems, newItems []E) (toAdd []S, toRemove []string) {
	oldMap := make(map[string]E, len(oldItems))
	for _, item := range oldItems {
		oldMap[r.getName(item)] = item
	}
	newMap := make(map[string]E, len(newItems))
	for _, item := range newItems {
		newMap[r.getName(item)] = item
	}
	for name, item := range newMap {
		if _, exists := oldMap[name]; !exists {
			toAdd = append(toAdd, r.toServer(item))
		}
	}
	for name := range oldMap {
		if _, exists := newMap[name]; !exists {
			toRemove = append(toRemove, prefixedName(r.prefix, name))
		}
	}
	return toAdd, toRemove
}

// findConflicts checks whether any of toAdd conflicts with items already
// registered in the gateway that belong to a different upstream server.
// Replaces findToolConflicts / findPromptConflicts.
func (r *entityRegistry[E, S]) findConflicts(toAdd []S) error {
	if r.listFromGateway == nil {
		return nil
	}
	gatewayItems := r.listFromGateway()
	var conflicting []string
	for _, candidate := range toAdd {
		candidateName := r.getServerName(candidate)
		for existingName, existingPtr := range gatewayItems {
			if existingPtr == nil {
				r.logger.Debug("skipping conflict check, nil entry in gateway items",
					"upstream mcp server", r.serverID, r.entityTag, existingName)
				continue
			}
			meta := r.getMetaFields(*existingPtr)
			if meta == nil {
				r.logger.Debug("skipping conflict check, meta is nil",
					"upstream mcp server", r.serverID, r.entityTag, existingName)
				continue
			}
			existingID, ok := meta[gatewayServerID]
			if !ok {
				r.logger.Debug("skipping conflict check, id is missing",
					"upstream mcp server", r.serverID, r.entityTag, existingName)
				continue
			}
			idStr, ok := existingID.(string)
			if !ok {
				r.logger.Debug("skipping conflict check, id is not a string",
					"upstream mcp server", r.serverID, r.entityTag, existingName, "type", reflect.TypeOf(existingID))
				continue
			}
			if existingName == candidateName && idStr != r.serverID {
				r.logger.Debug("conflict found",
					"upstream mcp server", r.serverID,
					"existing", existingName, "new", candidateName,
					"conflicting server", idStr)
				conflicting = append(conflicting, candidateName)
			}
		}
	}
	if len(conflicting) > 0 {
		return fmt.Errorf("conflicting %ss discovered. conflicting %s names %v", r.entityTag, r.entityTag, conflicting)
	}
	return nil
}

// removeAll removes all registered items from the gateway and clears internal
// state. The caller must hold the write lock before calling, and must NOT hold
// it when the gateway delete call happens — matching the existing pattern in
// removeAllTools / removeAllPrompts.
// Replaces removeAllTools / removeAllPrompts.
func (r *entityRegistry[E, S]) removeAll(lock interface {
	Lock()
	Unlock()
}) {
	lock.Lock()
	toRemove := make([]string, 0, len(r.serverItems))
	for _, item := range r.serverItems {
		toRemove = append(toRemove, r.getServerName(item))
	}
	r.serverItems = []S{}
	r.items = []E{}
	r.byName = make(map[string]*E)
	r.byServedName = make(map[string]*E)
	lock.Unlock()

	if len(toRemove) > 0 {
		r.deleteFromGateway(toRemove...)
	}
}

// get fetches the current items from the upstream MCP server and returns
// (existing, fetched, error). Replaces getTools / getPrompts.
func (r *entityRegistry[E, S]) get(ctx context.Context) (existing []E, fetched []E, err error) {
	existing = make([]E, len(r.items))
	copy(existing, r.items)
	fetched, err = r.listFromMCP(ctx)
	if err != nil {
		return existing, existing, fmt.Errorf("failed to get %ss: %w", r.entityTag, err)
	}
	return existing, fetched, nil
}

// getManagedCopy returns a copy of the current items slice.
// Replaces GetManagedTools / GetManagedPrompts.
func (r *entityRegistry[E, S]) getManagedCopy() []E {
	result := make([]E, len(r.items))
	copy(result, r.items)
	return result
}

// getServed returns the entity pointer for the given prefixed name, or nil.
// Replaces GetServedManagedTool / GetServedManagedPrompt.
func (r *entityRegistry[E, S]) getServed(servedName string) *E {
	return r.byServedName[servedName]
}

// setForTesting sets items directly, bypassing the normal fetch flow.
// Replaces SetToolsForTesting / SetPromptsForTesting.
func (r *entityRegistry[E, S]) setForTesting(items []E, prefix string) {
	r.items = items
	r.byName = make(map[string]*E, len(items))
	r.byServedName = make(map[string]*E, len(items))
	for i := range items {
		name := r.getName(items[i])
		r.byName[name] = &items[i]
		served := prefixedName(prefix, name)
		r.byServedName[served] = &items[i]
	}
}

// newToolRegistry constructs the tools entityRegistry for an MCPManager.
func newToolRegistry(prefix, serverID string, gatewayServer ToolsAdderDeleter, listFromMCP func(context.Context) ([]mcp.Tool, error), logger *slog.Logger) *entityRegistry[mcp.Tool, GatewayTool] {
	return newEntityRegistry[mcp.Tool, GatewayTool](
		func(t mcp.Tool) string { return t.Name },
		func(gt GatewayTool) string { return gt.Tool.Name },
		func(t mcp.Tool) GatewayTool {
			t.Name = prefixedName(prefix, t.Name)
			t.Meta = mcp.Meta{
				gatewayServerID: serverID,
			}
			return GatewayTool{
				Tool: t,
				Handler: func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return NewToolResultError("Kagenti MCP Broker doesn't forward tool calls"), nil
				},
			}
		},
		func(gt GatewayTool) map[string]any {
			return gt.Tool.Meta
		},
		gatewayServer.AddTools,
		gatewayServer.DeleteTools,
		func() map[string]*GatewayTool { return gatewayServer.ListTools() },
		listFromMCP,
		logger,
		serverID,
		"tool",
		prefix,
	)
}

// newPromptRegistry constructs the prompts entityRegistry for an MCPManager.
func newPromptRegistry(prefix, serverID string, promptsServer PromptsAdderDeleter, listFromMCP func(context.Context) ([]mcp.Prompt, error), logger *slog.Logger) *entityRegistry[mcp.Prompt, GatewayPrompt] {
	return newEntityRegistry[mcp.Prompt, GatewayPrompt](
		func(p mcp.Prompt) string { return p.Name },
		func(gp GatewayPrompt) string { return gp.Prompt.Name },
		func(p mcp.Prompt) GatewayPrompt {
			p.Name = prefixedName(prefix, p.Name)
			p.Meta = mcp.Meta{
				gatewayServerID: serverID,
			}
			return GatewayPrompt{
				Prompt: p,
				Handler: func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				},
			}
		},
		func(gp GatewayPrompt) map[string]any {
			return gp.Prompt.Meta
		},
		func(gp ...GatewayPrompt) {
			if promptsServer != nil {
				promptsServer.AddPrompts(gp...)
			}
		},
		func(names ...string) {
			if promptsServer != nil {
				promptsServer.DeletePrompts(names...)
			}
		},
		func() map[string]*GatewayPrompt {
			if promptsServer == nil {
				return nil
			}
			return promptsServer.ListPrompts()
		},
		listFromMCP,
		logger,
		serverID,
		"prompt",
		prefix,
	)
}
