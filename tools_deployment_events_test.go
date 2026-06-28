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

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// eventsServer answers deployment.get + an eventUrl returning the given JSON body.
func eventsServer(t *testing.T, eventsJSON string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.Header.Get("Authorization") != "" {
				t.Errorf("event GET must NOT send an Authorization header")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(eventsJSON))
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","eventUrl":%q}}`,
				srv.URL+"/events?t=JWTtokenVAL")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func eventsAdapter(t *testing.T, srv *httptest.Server) *jetder.Adapter {
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

func callEvents(t *testing.T, a *jetder.Adapter, args map[string]any) map[string]any {
	t.Helper()
	cs := connectInMemory(t, a)
	if args == nil {
		args = map[string]any{"name": "web"}
	}
	return callTool(t, cs, "deployment-events", args)
}

// The LIVE shape: a bare array of {lastSeen,type,reason,message}.
func TestDeploymentEvents_LiveShape(t *testing.T) {
	body := `[
		{"lastSeen":"2026-06-28T09:42:39Z","type":"Normal","reason":"Pulled","message":"Successfully pulled image"},
		{"lastSeen":"2026-06-28T09:44:18Z","type":"Warning","reason":"BackOff","message":"Back-off restarting failed container app"}
	]`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 2 {
		t.Fatalf("count = %v, want 2", sc["count"])
	}
	evs, _ := sc["events"].([]any)
	last, _ := evs[1].(map[string]any)
	if last["type"] != "Warning" || last["reason"] != "BackOff" {
		t.Fatalf("last event = %v, want Warning/BackOff", last)
	}
	if last["lastTimestamp"] != "2026-06-28T09:44:18Z" {
		t.Fatalf("lastTimestamp = %v (should come from lastSeen)", last["lastTimestamp"])
	}
}

// null events (healthy) → empty list, count 0, no error.
func TestDeploymentEvents_NullIsEmpty(t *testing.T) {
	srv := eventsServer(t, `null`)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 0 {
		t.Fatalf("count = %v, want 0", sc["count"])
	}
	if evs, ok := sc["events"].([]any); !ok || len(evs) != 0 {
		t.Fatalf("events = %v, want empty array (never null)", sc["events"])
	}
}

// Object-wrapped {items:[...]} shape is accepted (tolerant).
func TestDeploymentEvents_ItemsWrapper(t *testing.T) {
	body := `{"items":[{"type":"Normal","reason":"Scheduled","message":"assigned to node"}]}`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 1 {
		t.Fatalf("count = %v, want 1 (items wrapper)", sc["count"])
	}
}

// limit caps the number returned (keeps the most recent).
func TestDeploymentEvents_Limit(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"Normal","reason":"R%d","message":"m%d"}`, i, i)
	}
	b.WriteString("]")
	srv := eventsServer(t, b.String())
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, map[string]any{"name": "web", "limit": float64(3)})
	if c, _ := sc["count"].(float64); c != 3 {
		t.Fatalf("count = %v, want 3 (limited)", sc["count"])
	}
	// most-recent kept: last event is R9.
	evs, _ := sc["events"].([]any)
	last, _ := evs[2].(map[string]any)
	if last["reason"] != "R9" {
		t.Fatalf("last kept event = %v, want R9 (tail)", last["reason"])
	}
}

// Secrets in event messages / the eventUrl JWT must be scrubbed.
func TestDeploymentEvents_RedactsSecrets(t *testing.T) {
	body := `[{"type":"Warning","reason":"Failed","message":"auth failed token=SUPERSECRETtok999 from /events?t=JWTtokenVAL"}]`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	blob := fmt.Sprintf("%v", sc)
	for _, secret := range []string{"SUPERSECRETtok999", "JWTtokenVAL"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("events leaked secret %q: %s", secret, blob)
		}
	}
}

// {events:[...]} wrapper is accepted (tolerant).
func TestDeploymentEvents_EventsWrapper(t *testing.T) {
	body := `{"events":[{"type":"Warning","reason":"OOMKilling","message":"killed"}]}`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 1 {
		t.Fatalf("count = %v, want 1 (events wrapper)", sc["count"])
	}
}

// An event carrying only eventTime (no lastSeen/lastTimestamp) still surfaces a
// timestamp via when()'s fallback chain.
func TestDeploymentEvents_EventTimeFallback(t *testing.T) {
	body := `[{"eventTime":"2026-06-28T10:00:00Z","type":"Normal","reason":"Pulled","message":"ok"}]`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	evs, _ := sc["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %v", sc["count"])
	}
	e0, _ := evs[0].(map[string]any)
	if e0["lastTimestamp"] != "2026-06-28T10:00:00Z" {
		t.Fatalf("lastTimestamp = %v, want the eventTime value (when() fallback)", e0["lastTimestamp"])
	}
}

// {"events":[]} — a recognized key with an EMPTY list → count 0, NOT an error.
func TestDeploymentEvents_EmptyEventsArray(t *testing.T) {
	srv := eventsServer(t, `{"events":[]}`)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 0 {
		t.Fatalf("count = %v, want 0 (empty but valid)", sc["count"])
	}
	if evs, ok := sc["events"].([]any); !ok || len(evs) != 0 {
		t.Fatalf("events = %v, want empty array", sc["events"])
	}
}

// A populated-but-unrecognized shape → tool ERROR (must not pretend "no events").
func TestDeploymentEvents_InvalidShape_Errors(t *testing.T) {
	srv := eventsServer(t, `{"weird":"not an event list"}`)
	a := eventsAdapter(t, srv)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deployment-events", Arguments: map[string]any{"name": "web"}})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unrecognized event payload must surface as a tool error, not 'no events'")
	}
}

// No eventUrl → tool ERROR (not a silent empty list).
func TestDeploymentEvents_NoEventURL_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"name":"web","eventUrl":""}}`))
	}))
	t.Cleanup(srv.Close)
	a := eventsAdapter(t, srv)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deployment-events", Arguments: map[string]any{"name": "web"}})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if !res.IsError {
		t.Fatal("no event URL must be a tool error")
	}
}

// maxBytes truncation → truncated=true, parse degrades gracefully (no crash).
func TestDeploymentEvents_MaxBytesTruncate(t *testing.T) {
	// A large valid array; a tiny maxBytes cuts it mid-JSON.
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 50; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"Normal","reason":"R%d","message":"some longer message body %d"}`, i, i)
	}
	b.WriteString("]")
	srv := eventsServer(t, b.String())
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, map[string]any{"name": "web", "maxBytes": float64(120)})
	if tr, _ := sc["truncated"].(bool); !tr {
		t.Fatalf("expected truncated=true with a tiny maxBytes")
	}
}

// revision is passed through to deployment.get.
func TestDeploymentEvents_RevisionPassed(t *testing.T) {
	gotRevision := -1
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			_, _ = w.Write([]byte(`[{"type":"Normal","reason":"Pulled","message":"ok"}]`))
		case strings.HasSuffix(r.URL.Path, "deployment.get"):
			var body struct {
				Revision int `json:"revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotRevision = body.Revision
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"result":{"name":"web","eventUrl":%q}}`, srv.URL+"/events?t=x")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	a := eventsAdapter(t, srv)
	callEvents(t, a, map[string]any{"name": "web", "revision": float64(7)})
	if gotRevision != 7 {
		t.Fatalf("revision = %d, want 7", gotRevision)
	}
}

// involvedObject parses BOTH an object and a bare string without breaking the array.
func TestDeploymentEvents_InvolvedObject_ObjectAndString(t *testing.T) {
	body := `[
		{"type":"Normal","reason":"A","message":"m","involvedObject":{"kind":"Pod","name":"web-1","namespace":"deploys"}},
		{"type":"Normal","reason":"B","message":"m","involvedObject":"Pod/web-2"}
	]`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	sc := callEvents(t, a, nil)
	if c, _ := sc["count"].(float64); c != 2 {
		t.Fatalf("count = %v, want 2 (string involvedObject must not break the array)", sc["count"])
	}
	evs, _ := sc["events"].([]any)
	e0, _ := evs[0].(map[string]any)
	e1, _ := evs[1].(map[string]any)
	if e0["involvedObjectKind"] != "Pod" || e0["involvedObjectName"] != "web-1" {
		t.Fatalf("object involvedObject not parsed: %v", e0)
	}
	if e1["involvedObjectKind"] != "Pod" || e1["involvedObjectName"] != "web-2" {
		t.Fatalf("string involvedObject 'Pod/web-2' not split: %v", e1)
	}
}

// readOnly + no Authorization header (asserted in the server) + no URL leak.
func TestDeploymentEvents_NoAuthNoURLLeak(t *testing.T) {
	body := `[{"type":"Normal","reason":"Pulled","message":"ok"}]`
	srv := eventsServer(t, body)
	a := eventsAdapter(t, srv)
	cs := connectInMemory(t, a)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deployment-events", Arguments: map[string]any{"name": "web"}})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	blob := fmt.Sprintf("%v", res.StructuredContent)
	if strings.Contains(blob, "/events?t=") || strings.Contains(blob, "JWTtokenVAL") {
		t.Fatalf("output leaked the eventUrl/JWT: %s", blob)
	}
}
