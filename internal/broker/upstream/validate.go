package upstream

import (
	"encoding/json"
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

// extractSchema pulls type and properties from the official SDK's `any` schema field.
// type is returned raw (JSON Schema allows a type name or an array of them);
// hasType distinguishes an absent type from an explicit null.
func extractSchema(schema any) (schemaType any, hasType bool, properties map[string]any, ok bool) {
	m, is := schema.(map[string]any)
	if !is {
		return nil, false, nil, false
	}
	schemaType, hasType = m["type"]
	if p, has := m["properties"]; has {
		properties, _ = p.(map[string]any)
	}
	return schemaType, hasType, properties, true
}

// ValidateTool validates a single tool against the MCP Tool schema.
// nil or non-object input schemas are validation errors: the SDK's
// Server.AddTool panics on them, so they must never reach the gateway server.
func ValidateTool(tool mcp.Tool) InvalidToolInfo {
	info := InvalidToolInfo{Name: tool.Name}

	if tool.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}

	schemaType, hasType, properties, ok := extractSchema(tool.InputSchema)
	switch {
	case tool.InputSchema == nil:
		info.Errors = append(info.Errors, "inputSchema is required")
	case !ok:
		info.Errors = append(info.Errors, fmt.Sprintf("inputSchema must be a JSON object, got %T", tool.InputSchema))
	default:
		// AddTool panics on anything but exactly "object", unions included
		if schemaType != "object" {
			got := "no type"
			if hasType {
				got = formatType(schemaType)
			}
			info.Errors = append(info.Errors, "inputSchema.type must be \"object\", got "+got)
		}
		validateProperties(&info, "inputSchema", properties)
	}

	// the SDK hosts any JSON object output schema whatever its root type;
	// legacy clients that require exactly "object" get it stripped from
	// tools/list (see broker stripNonObjectOutputSchemas)
	outType, outHasType, outProps, outOk := extractSchema(tool.OutputSchema)
	if outOk && outHasType {
		if problem := checkType(outType); problem != "" {
			info.Errors = append(info.Errors, "outputSchema.type "+problem)
		}
	}
	validateProperties(&info, "outputSchema", outProps)

	return info
}

func validateProperties(info *InvalidToolInfo, prefix string, properties map[string]any) {
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
		if problem := checkType(typeVal); problem != "" {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type %s", prefix, propName, problem))
		}
	}
}

// checkType validates a JSON Schema type value: a type name or a non-empty
// array of unique type names. it returns an empty string when the value is valid.
func checkType(v any) (problem string) {
	switch t := v.(type) {
	case string:
		if !validJSONSchemaTypes[t] {
			return fmt.Sprintf("%q is not a valid JSON Schema type", t)
		}
		return ""
	case []string:
		names := make([]any, len(t))
		for i := range t {
			names[i] = t[i]
		}
		return checkType(names)
	case []any:
		if len(t) == 0 {
			return "must not be an empty array"
		}
		for i, e := range t {
			s, _ := e.(string)
			if !validJSONSchemaTypes[s] {
				return fmt.Sprintf("%s is not a valid JSON Schema type", formatType(e))
			}
			for _, prev := range t[:i] {
				if prev == s {
					return fmt.Sprintf("%q is listed more than once", s)
				}
			}
		}
		return ""
	default:
		return fmt.Sprintf("must be a string or array of strings, got %T", v)
	}
}

// formatType renders a raw type value as JSON for error messages.
func formatType(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
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
