package upstream

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "a name",
						},
					},
					"required": []string{"name"},
				},
			},
			expectValid: true,
		},
		{
			name: "valid tool with no properties",
			tool: mcp.Tool{
				Name:        "simple_tool",
				InputSchema: map[string]any{"type": "object"},
			},
			expectValid: true,
		},
		{
			name: "empty name",
			tool: mcp.Tool{
				Name:        "",
				InputSchema: map[string]any{"type": "object"},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
		{
			name: "inputSchema type is not object",
			tool: mcp.Tool{
				Name:        "bad_type",
				InputSchema: map[string]any{"type": "string"},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "inputSchema type is empty",
			tool: mcp.Tool{
				Name:        "empty_type",
				InputSchema: map[string]any{"type": ""},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "property value is not an object",
			tool: mcp.Tool{
				Name: "bad_prop",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
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
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
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
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
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
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
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
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
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
				Name:        "",
				InputSchema: map[string]any{"type": "string"},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
		{
			name:        "nil inputSchema rejected",
			tool:        mcp.Tool{Name: "nil_schema"},
			expectValid: false,
			errContains: "inputSchema is required",
		},
		{
			name:        "non-object inputSchema rejected",
			tool:        mcp.Tool{Name: "bool_schema", InputSchema: true},
			expectValid: false,
			errContains: "inputSchema must be a JSON object",
		},
		{
			name:        "string inputSchema rejected",
			tool:        mcp.Tool{Name: "string_schema", InputSchema: "not a schema"},
			expectValid: false,
			errContains: "inputSchema must be a JSON object",
		},
		{
			name: "property with union type is valid",
			tool: mcp.Tool{
				Name: "union_prop",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"items": map[string]any{
							"type": []any{"array", "null"},
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "property union with invalid member rejected",
			tool: mcp.Tool{
				Name: "bad_union_prop",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{
							"type": []any{"string", "int"},
						},
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"code\"].type \"int\" is not a valid JSON Schema type",
		},
		{
			name: "property type as non-string non-array rejected",
			tool: mcp.Tool{
				Name: "numeric_type",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"x": map[string]any{"type": 42},
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"x\"].type must be a string or array of strings",
		},
		{
			// AddTool panics on any non-"object" input top-level, including a
			// union that contains "object", so the validator must reject it.
			name: "input top-level union rejected (AddTool panics on it)",
			tool: mcp.Tool{
				Name:        "nullable_object_input",
				InputSchema: map[string]any{"type": []any{"object", "null"}},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
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
		Name:        "",
		InputSchema: map[string]any{"type": "string"},
	}
	info := ValidateTool(tool)
	assert.GreaterOrEqual(t, len(info.Errors), 2, "expected at least 2 errors for empty name + wrong type")
}

func TestValidateTools(t *testing.T) {
	tools := []mcp.Tool{
		{
			Name:        "valid_tool",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "invalid_tool",
			InputSchema: map[string]any{"type": "int"},
		},
		{
			Name: "another_valid",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
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

// regression for the SDK migration: Server.AddTool panics on nil or
// non-object input schemas, so ValidateTools must reject them before they
// reach the gateway server. one bad upstream tool must not crash the broker.
func TestValidateTools_SchemasThatPanicAddToolAreRejected(t *testing.T) {
	hostile := []mcp.Tool{
		{Name: "nil_schema"},
		{Name: "bool_schema", InputSchema: true},
		{Name: "string_schema", InputSchema: "not a schema"},
		{Name: "wrong_type_schema", InputSchema: map[string]any{"type": "string"}},
		{Name: "union_type_schema", InputSchema: map[string]any{"type": []any{"object", "null"}}},
	}

	valid, invalid := ValidateTools(hostile)
	assert.Empty(t, valid)
	assert.Len(t, invalid, len(hostile))

	// prove the guard matters: the SDK panics on every schema we reject
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0.0.1"}, nil)
	for i := range hostile {
		assert.Panics(t, func() { srv.AddTool(&hostile[i], NoopToolHandler) }, hostile[i].Name)
	}
}

func TestValidateTool_OutputSchema(t *testing.T) {
	tool := mcp.Tool{
		Name:         "with_output",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "string"},
	}

	info := ValidateTool(tool)
	assert.NotEmpty(t, info.Errors)
	assert.Contains(t, info.Errors[0], "outputSchema.type must be \"object\"")
}

// regression: github-mcp-server v2 (and other MCP 2026-07-28 backends) emit
// structured output schemas whose top-level and property `type` keywords are
// JSON Schema union arrays (e.g. ["object","null"]). These are valid JSON
// Schema and must not be filtered out of the aggregated tools/list.
func TestValidateTool_UnionTypeOutputSchemaAccepted(t *testing.T) {
	tool := mcp.Tool{
		Name:        "search_code",
		InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{
			"type": []any{"object", "null"},
			"properties": map[string]any{
				"items":       map[string]any{"type": []any{"array", "null"}},
				"total_count": map[string]any{"type": "integer"},
			},
		},
	}

	info := ValidateTool(tool)
	assert.Empty(t, info.Errors, "union-typed output schema should be valid, got %v", info.Errors)
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
			prompt:      mcp.Prompt{Name: "summarize", Arguments: []*mcp.PromptArgument{{Name: "text", Required: true}}},
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
			prompt:      mcp.Prompt{Name: "test", Arguments: []*mcp.PromptArgument{{Name: "", Required: true}}},
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
	assert.Equal(t, InvalidToolPolicy("FilterOut"), InvalidToolPolicyFilterOut)
	assert.Equal(t, InvalidToolPolicy("RejectServer"), InvalidToolPolicyRejectServer)
}
