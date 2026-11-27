package translator

import (
	"regexp"
	"strings"

	"github.com/kuadrant/authorino/api/v1beta3"
)

const mcpToolnameSelector = "context.request.http.headers.x-mcp-toolname"

var toolNameRegex = regexp.MustCompile(`request\.mcp\.tool_name\s*==\s*'([^']+)'`)

// TranslateCEL converts our custom policy CEL expressions into Authorino Pattern Expressions
func TranslateCEL(expression string) v1beta3.PatternExpressionOrRef {
	// 1. Check for empty/missing tool name (non-tools)
	if strings.Contains(expression, "== ''") {
		return v1beta3.PatternExpressionOrRef{
			PatternExpression: v1beta3.PatternExpression{
				Selector: mcpToolnameSelector,
				Operator: v1beta3.PatternExpressionOperator("eq"),
				Value:    "",
			},
		}
	}

	// 2. Extract tool name using regex
	matches := toolNameRegex.FindStringSubmatch(expression)
	if len(matches) > 1 {
		toolName := matches[1]
		return v1beta3.PatternExpressionOrRef{
			PatternExpression: v1beta3.PatternExpression{
				Selector: mcpToolnameSelector,
				Operator: v1beta3.PatternExpressionOperator("eq"),
				Value:    toolName,
			},
		}
	}

	// Default to failing pattern if we can't parse it
	return v1beta3.PatternExpressionOrRef{
		PatternExpression: v1beta3.PatternExpression{
			Selector: mcpToolnameSelector,
			Operator: v1beta3.PatternExpressionOperator("eq"),
			Value:    "UNKNOWN_PATTERN",
		},
	}
}

// ValidateCEL is a dummy for now since we aren't using raw CEL anymore
func ValidateCEL(expression string) error {
	return nil
}
