package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- unit: helper messages carry actionable remediation -----------------------

func TestErrProjectRequired_Message(t *testing.T) {
	msg := errProjectRequired().Error()
	for _, want := range []string{"project required", "JETDER_DEFAULT_PROJECT", "https://thunder.in.th/"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("errProjectRequired missing %q: %q", want, msg)
		}
	}
}

func TestErrLocationRequired_Message(t *testing.T) {
	msg := errLocationRequired().Error()
	if !strings.Contains(msg, "location required") || !strings.Contains(msg, "JETDER_DEFAULT_LOCATION") {
		t.Fatalf("errLocationRequired missing remediation: %q", msg)
	}
	// location is config, not access — must NOT include the owner contact.
	if strings.Contains(msg, "thunder.in.th") {
		t.Fatalf("errLocationRequired should not include the owner contact: %q", msg)
	}
}

func TestErrArgRequired_Message(t *testing.T) {
	msg := errArgRequired("zoneId").Error()
	if !strings.Contains(msg, "zoneId required") || !strings.Contains(msg, `pass the "zoneId" argument`) {
		t.Fatalf("errArgRequired wrong: %q", msg)
	}
}

// --- e2e: a tool surfaces the actionable error over MCP -----------------------

// callToolWantError invokes a tool expecting an error, returning the error text.
func callToolWantError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// transport error is unexpected here.
		t.Fatalf("CallTool %s transport error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool %s expected an error result", name)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// project default missing → project-get returns the actionable remediation.
func TestToolError_ProjectMissing_Actionable(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "", "") // no default project
	cs := connectInMemory(t, a)
	msg := callToolWantError(t, cs, "project-get", map[string]any{}) // no project arg
	for _, want := range []string{"project required", "JETDER_DEFAULT_PROJECT", "https://thunder.in.th/"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("project-get error missing %q: %q", want, msg)
		}
	}
}

// deployment target missing location → actionable location remediation.
func TestToolError_DeploymentLocationMissing_Actionable(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "proj", "") // project ok, no location
	cs := connectInMemory(t, a)
	msg := callToolWantError(t, cs, "deployment-get", map[string]any{"name": "web"})
	if !strings.Contains(msg, "location required") || !strings.Contains(msg, "JETDER_DEFAULT_LOCATION") {
		t.Fatalf("deployment-get error missing location remediation: %q", msg)
	}
}

// Note: a missing `name` on deployment-get is caught by the input SCHEMA
// (required property) before the handler runs, so it surfaces as a validation
// error, not the errArgRequired message. The errArgRequired wording is covered by
// its unit test above and by resolveDeploymentTarget for non-schema-required args.
