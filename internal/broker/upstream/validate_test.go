package upstream

import (
	"strings"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func TestValidateTool(t *testing.T) {
	tests := []struct {
		name        string
		tool        mcp.Tool
		expectValid bool
		errContains string
	}{
		{
			name: "valid tool with properties",
			tool: mcp.Tool{
				Name: "my_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "a name",
						},
					},
					Required: []string{"name"},
				},
			},
			expectValid: true,
		},
		{
			name: "valid tool with no properties",
			tool: mcp.Tool{
				Name: "simple_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
				},
			},
			expectValid: true,
		},
		{
			name: "empty name",
			tool: mcp.Tool{
				Name: "",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
				},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
		{
			name: "inputSchema type is not object",
			tool: mcp.Tool{
				Name: "bad_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "string",
				},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "inputSchema type is empty",
			tool: mcp.Tool{
				Name: "empty_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "",
				},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "property value is not an object",
			tool: mcp.Tool{
				Name: "bad_prop",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"name": "not-an-object",
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"name\"] must be an object",
		},
		{
			name: "property has invalid type field",
			tool: mcp.Tool{
				Name: "bad_prop_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"code": map[string]any{
							"type": "int",
						},
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"code\"].type \"int\" is not a valid JSON Schema type",
		},
		{
			name: "property type integer is valid",
			tool: mcp.Tool{
				Name: "good_integer",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"count": map[string]any{
							"type": "integer",
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "all valid json schema types accepted",
			tool: mcp.Tool{
				Name: "all_types",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"a": map[string]any{"type": "string"},
						"b": map[string]any{"type": "number"},
						"c": map[string]any{"type": "integer"},
						"d": map[string]any{"type": "boolean"},
						"e": map[string]any{"type": "array"},
						"f": map[string]any{"type": "object"},
						"g": map[string]any{"type": "null"},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "property with no type field is valid",
			tool: mcp.Tool{
				Name: "no_type_prop",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"data": map[string]any{
							"description": "some data",
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "multiple errors collected",
			tool: mcp.Tool{
				Name: "",
				InputSchema: mcp.ToolInputSchema{
					Type: "string",
				},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ValidateTool(tt.tool)
			if tt.expectValid {
				assert.Empty(t, info.Errors, "expected no errors")
			} else {
				assert.NotEmpty(t, info.Errors, "expected errors")
				found := false
				for _, e := range info.Errors {
					if strings.Contains(e, tt.errContains) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected error containing %q, got %v", tt.errContains, info.Errors)
			}
		})
	}
}

func TestValidateTool_MultipleErrors(t *testing.T) {
	tool := mcp.Tool{
		Name: "",
		InputSchema: mcp.ToolInputSchema{
			Type: "string",
		},
	}
	info := ValidateTool(tool)
	assert.GreaterOrEqual(t, len(info.Errors), 2, "expected at least 2 errors for empty name + wrong type")
}

func TestValidateTools(t *testing.T) {
	tools := []mcp.Tool{
		{
			Name:        "valid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "invalid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "int"},
		},
		{
			Name: "another_valid",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"x": map[string]any{"type": "number"},
				},
			},
		},
	}

	valid, invalid := ValidateTools(tools)
	assert.Len(t, valid, 2)
	assert.Len(t, invalid, 1)
	assert.Equal(t, "invalid_tool", invalid[0].Name)
	assert.Equal(t, "valid_tool", valid[0].Name)
	assert.Equal(t, "another_valid", valid[1].Name)
}

func TestValidateTool_OutputSchema(t *testing.T) {
	tool := mcp.Tool{
		Name:         "with_output",
		InputSchema:  mcp.ToolInputSchema{Type: "object"},
		OutputSchema: mcp.ToolOutputSchema{Type: "string"},
	}

	info := ValidateTool(tool)
	assert.NotEmpty(t, info.Errors)
	assert.Contains(t, info.Errors[0], "outputSchema.type must be \"object\"")
}

func TestValidatePrompt(t *testing.T) {
	tests := []struct {
		name        string
		prompt      mcp.Prompt
		expectValid bool
		errContains string
	}{
		{
			name:        "valid prompt",
			prompt:      mcp.Prompt{Name: "code_review", Description: "Reviews code"},
			expectValid: true,
		},
		{
			name:        "valid prompt with arguments",
			prompt:      mcp.Prompt{Name: "summarize", Arguments: []mcp.PromptArgument{{Name: "text", Required: true}}},
			expectValid: true,
		},
		{
			name:        "empty name",
			prompt:      mcp.Prompt{Name: ""},
			expectValid: false,
			errContains: "name must not be empty",
		},
		{
			name:        "empty argument name",
			prompt:      mcp.Prompt{Name: "test", Arguments: []mcp.PromptArgument{{Name: "", Required: true}}},
			expectValid: false,
			errContains: "arguments[0].name must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ValidatePrompt(tt.prompt)
			if tt.expectValid {
				assert.Empty(t, info.Errors, "expected no errors")
			} else {
				assert.NotEmpty(t, info.Errors, "expected errors")
				found := false
				for _, e := range info.Errors {
					if strings.Contains(e, tt.errContains) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected error containing %q, got %v", tt.errContains, info.Errors)
			}
		})
	}
}

func TestValidatePrompts(t *testing.T) {
	prompts := []mcp.Prompt{
		{Name: "valid_prompt"},
		{Name: ""},
		{Name: "another_valid"},
	}

	valid, invalid := ValidatePrompts(prompts)
	assert.Len(t, valid, 2)
	assert.Len(t, invalid, 1)
	assert.Equal(t, "", invalid[0].Name)
	assert.Equal(t, "valid_prompt", valid[0].Name)
	assert.Equal(t, "another_valid", valid[1].Name)
}

func TestInvalidToolPolicyConstants(t *testing.T) {
	assert.Equal(t, mcpv1alpha1.InvalidToolPolicy("FilterOut"), mcpv1alpha1.InvalidToolPolicyFilterOut)
	assert.Equal(t, mcpv1alpha1.InvalidToolPolicy("RejectServer"), mcpv1alpha1.InvalidToolPolicyRejectServer)
}
