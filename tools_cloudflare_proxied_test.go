package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

func boolPtr(b bool) *bool { return &b }

// --- resolveProxied truth table -----------------------------------------------

func TestResolveProxied_Matrix(t *testing.T) {
	cases := []struct {
		typ     string
		req     *bool
		want    bool
		wantErr bool
	}{
		{"A", nil, true, false},               // auto: proxiable → true
		{"AAAA", nil, true, false},            //
		{"CNAME", nil, true, false},           //
		{"cname", nil, true, false},           // case-insensitive
		{"TXT", nil, false, false},            // auto: non-proxiable → false
		{"MX", nil, false, false},             //
		{"A", boolPtr(false), false, false},   // explicit false honored
		{"A", boolPtr(true), true, false},     // explicit true on proxiable
		{"TXT", boolPtr(false), false, false}, // explicit false on TXT ok
		{"TXT", boolPtr(true), false, true},   // explicit true on TXT → error
		{"MX", boolPtr(true), false, true},    //
	}
	for _, tc := range cases {
		got, err := resolveProxied(tc.typ, tc.req)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s req=%v: err=%v wantErr=%v", tc.typ, tc.req, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Fatalf("%s req=%v: got %v want %v", tc.typ, tc.req, got, tc.want)
		}
	}
}

// cfProxiedMock captures the create POST body so we can assert the proxied value
// that actually went to Cloudflare (after auto-resolution).
func cfProxiedMock(t *testing.T, gotProxied *any, gotType *string) *cloudflare.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/zones") && strings.Contains(r.URL.RawQuery, "name="):
			// dns list preflight → empty (no conflicts)
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if gotProxied != nil {
				*gotProxied = body["proxied"]
			}
			if gotType != nil {
				*gotType, _ = body["type"].(string)
			}
			body["id"] = "rec1"
			b, _ := json.Marshal(body)
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":` + string(b) + `}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 500)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(cloudflare.EnvToken, "cf-tok")
	t.Setenv(cloudflare.EnvBaseURL, srv.URL)
	cf, err := cloudflare.New()
	if err != nil || cf == nil {
		t.Fatalf("cloudflare.New: %v", err)
	}
	return cf
}

// callCFTool invokes a cf-* tool over an in-memory MCP session with the given raw
// JSON arguments (so we test the real schema decode, incl. omitted vs explicit).
func callCFTool(t *testing.T, cf *cloudflare.Client, name, rawArgs string) map[string]any {
	t.Helper()
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	ctx := context.Background()
	server := buildServer(a, cf)
	st, ct := mcp.NewInMemoryTransports()
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
		t.Fatalf("bad rawArgs: %v", err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	return sc
}

// ⭐ The crucial test: through the real MCP JSON decode, an OMITTED proxied (auto)
// must differ from an explicit false. zoneId is passed so no zone lookup is needed.
func TestCFDNSCreate_OmittedVsFalse_E2E(t *testing.T) {
	// (a) omitted proxied on an A record → AUTO → proxied=true at the API.
	var gotA any
	cfA := cfProxiedMock(t, &gotA, nil)
	callCFTool(t, cfA, "cf-dns-create",
		`{"zoneId":"z1","type":"A","name":"a.example.com","content":"1.2.3.4"}`)
	if gotA != true {
		t.Fatalf("omitted proxied on A should auto → true, got %v", gotA)
	}

	// (b) explicit false on an A record → DNS-only → proxied=false at the API.
	var gotB any
	cfB := cfProxiedMock(t, &gotB, nil)
	callCFTool(t, cfB, "cf-dns-create",
		`{"zoneId":"z1","type":"A","name":"a.example.com","content":"1.2.3.4","proxied":false}`)
	if gotB != false {
		t.Fatalf("explicit proxied=false on A should stay false, got %v", gotB)
	}
}

// TXT auto → DNS-only at the API (proxied=false), never proxied.
func TestCFDNSCreate_TXTauto_NotProxied_E2E(t *testing.T) {
	var got any
	cf := cfProxiedMock(t, &got, nil)
	callCFTool(t, cf, "cf-dns-create",
		`{"zoneId":"z1","type":"TXT","name":"_v.example.com","content":"verify=123"}`)
	if got != false {
		t.Fatalf("TXT auto should be DNS-only (false), got %v", got)
	}
}

// Explicit proxied=true on TXT → tool error, NO POST to Cloudflare.
func TestCFDNSCreate_TXTexplicitProxied_Errors_NoPost(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(cloudflare.EnvToken, "cf-tok")
	t.Setenv(cloudflare.EnvBaseURL, srv.URL)
	cf, _ := cloudflare.New()

	ctx := context.Background()
	server := buildServer(a, cf)
	stt, ctt := mcp.NewInMemoryTransports()
	_, _ = server.Connect(ctx, stt, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, _ := client.Connect(ctx, ctt, nil)
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "cf-dns-create", Arguments: map[string]any{
		"zoneId": "z1", "type": "TXT", "name": "_v.example.com", "content": "x", "proxied": true,
	}})
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error for proxied=true on TXT")
	}
	if posted {
		t.Fatal("must NOT POST when proxied=true on a non-proxiable type")
	}
}
