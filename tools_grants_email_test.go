package main

import (
	"testing"
)

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
