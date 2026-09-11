package routing

import (
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
