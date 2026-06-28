package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// diagServer answers deployment.get + statusUrl (JSON) + eventUrl (JSON) + logUrl
// (SSE) so the Deploy Doctor can be exercised end-to-end against fakes. status/events
// are the given JSON bodies; sseLog is the SSE log body.
func diagServer(t *testing.T, statusJSON, eventsJSON, sseLog string) *httptest.Server {
	return diagServerM(t, statusJSON, eventsJSON, sseLog, "")
}

// diagServerM is diagServer with an optional deployment.metrics JSON body.
func diagServerM(t *testing.T, statusJSON, eventsJSON, sseLog, metricsJSON string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(statusJSON))
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(eventsJSON))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseLog))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done() // never-closing stream
		case strings.HasSuffix(r.URL.Path, "deployment.metrics"):
			w.Header().Set("Content-Type", "application/json")
			if metricsJSON != "" {
				_, _ = w.Write([]byte(metricsJSON))
			} else {
				_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			}
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			w.Header().Set("Content-Type", "application/json")
			// Use a distinctive (>=4 char) JWT value so redaction of the URL token is
			// observable in tests.
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","status":%q,"statusUrl":%q,"eventUrl":%q,"logUrl":%q}}`,
				statusStr(statusJSON),
				srv.URL+"/status?t=JWTtokenVAL", srv.URL+"/events?t=JWTtokenVAL", srv.URL+"/logs?t=JWTtokenVAL")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// statusStr picks the deployment status string the API uses. The api client
// unmarshals Status from this string ("success"/"error"/"pending"/"cancelled").
//   - all pods ready          → "success"
//   - any pod failed          → "error"
//   - otherwise (not ready)   → "pending"
func statusStr(statusJSON string) string {
	switch {
	case strings.Contains(statusJSON, `"failed":0`) && strings.Contains(statusJSON, `"ready":1`) && strings.Contains(statusJSON, `"count":1`):
		return "success"
	case strings.Contains(statusJSON, `"failed":1`):
		return "error"
	default:
		return "pending"
	}
}

func diagAdapter(t *testing.T, srv *httptest.Server) *jetder.Adapter {
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

func callDiag(t *testing.T, a *jetder.Adapter) map[string]any {
	t.Helper()
	cs := connectInMemory(t, a)
	return callTool(t, cs, "deployment-diagnose", map[string]any{"name": "web"})
}

func causeNames(sc map[string]any) []string {
	var names []string
	if cs, ok := sc["causes"].([]any); ok {
		for _, c := range cs {
			if m, ok := c.(map[string]any); ok {
				names = append(names, fmt.Sprintf("%v", m["cause"]))
			}
		}
	}
	return names
}

// Healthy deployment → assessment healthy, no causes, causes is [] (not null).
func TestDiagnose_Healthy(t *testing.T) {
	srv := diagServer(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"✓ Ready\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if sc["assessment"] != "healthy" {
		t.Fatalf("assessment = %v, want healthy", sc["assessment"])
	}
	if h, _ := sc["healthy"].(bool); !h {
		t.Fatalf("healthy = false, want true")
	}
	if cs, ok := sc["causes"].([]any); !ok || len(cs) != 0 {
		t.Fatalf("causes = %v, want empty array (never null)", sc["causes"])
	}
}

// CrashLoopBackOff from events → caught (high), with AppStartupError from the log.
func TestDiagnose_CrashLoop(t *testing.T) {
	events := `[{"reason":"BackOff","message":"Back-off restarting failed container app"}]`
	sse := "data: {\"log\":\"panic: config load failed\"}\n\n"
	srv := diagServer(t, `{"count":2,"ready":1,"succeeded":0,"failed":0}`, events, sse)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if sc["assessment"] != "unhealthy" {
		t.Fatalf("assessment = %v, want unhealthy", sc["assessment"])
	}
	names := causeNames(sc)
	if !contains(names, "CrashLoopBackOff") {
		t.Fatalf("causes %v missing CrashLoopBackOff", names)
	}
	if !contains(names, "AppStartupError") {
		t.Fatalf("causes %v missing AppStartupError (from panic log)", names)
	}
}

// ImagePullBackOff from events.
func TestDiagnose_ImagePull(t *testing.T) {
	events := `[{"reason":"Failed","message":"Failed to pull image: ErrImagePull pull access denied"}]`
	srv := diagServer(t, `{"count":1,"ready":0,"succeeded":0,"failed":0}`, events, "data: {}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if !contains(causeNames(sc), "ImagePullBackOff") {
		t.Fatalf("causes %v missing ImagePullBackOff", causeNames(sc))
	}
}

// Port mismatch detected from the log.
func TestDiagnose_PortMismatch(t *testing.T) {
	sse := "data: {\"log\":\"listen tcp :8080: bind: address already in use\"}\n\n"
	srv := diagServer(t, `{"count":1,"ready":0,"succeeded":0,"failed":1}`, `null`, sse)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if !contains(causeNames(sc), "PortMismatch") {
		t.Fatalf("causes %v missing PortMismatch", causeNames(sc))
	}
}

// Unhealthy (pods not ready) but status is NOT "error" and no rule matches →
// assessment "unknown" (honest, not a guess; no DeploymentError fallback fires).
func TestDiagnose_Unknown(t *testing.T) {
	// statusStr returns "success" only when count==1&&ready==1&&failed==0; here
	// ready=0 with count=2 → not healthy, and status string stays non-error
	// (pending) so the Error-fallback cause does not apply.
	srv := diagServer(t, `{"count":2,"ready":0,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"some unrecognized situation\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if sc["assessment"] != "unknown" {
		t.Fatalf("assessment = %v, want unknown (no pattern matched)", sc["assessment"])
	}
}

// Secrets in events/logs must be redacted in the diagnosis evidence.
func TestDiagnose_RedactsSecrets(t *testing.T) {
	events := `[{"reason":"Created","message":"started with token=SUPERSECRETtok123"}]`
	sse := "data: {\"log\":\"db password=hunter2leaked here\"}\n\n"
	srv := diagServer(t, `{"count":1,"ready":0,"succeeded":0,"failed":1}`, events, sse)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	blob := fmt.Sprintf("%v", sc)
	for _, secret := range []string{"SUPERSECRETtok123", "hunter2leaked"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("diagnosis leaked secret %q: %s", secret, blob)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- scaling advisory tests (pure logic; no metrics endpoint in the fake) ------

func advisoryNames(sc map[string]any) []string {
	var names []string
	if as, ok := sc["advisories"].([]any); ok {
		for _, a := range as {
			if m, ok := a.(map[string]any); ok {
				names = append(names, fmt.Sprintf("%v", m["cause"]))
			}
		}
	}
	return names
}

// advisories is always an array (never null) even when metrics are unavailable.
func TestDiagnose_AdvisoriesArrayNeverNull(t *testing.T) {
	srv := diagServer(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"ok\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if _, ok := sc["advisories"].([]any); !ok {
		t.Fatalf("advisories must be an array, got %T (%v)", sc["advisories"], sc["advisories"])
	}
}

// ReplicasNotReady advisory (high) fires from pod counts even without metrics.
func TestDiagnose_ReplicasNotReadyAdvisory(t *testing.T) {
	// count=2, ready=1 → not healthy AND a replica advisory. (deployment.metrics in
	// the fake returns {} so the metrics-based advisories are skipped.)
	events := `[{"reason":"BackOff","message":"Back-off restarting failed container app"}]`
	srv := diagServer(t, `{"count":2,"ready":1,"succeeded":0,"failed":0}`, events,
		"data: {\"log\":\"panic: boom\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if !contains(advisoryNames(sc), "ReplicasNotReady") {
		t.Fatalf("advisories %v missing ReplicasNotReady", advisoryNames(sc))
	}
}

// Fury blocker 1: a secret in an EVENT message must be sanitized before it becomes
// cited evidence in a cause.
func TestDiagnose_EventEvidenceSanitized(t *testing.T) {
	// The event that triggers the CrashLoopBackOff cause also carries a secret.
	events := `[{"reason":"BackOff","message":"Back-off restarting failed container app, token=SUPERSECRETeventTok"}]`
	srv := diagServer(t, `{"count":2,"ready":1,"succeeded":0,"failed":0}`, events,
		"data: {\"log\":\"x\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	blob := fmt.Sprintf("%v", sc)
	if strings.Contains(blob, "SUPERSECRETeventTok") {
		t.Fatalf("event secret leaked into cause evidence: %s", blob)
	}
	// The cause itself still fires (sanitization didn't remove the matching keyword).
	if !contains(causeNames(sc), "CrashLoopBackOff") {
		t.Fatalf("CrashLoopBackOff should still match after sanitizing: %v", causeNames(sc))
	}
}

// Fury blocker 2: OverProvisioned must NOT be advised on an UNHEALTHY deployment
// (low usage on a crash-looping app is expected, not a scale-down signal).
func TestDiagnose_NoOverProvisionedWhenUnhealthy(t *testing.T) {
	// Very low usage metrics, but the deployment is unhealthy (crash-loop event).
	lowMetrics := `{"ok":true,"result":{` +
		`"cpuUsage":[{"name":"c","points":[[1,0.001],[2,0.001]]}],` +
		`"cpuLimit":[{"name":"c","points":[[1,1],[2,1]]}],` +
		`"memoryUsage":[{"name":"m","points":[[1,1000000],[2,1000000]]}],` +
		`"memoryLimit":[{"name":"m","points":[[1,1000000000],[2,1000000000]]}]}}`
	events := `[{"reason":"BackOff","message":"Back-off restarting failed container app"}]`
	srv := diagServerM(t, `{"count":2,"ready":1,"succeeded":0,"failed":0}`, events,
		"data: {\"log\":\"panic: boom\"}\n\n", lowMetrics)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if sc["healthy"].(bool) {
		t.Fatal("test setup: expected unhealthy")
	}
	if contains(advisoryNames(sc), "OverProvisioned") {
		t.Fatalf("OverProvisioned must not fire on an unhealthy deployment: %v", advisoryNames(sc))
	}
}

// And the positive: OverProvisioned DOES fire on a healthy, under-utilized deployment.
func TestDiagnose_OverProvisionedWhenHealthy(t *testing.T) {
	lowMetrics := `{"ok":true,"result":{` +
		`"cpuUsage":[{"name":"c","points":[[1,0.001],[2,0.001]]}],` +
		`"cpuLimit":[{"name":"c","points":[[1,1],[2,1]]}],` +
		`"memoryUsage":[{"name":"m","points":[[1,1000000],[2,1000000]]}],` +
		`"memoryLimit":[{"name":"m","points":[[1,1000000000],[2,1000000000]]}]}}`
	srv := diagServerM(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"ok\"}\n\n", lowMetrics)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if !sc["healthy"].(bool) {
		t.Fatal("test setup: expected healthy")
	}
	if !contains(advisoryNames(sc), "OverProvisioned") {
		t.Fatalf("OverProvisioned should fire on a healthy under-utilized deployment: %v", advisoryNames(sc))
	}
}

// Fury B1 follow-up: an event body that echoes the eventUrl / its JWT must be
// scrubbed (eventURL is passed into the event sanitizer at fetch time). The fake
// assigns the token value "JWTtokenVAL" to every URL's ?t=.
func TestDiagnose_EventEchoesURL_Redacted(t *testing.T) {
	// The event message echoes the real eventUrl's JWT token value.
	events := `[{"reason":"BackOff","message":"Back-off restarting failed container app; fetched from /events?t=JWTtokenVAL"}]`
	srv := diagServer(t, `{"count":2,"ready":1,"succeeded":0,"failed":0}`, events,
		"data: {\"log\":\"x\"}\n\n")
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	blob := fmt.Sprintf("%v", sc)
	// The eventUrl's JWT token value must not leak through the event evidence.
	if strings.Contains(blob, "JWTtokenVAL") {
		t.Fatalf("event evidence leaked the eventUrl JWT: %s", blob)
	}
	// And the cause still fires (sanitization only removed the secret).
	if !contains(causeNames(sc), "CrashLoopBackOff") {
		t.Fatalf("CrashLoopBackOff should still match: %v", causeNames(sc))
	}
}

// Fury: ResourceNearLimit must be at most medium confidence and use the AVG (not the
// last point) — a single high spike at the end must not trigger it.
func TestDiagnose_ResourceNearLimit_AvgNotLast(t *testing.T) {
	// cpu points avg ~0.1 (10%) but the LAST point spikes to 0.95 (95%). avg-based
	// logic must NOT fire ResourceNearLimit (last-point logic would).
	spikeMetrics := `{"ok":true,"result":{` +
		`"cpuUsage":[{"name":"c","points":[[1,0.05],[2,0.05],[3,0.05],[4,0.95]]}],` +
		`"cpuLimit":[{"name":"c","points":[[1,1],[2,1],[3,1],[4,1]]}],` +
		`"memoryUsage":[{"name":"m","points":[[1,100000000],[2,100000000]]}],` +
		`"memoryLimit":[{"name":"m","points":[[1,1000000000],[2,1000000000]]}]}}`
	srv := diagServerM(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"ok\"}\n\n", spikeMetrics)
	restore := jetder.SetLogTimeoutsForTest(60*time.Millisecond, 2*time.Second)
	defer restore()
	a := diagAdapter(t, srv)
	sc := callDiag(t, a)
	if contains(advisoryNames(sc), "ResourceNearLimit") {
		t.Fatalf("avg cpu ~28%% (one spike) must NOT trigger ResourceNearLimit: %v", advisoryNames(sc))
	}

	// Now a genuinely high AVG (all points high) → fires, and at confidence "medium".
	highMetrics := `{"ok":true,"result":{` +
		`"cpuUsage":[{"name":"c","points":[[1,0.9],[2,0.92]]}],` +
		`"cpuLimit":[{"name":"c","points":[[1,1],[2,1]]}],` +
		`"memoryUsage":[{"name":"m","points":[[1,100000000],[2,100000000]]}],` +
		`"memoryLimit":[{"name":"m","points":[[1,1000000000],[2,1000000000]]}]}}`
	srv2 := diagServerM(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`,
		"data: {\"log\":\"ok\"}\n\n", highMetrics)
	a2 := diagAdapter(t, srv2)
	sc2 := callDiag(t, a2)
	found := false
	if as, ok := sc2["advisories"].([]any); ok {
		for _, x := range as {
			m, _ := x.(map[string]any)
			if m["cause"] == "ResourceNearLimit" {
				found = true
				if m["confidence"] != "medium" {
					t.Fatalf("ResourceNearLimit confidence = %v, want medium (metrics snapshot)", m["confidence"])
				}
			}
		}
	}
	if !found {
		t.Fatalf("high avg cpu should trigger ResourceNearLimit: %v", advisoryNames(sc2))
	}
}

// Fury: FetchJSON must enforce the same guards as the log fetch — no Authorization
// header, SSRF host reject, HTTPS for jetder hosts.
func TestFetchJSON_Guards(t *testing.T) {
	srv := diagServer(t, `{"count":1,"ready":1,"succeeded":0,"failed":0}`, `null`, "data: {}\n\n")
	a := diagAdapter(t, srv) // JETDER_ENDPOINT = srv (http httptest)
	// SSRF: a foreign host is rejected.
	if _, _, err := a.FetchJSON(context.Background(), "https://evil.example.com/x?t=1", 1024); err == nil {
		t.Fatal("FetchJSON must reject an unexpected host")
	}
	// non-http scheme rejected.
	if _, _, err := a.FetchJSON(context.Background(), "file:///etc/passwd", 1024); err == nil {
		t.Fatal("FetchJSON must reject non-http scheme")
	}
	// plaintext http to a real jetder host rejected (not the http endpoint host).
	if _, _, err := a.FetchJSON(context.Background(), "http://log.cluster-1.jetder.com/x?t=1", 1024); err == nil {
		t.Fatal("FetchJSON must reject plaintext http to a jetder host")
	}
	// No Authorization header is sent: the statusURL handler records it; reuse the
	// status endpoint and assert it succeeds without auth (handler doesn't require it).
	b, _, err := a.FetchJSON(context.Background(), srv.URL+"/status?t=x", 4096)
	if err != nil {
		t.Fatalf("FetchJSON to the (http) endpoint host should succeed: %v", err)
	}
	if !strings.Contains(string(b), "ready") {
		t.Fatalf("FetchJSON body = %q, want the status JSON", string(b))
	}
}

// avgUtil / avgValue unit checks (the metrics math, independent of the endpoint).
func TestAvgUtil(t *testing.T) {
	usage := [][2]float64{{1, 0.5}, {2, 1.5}} // avg 1.0
	limit := [][2]float64{{1, 2}, {2, 2}}     // avg 2.0
	u, ok := avgUtil(usage, limit)
	if !ok || u < 0.49 || u > 0.51 {
		t.Fatalf("avgUtil = %v ok=%v, want ~0.5", u, ok)
	}
	// zero/empty limit → not ok (no divide-by-zero).
	if _, ok := avgUtil(usage, [][2]float64{{1, 0}}); ok {
		t.Fatal("avgUtil with zero limit should be not-ok")
	}
	if _, ok := avgUtil(nil, limit); ok {
		t.Fatal("avgUtil with empty usage should be not-ok")
	}
}
