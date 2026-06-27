package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callToolErrorText calls a tool expected to fail and returns all error-surface
// text (transport error + result content). Used to assert no secret leaks.
func callToolErrorText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	if res.IsError {
		b.WriteString("IsError ")
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	raw, _ := json.Marshal(res.StructuredContent)
	b.Write(raw)
	return b.String()
}

// TestSecretCreate_NeverEchoesValue: value goes in as input but must NOT appear
// anywhere in the response (structured output or serialized JSON).
func TestSecretCreate_NeverEchoesValue(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "secret-create", map[string]any{
		"name":  "api-key",
		"value": "PLAINTEXT-SECRET-XYZ",
	})
	if sc["name"] != "api-key" || sc["success"] != true {
		t.Fatalf("unexpected result: %+v", sc)
	}
	if sc["resolvedProject"] != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", sc["resolvedProject"])
	}
	if _, present := sc["value"]; present {
		t.Fatalf("SECRET LEAK: value present in create response: %+v", sc)
	}
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "PLAINTEXT-SECRET-XYZ") {
		t.Fatalf("SECRET LEAK: value found in create response JSON: %s", raw)
	}
}

func TestPullSecretCreate_NeverEchoesPassword(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "pull-secret-create", map[string]any{
		"name":     "reg",
		"server":   "registry.example",
		"username": "u",
		"password": "REGISTRY-PASS-123",
	})
	raw, _ := json.Marshal(sc)
	if strings.Contains(string(raw), "REGISTRY-PASS-123") {
		t.Fatalf("PASSWORD LEAK in create response: %s", raw)
	}
	if sc["success"] != true {
		t.Fatalf("expected success: %+v", sc)
	}
}

// TestSecretCreate_ErrorPathNeverLeaksValue: upstream returns ok:false with an
// error message that echoes the submitted secret; MCP must redact it.
func TestSecretCreate_ErrorPathNeverLeaksValue(t *testing.T) {
	body := `{"ok":false,"error":{"message":"invalid secret value PLAINTEXT-SECRET-XYZ rejected"}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	got := callToolErrorText(t, cs, "secret-create", map[string]any{
		"name": "my-key", "value": "PLAINTEXT-SECRET-XYZ",
	})
	if strings.Contains(got, "PLAINTEXT-SECRET-XYZ") {
		t.Fatalf("SECRET LEAK on error path: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in error: %q", got)
	}
}

func TestPullSecretCreate_ErrorPathNeverLeaksPassword(t *testing.T) {
	body := `{"ok":false,"error":{"message":"registry auth failed for REGISTRY-PASS-123"}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	got := callToolErrorText(t, cs, "pull-secret-create", map[string]any{
		"name": "reg", "server": "r.example", "username": "u", "password": "REGISTRY-PASS-123",
	})
	if strings.Contains(got, "REGISTRY-PASS-123") {
		t.Fatalf("PASSWORD LEAK on error path: %q", got)
	}
}

func TestDiskCreate_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "disk-create", map[string]any{"name": "data", "size": float64(10)})
	if sc["action"] != "create" || sc["resource"] != "disk" || sc["success"] != true {
		t.Fatalf("unexpected: %+v", sc)
	}
	if sc["resolvedProject"] != "def-proj" || sc["resolvedLocation"] != "def-loc" {
		t.Fatalf("resolved context wrong: %+v", sc)
	}
}

func TestDiskUpdate_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "disk-update", map[string]any{"name": "data", "size": float64(20)})
	if sc["action"] != "update" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestOrganizationCreate_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "organization-create", map[string]any{"id": "neworg", "name": "New Org"})
	if sc["action"] != "create" || sc["name"] != "neworg" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestRoleCreate_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "role-create", map[string]any{
		"role": "deployer", "name": "Deployer", "permissions": []any{"deployment.deploy"},
	})
	if sc["action"] != "create" || sc["resolvedProject"] != "def-proj" {
		t.Fatalf("unexpected: %+v", sc)
	}
}

// ---- validation error paths ----

func TestSecretCreate_MissingValue(t *testing.T) {
	callToolExpectError(t, "secret-create", map[string]any{"name": "k"})
}

func TestDiskCreate_MissingName(t *testing.T) {
	callToolExpectError(t, "disk-create", map[string]any{"size": float64(5)})
}

func TestServiceAccountCreate_MissingSID(t *testing.T) {
	callToolExpectError(t, "service-account-create", map[string]any{"name": "x"})
}

func TestOrganizationCreate_MissingID(t *testing.T) {
	callToolExpectError(t, "organization-create", map[string]any{"name": "x"})
}

func TestWorkloadIdentityCreate_MissingGSA(t *testing.T) {
	callToolExpectError(t, "workload-identity-create", map[string]any{"name": "wi"})
}
