package mcprouter

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestResourceURIRewriter(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name           string
		prefix         string
		inputBody      string
		expectedOutput string
		description    string
	}{
		{
			name:           "rewrites single resourceUri",
			prefix:         "docs_",
			inputBody:      `{"result":{"_meta":{"ui":{"resourceUri":"resource://docs_0"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"resourceUri":"resource://0"}}}}`,
			description:    "should strip 'docs_' prefix from resource://docs_0",
		},
		{
			name:           "rewrites multiple resourceUris in array",
			prefix:         "app_",
			inputBody:      `{"result":{"content":[{"_meta":{"ui":{"resourceUri":"resource://app_file1"}}},{"_meta":{"ui":{"resourceUri":"resource://app_file2"}}}]}}`,
			expectedOutput: `{"result":{"content":[{"_meta":{"ui":{"resourceUri":"resource://file1"}}},{"_meta":{"ui":{"resourceUri":"resource://file2"}}}]}}`,
			description:    "should strip prefix from all resourceUris in array",
		},
		{
			name:           "handles ui:// scheme",
			prefix:         "docs_",
			inputBody:      `{"result":{"_meta":{"ui":{"resourceUri":"ui://docs_home"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"resourceUri":"ui://home"}}}}`,
			description:    "should work with ui:// scheme",
		},
		{
			name:           "handles deeply nested structures",
			prefix:         "prefix_",
			inputBody:      `{"result":{"data":{"nested":{"_meta":{"ui":{"resourceUri":"resource://prefix_doc"}}}}}}`,
			expectedOutput: `{"result":{"data":{"nested":{"_meta":{"ui":{"resourceUri":"resource://doc"}}}}}}`,
			description:    "should find and rewrite deeply nested resourceUri",
		},
		{
			name:           "no rewrite when no resourceUri field",
			prefix:         "docs_",
			inputBody:      `{"result":{"_meta":{"ui":{"other":"value"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"other":"value"}}}}`,
			description:    "should return unchanged when no resourceUri field",
		},
		{
			name:           "no rewrite with empty prefix",
			prefix:         "",
			inputBody:      `{"result":{"_meta":{"ui":{"resourceUri":"resource://docs_0"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"resourceUri":"resource://docs_0"}}}}`,
			description:    "should skip processing when prefix is empty",
		},
		{
			name:           "handles empty body",
			prefix:         "docs_",
			inputBody:      ``,
			expectedOutput: ``,
			description:    "should return empty body unchanged",
		},
		{
			name:           "handles invalid JSON gracefully",
			prefix:         "docs_",
			inputBody:      `{invalid json}`,
			expectedOutput: `{invalid json}`,
			description:    "should return unchanged when JSON is invalid",
		},
		{
			name:           "prefix without trailing underscore",
			prefix:         "docs",
			inputBody:      `{"result":{"_meta":{"ui":{"resourceUri":"resource://docs_0"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"resourceUri":"resource://0"}}}}`,
			description:    "should handle prefix without trailing underscore",
		},
		{
			name:           "resourceUri without prefix",
			prefix:         "docs_",
			inputBody:      `{"result":{"_meta":{"ui":{"resourceUri":"resource://0"}}}}`,
			expectedOutput: `{"result":{"_meta":{"ui":{"resourceUri":"resource://0"}}}}`,
			description:    "should not strip if prefix not present",
		},
		{
			name:           "multiple resourceUris in different branches",
			prefix:         "svc_",
			inputBody:      `{"result":{"content":[{"_meta":{"ui":{"resourceUri":"resource://svc_a"}}},{"other":{"_meta":{"ui":{"resourceUri":"resource://svc_b"}}}}]}}`,
			expectedOutput: `{"result":{"content":[{"_meta":{"ui":{"resourceUri":"resource://a"}}},{"other":{"_meta":{"ui":{"resourceUri":"resource://b"}}}}]}}`,
			description:    "should rewrite all resourceUris regardless of nesting path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewriter := &resourceURIRewriter{
				prefix: tt.prefix,
				logger: logger,
			}

			result := rewriter.Process(context.Background(), []byte(tt.inputBody))
			resultStr := string(result)

			// Try to unmarshal expected output; if it fails, do string comparison
			var expectedObj, resultObj interface{}
			expectedIsJSON := true
			if tt.expectedOutput != "" {
				if err := json.Unmarshal([]byte(tt.expectedOutput), &expectedObj); err != nil {
					expectedIsJSON = false
				}
			}

			resultIsJSON := true
			if resultStr != "" {
				if err := json.Unmarshal(result, &resultObj); err != nil {
					resultIsJSON = false
				}
			}

			// If either is not JSON (invalid JSON test), compare as strings
			if !expectedIsJSON || !resultIsJSON {
				if resultStr != tt.expectedOutput {
					t.Errorf("%s\nExpected: %s\nGot: %s", tt.description, tt.expectedOutput, resultStr)
				}
				return
			}

			// Both are valid JSON, compare objects
			expectedJSON, _ := json.Marshal(expectedObj)
			resultJSON, _ := json.Marshal(resultObj)

			if string(expectedJSON) != string(resultJSON) {
				t.Errorf("%s\nExpected: %s\nGot: %s", tt.description, string(expectedJSON), string(resultJSON))
			}
		})
	}
}

func TestEnsureSeparator(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"docs", "docs_"},
		{"docs_", "docs_"},
		{"", ""},
		{"a", "a_"},
		{"prefix_", "prefix_"},
	}

	for _, tt := range tests {
		result := ensureSeparator(tt.input)
		if result != tt.expected {
			t.Errorf("ensureSeparator(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}
