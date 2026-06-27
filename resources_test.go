package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readResource reads a resource over an in-memory session and returns its text.
func readResource(t *testing.T, cs *mcp.ClientSession, uri string) string {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("ReadResource %s: %v", uri, err)
	}
	if len(res.Contents) == 0 {
		t.Fatalf("ReadResource %s: no contents", uri)
	}
	return res.Contents[0].Text
}

// --- listing + capabilities ---------------------------------------------------

func TestResources_Listed(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)

	lr, err := cs.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	mime := map[string]string{}
	for _, r := range lr.Resources {
		mime[r.URI] = r.MIMEType
	}
	want := map[string]string{
		"jetder://status":   "text/markdown",
		"jetder://help":     "text/markdown",
		"jetder://projects": "application/json",
	}
	if len(mime) != len(want) {
		t.Fatalf("expected %d resources, got %d: %v", len(want), len(mime), mime)
	}
	for uri, m := range want {
		if mime[uri] != m {
			t.Fatalf("resource %s MIME = %q, want %q", uri, mime[uri], m)
		}
	}
}

// jetder://projects must expose ONLY id/project/name (+ count) and never leak a
// sensitive field — billingAccount, webhookUrl (can carry a secret), quota, config,
// or createdAt — even though the upstream project record carries all of them.
func TestProjectsResource_SafeFieldsOnly(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"project.list": `{"ok":true,"result":{"items":[
			{"id":"42","project":"dev-acme","name":"Acme Dev",
			 "billingAccount":"99887766","webhookUrl":"https://hooks.example/x?token=SUPERSECRET",
			 "quota":{"deployments":10,"deploymentMaxReplicas":3},
			 "config":{"domainAllowDisableCdn":true},"createdAt":"2026-01-02T03:04:05Z"}
		]}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)
	body := readResource(t, cs, "jetder://projects")

	// Present: the safe identifiers + count.
	for _, want := range []string{`"id": "42"`, `"project": "dev-acme"`, `"name": "Acme Dev"`, `"count": 1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("projects resource missing %q:\n%s", want, body)
		}
	}
	// Absent: every sensitive / noise field, and the secret in the webhook URL.
	for _, bad := range []string{"billingAccount", "99887766", "webhookUrl", "SUPERSECRET", "quota", "config", "createdAt"} {
		if strings.Contains(body, bad) {
			t.Fatalf("projects resource leaked %q:\n%s", bad, body)
		}
	}
	// Valid JSON with the expected shape.
	var p struct {
		Projects []map[string]any `json:"projects"`
		Count    int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("projects resource is not valid JSON: %v\n%s", err, body)
	}
	if p.Count != 1 || len(p.Projects) != 1 {
		t.Fatalf("expected 1 project, got count=%d items=%d", p.Count, len(p.Projects))
	}
	if len(p.Projects[0]) != 3 {
		t.Fatalf("each project must have exactly 3 fields (id/project/name), got %v", p.Projects[0])
	}
}

// A backend list failure → content with a redacted error note (not a read error),
// and never any credential.
func TestProjectsResource_ListFail_ContentNotError(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":       `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"project.list": `{"ok":false,"error":{"message":"boom"}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)
	body := readResource(t, cs, "jetder://projects") // must NOT be a transport error

	var p struct {
		Projects []map[string]any `json:"projects"`
		Count    int              `json:"count"`
		Error    string           `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
	if p.Count != 0 || len(p.Projects) != 0 || p.Error == "" {
		t.Fatalf("expected empty list + error note, got %+v", p)
	}
	for _, secret := range []string{"ZZ-secret-tokenval-XYZ123", "ci@test.example", "Basic "} {
		if strings.Contains(body, secret) {
			t.Fatalf("projects error leaked %q:\n%s", secret, body)
		}
	}
}

func TestResources_CapabilityAdvertised(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)
	init := cs.InitializeResult()
	if init == nil || init.Capabilities == nil || init.Capabilities.Resources == nil {
		t.Fatalf("resources capability not advertised: %+v", init)
	}
	if init.Capabilities.Resources.ListChanged || init.Capabilities.Resources.Subscribe {
		t.Fatalf("resources must not advertise listChanged/subscribe (static set)")
	}
	// tools + prompts still advertised.
	if init.Capabilities.Tools == nil || init.Capabilities.Prompts == nil {
		t.Fatalf("tools/prompts capability missing: %+v", init.Capabilities)
	}
}

// --- jetder://status content + PII masking ------------------------------------

func TestStatusResource_OK_NoEmailLeak(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"secret-person@corp.example","kyc":true}}`,
		"pullsecret.get": `{"ok":true,"result":{"name":"ghcr-pull"}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)
	md := readResource(t, cs, "jetder://status")

	// Must report readiness + project/location, but NEVER the account email.
	if strings.Contains(md, "secret-person@corp.example") {
		t.Fatalf("status resource leaked the account email:\n%s", md)
	}
	for _, want := range []string{"Jetder setup status", "jetder-auth", "Project: `proj`", "Location: `loc`"} {
		if !strings.Contains(md, want) {
			t.Fatalf("status missing %q:\n%s", want, md)
		}
	}
}

func TestStatusResource_NoSecretLeak_OnAuthFail(t *testing.T) {
	// auth fails; even if the error path embedded creds, the masked render must not
	// expose the token, username, or base64 header value.
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":false,"error":{"message":"boom"}}`,
	}, "", "") // also no project/location → fail
	cs := connectInMemory(t, a)
	md := readResource(t, cs, "jetder://status")

	for _, secret := range []string{"ZZ-secret-tokenval-XYZ123", "ci@test.example", "Basic "} {
		if strings.Contains(md, secret) {
			t.Fatalf("status resource leaked %q:\n%s", secret, md)
		}
	}
	// It should still convey not-ready + a remediation (which is secret-free).
	if !strings.Contains(md, "not ready") {
		t.Fatalf("expected not-ready status:\n%s", md)
	}
	if !strings.Contains(md, "https://thunder.in.th/") {
		t.Fatalf("expected owner-contact remediation:\n%s", md)
	}
}

// renderStatusMarkdown must never emit a SetupCheck.Detail (the auth-ok detail
// carries the email). Guard at the unit level too.
func TestRenderStatusMarkdown_OmitsDetail(t *testing.T) {
	r := CheckSetupOutput{
		ResolvedContext: ResolvedContext{ResolvedProject: "p", ResolvedLocation: "l"},
		Checks: []SetupCheck{
			{Name: "jetder-auth", Status: statusOK, Detail: "authenticated as leaky@example.com"},
		},
	}
	md := renderStatusMarkdown(r)
	if strings.Contains(md, "leaky@example.com") || strings.Contains(md, "authenticated as") {
		t.Fatalf("renderStatusMarkdown must not emit Detail:\n%s", md)
	}
}

// --- jetder://help content -----------------------------------------------------

func TestHelpResource_Content(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
	}, "proj", "loc")
	cs := connectInMemory(t, a)
	md := readResource(t, cs, "jetder://help")

	for _, want := range []string{"JETDER_AUTH_USER", "JETDER_TOKEN", "https://thunder.in.th/", "docs/CREDENTIALS.md", "check-setup"} {
		if !strings.Contains(md, want) {
			t.Fatalf("help missing %q:\n%s", want, md)
		}
	}
	// No secrets / paste-bait placeholders.
	for _, bad := range []string{"ghp_", "Bearer ", "@gmail.com", "John Doe"} {
		if strings.Contains(md, bad) {
			t.Fatalf("help contains a secret/placeholder %q:\n%s", bad, md)
		}
	}
}
