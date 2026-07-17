package upstream

// InvalidToolPolicy controls behavior when upstream MCP tools have invalid schemas
type InvalidToolPolicy string

const (
	// InvalidToolPolicyFilterOut skips invalid tools and serves valid ones
	InvalidToolPolicyFilterOut InvalidToolPolicy = "FilterOut"
	// InvalidToolPolicyRejectServer rejects all tools from a server if any are invalid
	InvalidToolPolicyRejectServer InvalidToolPolicy = "RejectServer"
)
