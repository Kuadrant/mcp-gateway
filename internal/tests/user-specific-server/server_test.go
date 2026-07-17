package userspecificserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func TestFilterToolsByAuth(t *testing.T) {
	allToolsList := []*mcp.Tool{
		{Name: "server_info"},
		{Name: "headers"},
		{Name: "list_repos"},
		{Name: "create_issue"},
		{Name: "run_pipeline"},
	}

	tests := []struct {
		name     string
		auth     string
		expected []string
	}{
		{
			name:     "no auth returns common tools only",
			auth:     "",
			expected: []string{"server_info", "headers"},
		},
		{
			name:     "user-a gets list_repos, create_issue, and common tools",
			auth:     "Bearer user-a-token",
			expected: []string{"server_info", "headers", "list_repos", "create_issue"},
		},
		{
			name:     "user-b gets run_pipeline and common tools",
			auth:     "Bearer user-b-token",
			expected: []string{"server_info", "headers", "run_pipeline"},
		},
		{
			name:     "unknown token gets common tools only",
			auth:     "Bearer unknown",
			expected: []string{"server_info", "headers"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := &mcp.ListToolsResult{
				Tools: make([]*mcp.Tool, len(allToolsList)),
			}
			copy(result.Tools, allToolsList)

			headers := http.Header{}
			if tc.auth != "" {
				headers.Set("Authorization", tc.auth)
			}
			filterToolsByAuth(headers, result)

			var names []string
			for _, tool := range result.Tools {
				names = append(names, tool.Name)
			}
			assert.ElementsMatch(t, tc.expected, names)
		})
	}
}

// regression: requests with no HTTP extra (nil headers) skipped the filter
// entirely, returning every tool to anonymous callers. in-memory transports
// produce nil Extra, exercising exactly that path.
func TestToolFilterMiddleware_NilExtraFailsClosed(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "user-specific-test-server", Version: "1.0.0"}, &mcp.ServerOptions{})
	for _, tt := range allTools {
		s.AddTool(&tt.tool, tt.handler)
	}
	s.AddReceivingMiddleware(toolFilterMiddleware())

	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _, _ = s.Connect(ctx, st, nil) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	assert.ElementsMatch(t, []string{"server_info", "headers"}, names,
		"anonymous caller (nil headers) must only see the base tool set")
}
