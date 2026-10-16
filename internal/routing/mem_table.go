package routing

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Table is an in-memory implementation of RoutingTable.
// It is built by the broker and swapped atomically so the router
// always reads a consistent snapshot.
type Table struct {
	tools            map[string]*ServerRoute
	prompts          map[string]*ServerRoute
	prefixes         map[string]*ServerRoute // prefix → route for userSpecificList servers
	resourcePrefixes map[string]*ServerRoute // prefix → route for resource-federated servers
	brokerTools      map[string]struct{}
	annotations      map[string]*ToolAnnotation // key: serverID + "/" + toolName
}

// TableBuilder accumulates entries for building a Table.
type TableBuilder struct {
	tools            map[string]*ServerRoute
	prompts          map[string]*ServerRoute
	prefixes         map[string]*ServerRoute
	resourcePrefixes map[string]*ServerRoute
	brokerTools      map[string]struct{}
	annotations      map[string]*ToolAnnotation
}

// NewTableBuilder creates a builder for constructing a Table.
func NewTableBuilder() *TableBuilder {
	return &TableBuilder{
		tools:            make(map[string]*ServerRoute),
		prompts:          make(map[string]*ServerRoute),
		prefixes:         make(map[string]*ServerRoute),
		resourcePrefixes: make(map[string]*ServerRoute),
		brokerTools:      make(map[string]struct{}),
		annotations:      make(map[string]*ToolAnnotation),
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

// AddResourcePrefix registers a prefix → server route mapping for
// resource-federated servers, used to resolve a resources/read URI's
// authority segment back to its owning server.
func (b *TableBuilder) AddResourcePrefix(prefix string, route *ServerRoute) *TableBuilder {
	b.resourcePrefixes[prefix] = route
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
		tools:            maps.Clone(b.tools),
		prompts:          maps.Clone(b.prompts),
		prefixes:         maps.Clone(b.prefixes),
		resourcePrefixes: maps.Clone(b.resourcePrefixes),
		brokerTools:      maps.Clone(b.brokerTools),
		annotations:      maps.Clone(b.annotations),
	}
	b.tools = nil
	b.prompts = nil
	b.prefixes = nil
	b.resourcePrefixes = nil
	b.brokerTools = nil
	b.annotations = nil
	return t
}

// DumpTools returns a formatted string of all registered tools and their routes.
func (t *Table) DumpTools() string {
	names := slices.Sorted(maps.Keys(t.tools))
	var sb strings.Builder
	for _, name := range names {
		r := t.tools[name]
		fmt.Fprintf(&sb, "  %s → %s (prefix=%q host=%q)\n", name, r.Name, r.Prefix, r.Host)
	}
	return sb.String()
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

// LookupPrefix finds a server route by longest-prefix match of the tool name
// against registered prefixes. Used for userSpecificList servers where
// per-user tools may not appear in the tool lookup. Prefixes may overlap
// (gh_ vs gh_ent_), so the longest one wins, as in the broker's lookup.
func (t *Table) LookupPrefix(name string) (*ServerRoute, bool) {
	var best *ServerRoute
	bestLen := -1
	for prefix, route := range t.prefixes {
		if strings.HasPrefix(name, prefix) && len(prefix) > bestLen {
			best = route
			bestLen = len(prefix)
		}
	}
	return best, best != nil
}

// LookupResourcePrefix finds a server route by longest-prefix match against a
// resource URI's authority segment. Mirrors the broker's
// GetServerInfoByResource, which uses the same longest-prefix approach to
// resolve ambiguous authorities (see resources-federation-design.md).
func (t *Table) LookupResourcePrefix(authority string) (*ServerRoute, bool) {
	var best *ServerRoute
	bestLen := -1
	for prefix, route := range t.resourcePrefixes {
		if strings.HasPrefix(authority, prefix) && len(prefix) > bestLen {
			best = route
			bestLen = len(prefix)
		}
	}
	return best, best != nil
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
