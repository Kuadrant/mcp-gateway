package upstream

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
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

// ValidateTool validates a single tool against the MCP Tool schema.
// Returns an InvalidToolInfo with any errors found. If Errors is empty the tool is valid.
func ValidateTool(tool mcp.Tool) InvalidToolInfo {
	info := InvalidToolInfo{Name: tool.Name}

	if tool.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}

	validateSchema(&info, "inputSchema", tool.InputSchema.Type, tool.InputSchema.Properties)

	if tool.OutputSchema.Type != "" || tool.OutputSchema.Properties != nil {
		validateSchema(&info, "outputSchema", tool.OutputSchema.Type, tool.OutputSchema.Properties)
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
// Returns an InvalidPromptInfo with any errors found. If Errors is empty the prompt is valid.
func ValidatePrompt(prompt mcp.Prompt) InvalidPromptInfo {
	info := InvalidPromptInfo{Name: prompt.Name}
	if prompt.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}
	for i, arg := range prompt.Arguments {
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
