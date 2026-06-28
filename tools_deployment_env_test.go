package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// envCaptureAdapter returns an adapter whose server records the deploy request body
// and returns an OK envelope. If failOnDeploy, hitting deployment.deploy fails the
// test (used to prove a conflict/bad key is rejected BEFORE any POST).
func envCaptureAdapter(t *testing.T, gotBody *map[string]any, failOnDeploy bool) *jetder.Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "deployment.deploy") {
			if failOnDeploy {
				t.Errorf("deployment.deploy must NOT be called (request should be rejected first)")
			}
			if gotBody != nil {
				_ = json.NewDecoder(r.Body).Decode(gotBody)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvEndpoint, srv.URL)
	t.Setenv(jetder.EnvDefaultProject, "p")
	t.Setenv(jetder.EnvDefaultLocation, "l")
	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func errText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// addEnv is passed through to the API, and the output echoes KEY NAMES only — no value.
func TestDeploymentDeploy_AddEnvPassThroughNoValueLeak(t *testing.T) {
	var body map[string]any
	a := envCaptureAdapter(t, &body, false)
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy", map[string]any{
		"name": "web", "image": "img:1",
		"addEnv": map[string]any{"DATABASE_URL": "postgres://SECRETvalue123", "APP_KEY": "sk-SECRETkey456"},
	})
	ae, _ := body["addEnv"].(map[string]any)
	if ae["DATABASE_URL"] != "postgres://SECRETvalue123" {
		t.Fatalf("addEnv value not passed through to API: %v", body["addEnv"])
	}
	blob := fmt.Sprintf("%v", sc)
	for _, secret := range []string{"SECRETvalue123", "sk-SECRETkey456", "postgres://"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("deploy output leaked an env value: %s", blob)
		}
	}
	for _, k := range []string{"DATABASE_URL", "APP_KEY"} {
		if !strings.Contains(blob, k) {
			t.Fatalf("env key %q should appear in output: %s", k, blob)
		}
	}
}

// addEnv + env together → rejected BEFORE any POST.
func TestDeploymentDeploy_EnvConflictNoPost(t *testing.T) {
	a := envCaptureAdapter(t, nil, true)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment-deploy",
		Arguments: map[string]any{
			"name": "web", "image": "img:1",
			"addEnv": map[string]any{"A": "1"},
			"env":    map[string]any{"B": "2"},
		},
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("addEnv+env together must be a tool error")
	}
}

// An env value reflected in an API error must be redacted.
func TestDeploymentDeploy_EnvValueRedactedInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"bad","message":"rejected value SECRETenv789 for KEY"}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvEndpoint, srv.URL)
	t.Setenv(jetder.EnvDefaultProject, "p")
	t.Setenv(jetder.EnvDefaultLocation, "l")
	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment-deploy",
		Arguments: map[string]any{"name": "web", "image": "img:1",
			"addEnv": map[string]any{"KEY": "SECRETenv789"}},
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error")
	}
	if strings.Contains(errText(res), "SECRETenv789") {
		t.Fatalf("env value leaked in error: %s", errText(res))
	}
}

// A key containing '=' is rejected (no POST) — and the error must NOT echo the raw
// key, because a "KEY=secret" paste would leak the secret after the '='.
func TestDeploymentDeploy_EnvKeyWithEqualsNoLeak(t *testing.T) {
	a := envCaptureAdapter(t, nil, true)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "deployment-deploy",
		Arguments: map[string]any{"name": "web", "image": "img:1",
			// User pasted a whole KEY=value pair (value is a secret) into the key slot.
			"addEnv": map[string]any{"DATABASE_URL=postgres://KEYSECRET999": "x"}},
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("a key containing '=' must be rejected")
	}
	msg := errText(res)
	for _, leak := range []string{"KEYSECRET999", "postgres://", "DATABASE_URL=postgres"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("env key-validation error leaked the pasted secret: %s", msg)
		}
	}
}

// The plan must also never echo a malformed (secret-bearing) env key.
func TestDeployPlan_EnvKeyWithEqualsNoLeak(t *testing.T) {
	a := envCaptureAdapter(t, nil, false)
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{
		"name": "web", "image": "img:1",
		"addEnv": map[string]any{"DATABASE_URL=postgres://PLANKEYSECRET": "x"},
	})
	blob := fmt.Sprintf("%v", sc)
	for _, leak := range []string{"PLANKEYSECRET", "postgres://", "DATABASE_URL=postgres"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("plan leaked a malformed env key/secret: %s", blob)
		}
	}
	// It should be flagged as a missing prereq (and rendered as [invalid]).
	if !strings.Contains(blob, "[invalid]") {
		t.Fatalf("plan should mask the malformed key as [invalid]: %s", blob)
	}
}

// removeEnv + envGroups pass through.
func TestDeploymentDeploy_RemoveEnvAndGroupsPassThrough(t *testing.T) {
	var body map[string]any
	a := envCaptureAdapter(t, &body, false)
	cs := connectInMemory(t, a)
	_ = callTool(t, cs, "deployment-deploy", map[string]any{
		"name": "web", "image": "img:1",
		"removeEnv": []any{"OLD_VAR"}, "envGroups": []any{"shared-db"},
	})
	if re, _ := body["removeEnv"].([]any); len(re) != 1 || re[0] != "OLD_VAR" {
		t.Fatalf("removeEnv not passed through: %v", body["removeEnv"])
	}
	if eg, _ := body["envGroups"].([]any); len(eg) != 1 || eg[0] != "shared-db" {
		t.Fatalf("envGroups not passed through: %v", body["envGroups"])
	}
}

// deployment-deploy-plan previews env KEY NAMES only — never values.
func TestDeployPlan_EnvKeysOnlyNoValue(t *testing.T) {
	a := envCaptureAdapter(t, nil, false) // plan never POSTs deploy anyway
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{
		"name": "web", "image": "img:1",
		"addEnv": map[string]any{"DATABASE_URL": "postgres://PLANSECRET999"},
	})
	blob := fmt.Sprintf("%v", sc)
	if strings.Contains(blob, "PLANSECRET999") || strings.Contains(blob, "postgres://") {
		t.Fatalf("plan leaked an env value: %s", blob)
	}
	if !strings.Contains(blob, "DATABASE_URL") {
		t.Fatalf("plan should preview the env KEY name: %s", blob)
	}
}
