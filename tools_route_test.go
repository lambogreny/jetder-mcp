package main

import (
	"testing"
)

func TestRouteCreateV2_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "route-create-v2", map[string]any{
		"domain": "example.com",
		"path":   "/api",
		"target": "deployment://web",
	})
	if sc["resolvedProject"] != "def-proj" || sc["resolvedLocation"] != "def-loc" {
		t.Fatalf("resolved context missing: %+v", sc)
	}
	if sc["action"] != "create" || sc["target"] != "deployment://web" || sc["success"] != true {
		t.Fatalf("unexpected: %+v", sc)
	}
}

func TestRouteCreateV2_MissingTarget(t *testing.T) {
	callToolExpectError(t, "route-create-v2", map[string]any{"domain": "example.com"})
}

func TestRouteCreateV2_BasicAuth(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "route-create-v2", map[string]any{
		"domain":    "example.com",
		"target":    "deployment://web",
		"basicAuth": map[string]any{"user": "u", "password": "secret"},
	})
	if sc["action"] != "create" || sc["success"] != true {
		t.Fatalf("basicAuth route create failed: %+v", sc)
	}
}

func TestRouteList_EndToEnd(t *testing.T) {
	body := `{"ok":true,"result":{"items":[{"domain":"a.com","path":"/","target":"deployment://x","location":"l"}]}}`
	a := newTestAdapter(t, body, "p", "l")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "route-list", map[string]any{})
	items, ok := sc["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want 1", sc["items"])
	}
	if sc["resolvedProject"] != "p" {
		t.Fatalf("resolvedProject = %v, want p", sc["resolvedProject"])
	}
}

func TestRouteGet_MissingDomain(t *testing.T) {
	callToolExpectError(t, "route-get", map[string]any{})
}
