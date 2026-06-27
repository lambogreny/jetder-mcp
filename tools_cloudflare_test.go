package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

// connectWithCF wires a server (jetder + a CF client pointed at the given mock)
// to an in-process client.
func connectWithCF(t *testing.T, cfHandler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	// jetder adapter (endpoint unused by CF tools).
	jad := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")

	var cf *cloudflare.Client
	if cfHandler != nil {
		srv := httptest.NewServer(cfHandler)
		t.Cleanup(srv.Close)
		t.Setenv(cloudflare.EnvToken, "cf-tok")
		t.Setenv(cloudflare.EnvAccountID, "acct-1")
		t.Setenv(cloudflare.EnvBaseURL, srv.URL)
		c, err := cloudflare.New()
		if err != nil {
			t.Fatalf("cloudflare.New: %v", err)
		}
		cf = c
	}

	ctx := context.Background()
	server := buildServer(jad, cf)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func cfOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func TestCFTool_NotConfigured(t *testing.T) {
	cs := connectWithCF(t, nil) // cf nil
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "cf-domain-check", Arguments: map[string]any{"domains": []any{"x.com"}}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when CF not configured")
	}
	txt := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			txt += tc.Text
		}
	}
	if !strings.Contains(txt, "not configured") {
		t.Fatalf("expected 'not configured', got %q", txt)
	}
}

func TestCFTool_DomainCheck(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"errors":[],"result":{"domains":[{"name":"x.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":9.5,"renewal_cost":9.5}}]}}`))
	sc := callTool(t, cs, "cf-domain-check", map[string]any{"domains": []any{"x.com"}})
	doms, ok := sc["domains"].([]any)
	if !ok || len(doms) != 1 {
		t.Fatalf("domains = %v", sc["domains"])
	}
	if m := doms[0].(map[string]any); m["name"] != "x.com" || m["registrationCost"] != 9.5 {
		t.Fatalf("offer = %v", doms[0])
	}
}

func TestCFTool_DNSCreate_Idempotent(t *testing.T) {
	// GET list returns identical record → create reports alreadyExists, no POST.
	cs := connectWithCF(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"r1","type":"TXT","name":"_v.example.com","content":"tok"}]}`))
			return
		}
		t.Fatal("must not POST when identical record exists")
	})
	sc := callTool(t, cs, "cf-dns-create", map[string]any{
		"zoneId": "z1", "type": "TXT", "name": "_v.example.com", "content": "tok",
	})
	if sc["alreadyExists"] != true {
		t.Fatalf("expected alreadyExists=true, got %+v", sc)
	}
}

func TestCFTool_Register_GuardRejectViaToolLayer(t *testing.T) {
	// CF check returns a price; tool is called with WRONG confirmText → reject,
	// and no registrations POST should happen.
	cs := connectWithCF(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domains":[{"name":"example.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":10}}]}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/registrar/registrations") {
			t.Fatal("MONEY SPENT: register POST happened despite bad confirmText")
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "cf-domain-register",
		Arguments: map[string]any{
			"domain": "example.com", "confirmText": "buy example.com", // wrong
			"maxRegistrationCost": 12.0, "currency": "USD", "acceptNonRefundable": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected guard rejection (IsError) for wrong confirmText")
	}
}

func TestCFTool_Register_SuccessViaToolLayer(t *testing.T) {
	posted := false
	cs := connectWithCF(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domains":[{"name":"example.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":10}}]}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/registrar/registrations") {
			posted = true
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domain_name":"example.com","state":"in_progress","completed":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	})
	sc := callTool(t, cs, "cf-domain-register", map[string]any{
		"domain": "example.com", "confirmText": "REGISTER example.com",
		"maxRegistrationCost": 12.0, "currency": "USD", "acceptNonRefundable": true,
	})
	if !posted {
		t.Fatal("expected register POST on valid guard")
	}
	if sc["state"] != "in_progress" {
		t.Fatalf("state = %v", sc["state"])
	}
}

// TestCFTool_TokenNeverLeaksViaToolLayer: the CF API error echoes the token; the
// tool result content must NOT contain it (redaction boundary holds at the tool layer).
func TestCFTool_TokenNeverLeaksViaToolLayer(t *testing.T) {
	cs := connectWithCF(t, func(w http.ResponseWriter, _ *http.Request) {
		// token value used by connectWithCF is "cf-tok"; echo it in the error.
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"bad token Bearer cf-tok / cf-tok"}]}`))
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "cf-domain-check", Arguments: map[string]any{"domains": []any{"x.com"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError from CF api error")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	if strings.Contains(sb.String(), "cf-tok") {
		t.Fatalf("TOKEN LEAK via tool layer: %q", sb.String())
	}
}

// TestCFTool_Annotations: cf reads = readOnly, cf-dns-create + cf-domain-register = destructive.
func TestCFTool_Annotations(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	lt, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]struct {
		readOnly    bool
		destructive bool
	}{
		"cf-domain-search":       {readOnly: true},
		"cf-domain-check":        {readOnly: true},
		"cf-zone-lookup":         {readOnly: true},
		"cf-dns-list":            {readOnly: true},
		"cf-registration-status": {readOnly: true},
		"cf-dns-create":          {readOnly: false, destructive: true},
		"cf-domain-register":     {readOnly: false, destructive: true},
	}
	got := map[string]*mcp.ToolAnnotations{}
	for _, tool := range lt.Tools {
		got[tool.Name] = tool.Annotations
	}
	for name, w := range want {
		ann := got[name]
		if ann == nil {
			t.Errorf("%s: missing", name)
			continue
		}
		if ann.ReadOnlyHint != w.readOnly {
			t.Errorf("%s readOnly=%v want %v", name, ann.ReadOnlyHint, w.readOnly)
		}
		if !w.readOnly {
			if ann.DestructiveHint == nil || *ann.DestructiveHint != w.destructive {
				t.Errorf("%s destructive=%v want %v", name, ann.DestructiveHint, w.destructive)
			}
		}
	}
}
