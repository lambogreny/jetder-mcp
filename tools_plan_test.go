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

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// planAdapter wires an adapter at an httptest server that FAILS the test if any
// write/mutating method is invoked — a plan must never POST a deploy/register.
func planAdapter(t *testing.T, routes map[string]string) *jetder.Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Any mutating endpoint is forbidden from a plan.
		for _, bad := range []string{"deployment.deploy", "deployment.delete", "domain.create", "route.create"} {
			if strings.Contains(path, bad) {
				t.Errorf("plan must NOT call mutating endpoint %q", path)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		for suffix, body := range routes {
			if strings.HasSuffix(path, suffix) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "ZZ-secret-tokenval-XYZ123")
	t.Setenv(jetder.EnvEndpoint, srv.URL)
	t.Setenv(jetder.EnvDefaultProject, "proj")
	t.Setenv(jetder.EnvDefaultLocation, "loc")
	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// deployment-deploy-plan: planOnly/willExecute invariants, no write endpoint hit,
// and the pull-secret read-check surfaces a missing secret.
func TestDeployPlan_PreviewNoWrite(t *testing.T) {
	a := planAdapter(t, map[string]string{
		// pullsecret.get returns "not found" so the plan flags the missing prereq.
		"pullsecret.get": `{"ok":false,"error":{"code":"not_found","message":"pull secret not found"}}`,
	})
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{
		"name": "web", "image": "ghcr.io/x/y:v1", "pullSecret": "ps1",
	})
	if po, _ := sc["planOnly"].(bool); !po {
		t.Fatalf("planOnly = %v, want true", sc["planOnly"])
	}
	if we, _ := sc["willExecute"].(bool); we {
		t.Fatalf("willExecute = %v, want false", sc["willExecute"])
	}
	if d, _ := sc["wouldBeDestructive"].(bool); !d {
		t.Fatalf("wouldBeDestructive = %v, want true", sc["wouldBeDestructive"])
	}
	if _, ok := sc["success"]; ok {
		t.Fatalf("plan output must NOT carry success: %v", sc)
	}
	mp, _ := sc["missingPrereqs"].([]any)
	if len(mp) == 0 {
		t.Fatalf("expected a missing-prereq for the absent pull secret: %v", sc["missingPrereqs"])
	}
	rc, _ := sc["readChecksPerformed"].([]any)
	if len(rc) == 0 {
		t.Fatalf("expected a read-check to be recorded: %v", sc["readChecksPerformed"])
	}
}

// deployment-deploy-plan: missing image is flagged.
func TestDeployPlan_MissingImage(t *testing.T) {
	a := planAdapter(t, nil)
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{"name": "web"})
	mp, _ := sc["missingPrereqs"].([]any)
	found := false
	for _, m := range mp {
		if strings.Contains(fmt.Sprintf("%v", m), "image") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing image should be a prereq: %v", sc["missingPrereqs"])
	}
}

// Input parity: the plan struct accepts the same fields as the real deploy input.
func TestDeployPlan_InputParity(t *testing.T) {
	// A representative call with every field set must validate cleanly (no error).
	a := planAdapter(t, map[string]string{
		"pullsecret.get": `{"ok":true,"result":{"name":"ps1"}}`,
	})
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{
		"project": "proj", "location": "loc", "name": "web", "branch": "main",
		"image": "ghcr.io/x/y:v1", "minReplicas": float64(1), "maxReplicas": float64(3),
		"pullSecret": "ps1",
	})
	if sc["nextTool"] != "deployment-deploy" {
		t.Fatalf("nextTool = %v, want deployment-deploy", sc["nextTool"])
	}
}

// minReplicas > maxReplicas → a warning (not a silent pass).
func TestDeployPlan_ReplicaWarning(t *testing.T) {
	a := planAdapter(t, nil)
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "deployment-deploy-plan", map[string]any{
		"name": "web", "image": "x:1", "minReplicas": float64(5), "maxReplicas": float64(2),
	})
	w, _ := sc["warnings"].([]any)
	found := false
	for _, x := range w {
		if strings.Contains(fmt.Sprintf("%v", x), "minReplicas") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a min>max warning: %v", sc["warnings"])
	}
}

// --- cf-domain-register-plan (money path — must NEVER buy) --------------------

// registerPlanMock serves the read-only domain-check and FAILS the test if the buy
// endpoint (/registrar/registrations) is ever hit.
func registerPlanMock(t *testing.T, registrable bool, tier string) *cloudflare.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ⭐ The buy endpoint must never be called by a plan.
		if strings.Contains(r.URL.Path, "/registrar/registrations") {
			t.Errorf("register-plan must NOT hit the buy endpoint %q (%s)", r.URL.Path, r.Method)
			http.Error(w, "forbidden", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") && r.Method == http.MethodPost {
			if tier == "" {
				tier = "standard"
			}
			fmt.Fprintf(w, `{"success":true,"errors":[],"result":{"domains":[`+
				`{"name":"example.com","registrable":%t,"tier":%q,`+
				`"pricing":{"currency":"USD","registration_cost":"10.00","renewal_cost":"12.00"}}]}}`,
				registrable, tier)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 500)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(cloudflare.EnvToken, "cf-tok")
	t.Setenv(cloudflare.EnvAccountID, "acct1")
	t.Setenv(cloudflare.EnvBaseURL, srv.URL)
	cf, err := cloudflare.New()
	if err != nil || cf == nil {
		t.Fatalf("cloudflare.New: %v", err)
	}
	return cf
}

func callRegisterPlan(t *testing.T, cf *cloudflare.Client, rawArgs string) map[string]any {
	t.Helper()
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	server := buildServer(a, cf)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		t.Fatalf("bad args: %v", err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "cf-domain-register-plan", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("register-plan errored: %v", res.Content)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	return sc
}

func hasItem(sc map[string]any, key, substr string) bool {
	xs, _ := sc[key].([]any)
	for _, x := range xs {
		if strings.Contains(fmt.Sprintf("%v", x), substr) {
			return true
		}
	}
	return false
}

// The price is shown, money flags set, and NO buy endpoint is hit.
func TestRegisterPlan_ShowsPriceNoBuy(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","years":2,"acceptRegistrantAccuracy":true}`)
	if po, _ := sc["planOnly"].(bool); !po {
		t.Fatalf("planOnly = %v", sc["planOnly"])
	}
	if we, _ := sc["willExecute"].(bool); we {
		t.Fatalf("willExecute = %v, want false", sc["willExecute"])
	}
	if sm, _ := sc["spendsMoney"].(bool); !sm {
		t.Fatalf("spendsMoney = %v, want true", sc["spendsMoney"])
	}
	if ec, _ := sc["estimatedCost"].(float64); ec != 20.0 { // 10.00 x 2 years
		t.Fatalf("estimatedCost = %v, want 20", sc["estimatedCost"])
	}
	if _, ok := sc["success"]; ok {
		t.Fatalf("must not carry success: %v", sc)
	}
	if !hasItem(sc, "requiredConfirmations", "acceptNonRefundable") {
		t.Fatalf("missing acceptNonRefundable confirmation: %v", sc["requiredConfirmations"])
	}
}

// years > 10 → flagged as a missing prereq (real registrar rejects).
func TestRegisterPlan_YearsOutOfRange(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","years":99,"acceptRegistrantAccuracy":true}`)
	if !hasItem(sc, "missingPrereqs", "1..10") {
		t.Fatalf("years=99 should be flagged out of range: %v", sc["missingPrereqs"])
	}
}

// Registrant ack parity: ack is required ONLY when a registrant is supplied.
func TestRegisterPlan_RegistrantAccuracy(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")

	// No registrant → ack NOT required; account-default warning instead.
	noReg := callRegisterPlan(t, cf, `{"domain":"example.com"}`)
	if hasItem(noReg, "missingPrereqs", "acceptRegistrantAccuracy") {
		t.Fatalf("no registrant must NOT require acceptRegistrantAccuracy: %v", noReg["missingPrereqs"])
	}
	if !hasItem(noReg, "warnings", "account default") {
		t.Fatalf("no registrant should warn about the account default: %v", noReg["warnings"])
	}

	// With an inline registrant but no ack → flagged as a missing prereq.
	withReg := callRegisterPlan(t, cf,
		`{"domain":"example.com","registrant":{"name":"Test Co","email":"a@b.com"}}`)
	if !hasItem(withReg, "missingPrereqs", "acceptRegistrantAccuracy") {
		t.Fatalf("supplied registrant without ack should be flagged: %v", withReg["missingPrereqs"])
	}
	// ⭐ PII must never leak into the plan output (only the SOURCE is reported).
	blob := fmt.Sprintf("%v", withReg)
	for _, pii := range []string{"a@b.com", "Test Co"} {
		if strings.Contains(blob, pii) {
			t.Fatalf("registrant PII leaked into plan output: %s", blob)
		}
	}
}

// years < 0 → flagged invalid (not silently defaulted), and still NO buy.
func TestRegisterPlan_NegativeYears(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","years":-1}`)
	if !hasItem(sc, "missingPrereqs", "invalid") {
		t.Fatalf("years=-1 should be flagged invalid: %v", sc["missingPrereqs"])
	}
	if we, _ := sc["willExecute"].(bool); we {
		t.Fatalf("willExecute must stay false")
	}
}

// A PARTIAL registrant WITH ack=true is still flagged — the real buy validates the
// contact. The error names a field but echoes NO value.
func TestRegisterPlan_PartialRegistrantValidated(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf,
		`{"domain":"example.com","acceptRegistrantAccuracy":true,"registrant":{"name":"Jane Doe","email":"jane@example.org"}}`)
	if !hasItem(sc, "missingPrereqs", "registrant.") {
		t.Fatalf("partial registrant should be flagged by Validate(): %v", sc["missingPrereqs"])
	}
	blob := fmt.Sprintf("%v", sc)
	for _, pii := range []string{"Jane Doe", "jane@example.org"} {
		if strings.Contains(blob, pii) {
			t.Fatalf("registrant PII leaked: %s", blob)
		}
	}
}

// Premium tier without acceptPremium → flagged.
func TestRegisterPlan_PremiumTier(t *testing.T) {
	cf := registerPlanMock(t, true, "premium")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","acceptRegistrantAccuracy":true}`)
	if !hasItem(sc, "missingPrereqs", "acceptPremium") {
		t.Fatalf("premium tier without acceptPremium should be flagged: %v", sc["missingPrereqs"])
	}
}

// privacyMode is a STRING ("redaction") and is echoed in the side effects + summary.
func TestRegisterPlan_PrivacyModeString(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","privacyMode":"redaction","acceptRegistrantAccuracy":true}`)
	if !hasItem(sc, "sideEffectsIfExecuted", "redaction") {
		t.Fatalf("privacyMode string should appear in side effects: %v", sc["sideEffectsIfExecuted"])
	}
	if !hasItem(sc, "requestSummary", "privacyMode=redaction") {
		t.Fatalf("privacyMode should appear in requestSummary: %v", sc["requestSummary"])
	}
}

// requestSummary lists the exact (PII-free) request parameters.
func TestRegisterPlan_RequestSummary(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"EXAMPLE.com","years":3,"currency":"USD"}`)
	for _, want := range []string{"domain=example.com", "years=3", "currency=USD", "registrantSource="} {
		if !hasItem(sc, "requestSummary", want) {
			t.Fatalf("requestSummary missing %q: %v", want, sc["requestSummary"])
		}
	}
}

// Both *-plan tools are readOnly=true / destructive=false in tools/list.
func TestPlanTools_ReadOnlyAnnotation(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	server := buildServer(a, nil)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	lst, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"deployment-deploy-plan": false, "cf-domain-register-plan": false}
	for _, tool := range lst.Tools {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		want[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s must have readOnlyHint=true", tool.Name)
		}
		if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
			t.Fatalf("%s must NOT be destructive", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("%s not found in tools/list", name)
		}
	}
}

// autoRenew without acceptAutoRenew → warning + side effect.
func TestRegisterPlan_AutoRenew(t *testing.T) {
	cf := registerPlanMock(t, true, "standard")
	sc := callRegisterPlan(t, cf, `{"domain":"example.com","years":1,"autoRenew":true,"acceptRegistrantAccuracy":true}`)
	if !hasItem(sc, "sideEffectsIfExecuted", "AUTO-RENEW") {
		t.Fatalf("autoRenew side effect missing: %v", sc["sideEffectsIfExecuted"])
	}
	if !hasItem(sc, "warnings", "acceptAutoRenew") {
		t.Fatalf("autoRenew without acceptAutoRenew should warn: %v", sc["warnings"])
	}
}
