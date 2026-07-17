package routing

import (
	"testing"

	"k8s.io/utils/ptr"
)

func buildTestTable() *Table {
	return NewTableBuilder().
		AddTool("github_search", &ServerRoute{
			Name:   "github",
			Host:   "github.mcp.local",
			Prefix: "github_",
			Path:   "/mcp",
		}).
		AddTool("slack_post", &ServerRoute{
			Name: "slack",
			Host: "slack.mcp.local",
			Path: "/mcp",
		}).
		AddPrompt("summarize", &ServerRoute{
			Name: "github",
			Host: "github.mcp.local",
			Path: "/mcp",
		}).
		AddPrefix("github_", &ServerRoute{
			Name:             "github",
			Host:             "github.mcp.local",
			Prefix:           "github_",
			Path:             "/mcp",
			UserSpecificList: true,
		}).
		AddBrokerTool("discover_tools").
		AddBrokerTool("select_tools").
		AddAnnotation("github:github_:github.mcp.local", "search", &ToolAnnotation{
			ReadOnlyHint:    ptr.To(true),
			DestructiveHint: new(bool),
		}).
		Build()
}

func TestLookupTool(t *testing.T) {
	table := buildTestTable()

	r, ok := table.LookupTool("github_search")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if r.Host != "github.mcp.local" {
		t.Errorf("expected host github.mcp.local, got %s", r.Host)
	}
	if r.Prefix != "github_" {
		t.Errorf("expected prefix github_, got %s", r.Prefix)
	}

	_, ok = table.LookupTool("nonexistent")
	if ok {
		t.Error("expected tool not to be found")
	}
}

func TestLookupPrompt(t *testing.T) {
	table := buildTestTable()

	r, ok := table.LookupPrompt("summarize")
	if !ok {
		t.Fatal("expected prompt to be found")
	}
	if r.Name != "github" {
		t.Errorf("expected name github, got %s", r.Name)
	}

	_, ok = table.LookupPrompt("nonexistent")
	if ok {
		t.Error("expected prompt not to be found")
	}
}

func TestLookupPrefix(t *testing.T) {
	table := buildTestTable()

	r, ok := table.LookupPrefix("github_user_repos")
	if !ok {
		t.Fatal("expected prefix match")
	}
	if r.Name != "github" {
		t.Errorf("expected name github, got %s", r.Name)
	}

	_, ok = table.LookupPrefix("jira_list")
	if ok {
		t.Error("expected no prefix match")
	}
}

func TestIsBrokerTool(t *testing.T) {
	table := buildTestTable()

	if !table.IsBrokerTool("discover_tools") {
		t.Error("expected discover_tools to be a broker tool")
	}
	if !table.IsBrokerTool("select_tools") {
		t.Error("expected select_tools to be a broker tool")
	}
	if table.IsBrokerTool("github_search") {
		t.Error("expected github_search not to be a broker tool")
	}
}

func TestToolAnnotations(t *testing.T) {
	table := buildTestTable()

	a, ok := table.ToolAnnotations("github:github_:github.mcp.local", "search")
	if !ok {
		t.Fatal("expected annotations to be found")
	}
	if a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
		t.Error("expected readOnlyHint=true")
	}
	if a.DestructiveHint == nil || *a.DestructiveHint {
		t.Error("expected destructiveHint=false")
	}
	if a.IdempotentHint != nil {
		t.Error("expected idempotentHint=nil (unspecified)")
	}

	_, ok = table.ToolAnnotations("github:github_:github.mcp.local", "nonexistent")
	if ok {
		t.Error("expected annotations not to be found")
	}
}

func TestEmptyTable(t *testing.T) {
	table := NewTableBuilder().Build()

	if _, ok := table.LookupTool("anything"); ok {
		t.Error("empty table should not match tools")
	}
	if _, ok := table.LookupPrompt("anything"); ok {
		t.Error("empty table should not match prompts")
	}
	if _, ok := table.LookupPrefix("anything"); ok {
		t.Error("empty table should not match prefixes")
	}
	if table.IsBrokerTool("anything") {
		t.Error("empty table should not have broker tools")
	}
}
