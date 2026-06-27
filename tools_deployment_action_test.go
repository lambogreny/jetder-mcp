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

func TestDeploymentDeploy_PullSecretEchoed(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "deployment-deploy", map[string]any{
		"name": "web", "image": "ghcr.io/lambogreny/app:sha", "pullSecret": "ghcr-cred",
	})
	if sc["pullSecret"] != "ghcr-cred" {
		t.Fatalf("expected pullSecret echoed, got %+v", sc)
	}
}

func TestDeploymentDeploy_NoPullSecretByDefault(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy", map[string]any{"name": "web", "image": "img:1"})
	// pullSecret omitempty → absent (or empty) when not requested.
	if v, present := sc["pullSecret"]; present && v != "" {
		t.Fatalf("pullSecret should be absent/empty by default, got %v", v)
	}
}

// TestValidatePullSecretName: unit truth table — names accepted, credentials/junk
// rejected (the regex+len guard that runs BEFORE any deploy).
func TestValidatePullSecretName(t *testing.T) {
	good := []string{"ghcr-pull", "abc", "a-b-c", "pull123"}
	for _, v := range good {
		if got, err := validatePullSecretName(v); err != nil || got != v {
			t.Fatalf("valid %q rejected: %v", v, err)
		}
	}
	if got, _ := validatePullSecretName("  ghcr-pull  "); got != "ghcr-pull" {
		t.Fatalf("trim failed: %q", got)
	}
	if got, err := validatePullSecretName(""); err != nil || got != "" {
		t.Fatalf("empty should omit: %q %v", got, err)
	}
	bad := []string{
		"ghp_AbC123def456GHI789jkl012", // PAT-lookalike (uppercase + len>25)
		"https://ghcr.io/x",            // URL
		"user:token",                   // colon
		"has space",                    // space
		"ab",                           // too short
		"UPPER",                        // uppercase
		"toolongtoolongtoolongtoolong", // >25
		"-leading",                     // leading dash
		"trailing-",                    // trailing dash
	}
	for _, v := range bad {
		if _, err := validatePullSecretName(v); err == nil {
			t.Fatalf("invalid %q should be rejected (credential/URL/junk)", v)
		}
	}
}

// TestDeploymentDeploy_PullSecretRejectsCredential: a misplaced credential in the
// pullSecret arg must be rejected before any deploy POST.
func TestDeploymentDeploy_PullSecretRejectsCredential(t *testing.T) {
	callToolExpectError(t, "deployment-deploy", map[string]any{
		"name": "web", "image": "img:1", "pullSecret": "ghp_AbC123def456GHI789jkl012",
	})
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
