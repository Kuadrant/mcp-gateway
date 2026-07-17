package routing

import (
	"maps"
	"strings"
)

// Table is an in-memory implementation of RoutingTable.
// It is built by the broker and swapped atomically so the router
// always reads a consistent snapshot.
type Table struct {
	tools       map[string]*ServerRoute
	prompts     map[string]*ServerRoute
	prefixes    map[string]*ServerRoute // prefix → route for userSpecificList servers
	brokerTools map[string]struct{}
	annotations map[string]*ToolAnnotation // key: serverID + "/" + toolName
}

// TableBuilder accumulates entries for building a Table.
type TableBuilder struct {
	tools       map[string]*ServerRoute
	prompts     map[string]*ServerRoute
	prefixes    map[string]*ServerRoute
	brokerTools map[string]struct{}
	annotations map[string]*ToolAnnotation
}

// NewTableBuilder creates a builder for constructing a Table.
func NewTableBuilder() *TableBuilder {
	return &TableBuilder{
		tools:       make(map[string]*ServerRoute),
		prompts:     make(map[string]*ServerRoute),
		prefixes:    make(map[string]*ServerRoute),
		brokerTools: make(map[string]struct{}),
		annotations: make(map[string]*ToolAnnotation),
	}
}

// AddTool registers a tool name → server route mapping.
func (b *TableBuilder) AddTool(name string, route *ServerRoute) *TableBuilder {
	b.tools[name] = route
	return b
}

// AddPrompt registers a prompt name → server route mapping.
func (b *TableBuilder) AddPrompt(name string, route *ServerRoute) *TableBuilder {
	b.prompts[name] = route
	return b
}

// AddPrefix registers a prefix → server route mapping for userSpecificList servers.
func (b *TableBuilder) AddPrefix(prefix string, route *ServerRoute) *TableBuilder {
	b.prefixes[prefix] = route
	return b
}

// AddBrokerTool marks a tool name as a broker-internal meta-tool.
func (b *TableBuilder) AddBrokerTool(name string) *TableBuilder {
	b.brokerTools[name] = struct{}{}
	return b
}

// AddAnnotation registers tool annotations for a server/tool pair.
func (b *TableBuilder) AddAnnotation(serverID, toolName string, annotation *ToolAnnotation) *TableBuilder {
	b.annotations[annotationKey(serverID, toolName)] = annotation
	return b
}

// Build creates an immutable Table from the accumulated entries.
// The builder must not be reused after calling Build.
func (b *TableBuilder) Build() *Table {
	t := &Table{
		tools:       maps.Clone(b.tools),
		prompts:     maps.Clone(b.prompts),
		prefixes:    maps.Clone(b.prefixes),
		brokerTools: maps.Clone(b.brokerTools),
		annotations: maps.Clone(b.annotations),
	}
	b.tools = nil
	b.prompts = nil
	b.prefixes = nil
	b.brokerTools = nil
	b.annotations = nil
	return t
}

// LookupTool finds server route for tool name
func (t *Table) LookupTool(name string) (*ServerRoute, bool) {
	r, ok := t.tools[name]
	return r, ok
}

// LookupPrompt finds server route for prompt name
func (t *Table) LookupPrompt(name string) (*ServerRoute, bool) {
	r, ok := t.prompts[name]
	return r, ok
}

// LookupPrefix finds a server route by matching the tool name against
// registered prefixes. Used for userSpecificList servers where per-user
// tools may not appear in the tool lookup.
func (t *Table) LookupPrefix(name string) (*ServerRoute, bool) {
	for prefix, route := range t.prefixes {
		if strings.HasPrefix(name, prefix) {
			return route, true
		}
	}
	return nil, false
}

// IsBrokerTool checks if tool name is broker meta-tool
func (t *Table) IsBrokerTool(name string) bool {
	_, ok := t.brokerTools[name]
	return ok
}

// ToolAnnotations returns annotations for server tool pair
func (t *Table) ToolAnnotations(serverID, toolName string) (*ToolAnnotation, bool) {
	a, ok := t.annotations[annotationKey(serverID, toolName)]
	return a, ok
}

func annotationKey(serverID, toolName string) string {
	return serverID + "/" + toolName
}
