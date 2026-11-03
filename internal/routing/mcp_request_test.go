package routing

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestParamsJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		in        string // compact with sorted top-level keys, so output must match byte for byte
		wantName  string
		wantURI   string
		wantArgs  string
		wantCaps  string
		wantExtra []string
	}{
		{
			name:      "all fields with _meta, unknown key and large int",
			in:        `{"_meta":{"progressToken":7},"arguments":{"id":12345678901234567890,"a":1.50},"capabilities":{"elicitation":{}},"name":"weather","uri":"ui://app/x","x-custom":{"a":1}}`,
			wantName:  "weather",
			wantURI:   "ui://app/x",
			wantArgs:  `{"id":12345678901234567890,"a":1.50}`,
			wantCaps:  `{"elicitation":{}}`,
			wantExtra: []string{"_meta", "x-custom"},
		},
		{name: "non-string name stays in extra", in: `{"name":42}`, wantExtra: []string{"name"}},
		{name: "empty name stays in extra", in: `{"name":""}`, wantExtra: []string{"name"}},
		{name: "non-string uri stays in extra", in: `{"uri":42}`, wantExtra: []string{"uri"}},
		{name: "missing arguments", in: `{"name":"t"}`, wantName: "t"},
		{name: "null arguments", in: `{"arguments":null,"name":"t"}`, wantName: "t", wantArgs: "null"},
		{name: "empty params", in: `{}`},
		{name: "extra key that needs escaping", in: `{"a\"b":1}`, wantExtra: []string{`a"b`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Params
			require.NoError(t, json.Unmarshal([]byte(tt.in), &p))
			require.Equal(t, tt.wantName, p.Name)
			require.Equal(t, tt.wantURI, p.URI)
			require.Equal(t, tt.wantArgs, string(p.Arguments))
			require.Equal(t, tt.wantCaps, string(p.Capabilities))
			require.ElementsMatch(t, tt.wantExtra, slices.Collect(maps.Keys(p.Extra)))

			out, err := json.Marshal(&p)
			require.NoError(t, err)
			// byte comparison, not JSONEq: JSONEq decodes numbers as float64 and hides corruption
			require.Equal(t, tt.in, string(out))
		})
	}
}

func TestParamsNameEncoding(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "escapes", in: `{"name":"café \"x\""}`, want: `café "x"`},
		{name: "raw non-ascii", in: `{"name":"café"}`, want: "café"},
		{name: "invalid utf-8 is replaced", in: "{\"name\":\"a\xff\"}", want: "a�"},
		{name: "whitespace around value", in: `{ "name" : "t" }`, want: "t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Params
			require.NoError(t, json.Unmarshal([]byte(tt.in), &p))
			require.Equal(t, tt.want, p.Name)

			out, err := json.Marshal(&p)
			require.NoError(t, err)
			want, err := json.Marshal(map[string]string{"name": tt.want})
			require.NoError(t, err)
			require.Equal(t, string(want), string(out))
		})
	}
}

func TestParamsRewriteName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keeps other keys",
			in:   `{"_meta":{"progressToken":"t1"},"arguments":{"id":12345678901234567890},"name":"prefix_weather"}`,
			want: `{"_meta":{"progressToken":"t1"},"arguments":{"id":12345678901234567890},"name":"weather"}`,
		},
		{name: "replaces non-string name", in: `{"name":42}`, want: `{"name":"weather"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Params
			require.NoError(t, json.Unmarshal([]byte(tt.in), &p))

			p.Name = "weather"

			out, err := json.Marshal(&p)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(out))
		})
	}
}
