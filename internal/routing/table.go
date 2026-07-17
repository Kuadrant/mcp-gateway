// Package routing provides the routing table interface and types shared
// between the broker (producer) and router (consumer).
package routing

// RoutingTable resolves tool and prompt names to upstream server routes.
// The broker populates the table; the router reads it.
//
//nolint:revive // package-qualified name is clearer
type RoutingTable interface {
	LookupTool(name string) (*ServerRoute, bool)
	LookupPrompt(name string) (*ServerRoute, bool)
	LookupPrefix(name string) (*ServerRoute, bool)
	IsBrokerTool(name string) bool
	ToolAnnotations(serverID, toolName string) (*ToolAnnotation, bool)
}

// ServerRoute identifies the upstream server that handles a tool or prompt.
type ServerRoute struct {
	Name                string
	Host                string
	Prefix              string
	Path                string
	URL                 string
	TokenURLElicitation *TokenURLElicitationRoute
	UserSpecificList    bool
}

// TokenURLElicitationRoute holds the URL elicitation config relevant to routing.
type TokenURLElicitationRoute struct {
	URL string
}

// ToolAnnotation preserves the *bool annotation fidelity from upstream
// tools/list responses. nil means unspecified.
type ToolAnnotation struct {
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	OpenWorldHint   *bool
}
