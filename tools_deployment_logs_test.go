package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// logTestServer is one httptest server that answers BOTH the jetder API
// (deployment.get) and the log GET. deployment.get returns a logUrl pointing back
// at this same server's /logs path (so allowedLogHost matches the API host). The
// /logs handler records whether an Authorization header was sent and returns the
// supplied body. Pointing JETDER_ENDPOINT at this server wires it all together.
func logTestServer(t *testing.T, logBody string, logStatus int, sawAuth *bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			if sawAuth != nil && r.Header.Get("Authorization") != "" {
				*sawAuth = true
			}
			if logStatus == 0 {
				logStatus = http.StatusOK
			}
			w.WriteHeader(logStatus)
			_, _ = w.Write([]byte(logBody))
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			w.Header().Set("Content-Type", "application/json")
			logURL := srv.URL + "/logs?t=SECRET-JWT-abc123"
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","logUrl":%q}}`, logURL)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func logsAdapter(t *testing.T, srv *httptest.Server) *jetder.Adapter {
	t.Helper()
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

func callLogs(t *testing.T, a *jetder.Adapter, args map[string]any) map[string]any {
	t.Helper()
	cs := connectInMemory(t, a)
	return callTool(t, cs, "deployment-logs", args)
}

// --- success + tail -----------------------------------------------------------

func TestDeploymentLogs_Success(t *testing.T) {
	sawAuth := false
	srv := logTestServer(t, "line1\nline2\nline3\n", 200, &sawAuth)
	a := logsAdapter(t, srv)

	sc := callLogs(t, a, map[string]any{"name": "web"})
	if sc["logs"] != "line1\nline2\nline3" {
		t.Fatalf("logs = %q", sc["logs"])
	}
	if sc["source"] != "logUrl" {
		t.Fatalf("source = %v, want logUrl", sc["source"])
	}
	if c, _ := sc["lineCount"].(float64); c != 3 {
		t.Fatalf("lineCount = %v, want 3", sc["lineCount"])
	}
	// ⭐ NO Authorization header on the log GET.
	if sawAuth {
		t.Fatal("log GET must NOT send an Authorization header")
	}
	// The logUrl (and its JWT) must never appear in output.
	blob := fmt.Sprintf("%v", sc)
	for _, secret := range []string{"SECRET-JWT-abc123", "/logs?t="} {
		if strings.Contains(blob, secret) {
			t.Fatalf("output leaked the log URL/JWT: %s", blob)
		}
	}
}

func TestDeploymentLogs_Tail(t *testing.T) {
	srv := logTestServer(t, "a\nb\nc\nd\ne\n", 200, nil)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web", "tailLines": float64(2)})
	if sc["logs"] != "d\ne" {
		t.Fatalf("tail logs = %q, want d\\ne", sc["logs"])
	}
}

// --- no log url ---------------------------------------------------------------

func TestDeploymentLogs_NoLogURL(t *testing.T) {
	// deployment.get returns an empty logUrl.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"name":"web","logUrl":""}}`))
	}))
	t.Cleanup(srv.Close)
	a := logsAdapter(t, srv)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deployment-logs", Arguments: map[string]any{"name": "web"}})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error when no log URL")
	}
}

// --- 403 from log server → redacted error (no JWT/URL leak) --------------------

func TestDeploymentLogs_UpstreamError_Redacted(t *testing.T) {
	srv := logTestServer(t, "forbidden", http.StatusForbidden, nil)
	a := logsAdapter(t, srv)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deployment-logs", Arguments: map[string]any{"name": "web"}})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error on 403")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	msg := sb.String()
	if strings.Contains(msg, "SECRET-JWT-abc123") || strings.Contains(msg, "/logs?t=") {
		t.Fatalf("403 error leaked the log URL/JWT: %s", msg)
	}
}

// --- oversize → truncated -----------------------------------------------------

func TestDeploymentLogs_Truncated(t *testing.T) {
	big := strings.Repeat("x", int(logsMaxBytesDefault)+5000) + "\n"
	srv := logTestServer(t, big, 200, nil)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	if tr, _ := sc["truncated"].(bool); !tr {
		t.Fatalf("expected truncated=true for an oversize log")
	}
}

// --- redaction: a fake secret in the log is masked ----------------------------

func TestDeploymentLogs_RedactsSecretsInLog(t *testing.T) {
	body := strings.Join([]string{
		"starting up",
		"Authorization: Bearer abcdef123456",
		"my github token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"DB_PASSWORD=hunter2supersecret",
		"echoed our token ZZ-secret-tokenval-XYZ123 oops",
		"done",
	}, "\n") + "\n"
	srv := logTestServer(t, body, 200, nil)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	logs, _ := sc["logs"].(string)
	for _, secret := range []string{"abcdef123456", "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "hunter2supersecret", "ZZ-secret-tokenval-XYZ123"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log sanitizer leaked %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "[REDACTED]") {
		t.Fatalf("expected redaction markers:\n%s", logs)
	}
	// non-secret lines survive.
	if !strings.Contains(logs, "starting up") || !strings.Contains(logs, "done") {
		t.Fatalf("over-redacted (lost normal lines):\n%s", logs)
	}
}

// --- SSRF: a logUrl on an unexpected host is rejected -------------------------

func TestFetchURL_SSRF_Reject(t *testing.T) {
	srv := logTestServer(t, "ok", 200, nil)
	a := logsAdapter(t, srv) // apiHost = srv host
	// A different host (not the API host, not *.jetder.com) must be refused.
	_, err := a.FetchLogSnapshot(context.Background(), "https://evil.example.com/logs?t=x", 1024, 100)
	if err == nil {
		t.Fatal("expected SSRF rejection for an unexpected host")
	}
	// And a non-http scheme.
	if _, err := a.FetchLogSnapshot(context.Background(), "file:///etc/passwd", 1024, 100); err == nil {
		t.Fatal("expected rejection for non-http scheme")
	}
}

// --- FetchURL: a *.jetder.com host is allowed (host suffix) -------------------

func TestFetchURL_AllowsJetderHost(t *testing.T) {
	srv := logTestServer(t, "ok", 200, nil)
	a := logsAdapter(t, srv)
	if !a.AllowedLogHostForTest("log.cluster-1.jetder.com") {
		t.Fatal("*.jetder.com host should be allowed")
	}
	if a.AllowedLogHostForTest("notjetder.com") {
		t.Fatal("notjetder.com must not be allowed")
	}
}

// JSON-style structured logs: "password":"x" / "token":"x" / "api_key":"x" must be
// redacted too (the quote sits between key and colon).
func TestDeploymentLogs_RedactsJSONSecrets(t *testing.T) {
	body := strings.Join([]string{
		`{"level":"info","msg":"starting"}`,
		`{"password":"hunter2supersecret"}`,
		`{"token":"tok_AAAAAAAAAAAAAAAAAAAA"}`,
		`{"api_key":"ak_BBBBBBBBBBBBBBBBBBBB"}`,
		`{"authorization":"Bearer ccccccccccccdddddddddddd"}`,
		`{"level":"info","msg":"done"}`,
	}, "\n") + "\n"
	srv := logTestServer(t, body, 200, nil)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	logs, _ := sc["logs"].(string)
	for _, secret := range []string{"hunter2supersecret", "tok_AAAAAAAAAAAAAAAAAAAA", "ak_BBBBBBBBBBBBBBBBBBBB", "ccccccccccccdddddddddddd"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("JSON sanitizer leaked %q:\n%s", secret, logs)
		}
	}
	// non-secret JSON lines survive.
	if !strings.Contains(logs, "starting") || !strings.Contains(logs, "done") {
		t.Fatalf("over-redacted JSON logs:\n%s", logs)
	}
}

// BLOCKING 1: even if the log BODY echoes the full logUrl / its JWT, the success
// output must not contain it.
func TestDeploymentLogs_BodyEchoesURL_Redacted(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			// The app foolishly logged its own request URL + token.
			logURL := srv.URL + "/logs?t=SECRET-JWT-abc123"
			fmt.Fprintf(w, "hi\nfetched from %s\nraw token SECRET-JWT-abc123\nbye\n", logURL)
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","logUrl":%q}}`, srv.URL+"/logs?t=SECRET-JWT-abc123")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	blob := fmt.Sprintf("%v", sc)
	for _, secret := range []string{"SECRET-JWT-abc123", "/logs?t=", srv.URL + "/logs"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("success output leaked %q from the body:\n%s", secret, blob)
		}
	}
	if logs, _ := sc["logs"].(string); !strings.Contains(logs, "hi") || !strings.Contains(logs, "bye") {
		t.Fatalf("normal lines lost: %q", logs)
	}
}

// BLOCKING 2: plaintext http to a jetder host is refused; https is fine.
func TestFetchURL_RequiresHTTPS_ForJetderHost(t *testing.T) {
	srv := logTestServer(t, "ok", 200, nil)
	a := logsAdapter(t, srv) // JETDER_ENDPOINT = srv (http httptest) → same-host http allowed
	// http to a real *.jetder.com host (NOT the http endpoint host) → reject.
	if _, err := a.FetchLogSnapshot(context.Background(), "http://log.cluster-1.jetder.com/logs?t=x", 1024, 100); err == nil {
		t.Fatal("plaintext http to a jetder host must be rejected")
	}
	// https to the same host → allowed past the scheme guard (will fail at dial, but
	// not with our scheme error).
	_, err := a.FetchLogSnapshot(context.Background(), "https://log.cluster-1.jetder.com/logs?t=x", 1024, 100)
	if err != nil && strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("https must pass the scheme guard, got %v", err)
	}
}

// GAP 3: maxBytes is configurable; <=0 → default; oversize maxBytes is clamped but
// still bounds the read.
func TestDeploymentLogs_MaxBytesClamp(t *testing.T) {
	// A small maxBytes truncates a modest body.
	srv := logTestServer(t, strings.Repeat("y", 5000)+"\n", 200, nil)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web", "maxBytes": float64(100)})
	if tr, _ := sc["truncated"].(bool); !tr {
		t.Fatalf("expected truncated=true with maxBytes=100")
	}
	// An absurd maxBytes is clamped to the hard max (no panic, still works).
	sc2 := callLogs(t, a, map[string]any{"name": "web", "maxBytes": float64(99999999999)})
	if sc2["source"] != "logUrl" {
		t.Fatalf("clamped maxBytes should still work: %v", sc2)
	}
}

// GAP 4: revision is passed through to deployment.get.
func TestDeploymentLogs_RevisionPassed(t *testing.T) {
	gotRevision := -1
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte("ok\n"))
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			var body struct {
				Revision int `json:"revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotRevision = body.Revision
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","logUrl":%q}}`, srv.URL+"/logs?t=x")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	a := logsAdapter(t, srv)
	callLogs(t, a, map[string]any{"name": "web", "revision": float64(7)})
	if gotRevision != 7 {
		t.Fatalf("revision = %d, want 7 (not passed to deployment.get)", gotRevision)
	}
}

// --- SSE bounded snapshot tests ----------------------------------------------

// sseServer answers deployment.get + an SSE /logs endpoint that emits the given raw
// SSE text, then (if block) hangs without closing — simulating the real log server.
func sseServer(t *testing.T, sse string, block bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(sse))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if block {
				<-r.Context().Done() // hang until the client closes the connection.
			}
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","logUrl":%q}}`, srv.URL+"/logs?t=SECRET-JWT-abc123")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A never-closing SSE stream must return a bounded snapshot quickly (idle timeout),
// parsing data: {json} into clean lines.
func TestDeploymentLogs_SSE_NeverClosing_ReturnsFast(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(80*time.Millisecond, 3*time.Second)
	defer restore()

	sse := `data: {"timestamp":"2026-01-02T03:04:05Z","log":"npm WARN config"}` + "\n\n" +
		`data: {"timestamp":"2026-01-02T03:04:06Z","log":"server started"}` + "\n\n"
	srv := sseServer(t, sse, true)
	a := logsAdapter(t, srv)

	start := time.Now()
	sc := callLogs(t, a, map[string]any{"name": "web"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("never-closing stream took too long (%v) — idle timeout not working", elapsed)
	}
	if tr, _ := sc["streamSnapshot"].(bool); !tr {
		t.Fatalf("streamSnapshot should be true for SSE: %v", sc)
	}
	logs, _ := sc["logs"].(string)
	if !strings.Contains(logs, "npm WARN config") || !strings.Contains(logs, "server started") {
		t.Fatalf("SSE log not parsed: %q", logs)
	}
	if !strings.Contains(logs, "[2026-01-02T03:04:05Z]") {
		t.Fatalf("expected timestamp prefix: %q", logs)
	}
	if strings.Contains(logs, "data:") || strings.Contains(logs, `"log":`) {
		t.Fatalf("raw SSE/JSON framing leaked into output: %q", logs)
	}
}

// Invalid-JSON SSE data falls back to the raw payload (still sanitized).
func TestDeploymentLogs_SSE_InvalidJSON_Fallback(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(80*time.Millisecond, 3*time.Second)
	defer restore()
	sse := "data: this is not json\n\ndata: plain line two\n\n"
	srv := sseServer(t, sse, true)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	logs, _ := sc["logs"].(string)
	if !strings.Contains(logs, "this is not json") || !strings.Contains(logs, "plain line two") {
		t.Fatalf("invalid-JSON fallback failed: %q", logs)
	}
}

// A secret inside the JSON .log field must be redacted (after unwrap).
func TestDeploymentLogs_SSE_RedactsSecretInJSONLog(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(80*time.Millisecond, 3*time.Second)
	defer restore()
	sse := `data: {"log":"connecting with password=hunter2supersecret"}` + "\n\n" +
		`data: {"log":"token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}` + "\n\n" +
		`data: {"log":"normal line"}` + "\n\n"
	srv := sseServer(t, sse, true)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	logs, _ := sc["logs"].(string)
	for _, secret := range []string{"hunter2supersecret", "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("SSE JSON sanitizer leaked %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "normal line") {
		t.Fatalf("over-redacted: %q", logs)
	}
}

// SSE allows multiple data: lines per event — they must be joined (not split into
// separate log lines) and dispatched on the blank line.
func TestDeploymentLogs_SSE_MultiLineData(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(80*time.Millisecond, 3*time.Second)
	defer restore()
	// One event with two data: lines, then another single-line event.
	sse := "data: line A part 1\ndata: line A part 2\n\ndata: second event\n\n"
	srv := sseServer(t, sse, true)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web"})
	logs, _ := sc["logs"].(string)
	// The two data: lines of event 1 join into ONE log line (joined with \n).
	if !strings.Contains(logs, "line A part 1\nline A part 2") {
		t.Fatalf("multi-line data not joined into one event: %q", logs)
	}
	if !strings.Contains(logs, "second event") {
		t.Fatalf("second event missing: %q", logs)
	}
	// lineCount: event1 (multi-line, counts as 1 parsed event but its joined text has
	// an embedded newline) + event2. tailLinesSlice splits the joined text on \n when
	// rendering, so just assert both payloads present and no raw "data:" framing.
	if strings.Contains(logs, "data:") {
		t.Fatalf("raw SSE framing leaked: %q", logs)
	}
}

// A never-closing stream must NOT leak the log-reader producer goroutine after the
// tool returns. We count goroutines parked specifically in readBoundedLog (the
// producer) — other harness goroutines (httptest conns, in-memory MCP sessions) are
// expected to linger in-test and aren't what this guards.
func TestDeploymentLogs_SSE_NoGoroutineLeak(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	// Emit many events fast (fills the channel) then block forever — the consumer
	// stops at maxEvents/idle while the producer is mid-send.
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "data: {\"log\":\"line %d\"}\n\n", i)
	}
	srv := sseServer(t, b.String(), true)
	a := logsAdapter(t, srv)

	for i := 0; i < 5; i++ {
		_ = callLogs(t, a, map[string]any{"name": "web", "tailLines": float64(5)})
	}
	// Give any leaked producers a chance to (fail to) exit, then sample stacks.
	time.Sleep(400 * time.Millisecond)
	runtime.GC()
	buf := make([]byte, 1<<20)
	stacks := string(buf[:runtime.Stack(buf, true)])
	if n := strings.Count(stacks, "jetder.readBoundedLog"); n > 0 {
		t.Fatalf("producer goroutine leak: %d readBoundedLog goroutine(s) still alive", n)
	}
}

// maxEvents caps the number of parsed lines and sets truncated.
func TestDeploymentLogs_SSE_MaxEventsTruncate(t *testing.T) {
	restore := jetder.SetLogTimeoutsForTest(80*time.Millisecond, 3*time.Second)
	defer restore()
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "data: {\"log\":\"line %d\"}\n\n", i)
	}
	srv := sseServer(t, b.String(), true)
	a := logsAdapter(t, srv)
	sc := callLogs(t, a, map[string]any{"name": "web", "tailLines": float64(10)})
	if tr, _ := sc["truncated"].(bool); !tr {
		t.Fatalf("expected truncated=true with 500 events")
	}
	if c, _ := sc["lineCount"].(float64); c != 10 {
		t.Fatalf("tail should cap to 10 lines, got %v", sc["lineCount"])
	}
}
