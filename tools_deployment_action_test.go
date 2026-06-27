package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// In-memory end-to-end: each action returns resolved context + success.
// The fake API returns an empty OK envelope (actions return *Empty upstream).

func TestDeploymentDeploy_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "deployment-deploy", map[string]any{
		"name":  "web",
		"image": "nginx:1",
	})
	if sc["resolvedProject"] != "def-proj" || sc["resolvedLocation"] != "def-loc" {
		t.Fatalf("resolved context missing: %+v", sc)
	}
	if sc["action"] != "deploy" || sc["success"] != true || sc["name"] != "web" {
		t.Fatalf("unexpected result: %+v", sc)
	}
}

func TestDeploymentPause_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-pause", map[string]any{"name": "web"})
	if sc["action"] != "pause" || sc["resolvedProject"] != "p" || sc["resolvedLocation"] != "l" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestDeploymentResume_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-resume", map[string]any{"name": "web"})
	if sc["action"] != "resume" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestDeploymentRollback_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-rollback", map[string]any{"name": "web", "revision": float64(3)})
	if sc["action"] != "rollback" || sc["detail"] != "revision=3" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

// ---- validation (error) paths ----

func callToolExpectError(t *testing.T, name string, args map[string]any) {
	t.Helper()
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// schema-level rejection surfaces as a transport error — that's a valid rejection.
		return
	}
	if !res.IsError {
		t.Fatalf("tool %s expected error result, got success: %+v", name, res.StructuredContent)
	}
}

func TestDeploymentRollback_RevisionTooLow(t *testing.T) {
	callToolExpectError(t, "deployment-rollback", map[string]any{"name": "web", "revision": float64(0)})
}

func TestDeploymentDeploy_MissingImage(t *testing.T) {
	callToolExpectError(t, "deployment-deploy", map[string]any{"name": "web"})
}

func TestDeploymentPause_WhitespaceName(t *testing.T) {
	callToolExpectError(t, "deployment-pause", map[string]any{"name": "   "})
}

// TestActionAnnotations locks the destructive classification:
// destructive = deploy, pause, rollback; non-destructive (restorative) = resume;
// reads are read-only.
func TestActionAnnotations(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)

	lt, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	type want struct {
		readOnly    bool
		destructive bool // only checked when not read-only
	}
	expect := map[string]want{
		"deployment-deploy":   {readOnly: false, destructive: true},
		"deployment-pause":    {readOnly: false, destructive: true},
		"deployment-resume":   {readOnly: false, destructive: false},
		"deployment-rollback": {readOnly: false, destructive: true},
		"deployment-list":     {readOnly: true},
		"me-get":              {readOnly: true},
	}

	got := map[string]*mcp.ToolAnnotations{}
	for _, tool := range lt.Tools {
		got[tool.Name] = tool.Annotations
	}

	for name, w := range expect {
		ann := got[name]
		if ann == nil {
			t.Errorf("%s: missing annotations", name)
			continue
		}
		if ann.ReadOnlyHint != w.readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", name, ann.ReadOnlyHint, w.readOnly)
		}
		if !w.readOnly {
			if ann.DestructiveHint == nil {
				t.Errorf("%s: destructiveHint nil, want %v", name, w.destructive)
				continue
			}
			if *ann.DestructiveHint != w.destructive {
				t.Errorf("%s: destructiveHint = %v, want %v", name, *ann.DestructiveHint, w.destructive)
			}
		}
	}
}
