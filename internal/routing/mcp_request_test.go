package routing

import (
	"encoding/json"
	"testing"
)

func TestInjectResourcePrefix(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		prefix  string
		wantURI string
		wantOK  bool
	}{
		{"basic prefix injection", "ui://template.html", "insights", "ui://insights_template.html", true},
		{"prefix already has separator", "ui://template.html", "insights_", "ui://insights_template.html", true},
		{"non-ui scheme untouched", "https://example.com/x.html", "insights", "https://example.com/x.html", false},
		{"malformed uri untouched", "ui://\x7fbad", "insights", "ui://\x7fbad", false},
		{"empty prefix untouched host", "ui://template.html", "", "ui://template.html", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := InjectResourcePrefix(tt.uri, tt.prefix)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantURI {
				t.Errorf("got %q, want %q", got, tt.wantURI)
			}
		})
	}
}

func TestStripResourcePrefix(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		prefix  string
		wantURI string
	}{
		{"basic prefix stripped", "ui://insights_template.html", "insights", "ui://template.html"},
		{"prefix already has separator", "ui://insights_template.html", "insights_", "ui://template.html"},
		{"non-ui scheme untouched", "https://example.com/x.html", "insights", "https://example.com/x.html"},
		{"malformed uri untouched", "ui://\x7fbad", "insights", "ui://\x7fbad"},
		{"host without prefix untouched", "ui://template.html", "insights", "ui://template.html"},
		{"empty prefix untouched", "ui://template.html", "", "ui://template.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripResourcePrefix(tt.uri, tt.prefix)
			if got != tt.wantURI {
				t.Errorf("got %q, want %q", got, tt.wantURI)
			}
		})
	}
}

func TestInjectThenStripResourcePrefix_RoundTrips(t *testing.T) {
	uri := "ui://template.html"
	prefix := "insights"

	injected, ok := InjectResourcePrefix(uri, prefix)
	if !ok {
		t.Fatalf("InjectResourcePrefix(%q, %q) returned ok=false", uri, prefix)
	}
	if got := StripResourcePrefix(injected, prefix); got != uri {
		t.Errorf("StripResourcePrefix(%q, %q) = %q, want %q", injected, prefix, got, uri)
	}
}

func TestStripAuthorityPrefix(t *testing.T) {
	tests := []struct {
		name          string
		authority     string
		prefix        string
		wantAuthority string
	}{
		{"basic prefix stripped", "app_example.com", "app", "example.com"},
		{"prefix already has separator", "app_example.com", "app_", "example.com"},
		{"authority without matching prefix untouched", "example.com", "app", "example.com"},
		{"empty prefix untouched", "example.com", "", "example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripAuthorityPrefix(tt.authority, tt.prefix)
			if got != tt.wantAuthority {
				t.Errorf("got %q, want %q", got, tt.wantAuthority)
			}
		})
	}
}

func TestMCPRequest_Arguments(t *testing.T) {
	t.Run("marshals params.arguments", func(t *testing.T) {
		req := &MCPRequest{Params: map[string]any{"name": "mytool", "arguments": map[string]any{"q": 1}}}
		raw, err := req.Arguments()
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["q"] != float64(1) {
			t.Errorf("got %#v", got)
		}
	})

	t.Run("missing arguments yields empty object", func(t *testing.T) {
		req := &MCPRequest{Params: map[string]any{"name": "mytool"}}
		raw, err := req.Arguments()
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{}` {
			t.Errorf("got %s, want {}", raw)
		}
	})
}

func TestMCPRequest_ElicitationArguments(t *testing.T) {
	t.Run("strips action and keeps the rest", func(t *testing.T) {
		req := &MCPRequest{Result: map[string]any{"action": "accept", "content": map[string]any{"name": "test"}}}
		raw, err := req.ElicitationArguments()
		if err != nil {
			t.Fatal(err)
		}
		restored := map[string]any{}
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		content, ok := restored["content"].(map[string]any)
		if !ok || content["name"] != "test" {
			t.Errorf("got %#v", restored)
		}
		if _, ok := restored["action"]; ok {
			t.Error("action should be stripped")
		}
	})

	t.Run("action only yields empty object", func(t *testing.T) {
		req := &MCPRequest{Result: map[string]any{"action": "accept"}}
		raw, err := req.ElicitationArguments()
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{}` {
			t.Errorf("got %s, want {}", raw)
		}
	})
}

func TestMCPRequest_IsElicitationAccept(t *testing.T) {
	if !(&MCPRequest{Result: map[string]any{"action": "accept"}}).IsElicitationAccept() {
		t.Error("accept should be true")
	}
	if (&MCPRequest{Result: map[string]any{"action": "decline"}}).IsElicitationAccept() {
		t.Error("decline should be false")
	}
	if (&MCPRequest{Result: map[string]any{"action": "cancel"}}).IsElicitationAccept() {
		t.Error("cancel should be false")
	}
	if (&MCPRequest{Method: "tools/call"}).IsElicitationAccept() {
		t.Error("tools/call should be false")
	}
}
