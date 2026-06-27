package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient builds a Client pointed at a mock CF server.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
		token:      "cf-secret-token",
		accountID:  "acct-1",
		redactions: []string{"Bearer cf-secret-token", "cf-secret-token"},
	}
}

func ok(result any) string {
	b, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": result})
	return string(b)
}

func okPaged(result any, page, totalPages int) string {
	b, _ := json.Marshal(map[string]any{
		"success": true, "errors": []any{}, "result": result,
		"result_info": map[string]any{"page": page, "total_pages": totalPages},
	})
	return string(b)
}

func TestNew_NotConfigured(t *testing.T) {
	old := getenv
	getenv = func(string) string { return "" }
	defer func() { getenv = old }()

	c, err := New()
	if err != nil {
		t.Fatalf("New() with no env should not error, got %v", err)
	}
	if c != nil {
		t.Fatalf("New() with no env should be nil (optional), got %v", c)
	}
}

func TestDo_SendsBearer(t *testing.T) {
	var gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(ok([]Zone{})))
	})
	if _, err := c.ListZones(context.Background(), ""); err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if gotAuth != "Bearer cf-secret-token" {
		t.Fatalf("Authorization = %q, want Bearer cf-secret-token", gotAuth)
	}
}

func TestDo_APIError_Redacted(t *testing.T) {
	// CF returns success:false; error message even echoes the token.
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"bad token cf-secret-token"}]}`))
	})
	_, err := c.ListZones(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on success:false")
	}
	if strings.Contains(err.Error(), "cf-secret-token") {
		t.Fatalf("TOKEN LEAK in error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected [REDACTED], got %v", err)
	}
}

func TestFindZoneForName_LongestSuffix_ExactQuery(t *testing.T) {
	// FindZoneForName queries candidate names specific→apex via ?name=<exact>.
	// app.sub.example.com → first queries "app.sub.example.com" (none), then
	// "sub.example.com" (exists) → wins, before ever reaching apex example.com.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		switch name {
		case "sub.example.com":
			_, _ = w.Write([]byte(ok([]Zone{{ID: "z2", Name: "sub.example.com"}})))
		case "example.com":
			t.Fatalf("should have matched sub.example.com before querying apex")
		default:
			_, _ = w.Write([]byte(ok([]Zone{})))
		}
	})
	z, err := c.FindZoneForName(context.Background(), "app.sub.example.com")
	if err != nil {
		t.Fatalf("FindZoneForName: %v", err)
	}
	if z.ID != "z2" {
		t.Fatalf("zone = %q (%s), want z2 sub.example.com", z.ID, z.Name)
	}
}

func TestFindZoneForName_NotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ok([]Zone{}))) // exact-name queries all return empty
	})
	_, err := c.FindZoneForName(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "no Cloudflare zone owns") {
		t.Fatalf("expected zone-not-found error, got %v", err)
	}
}

// TestListZones_Paginates: a zone on page 2 must be returned.
func TestListZones_Paginates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			_, _ = w.Write([]byte(okPaged([]Zone{{ID: "z1", Name: "a.com"}}, 1, 2)))
		case "2":
			_, _ = w.Write([]byte(okPaged([]Zone{{ID: "z2", Name: "b.com"}}, 2, 2)))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	})
	zones, err := c.ListZones(context.Background(), "")
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones across pages, got %d", len(zones))
	}
}

func TestCreateDNSRecord_Idempotent_AlreadyExists(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// list returns an identical record → create must NOT POST.
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ok([]DNSRecord{{ID: "r1", Type: "A", Name: "x.example.com", Content: "1.2.3.4"}})))
			return
		}
		t.Fatalf("unexpected %s — should not write when record exists", r.Method)
	})
	res, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"})
	if err != nil {
		t.Fatalf("CreateDNSRecord: %v", err)
	}
	if !res.AlreadyExists {
		t.Fatalf("expected AlreadyExists=true, got %+v", res)
	}
}

func TestCreateDNSRecord_ConflictNoOverwrite(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// same type+name but DIFFERENT content.
			_, _ = w.Write([]byte(ok([]DNSRecord{{ID: "r1", Type: "A", Name: "x.example.com", Content: "9.9.9.9"}})))
			return
		}
		t.Fatalf("must NOT overwrite on content conflict (%s)", r.Method)
	})
	_, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestCreateDNSRecord_Creates(t *testing.T) {
	posted := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ok([]DNSRecord{}))) // none existing
			return
		}
		posted = true
		var body DNSRecord
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TTL != 1 {
			t.Fatalf("default TTL should be 1 (auto), got %d", body.TTL)
		}
		if body.Proxied {
			t.Fatalf("default proxied should be false")
		}
		_, _ = w.Write([]byte(ok(DNSRecord{ID: "new1", Type: body.Type, Name: body.Name, Content: body.Content})))
	})
	res, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{Type: "TXT", Name: "_v.example.com", Content: "tok"})
	if err != nil {
		t.Fatalf("CreateDNSRecord: %v", err)
	}
	if !posted || res.AlreadyExists || res.Record.ID != "new1" {
		t.Fatalf("expected fresh create, got posted=%v res=%+v", posted, res)
	}
}

// TestCreateDNSRecord_ConflictMatrix covers Cloudflare same-name rules.
func TestCreateDNSRecord_ConflictMatrix(t *testing.T) {
	cases := []struct {
		name     string
		existing DNSRecord
		want     DNSRecord
		conflict bool
	}{
		{"CNAME vs A", DNSRecord{Type: "CNAME", Name: "x.example.com", Content: "t.example.com"}, DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"}, true},
		{"A vs CNAME", DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"}, DNSRecord{Type: "CNAME", Name: "x.example.com", Content: "t.example.com"}, true},
		{"NS vs A", DNSRecord{Type: "NS", Name: "x.example.com", Content: "ns1.example.com"}, DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"}, true},
		{"A diff content", DNSRecord{Type: "A", Name: "x.example.com", Content: "9.9.9.9"}, DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"}, true},
		{"A + TXT coexist ok", DNSRecord{Type: "TXT", Name: "x.example.com", Content: "hello"}, DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			posted := false
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(ok([]DNSRecord{tc.existing})))
					return
				}
				posted = true
				_, _ = w.Write([]byte(ok(DNSRecord{ID: "new", Type: tc.want.Type, Name: tc.want.Name, Content: tc.want.Content})))
			})
			_, err := c.CreateDNSRecord(context.Background(), "z1", tc.want)
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "conflict") {
					t.Fatalf("expected conflict, got err=%v posted=%v", err, posted)
				}
				if posted {
					t.Fatal("must NOT POST on conflict")
				}
			} else {
				if err != nil {
					t.Fatalf("expected success (coexist), got %v", err)
				}
				if !posted {
					t.Fatal("expected POST for coexisting types")
				}
			}
		})
	}
}

// TestListDNSRecords_Paginates: a record on page 2 must be seen (conflict detection).
func TestListDNSRecords_Paginates(t *testing.T) {
	// existing conflicting A on page 2; create A diff content → must detect conflict.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("must not POST — conflict is on page 2")
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(okPaged([]DNSRecord{{Type: "TXT", Name: "x.example.com", Content: "a"}}, 1, 2)))
		case "2":
			_, _ = w.Write([]byte(okPaged([]DNSRecord{{Type: "A", Name: "x.example.com", Content: "9.9.9.9"}}, 2, 2)))
		default:
			t.Fatalf("unexpected page")
		}
	})
	_, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{Type: "A", Name: "x.example.com", Content: "1.2.3.4"})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict detected across pages, got %v", err)
	}
}

func TestRedact(t *testing.T) {
	c := &Client{redactions: []string{"Bearer abc", "abc"}}
	got := c.Redact("header Bearer abc and raw abc here")
	if strings.Contains(got, "abc") {
		t.Fatalf("redact failed: %q", got)
	}
}
