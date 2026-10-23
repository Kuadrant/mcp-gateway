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

// extractSchema pulls type and properties from the official SDK's `any` schema field.
// The `type` keyword is returned as the raw value: JSON Schema permits it to be
// either a string ("object") or an array of strings (a union, e.g.
// ["object","null"]), so normalization is deferred to schemaTypeNames.
func extractSchema(schema any) (schemaType any, properties map[string]any, ok bool) {
	if schema == nil {
		return nil, nil, false
	}
	m, is := schema.(map[string]any)
	if !is {
		return nil, nil, false
	}
	if t, has := m["type"]; has {
		schemaType = t
	}
	if p, has := m["properties"]; has {
		properties, _ = p.(map[string]any)
	}
	return schemaType, properties, true
}

// schemaTypeNames normalizes a JSON Schema `type` value into a slice of type
// names. Per JSON Schema, `type` may be a single string or an array of strings
// (a union type). ok is false when `type` is present but is neither.
func schemaTypeNames(v any) (names []string, ok bool) {
	switch t := v.(type) {
	case string:
		return []string{t}, true
	case []any:
		names = make([]string, 0, len(t))
		for _, e := range t {
			s, is := e.(string)
			if !is {
				return nil, false
			}
			names = append(names, s)
		}
		return names, true
	default:
		return nil, false
	}
}

// typesInclude reports whether the normalized `type` value contains want.
func typesInclude(v any, want string) bool {
	names, ok := schemaTypeNames(v)
	if !ok {
		return false
	}
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
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
		// Input top-level type must be exactly "object": Server.AddTool panics
		// on anything else, including a union such as ["object","null"].
		validateSchema(&info, "inputSchema", schemaType, properties, false)
	}

	outType, outProps, outOk := extractSchema(tool.OutputSchema)
	if outOk && (outType != nil || outProps != nil) {
		// Output top-level type may be a union that includes "object" (e.g. a
		// nullable result, ["object","null"]); AddTool accepts those.
		validateSchema(&info, "outputSchema", outType, outProps, true)
	}

	return info
}

// validateSchema checks a tool's input or output schema. allowUnionTopLevel
// mirrors what the SDK's Server.AddTool actually accepts: an input schema's
// top-level type must be exactly "object", while an output schema's may be a
// union that includes "object". Property types may be a single type or a union
// (array of types) in both cases.
func validateSchema(info *InvalidToolInfo, prefix string, schemaType any, properties map[string]any, allowUnionTopLevel bool) {
	if allowUnionTopLevel {
		if !typesInclude(schemaType, "object") {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.type must be %q, got %q", prefix, "object", formatSchemaType(schemaType)))
		}
	} else if s, ok := schemaType.(string); !ok || s != "object" {
		info.Errors = append(info.Errors, fmt.Sprintf("%s.type must be %q, got %q", prefix, "object", formatSchemaType(schemaType)))
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
		names, ok := schemaTypeNames(typeVal)
		if !ok {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type must be a string or array of strings, got %T", prefix, propName, typeVal))
			continue
		}
		for _, n := range names {
			if !validJSONSchemaTypes[n] {
				info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type %q is not a valid JSON Schema type", prefix, propName, n))
			}
		}
	}
}

// formatSchemaType renders a `type` value for error messages: "" for an absent
// type, the string itself for a scalar, or a bracketed list for a union.
func formatSchemaType(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
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
