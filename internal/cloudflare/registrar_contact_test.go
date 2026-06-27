package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registrarMockCapture is registrarMock but also captures the POST body (so we can
// assert the exact contacts.registrant payload) and lets the registrations
// response be overridden (e.g. to return a CF error that echoes the contact).
func registrarMockCapture(t *testing.T, checkOffer DomainOffer, posted *bool, body *map[string]any, regResp func() string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/registrar/domain-check"):
			_, _ = w.Write([]byte(ok(map[string]any{"domains": []DomainOffer{checkOffer}})))
		case strings.HasSuffix(r.URL.Path, "/registrar/registrations"):
			if posted != nil {
				*posted = true
			}
			if body != nil {
				var m map[string]any
				_ = json.NewDecoder(r.Body).Decode(&m)
				*body = m
			}
			if regResp != nil {
				_, _ = w.Write([]byte(regResp()))
				return
			}
			_, _ = w.Write([]byte(ok(RegistrationResult{DomainName: checkOffer.Name, State: StateInProgress, Completed: false})))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{
		httpClient: srv.Client(), baseURL: srv.URL,
		token: "cf-tok", accountID: "acct-1",
		redactions: []string{"Bearer cf-tok", "cf-tok"},
	}
}

func validContact() *RegistrantContact {
	return &RegistrantContact{
		Name:        "Ada Lovelace",
		Email:       "ada@example.com",
		Phone:       "+1.5555550123",
		Street:      "12 Analytical Engine Way",
		City:        "London",
		State:       "LDN",
		PostalCode:  "EC1A1BB",
		CountryCode: "gb", // lower → canonicalized to GB
	}
}

func confWithAccuracy() PurchaseConfirmation {
	c := goodConf()
	c.AcceptRegistrantAccuracy = true
	return c
}

// --- backward compat: no contact => no contacts key in body --------------------

func TestRegister_NoContact_BodyUnchanged(t *testing.T) {
	posted := false
	var body map[string]any
	c := registrarMockCapture(t, goodOffer(), &posted, &body, nil)
	if _, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !posted {
		t.Fatal("expected POST")
	}
	if _, ok := body["contacts"]; ok {
		t.Fatalf("contacts must be absent when no registrant supplied; body=%v", body)
	}
}

// --- full contact => exact contacts.registrant JSON ---------------------------

func TestRegister_WithContact_ExactBody(t *testing.T) {
	posted := false
	var body map[string]any
	c := registrarMockCapture(t, goodOffer(), &posted, &body, nil)
	opts := RegisterOptions{Registrant: validContact()}
	if _, err := c.Register(context.Background(), "", "example.com", confWithAccuracy(), opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	contacts, ok := body["contacts"].(map[string]any)
	if !ok {
		t.Fatalf("contacts missing/!object: %v", body["contacts"])
	}
	reg, ok := contacts["registrant"].(map[string]any)
	if !ok {
		t.Fatalf("registrant missing: %v", contacts)
	}
	if reg["email"] != "ada@example.com" || reg["phone"] != "+1.5555550123" {
		t.Fatalf("email/phone wrong: %v", reg)
	}
	postal := reg["postal_info"].(map[string]any)
	if postal["name"] != "Ada Lovelace" {
		t.Fatalf("name wrong: %v", postal)
	}
	if _, hasOrg := postal["organization"]; hasOrg {
		t.Fatalf("organization should be omitted when empty: %v", postal)
	}
	addr := postal["address"].(map[string]any)
	if addr["street"] != "12 Analytical Engine Way" || addr["city"] != "London" ||
		addr["state"] != "LDN" || addr["postal_code"] != "EC1A1BB" || addr["country_code"] != "GB" {
		t.Fatalf("address wrong (country should be canonicalized GB): %v", addr)
	}
	// No admin/tech/billing keys — only registrant.
	if len(contacts) != 1 {
		t.Fatalf("only registrant expected, got keys %v", contacts)
	}
}

// --- accuracy ack required when contact present (fail-closed, NO POST) ---------

func TestRegister_Contact_RequiresAccuracyAck_NoPost(t *testing.T) {
	posted := false
	c := registrarMockCapture(t, goodOffer(), &posted, nil, nil)
	opts := RegisterOptions{Registrant: validContact()}
	_, err := c.Register(context.Background(), "", "example.com", goodConf() /* no accuracy ack */, opts)
	if err == nil || !strings.Contains(err.Error(), "acceptRegistrantAccuracy") {
		t.Fatalf("expected accuracy-ack rejection, got %v", err)
	}
	if posted {
		t.Fatal("must NOT POST when accuracy ack missing")
	}
}

// --- invalid/partial contact => reject BEFORE any POST ------------------------

func TestRegister_ContactValidation_NoPost(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*RegistrantContact)
		wantSub string
	}{
		{"missing name", func(r *RegistrantContact) { r.Name = "" }, "registrant.name"},
		{"missing street (partial)", func(r *RegistrantContact) { r.Street = "" }, "registrant.street"},
		{"bad email", func(r *RegistrantContact) { r.Email = "not-an-email" }, "registrant.email"},
		{"bad phone (no dot)", func(r *RegistrantContact) { r.Phone = "+15555550123" }, "registrant.phone"},
		{"bad country", func(r *RegistrantContact) { r.CountryCode = "USA" }, "registrant.countryCode"},
		{"oversized name", func(r *RegistrantContact) { r.Name = strings.Repeat("x", maxContactNameLen+1) }, "registrant.name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			posted := false
			c := registrarMockCapture(t, goodOffer(), &posted, nil, nil)
			rc := validContact()
			tc.mutate(rc)
			_, err := c.Register(context.Background(), "", "example.com", confWithAccuracy(), RegisterOptions{Registrant: rc})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want rejection mentioning %q, got %v", tc.wantSub, err)
			}
			if posted {
				t.Fatalf("%s: must NOT POST on invalid contact", tc.name)
			}
			// The error must name the FIELD, never leak a value.
			if strings.Contains(err.Error(), "ada@example.com") || strings.Contains(err.Error(), "Ada Lovelace") {
				t.Fatalf("validation error leaked PII: %v", err)
			}
		})
	}
}

// --- CF error that echoes the contact => PII redacted -------------------------

func TestRegister_CFErrorEchoesContact_Redacted(t *testing.T) {
	posted := false
	rc := validContact()
	// CF returns an error message containing the registrant's email + name.
	regResp := func() string {
		return `{"success":false,"errors":[{"code":1004,"message":"invalid contact ada@example.com for Ada Lovelace at 12 Analytical Engine Way"}],"result":null}`
	}
	c := registrarMockCapture(t, goodOffer(), &posted, nil, regResp)
	_, err := c.Register(context.Background(), "", "example.com", confWithAccuracy(), RegisterOptions{Registrant: rc})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, pii := range []string{"ada@example.com", "Ada Lovelace", "12 Analytical Engine Way", "+1.5555550123"} {
		if strings.Contains(err.Error(), pii) {
			t.Fatalf("CF error leaked PII %q: %v", pii, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redaction markers, got %v", err)
	}
}

// --- redactRegistrantErr is a no-op-safe helper -------------------------------

func TestRedactRegistrantErr_Nil(t *testing.T) {
	c := &Client{}
	if got := c.redactRegistrantErr(nil, validContact()); got != nil {
		t.Fatalf("nil err should stay nil, got %v", got)
	}
	// nil contact must not panic.
	err := errors.New("boom")
	if got := c.redactRegistrantErr(err, nil); got.Error() != "boom" {
		t.Fatalf("nil contact: got %v", got)
	}
}
