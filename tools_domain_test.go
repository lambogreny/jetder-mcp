package main

import (
	"testing"

	"github.com/jetder-core/api"
)

// TestToDomainItem_PointingRecords is the core "ชี้โดเมน" contract: the view must
// surface ownership TXT, SSL/DCV records, and A/AAAA/CNAME point-to records.
func TestToDomainItem_PointingRecords(t *testing.T) {
	in := &api.DomainItem{
		Project:  "p",
		Location: "l",
		Domain:   "example.com",
		Status:   api.DomainStatusVerify,
	}
	in.Verification.Ownership.Type = "TXT"
	in.Verification.Ownership.Name = "_jetder.example.com"
	in.Verification.Ownership.Value = "verify-token"
	in.Verification.SSL.Pending = true
	in.Verification.SSL.DCV.Name = "_acme.example.com"
	in.Verification.SSL.DCV.Value = "dcv-token"
	in.Verification.SSL.Records = []api.DomainVerificationSSLRecord{
		{TxtName: "_ssl.example.com", TxtValue: "ssl-token"},
	}
	in.DNSConfig.IPv4 = []string{"1.2.3.4"}
	in.DNSConfig.IPv6 = []string{"::1"}
	in.DNSConfig.CName = []string{"cname.jetder.com"}

	got := toDomainItem(in)

	if got.Status != "verify" {
		t.Fatalf("status = %q, want verify", got.Status)
	}
	if got.OwnershipRecord == nil || got.OwnershipRecord.Value != "verify-token" || got.OwnershipRecord.Type != "TXT" {
		t.Fatalf("ownership record wrong: %+v", got.OwnershipRecord)
	}
	if !got.SSLPending {
		t.Fatal("sslPending should be true")
	}
	// DCV + 1 record = 2 SSL records.
	if len(got.SSLRecords) != 2 {
		t.Fatalf("sslRecords = %d, want 2: %+v", len(got.SSLRecords), got.SSLRecords)
	}
	// A + AAAA + CNAME = 3 point-to records.
	if len(got.PointTo) != 3 {
		t.Fatalf("pointTo = %d, want 3: %+v", len(got.PointTo), got.PointTo)
	}
	want := map[string]string{"A": "1.2.3.4", "AAAA": "::1", "CNAME": "cname.jetder.com"}
	for _, r := range got.PointTo {
		if want[r.Type] != r.Value {
			t.Fatalf("point-to %s = %q, want %q", r.Type, r.Value, want[r.Type])
		}
	}
}

func TestToDomainItem_NoRecordsWhenEmpty(t *testing.T) {
	in := &api.DomainItem{Domain: "x.com", Status: api.DomainStatusPending}
	got := toDomainItem(in)
	if got.OwnershipRecord != nil {
		t.Fatalf("ownership should be nil when empty: %+v", got.OwnershipRecord)
	}
	if len(got.SSLRecords) != 0 || len(got.PointTo) != 0 {
		t.Fatalf("expected no records, got ssl=%d pointTo=%d", len(got.SSLRecords), len(got.PointTo))
	}
}

// ---- in-memory end-to-end ----

func TestDomainGet_EndToEnd_ResolvedContextAndRecords(t *testing.T) {
	body := `{"ok":true,"result":{
		"domain":"example.com","status":"verify",
		"verification":{"ownership":{"type":"TXT","name":"_v.example.com","value":"tok"},"ssl":{"pending":true}},
		"dnsConfig":{"ipv4":["1.2.3.4"],"cname":["c.jetder.com"]}
	}}`
	a := newTestAdapter(t, body, "def-proj", "def-loc")
	cs := connectInMemory(t, a)

	sc := callTool(t, cs, "domain-get", map[string]any{"domain": "example.com"})
	if sc["resolvedProject"] != "def-proj" {
		t.Fatalf("resolvedProject = %v, want def-proj", sc["resolvedProject"])
	}
	if sc["status"] != "verify" {
		t.Fatalf("status = %v, want verify", sc["status"])
	}
	pt, ok := sc["pointTo"].([]any)
	if !ok || len(pt) != 2 { // A + CNAME
		t.Fatalf("pointTo = %v, want 2 records", sc["pointTo"])
	}
}

func TestDomainGet_MissingDomain(t *testing.T) {
	callToolExpectError(t, "domain-get", map[string]any{})
}

func TestDomainPurgeCache_EndToEnd(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	sc := callTool(t, cs, "domain-purge-cache", map[string]any{"domain": "example.com"})
	if sc["action"] != "purgeCache" || sc["success"] != true || sc["resolvedProject"] != "p" {
		t.Fatalf("unexpected: %+v", sc)
	}
}
