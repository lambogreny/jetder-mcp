package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dnsMock serves list (returns the given existing records at the name), POST
// (create), and PATCH (update). It records the method + body of the mutating call.
type dnsCall struct {
	method string
	path   string
	body   map[string]any
}

func dnsMock(t *testing.T, existing []DNSRecord, calls *[]dnsCall) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			// list dns_records — return the existing slice (ignore filters for simplicity).
			_, _ = w.Write([]byte(ok(existing)))
		case http.MethodPost, http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if calls != nil {
				*calls = append(*calls, dnsCall{method: r.Method, path: r.URL.Path, body: body})
			}
			// Echo back a record reflecting the written fields.
			rec := DNSRecord{
				Type:    str(body["type"]),
				Name:    str(body["name"]),
				Content: str(body["content"]),
				Proxied: boolOf(body["proxied"]),
				ID:      "rec-new",
			}
			_, _ = w.Write([]byte(ok(rec)))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "t", redactions: []string{"t"}}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// --- create new proxied record: POST carries proxied=true ----------------------

func TestCreateDNS_NewProxied_PostBody(t *testing.T) {
	var calls []dnsCall
	c := dnsMock(t, nil, &calls)
	res, err := c.CreateDNSRecord(context.Background(), "z1",
		DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(calls) != 1 || calls[0].method != http.MethodPost {
		t.Fatalf("expected one POST, got %+v", calls)
	}
	if calls[0].body["proxied"] != true {
		t.Fatalf("POST body proxied should be true: %v", calls[0].body)
	}
	if res.AlreadyExists || res.ProxiedUpdated {
		t.Fatalf("fresh create flags wrong: %+v", res)
	}
}

// --- identical record incl proxied => AlreadyExists, NO write ------------------

func TestCreateDNS_IdenticalProxied_NoWrite(t *testing.T) {
	var calls []dnsCall
	existing := []DNSRecord{{ID: "r1", Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: true}}
	c := dnsMock(t, existing, &calls)
	res, err := c.CreateDNSRecord(context.Background(), "z1",
		DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !res.AlreadyExists || res.ProxiedUpdated {
		t.Fatalf("expected AlreadyExists no update: %+v", res)
	}
	for _, c := range calls {
		if c.method == http.MethodPost || c.method == http.MethodPatch {
			t.Fatalf("no write expected, got %s", c.method)
		}
	}
}

// --- THE FIX: existing grey (DNS-only) record + want proxied => PATCH ----------

func TestCreateDNS_GreyToOrange_Patches(t *testing.T) {
	var calls []dnsCall
	existing := []DNSRecord{{ID: "r1", Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: false}}
	c := dnsMock(t, existing, &calls)
	res, err := c.CreateDNSRecord(context.Background(), "z1",
		DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.AlreadyExists {
		t.Fatalf("must NOT report AlreadyExists when proxied differs: %+v", res)
	}
	if !res.ProxiedUpdated {
		t.Fatalf("expected ProxiedUpdated=true: %+v", res)
	}
	// Exactly one PATCH to the existing record id, flipping proxied to true.
	var patches int
	for _, cl := range calls {
		if cl.method == http.MethodPatch {
			patches++
			if !strings.HasSuffix(cl.path, "/dns_records/r1") {
				t.Fatalf("PATCH wrong record id: %s", cl.path)
			}
			if cl.body["proxied"] != true {
				t.Fatalf("PATCH should set proxied=true: %v", cl.body)
			}
		}
		if cl.method == http.MethodPost {
			t.Fatalf("must PATCH, not POST a new record")
		}
	}
	if patches != 1 {
		t.Fatalf("expected exactly one PATCH, got %d", patches)
	}
}
