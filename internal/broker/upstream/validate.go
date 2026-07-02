package upstream

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// InvalidToolInfo contains validation errors for a single tool
type InvalidToolInfo struct {
	Name   string   `json:"name"`
	Errors []string `json:"errors"`
}

var validJSONSchemaTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"array":   true,
	"object":  true,
	"null":    true,
}

// extractSchema pulls type and properties from the official SDK's `any` schema field
func extractSchema(schema any) (schemaType string, properties map[string]any, ok bool) {
	if schema == nil {
		return "", nil, false
	}
	m, is := schema.(map[string]any)
	if !is {
		return "", nil, false
	}
	if t, has := m["type"]; has {
		schemaType, _ = t.(string)
	}
	if p, has := m["properties"]; has {
		properties, _ = p.(map[string]any)
	}
	return schemaType, properties, true
}

// ValidateTool validates a single tool against the MCP Tool schema.
// nil or non-object input schemas are validation errors: the SDK's
// Server.AddTool panics on them, so they must never reach the gateway server.
func ValidateTool(tool mcp.Tool) InvalidToolInfo {
	info := InvalidToolInfo{Name: tool.Name}

	if tool.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}

	schemaType, properties, ok := extractSchema(tool.InputSchema)
	switch {
	case tool.InputSchema == nil:
		info.Errors = append(info.Errors, "inputSchema is required")
	case !ok:
		info.Errors = append(info.Errors, fmt.Sprintf("inputSchema must be a JSON object, got %T", tool.InputSchema))
	default:
		validateSchema(&info, "inputSchema", schemaType, properties)
	}

	outType, outProps, outOk := extractSchema(tool.OutputSchema)
	if outOk && (outType != "" || outProps != nil) {
		validateSchema(&info, "outputSchema", outType, outProps)
	}

	return info
}

func validateSchema(info *InvalidToolInfo, prefix, schemaType string, properties map[string]any) {
	if schemaType != "object" {
		info.Errors = append(info.Errors, fmt.Sprintf("%s.type must be \"object\", got %q", prefix, schemaType))
	}

	for propName, propValue := range properties {
		propMap, ok := propValue.(map[string]any)
		if !ok {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q] must be an object, got %T", prefix, propName, propValue))
			continue
		}
		typeVal, hasType := propMap["type"]
		if !hasType {
			continue
		}
		typeStr, ok := typeVal.(string)
		if !ok {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type must be a string, got %T", prefix, propName, typeVal))
			continue
		}
		if !validJSONSchemaTypes[typeStr] {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type %q is not a valid JSON Schema type", prefix, propName, typeStr))
		}
	}
}

// InvalidPromptInfo contains validation errors for a single prompt
type InvalidPromptInfo struct {
	Name   string   `json:"name"`
	Errors []string `json:"errors"`
}

// ValidatePrompt validates a single prompt against the MCP Prompt schema.
func ValidatePrompt(prompt mcp.Prompt) InvalidPromptInfo {
	info := InvalidPromptInfo{Name: prompt.Name}
	if prompt.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}
	for i, arg := range prompt.Arguments {
		if arg == nil {
			continue
		}
		if arg.Name == "" {
			info.Errors = append(info.Errors, fmt.Sprintf("arguments[%d].name must not be empty", i))
		}
	}
	return info
}

// ValidatePrompts validates a list of prompts and returns the valid prompts and info about invalid ones.
func ValidatePrompts(prompts []mcp.Prompt) ([]mcp.Prompt, []InvalidPromptInfo) {
	var valid []mcp.Prompt
	var invalid []InvalidPromptInfo

	for _, prompt := range prompts {
		info := ValidatePrompt(prompt)
		if len(info.Errors) > 0 {
			invalid = append(invalid, info)
		} else {
			valid = append(valid, prompt)
		}
	}

	return valid, invalid
}

// ValidateTools validates a list of tools and returns the valid tools and info about invalid ones.
func ValidateTools(tools []mcp.Tool) ([]mcp.Tool, []InvalidToolInfo) {
	var valid []mcp.Tool
	var invalid []InvalidToolInfo

	for _, tool := range tools {
		info := ValidateTool(tool)
		if len(info.Errors) > 0 {
			invalid = append(invalid, info)
		} else {
			valid = append(valid, tool)
		}
	}

	return valid, invalid
}
