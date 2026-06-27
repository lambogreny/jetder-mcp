package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGrantsEmailAnnotations locks the destructive classification of the
// side-effecting grant/key/email tools: each performs a real outward action
// (grant access, mint a credential, send mail), so MCP clients should confirm
// before calling — destructiveHint=true (and never read-only).
func TestGrantsEmailAnnotations(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)

	lt, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]*mcp.ToolAnnotations{}
	for _, tool := range lt.Tools {
		got[tool.Name] = tool.Annotations
	}

	for _, name := range []string{"role-grant", "service-account-create-key", "email-send"} {
		ann := got[name]
		if ann == nil {
			t.Errorf("%s: missing annotations", name)
			continue
		}
		if ann.ReadOnlyHint {
			t.Errorf("%s: readOnlyHint must be false (it mutates)", name)
		}
		if ann.DestructiveHint == nil || !*ann.DestructiveHint {
			t.Errorf("%s: destructiveHint must be true (real side effect)", name)
		}
	}
}

func TestRoleGrant_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "role-grant", map[string]any{"role": "admin-role", "email": "u@x.com"})
	if sc["action"] != "grant" || sc["resolvedProject"] != "def-proj" || sc["success"] != true {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestServiceAccountCreateKey_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "service-account-create-key", map[string]any{"id": "sa-1"})
	if sc["action"] != "create" || sc["resource"] != "serviceAccountKey" {
		t.Fatalf("unexpected: %+v", sc)
	}
	// No key material field should exist (upstream returns Empty).
	if _, present := sc["secret"]; present {
		t.Fatalf("unexpected secret field in create-key output: %+v", sc)
	}
}

func TestEmailSend_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "email-send", map[string]any{
		"from":        map[string]any{"email": "from@x.com"},
		"to":          []any{map[string]any{"email": "to@x.com"}},
		"subject":     "Hi",
		"contentType": "text/plain",
		"body":        "Hello",
	})
	if sc["success"] != true || sc["subject"] != "Hi" {
		t.Fatalf("unexpected: %+v", sc)
	}
	to, ok := sc["to"].([]any)
	if !ok || len(to) != 1 {
		t.Fatalf("to = %v, want 1", sc["to"])
	}
}

// ---- validation error paths ----

func TestRoleGrant_MissingEmail(t *testing.T) {
	callToolExpectError(t, "role-grant", map[string]any{"role": "admin"})
}

func TestServiceAccountCreateKey_MissingID(t *testing.T) {
	callToolExpectError(t, "service-account-create-key", map[string]any{})
}

func TestEmailSend_MissingRecipients(t *testing.T) {
	callToolExpectError(t, "email-send", map[string]any{
		"from": map[string]any{"email": "f@x.com"}, "subject": "s",
		"contentType": "text/plain", "body": "b", "to": []any{},
	})
}
