package mcprouter

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
)

// resourceURIRewriter rewrites resource URIs in tool call responses by stripping the server prefix.
type resourceURIRewriter struct {
	prefix string
	logger *slog.Logger
}

// Process processes response body and rewrites resource URIs by stripping the prefix.
func (r *resourceURIRewriter) Process(_ context.Context, body []byte) []byte {
	if r.prefix == "" || len(body) == 0 {
		return body
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		// If response is not valid JSON, return unchanged
		return body
	}

	// Recursively rewrite URIs in the response
	if result, ok := response["result"]; ok {
		r.rewriteURIsInValue(result)
	}

	// Marshal back to JSON
	modified, err := json.Marshal(response)
	if err != nil {
		// If marshaling fails, return original body
		return body
	}

	return modified
}

// rewriteURIsInValue recursively searches for _meta.ui.resourceUri fields and strips prefixes.
func (r *resourceURIRewriter) rewriteURIsInValue(v interface{}) {
	switch val := v.(type) {
	case map[string]interface{}:
		// Check for _meta.ui.resourceUri field
		if meta, ok := val["_meta"]; ok {
			if metaMap, ok := meta.(map[string]interface{}); ok {
				if ui, ok := metaMap["ui"]; ok {
					if uiMap, ok := ui.(map[string]interface{}); ok {
						if resourceURI, ok := uiMap["resourceUri"]; ok {
							if uriStr, ok := resourceURI.(string); ok {
								uiMap["resourceUri"] = r.stripResourcePrefix(uriStr)
							}
						}
					}
				}
			}
		}
		// Recursively process all map values
		for _, v := range val {
			r.rewriteURIsInValue(v)
		}
	case []interface{}:
		// Recursively process all array elements
		for _, item := range val {
			r.rewriteURIsInValue(item)
		}
	}
}

// stripResourcePrefix removes the server prefix from a resource URI.
func (r *resourceURIRewriter) stripResourcePrefix(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "ui" && u.Scheme != "resource") || u.Host == "" {
		return uri
	}
	u.Host = strings.TrimPrefix(u.Host, ensureSeparator(r.prefix))
	return u.String()
}

// ensureSeparator adds trailing underscore if not present.
func ensureSeparator(prefix string) string {
	if prefix == "" {
		return prefix
	}
	if !strings.HasSuffix(prefix, "_") {
		return prefix + "_"
	}
	return prefix
}

// Flush flushes any pending data (idempotent).
func (r *resourceURIRewriter) Flush(_ context.Context) []byte {
	return nil
}
