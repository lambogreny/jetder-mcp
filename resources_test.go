package main

import (
	"context"
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
	uris := map[string]bool{}
	for _, r := range lr.Resources {
		uris[r.URI] = true
		if r.MIMEType != "text/markdown" {
			t.Errorf("%s: MIME = %q, want text/markdown", r.URI, r.MIMEType)
		}
	}
	for _, want := range []string{"jetder://status", "jetder://help"} {
		if !uris[want] {
			t.Fatalf("resource %s not listed; got %v", want, uris)
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
