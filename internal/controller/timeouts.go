package controller

import (
	"fmt"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// buildServerTimeouts converts the API-level timeout configuration into the
// validated, parsed form consumed by the broker and router.
// Returns (nil, nil) when the spec declares no timeouts. An error is returned
// when any duration string cannot be parsed or is non-positive, so that the
// reconciler can surface it via the resource's Ready condition rather than
// silently writing an unusable config.
func buildServerTimeouts(spec *mcpv1alpha1.MCPServerTimeouts) (*config.ServerTimeouts, error) {
	if spec == nil {
		return nil, nil
	}

	out := &config.ServerTimeouts{}

	if spec.ToolCall != "" {
		d, err := parsePositiveDuration("toolCall", spec.ToolCall)
		if err != nil {
			return nil, err
		}
		out.ToolCall = d
	}

	if len(spec.PerTool) > 0 {
		out.PerTool = make(map[string]time.Duration, len(spec.PerTool))
		for i := range spec.PerTool {
			pt := spec.PerTool[i]
			if pt.Name == "" {
				return nil, fmt.Errorf("perTool[%d].name must not be empty", i)
			}
			if _, dup := out.PerTool[pt.Name]; dup {
				return nil, fmt.Errorf("perTool[%d]: duplicate entry for tool %q", i, pt.Name)
			}
			d, err := parsePositiveDuration(fmt.Sprintf("perTool[%s].toolCall", pt.Name), pt.ToolCall)
			if err != nil {
				return nil, err
			}
			out.PerTool[pt.Name] = d
		}
	}

	if out.ToolCall == 0 && len(out.PerTool) == 0 {
		// nothing meaningful to apply; keep config slim by returning nil
		return nil, nil
	}
	return out, nil
}

func parsePositiveDuration(field, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero, got %q", field, raw)
	}
	return d, nil
}
