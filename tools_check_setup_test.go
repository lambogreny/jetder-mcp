package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// routedAdapter spins up a fake jetder server that dispatches on the request path
// (the API method, e.g. "/me.get", "/pullsecret.get") and returns the matching
// canned JSON envelope. Unmatched paths return a generic ok envelope.
func routedAdapter(t *testing.T, routes map[string]string, defProject, defLocation string) *jetder.Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if body, ok := routes[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
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

// configuredCF builds a Cloudflare client via the real constructor by setting the
// env it reads. token must be non-empty (else New returns nil). accountID may be "".
func configuredCF(t *testing.T, token, accountID string) *cloudflare.Client {
	t.Helper()
	t.Setenv(cloudflare.EnvToken, token)
	t.Setenv(cloudflare.EnvAccountID, accountID)
	cf, err := cloudflare.New()
	if err != nil {
		t.Fatalf("cloudflare.New: %v", err)
	}
	if cf == nil {
		t.Fatal("cloudflare.New returned nil despite a token")
	}
	return cf
}

// connectInMemoryCF is connectInMemory but with a (possibly nil) Cloudflare client
// so check-setup's CF branch can be exercised.
func connectInMemoryCF(t *testing.T, adapter *jetder.Adapter, cf *cloudflare.Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := buildServer(adapter, cf)
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

// decodeCheckSetup converts the structured-content map into the typed output.
func decodeCheckSetup(t *testing.T, sc map[string]any) CheckSetupOutput {
	t.Helper()
	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out CheckSetupOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal CheckSetupOutput: %v", err)
	}
	return out
}

// runCheckSetup invokes the tool through an in-memory MCP round-trip and returns
// the typed output (more convenient than the structured-content map).
func runCheckSetup(t *testing.T, adapter *jetder.Adapter, cf *cloudflare.Client, in CheckSetupInput) CheckSetupOutput {
	t.Helper()
	cs := connectInMemoryCF(t, adapter, cf)
	args := map[string]any{}
	if in.Project != "" {
		args["project"] = in.Project
	}
	if in.Location != "" {
		args["location"] = in.Location
	}
	if in.PullSecret != "" {
		args["pullSecret"] = in.PullSecret
	}
	sc := callTool(t, cs, "check-setup", args)
	return decodeCheckSetup(t, sc)
}

func byName(out CheckSetupOutput, name string) (SetupCheck, bool) {
	for _, c := range out.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return SetupCheck{}, false
}

func mustCheck(t *testing.T, out CheckSetupOutput, name string) SetupCheck {
	t.Helper()
	c, ok := byName(out, name)
	if !ok {
		t.Fatalf("missing check %q in %+v", name, out.Checks)
	}
	return c
}

// --- auth ok, full readiness -------------------------------------------------

func TestCheckSetup_AllOK(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"pullsecret.get": `{"ok":true,"result":{"name":"ghcr-pull"}}`,
	}, "proj", "loc")
	cf := configuredCF(t, "cf-tok", "acct-123")

	out := runCheckSetup(t, a, cf, CheckSetupInput{})
	if !out.OverallReady {
		t.Fatalf("expected ready, got %+v", out)
	}
	if out.Fails != 0 {
		t.Fatalf("expected 0 fails, got %d (%+v)", out.Fails, out.Checks)
	}
	if c := mustCheck(t, out, "jetder-auth"); c.Status != statusOK || !strings.Contains(c.Detail, "me@test.example") {
		t.Fatalf("jetder-auth = %+v", c)
	}
	if c := mustCheck(t, out, "jetder-kyc"); c.Status != statusOK {
		t.Fatalf("jetder-kyc = %+v", c)
	}
	if c := mustCheck(t, out, "pull-secret"); c.Status != statusOK {
		t.Fatalf("pull-secret = %+v", c)
	}
	if out.ResolvedProject != "proj" || out.ResolvedLocation != "loc" {
		t.Fatalf("resolved = (%q,%q)", out.ResolvedProject, out.ResolvedLocation)
	}
}

// --- missing project/location => fail ---------------------------------------

func TestCheckSetup_MissingProjectLocation_Fails(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
	}, "", "")
	out := runCheckSetup(t, a, nil, CheckSetupInput{})

	if out.OverallReady {
		t.Fatalf("expected NOT ready with no project/location")
	}
	if c := mustCheck(t, out, "project"); c.Status != statusFail {
		t.Fatalf("project should fail: %+v", c)
	}
	if c := mustCheck(t, out, "location"); c.Status != statusFail {
		t.Fatalf("location should fail: %+v", c)
	}
	// pull-secret must be skipped (warn), never call the API.
	if c := mustCheck(t, out, "pull-secret"); c.Status != statusWarn {
		t.Fatalf("pull-secret should be skipped/warn: %+v", c)
	}
}

// --- auth failure => structured fail, NOT a tool error -----------------------

func TestCheckSetup_AuthFail_IsStructuredNotToolError(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":false,"error":{"message":"api: user not found"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, nil, CheckSetupInput{})

	if out.OverallReady {
		t.Fatalf("expected NOT ready on auth fail")
	}
	c := mustCheck(t, out, "jetder-auth")
	if c.Status != statusFail {
		t.Fatalf("jetder-auth should fail: %+v", c)
	}
	// KYC check is skipped when auth fails (no item).
	if _, ok := byName(out, "jetder-kyc"); ok {
		t.Fatalf("jetder-kyc must not appear when auth failed")
	}
	// pull-secret gated behind auth -> warn (skipped), not a live call.
	if c := mustCheck(t, out, "pull-secret"); c.Status != statusWarn {
		t.Fatalf("pull-secret should be skipped on auth fail: %+v", c)
	}
}

// --- KYC false => warn, still ready -----------------------------------------

func TestCheckSetup_KYCFalse_Warns(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":false}}`,
		"pullsecret.get": `{"ok":true,"result":{"name":"ghcr-pull"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, configuredCF(t, "cf-tok", "acct"), CheckSetupInput{})

	c := mustCheck(t, out, "jetder-kyc")
	if c.Status != statusWarn {
		t.Fatalf("jetder-kyc should warn when kyc=false: %+v", c)
	}
	if !out.OverallReady {
		t.Fatalf("KYC warn must NOT block readiness: %+v", out)
	}
}

// --- Cloudflare states -------------------------------------------------------

func TestCheckSetup_CloudflareNil_Warns(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"pullsecret.get": `{"ok":true,"result":{"name":"ghcr-pull"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, nil, CheckSetupInput{})

	c := mustCheck(t, out, "cloudflare")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "not configured") {
		t.Fatalf("cloudflare nil should warn: %+v", c)
	}
	if !out.OverallReady {
		t.Fatalf("CF warn must not block readiness")
	}
}

func TestCheckSetup_CloudflareTokenNoAccount_Warns(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"pullsecret.get": `{"ok":true,"result":{"name":"ghcr-pull"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, configuredCF(t, "cf-tok", ""), CheckSetupInput{})

	c := mustCheck(t, out, "cloudflare")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "Registrar") {
		t.Fatalf("cloudflare token-no-account should warn about registrar: %+v", c)
	}
}

// --- pull-secret absent => warn (public images still deploy) -----------------

func TestCheckSetup_PullSecretAbsent_Warns(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"pullsecret.get": `{"ok":false,"error":{"message":"api: pull secret not found"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, configuredCF(t, "cf", "acct"), CheckSetupInput{})

	c := mustCheck(t, out, "pull-secret")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "not found") {
		t.Fatalf("absent pull-secret should warn: %+v", c)
	}
	if !out.OverallReady {
		t.Fatalf("absent pull-secret must not block readiness: %+v", out)
	}
}

// --- pull-secret other error => fail ----------------------------------------

func TestCheckSetup_PullSecretError_Fails(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get":         `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
		"pullsecret.get": `{"ok":false,"error":{"message":"permission denied"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, nil, CheckSetupInput{})

	c := mustCheck(t, out, "pull-secret")
	if c.Status != statusFail {
		t.Fatalf("non-404 pull-secret error should fail: %+v", c)
	}
	if out.OverallReady {
		t.Fatalf("pull-secret error must block readiness")
	}
}

// --- invalid pullSecret (PAT-like) => fail, value NEVER echoed ---------------

func TestCheckSetup_PullSecretInvalidName_FailNoEcho(t *testing.T) {
	const patLike = "ghp_SUPERSECRETtoken1234567890ABCDEFG"
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":true,"result":{"email":"me@test.example","kyc":true}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, nil, CheckSetupInput{PullSecret: patLike})

	c := mustCheck(t, out, "pull-secret")
	if c.Status != statusFail {
		t.Fatalf("invalid name should fail: %+v", c)
	}
	if strings.Contains(c.Detail+c.Remediation, patLike) {
		t.Fatalf("PAT-like value leaked into check output: %+v", c)
	}
}

// --- redaction: jetder token / user / basic b64 must never appear ------------

func TestCheckSetup_NoSecretLeak_OnAuthError(t *testing.T) {
	// The fake returns an error string; even if the client surfaced raw auth
	// material, adapter.Redact + our output must not contain the token/user/b64.
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":false,"error":{"message":"boom"}}`,
	}, "proj", "loc")
	out := runCheckSetup(t, a, nil, CheckSetupInput{})

	var blob strings.Builder
	for _, c := range out.Checks {
		blob.WriteString(c.Name)
		blob.WriteString(c.Detail)
		blob.WriteString(c.Remediation)
	}
	s := blob.String()
	for _, secret := range []string{"tok", "ci@test.example", "Basic ", "Y2k6dG9r" /* base64(ci:tok)-ish */} {
		if strings.Contains(s, secret) {
			t.Fatalf("output leaked %q: %s", secret, s)
		}
	}
}

// --- ensure the tool round-trips as readOnly + structured (no IsError) -------

func TestCheckSetup_ToolNeverErrors_OnPrereqFailures(t *testing.T) {
	a := routedAdapter(t, map[string]string{
		"me.get": `{"ok":false,"error":{"message":"api: user not found"}}`,
	}, "", "")
	cs := connectInMemoryCF(t, a, nil)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "check-setup"})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("check-setup must not return a tool error on prereq failures; content=%v", res.Content)
	}
}

// avoid api import-unused if a future edit drops the sentinel reference.
var _ = api.ErrPullSecretNotFound
