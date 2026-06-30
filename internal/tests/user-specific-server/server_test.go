package userspecificserver

import (
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func TestFilterToolsByAuth(t *testing.T) {
	allToolsList := []mcp.Tool{
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
				Tools: make([]mcp.Tool, len(allToolsList)),
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
