package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// newTestAdapter spins up an httptest server returning a fixed OK envelope for
// every API call, and an adapter pointed at it with the given env defaults.
func newTestAdapter(t *testing.T, body, defProject, defLocation string) *jetder.Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvEndpoint, srv.URL)
	t.Setenv(jetder.EnvDefaultProject, defProject)
	t.Setenv(jetder.EnvDefaultLocation, defLocation)

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// connectInMemory wires the real server (all tools) to an in-process client.
func connectInMemory(t *testing.T, adapter *jetder.Adapter) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := buildServer(adapter)
	st, ct := mcp.NewInMemoryTransports()

	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s returned IsError; content=%v", name, res.Content)
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("CallTool %s: StructuredContent not a map: %T", name, res.StructuredContent)
	}
	return sc
}

// TestProjectUsage_ResolvedContext_FromDefault: empty project arg -> env default
// must appear as resolvedProject in the structured output.
func TestProjectUsage_ResolvedContext_FromDefault(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{"cpu":1,"memory":2}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "project-usage", map[string]any{}) // no project -> use default
	if got := sc["resolvedProject"]; got != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", got)
	}
}

// TestDeploymentList_ResolvedContext_EmptyResult: even with empty items, the
// resolved project+location appear (defaults are not hidden state).
func TestDeploymentList_ResolvedContext_EmptyResult(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{"items":[]}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "deployment-list", map[string]any{})
	if got := sc["resolvedProject"]; got != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", got)
	}
	if got := sc["resolvedLocation"]; got != "def-loc" {
		t.Fatalf("resolvedLocation = %v, want def-loc", got)
	}
}

// TestArgOverridesDefault: explicit arg wins over env default in the output.
func TestArgOverridesDefault(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{"items":[]}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "deployment-list", map[string]any{
		"project":  "explicit-proj",
		"location": "explicit-loc",
	})
	if got := sc["resolvedProject"]; got != "explicit-proj" {
		t.Fatalf("resolvedProject = %v, want explicit-proj", got)
	}
	if got := sc["resolvedLocation"]; got != "explicit-loc" {
		t.Fatalf("resolvedLocation = %v, want explicit-loc", got)
	}
}

// TestDeploymentMetrics_TimeRangeInOutput: timeRange echoed in output.
func TestDeploymentMetrics_TimeRangeInOutput(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "deployment-metrics", map[string]any{
		"name":      "web",
		"timeRange": "1h",
	})
	if got := sc["timeRange"]; got != "1h" {
		t.Fatalf("timeRange = %v, want 1h", got)
	}
	if got := sc["resolvedProject"]; got != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", got)
	}
}
