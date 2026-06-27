package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSecretGet_NeverLeaksValue is the redaction contract: even though the API
// returns a "value" field, secret-get output must NOT contain it.
func TestSecretGet_NeverLeaksValue(t *testing.T) {
	body := `{"ok":true,"result":{"name":"db-pass","value":"SUPER-SECRET-VALUE","createdBy":"alice"}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "secret-get", map[string]any{"name": "db-pass"})
	if sc["name"] != "db-pass" {
		t.Fatalf("name = %v, want db-pass", sc["name"])
	}
	if _, present := sc["value"]; present {
		t.Fatalf("SECRET LEAK: 'value' present in output: %+v", sc)
	}
	// Belt-and-suspenders: the secret string must not appear anywhere in the
	// serialized structured output.
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "SUPER-SECRET-VALUE") {
		t.Fatalf("SECRET LEAK: value string found in output JSON: %s", raw)
	}
}

func TestSecretList_NeverLeaksValue(t *testing.T) {
	body := `{"ok":true,"result":{"items":[{"name":"a","value":"LEAK-ME","createdBy":"bob"}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "secret-list", map[string]any{})
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "LEAK-ME") {
		t.Fatalf("SECRET LEAK in list output: %s", raw)
	}
	items, ok := sc["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1", sc["items"])
	}
	if m, _ := items[0].(map[string]any); m["name"] != "a" {
		t.Fatalf("item name = %v, want a", items[0])
	}
}

func TestPullSecretGet_NeverLeaksValue(t *testing.T) {
	body := `{"ok":true,"result":{"name":"reg","value":"REGISTRY-CREDS","location":"l"}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "pull-secret-get", map[string]any{"name": "reg"})
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "REGISTRY-CREDS") {
		t.Fatalf("PULL SECRET LEAK: %s", raw)
	}
	if _, present := sc["value"]; present {
		t.Fatalf("PULL SECRET LEAK: value present: %+v", sc)
	}
}

func TestBillingList_EndToEnd(t *testing.T) {
	body := `{"ok":true,"result":{"items":[{"id":"42","name":"Acme","active":true}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "billing-list", map[string]any{})
	items, ok := sc["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1", sc["items"])
	}
	if m, _ := items[0].(map[string]any); m["id"] != "42" || m["name"] != "Acme" {
		t.Fatalf("billing item = %v", items[0])
	}
}

func TestBillingProjectPrice_ResolvedContext(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{"price":12.5}}`, "def-proj", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "billing-project-price", map[string]any{})
	if sc["resolvedProject"] != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", sc["resolvedProject"])
	}
	if sc["price"] != 12.5 {
		t.Fatalf("price = %v, want 12.5", sc["price"])
	}
}

func TestRolePermissions_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":["read","write"]}`, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "role-permissions", map[string]any{})
	perms, ok := sc["permissions"].([]any)
	if !ok || len(perms) != 2 {
		t.Fatalf("permissions = %v, want 2", sc["permissions"])
	}
}

func TestOrganizationList_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{"items":[{"id":"org1","name":"Org One"}]}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "organization-list", map[string]any{})
	items, ok := sc["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1", sc["items"])
	}
}

func TestParseID(t *testing.T) {
	if v, err := parseID(" 42 "); err != nil || v != 42 {
		t.Fatalf("parseID(42) = %d,%v", v, err)
	}
	for _, bad := range []string{"", "0", "-1", "abc", "42abc", "42 abc", "4 2", "0x10", "1.5"} {
		if v, err := parseID(bad); err == nil {
			t.Fatalf("parseID(%q) should error, got %d", bad, v)
		}
	}
}

func TestPullSecretList_NeverLeaksValue(t *testing.T) {
	body := `{"ok":true,"result":{"items":[{"name":"reg","value":"REGISTRY-LEAK","location":"l"}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "pull-secret-list", map[string]any{})
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "REGISTRY-LEAK") {
		t.Fatalf("PULL SECRET LIST LEAK: %s", raw)
	}
	items, ok := sc["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1", sc["items"])
	}
	if m, _ := items[0].(map[string]any); m["name"] != "reg" {
		t.Fatalf("item name = %v, want reg", items[0])
	}
}

func TestRoleUsers_MapsItemsField(t *testing.T) {
	// upstream returns the canonical "items" field; MCP must surface it.
	body := `{"ok":true,"result":{"items":[{"email":"a@x.com","roles":["admin"]}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "role-users", map[string]any{})
	users, ok := sc["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("users = %v, want 1 (items field not mapped)", sc["users"])
	}
	if m, _ := users[0].(map[string]any); m["email"] != "a@x.com" {
		t.Fatalf("user email = %v, want a@x.com", users[0])
	}
}

func TestRoleUsers_FallbackUsersField(t *testing.T) {
	// if only the legacy "users" field is populated, fall back to it.
	body := `{"ok":true,"result":{"users":[{"email":"b@x.com","roles":["viewer"]}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "role-users", map[string]any{})
	users, ok := sc["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("users = %v, want 1 (users fallback failed)", sc["users"])
	}
}

func TestDiskGet_MissingName(t *testing.T) {
	callToolExpectError(t, "disk-get", map[string]any{})
}

func TestRoleGet_MissingRole(t *testing.T) {
	callToolExpectError(t, "role-get", map[string]any{})
}
