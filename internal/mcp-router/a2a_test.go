package mcprouter

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

func TestIsA2APath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/a2a/weather", true},
		{"/a2a/mcp-test/weather", true},
		{"/a2a/weather/.well-known/agent-card.json", true},
		{"/mcp", false},
		{"/a2a", false}, // exactly /a2a with no trailing slash is not a routed A2A path
		{"/a2axyz", false},
		{"/", false},
	}
	for _, c := range cases {
		if got := isA2APath(c.path); got != c.want {
			t.Errorf("isA2APath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestA2AAgentFromPath(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"/a2a/weather", "weather"},
		{"/a2a/weather/tasks", "weather"},
		{"/a2a/weather/.well-known/agent-card.json", "weather"},
		{"/a2a/weather?x=1", "weather"},
		{"/a2a/", ""},
	}
	for _, c := range cases {
		if got := a2aAgentFromPath(c.path); got != c.want {
			t.Errorf("a2aAgentFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestNormalizeA2AMethod(t *testing.T) {
	cases := []struct {
		method, want string
	}{
		{"SendMessage", "SendMessage"},
		{"SendStreamingMessage", "SendStreamingMessage"},
		{"GetTask", "GetTask"},
		{"CancelTask", "CancelTask"},
		{"SubscribeToTask", "SubscribeToTask"},
		{"ListTasks", "other"},               // unknown v1 method
		{"message/send", "other"},            // v0.3 name, not v1
		{"'; DROP TABLE tasks; --", "other"}, // arbitrary client string never becomes a raw label
		{"", "other"},
	}
	for _, c := range cases {
		if got := normalizeA2AMethod(c.method); got != c.want {
			t.Errorf("normalizeA2AMethod(%q) = %q, want %q", c.method, got, c.want)
		}
	}
}

func TestParseA2AMethod(t *testing.T) {
	t.Run("valid envelope", func(t *testing.T) {
		method, id, err := parseA2AMethod([]byte(`{"jsonrpc":"2.0","id":7,"method":"SendMessage","params":{"message":{}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if method != "SendMessage" {
			t.Errorf("method = %q, want SendMessage", method)
		}
		if string(id) != "7" {
			t.Errorf("id = %s, want 7", id)
		}
	})
	t.Run("case-variant method is an invalid request", func(t *testing.T) {
		method, id, err := parseA2AMethod([]byte(`{"jsonrpc":"2.0","id":7,"method":"SendMessage","METHOD":"GetTask"}`))
		if !errors.Is(err, routing.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
		if method != "" {
			t.Errorf("method = %q, want empty", method)
		}
		if string(id) != "7" {
			t.Errorf("id = %s, want 7", id)
		}
	})
	t.Run("invalid json fails", func(t *testing.T) {
		if _, _, err := parseA2AMethod([]byte(`{not json`)); err == nil {
			t.Error("expected error for invalid json")
		}
	})
	t.Run("empty body fails", func(t *testing.T) {
		if _, _, err := parseA2AMethod([]byte(``)); err == nil {
			t.Error("expected error for empty body")
		}
	})
}

func TestA2AErrorBody(t *testing.T) {
	t.Run("echoes id and codes error", func(t *testing.T) {
		var resp a2aErrorResponse
		if err := json.Unmarshal([]byte(a2aErrorBody(json.RawMessage("7"), a2aErrParse, "invalid json-rpc request")), &resp); err != nil {
			t.Fatalf("output not json: %v", err)
		}
		if resp.JSONRPC != "2.0" || string(resp.ID) != "7" || resp.Error.Code != a2aErrParse {
			t.Errorf("unexpected error body: %+v", resp)
		}
	})
	t.Run("null id when unknown", func(t *testing.T) {
		var resp a2aErrorResponse
		if err := json.Unmarshal([]byte(a2aErrorBody(nil, a2aErrParse, "x")), &resp); err != nil {
			t.Fatal(err)
		}
		if string(resp.ID) != "null" {
			t.Errorf("id = %s, want null", resp.ID)
		}
	})
}
