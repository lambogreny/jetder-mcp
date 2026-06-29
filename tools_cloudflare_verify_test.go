package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

// cfVerifyClient builds a CF client whose API server returns the given /zones
// response (status + body). accountID controls CLOUDFLARE_ACCOUNT_ID.
func cfVerifyClient(t *testing.T, zonesStatus int, zonesBody, accountID string) *cloudflare.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/zones") {
			w.WriteHeader(zonesStatus)
			_, _ = w.Write([]byte(zonesBody))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(cloudflare.EnvToken, "cf-tok-SECRETvalue")
	t.Setenv(cloudflare.EnvBaseURL, srv.URL)
	if accountID != "" {
		t.Setenv(cloudflare.EnvAccountID, accountID)
	} else {
		t.Setenv(cloudflare.EnvAccountID, "")
	}
	cf, err := cloudflare.New()
	if err != nil || cf == nil {
		t.Fatalf("cloudflare.New: %v", err)
	}
	return cf
}

// nil cf → not configured + restart hint, no error.
func TestCFVerify_NotConfigured(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "cf-verify", map[string]any{})
	if cfg, _ := sc["configured"].(bool); cfg {
		t.Fatalf("configured = %v, want false", sc["configured"])
	}
	if conn, _ := sc["connected"].(bool); conn {
		t.Fatalf("connected = %v, want false", sc["connected"])
	}
	if !strings.Contains(fmt.Sprintf("%v", sc["remediation"]), "RESTART") &&
		!strings.Contains(fmt.Sprintf("%v", sc["remediation"]), "restart") {
		t.Fatalf("remediation should mention restart: %v", sc["remediation"])
	}
}

// success: token works → connected, zoneCount, capabilities; with account id the
// Registrar capability appears.
func TestCFVerify_ConnectedWithAccount(t *testing.T) {
	body := `{"success":true,"errors":[],"result":[{"id":"z1","name":"example.com"}],"result_info":{"page":1,"total_pages":1}}`
	cf := cfVerifyClient(t, 200, body, "acct1")
	sc := callCFTool(t, cf, "cf-verify", `{}`)
	if conn, _ := sc["connected"].(bool); !conn {
		t.Fatalf("connected = %v, want true", sc["connected"])
	}
	if zc, _ := sc["zoneCount"].(float64); zc != 1 {
		t.Fatalf("zoneCount = %v, want 1", sc["zoneCount"])
	}
	if ac, _ := sc["accountConfigured"].(bool); !ac {
		t.Fatalf("accountConfigured = %v, want true", sc["accountConfigured"])
	}
	caps := fmt.Sprintf("%v", sc["capabilities"])
	if !strings.Contains(caps, "Registrar") {
		t.Fatalf("with an account id, Registrar capability should appear: %v", sc["capabilities"])
	}
	// No token ever appears.
	if strings.Contains(fmt.Sprintf("%v", sc), "cf-tok-SECRETvalue") {
		t.Fatalf("cf-verify leaked the token: %v", sc)
	}
}

// connected but no account id → DNS works, Registrar flagged unavailable.
func TestCFVerify_ConnectedNoAccount(t *testing.T) {
	body := `{"success":true,"errors":[],"result":[],"result_info":{"page":1,"total_pages":1}}`
	cf := cfVerifyClient(t, 200, body, "")
	sc := callCFTool(t, cf, "cf-verify", `{}`)
	if conn, _ := sc["connected"].(bool); !conn {
		t.Fatalf("connected = %v, want true", sc["connected"])
	}
	if ac, _ := sc["accountConfigured"].(bool); ac {
		t.Fatalf("accountConfigured = %v, want false", sc["accountConfigured"])
	}
	if !strings.Contains(fmt.Sprintf("%v", sc["remediation"]), "Registrar") {
		t.Fatalf("should hint about the Registrar/account id: %v", sc["remediation"])
	}
	if strings.Contains(fmt.Sprintf("%v", sc), "Registrar (cf-domain") {
		t.Fatalf("Registrar capability should NOT appear without an account id: %v", sc["capabilities"])
	}
}

// API error (bad token) → connected=false, redacted remediation, NO token leak.
func TestCFVerify_APIErrorRedacted(t *testing.T) {
	body := `{"success":false,"errors":[{"code":1000,"message":"Invalid API token"}],"result":null}`
	cf := cfVerifyClient(t, 403, body, "acct1")
	sc := callCFTool(t, cf, "cf-verify", `{}`)
	if conn, _ := sc["connected"].(bool); conn {
		t.Fatalf("connected = %v, want false on API error", sc["connected"])
	}
	blob := fmt.Sprintf("%v", sc)
	if strings.Contains(blob, "cf-tok-SECRETvalue") {
		t.Fatalf("cf-verify leaked the token in the error path: %v", sc)
	}
	if !strings.Contains(fmt.Sprintf("%v", sc["remediation"]), "Zone:Read") {
		t.Fatalf("error remediation should mention the needed scope: %v", sc["remediation"])
	}
}

// cf-verify is readOnly in tools/list.
func TestCFVerify_ReadOnlyAnnotation(t *testing.T) {
	cf := cfVerifyClient(t, 200, `{"success":true,"errors":[],"result":[]}`, "acct1")
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemoryCF(t, a, cf)
	lst, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range lst.Tools {
		if tool.Name == "cf-verify" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("cf-verify must be readOnly")
			}
			return
		}
	}
	t.Fatal("cf-verify not in tools/list")
}

// The connect-cloudflare prompt renders WITHOUT any secret, mentions restart +
// cf-verify + the unlocked tools, and takes no token argument.
func TestConnectCloudflarePrompt_NoSecret(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)

	// The prompt must not declare any secret-bearing argument.
	lp, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	var found bool
	for _, p := range lp.Prompts {
		if p.Name != "connect-cloudflare" {
			continue
		}
		found = true
		for _, arg := range p.Arguments {
			low := strings.ToLower(arg.Name)
			if strings.Contains(low, "token") || strings.Contains(low, "secret") || strings.Contains(low, "key") {
				t.Fatalf("connect-cloudflare must not take a secret argument: %q", arg.Name)
			}
		}
	}
	if !found {
		t.Fatal("connect-cloudflare prompt not registered")
	}

	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "connect-cloudflare"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	var text string
	for _, m := range res.Messages {
		if tc, ok := m.Content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	for _, want := range []string{"RESTART", "cf-verify", "CLOUDFLARE_API_TOKEN", "<your-cloudflare-token>", "point-a-domain"} {
		if !strings.Contains(text, want) {
			t.Fatalf("connect-cloudflare prompt missing %q", want)
		}
	}
	// No real-looking token literal.
	for _, leak := range []string{"Bearer ", "ghp_", "cf-tok-"} {
		if strings.Contains(text, leak) {
			t.Fatalf("connect-cloudflare prompt contains a token-like literal %q", leak)
		}
	}

	// Copy-paste safety: the `claude mcp add` command must be a single continued block
	// that REACHES `--scope user jetder-mcp -- jetder-mcp`, with no inline '#' comment
	// cutting the line-continuation, and every -e KEY=value quoted.
	assertCommandSafe(t, text)
}

// assertCommandSafe checks the claude-mcp-add command renders as one runnable block.
func assertCommandSafe(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, "claude mcp add") {
		t.Fatal("prompt should show the claude mcp add command")
	}
	if !strings.Contains(text, "--scope user jetder-mcp -- jetder-mcp") {
		t.Fatal("the command must reach the --scope ... -- jetder-mcp tail")
	}
	// Walk the command's continued lines (between `claude mcp add` and the --scope tail)
	// and assert none ends with a '#' comment that would break the '\' continuation,
	// and each -e value is quoted.
	lines := strings.Split(text, "\n")
	in := false
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if strings.HasPrefix(s, "claude mcp add") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.Contains(s, "--scope user jetder-mcp -- jetder-mcp") {
			break // reached the tail intact
		}
		// Continued lines must end with a backslash...
		if !strings.HasSuffix(s, "\\") {
			t.Fatalf("command line is not continued (missing trailing '\\'): %q", s)
		}
		// ...and must NOT contain an inline '#' comment (it would swallow the '\').
		if strings.Contains(s, "#") {
			t.Fatalf("command line has an inline '#' comment that breaks continuation: %q", s)
		}
		// Each -e arg's KEY=value must be quoted (so <placeholder> isn't a redirection).
		if strings.HasPrefix(s, "-e ") {
			arg := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, "-e ")), "\\")
			arg = strings.TrimSpace(arg)
			if !strings.HasPrefix(arg, "\"") || !strings.HasSuffix(arg, "\"") {
				t.Fatalf("-e arg must be quoted to be copy-paste safe: %q", arg)
			}
		}
	}
}
