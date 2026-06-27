package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registrarMock serves domain-check (with the given offer) and registrations
// (records whether a buy POST happened).
func registrarMock(t *testing.T, checkOffer DomainOffer, posted *bool) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/registrar/domain-check"):
			_, _ = w.Write([]byte(ok(map[string]any{"domains": []DomainOffer{checkOffer}})))
		case strings.HasSuffix(r.URL.Path, "/registrar/registrations"):
			if posted != nil {
				*posted = true
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

func goodOffer() DomainOffer {
	return DomainOffer{Name: "example.com", Registrable: true, Tier: "standard",
		Pricing: Pricing{Currency: "USD", RegistrationCost: 10.0, RenewalCost: 10.0}}
}

func goodConf() PurchaseConfirmation {
	return PurchaseConfirmation{
		ConfirmText: "REGISTER example.com", MaxRegistrationCost: 12.0,
		Currency: "USD", AcceptNonRefundable: true,
	}
}

func TestRegister_Success(t *testing.T) {
	posted := false
	c := registrarMock(t, goodOffer(), &posted)
	res, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !posted {
		t.Fatal("expected a register POST")
	}
	if res.State != StateInProgress {
		t.Fatalf("state = %q", res.State)
	}
}

// TestRegister_GuardTruthTable: every guard violation must REJECT before any POST.
func TestRegister_GuardTruthTable(t *testing.T) {
	cases := []struct {
		name  string
		offer DomainOffer
		conf  PurchaseConfirmation
		opts  RegisterOptions
		want  string // substring of the rejection
	}{
		{"wrong confirmText", goodOffer(), func() PurchaseConfirmation { c := goodConf(); c.ConfirmText = "register example.com"; return c }(), RegisterOptions{}, "confirmText"},
		{"no acceptNonRefundable", goodOffer(), func() PurchaseConfirmation { c := goodConf(); c.AcceptNonRefundable = false; return c }(), RegisterOptions{}, "non-refundable"},
		{"no max/currency", goodOffer(), func() PurchaseConfirmation { c := goodConf(); c.MaxRegistrationCost = 0; return c }(), RegisterOptions{}, "maxRegistrationCost"},
		{"price too high", DomainOffer{Name: "example.com", Registrable: true, Tier: "standard", Pricing: Pricing{Currency: "USD", RegistrationCost: 99.0}}, goodConf(), RegisterOptions{}, "exceeds max"},
		{"currency drift", DomainOffer{Name: "example.com", Registrable: true, Tier: "standard", Pricing: Pricing{Currency: "EUR", RegistrationCost: 10.0}}, goodConf(), RegisterOptions{}, "currency drift"},
		{"not registrable", DomainOffer{Name: "example.com", Registrable: false, Reason: "domain_unavailable"}, goodConf(), RegisterOptions{}, "not registrable"},
		{"premium not allowed", DomainOffer{Name: "example.com", Registrable: true, Tier: "premium", Pricing: Pricing{Currency: "USD", RegistrationCost: 10.0}}, goodConf(), RegisterOptions{}, "premium"},
		{"years out of range", goodOffer(), goodConf(), RegisterOptions{Years: 99}, "years must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			posted := false
			c := registrarMock(t, tc.offer, &posted)
			_, err := c.Register(context.Background(), "", "example.com", tc.conf, tc.opts)
			if err == nil {
				t.Fatalf("expected rejection, got success (posted=%v)", posted)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
			if posted {
				t.Fatalf("MONEY SPENT on a rejected purchase (%s)!", tc.name)
			}
		})
	}
}

// TestRegister_YearsTotalCost: domain-check is a 1-yr quote; multi-year must be
// compared as total. price=10, years=2 → total 20. max=12 reject (no POST); max=20 ok.
func TestRegister_YearsTotalCost(t *testing.T) {
	t.Run("years overspend rejected", func(t *testing.T) {
		posted := false
		c := registrarMock(t, goodOffer(), &posted) // price 10, max(conf)=12
		_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{Years: 2})
		if err == nil || !strings.Contains(err.Error(), "total cost") {
			t.Fatalf("expected total-cost rejection, got %v", err)
		}
		if posted {
			t.Fatal("MONEY SPENT: years=2 total 20 > max 12 should not POST")
		}
	})
	t.Run("years within max ok", func(t *testing.T) {
		posted := false
		c := registrarMock(t, goodOffer(), &posted)
		conf := goodConf()
		conf.MaxRegistrationCost = 20 // covers 10 × 2
		if _, err := c.Register(context.Background(), "", "example.com", conf, RegisterOptions{Years: 2}); err != nil {
			t.Fatalf("years=2 total 20 <= max 20 should succeed, got %v", err)
		}
		if !posted {
			t.Fatal("expected POST when total within max")
		}
	})
}

func TestRegister_AutoRenewRequiresAck(t *testing.T) {
	t.Run("autoRenew without ack rejected", func(t *testing.T) {
		posted := false
		c := registrarMock(t, goodOffer(), &posted)
		_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{AutoRenew: true})
		if err == nil || !strings.Contains(err.Error(), "acceptAutoRenew") {
			t.Fatalf("expected autoRenew-ack rejection, got %v", err)
		}
		if posted {
			t.Fatal("MONEY/COMMITMENT: autoRenew without ack should not POST")
		}
	})
	t.Run("autoRenew with ack ok", func(t *testing.T) {
		posted := false
		c := registrarMock(t, goodOffer(), &posted)
		conf := goodConf()
		conf.AcceptAutoRenew = true
		if _, err := c.Register(context.Background(), "", "example.com", conf, RegisterOptions{AutoRenew: true}); err != nil {
			t.Fatalf("autoRenew with ack should succeed, got %v", err)
		}
		if !posted {
			t.Fatal("expected POST with autoRenew ack")
		}
	})
}

func TestRegister_FailClosedMissingData(t *testing.T) {
	t.Run("missing price", func(t *testing.T) {
		posted := false
		offer := DomainOffer{Name: "example.com", Registrable: true, Tier: "standard", Pricing: Pricing{Currency: "USD", RegistrationCost: 0}}
		c := registrarMock(t, offer, &posted)
		_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{})
		if err == nil || !strings.Contains(err.Error(), "cost missing") {
			t.Fatalf("expected missing-price rejection, got %v", err)
		}
		if posted {
			t.Fatal("MONEY: must not POST on missing price")
		}
	})
	t.Run("missing tier", func(t *testing.T) {
		posted := false
		offer := DomainOffer{Name: "example.com", Registrable: true, Tier: "", Pricing: Pricing{Currency: "USD", RegistrationCost: 10}}
		c := registrarMock(t, offer, &posted)
		_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{})
		if err == nil || !strings.Contains(err.Error(), "tier unknown") {
			t.Fatalf("expected missing-tier rejection, got %v", err)
		}
		if posted {
			t.Fatal("must not POST on unknown tier")
		}
	})
}

func TestRegister_PremiumAllowed(t *testing.T) {
	posted := false
	offer := DomainOffer{Name: "example.com", Registrable: true, Tier: "premium",
		Pricing: Pricing{Currency: "USD", RegistrationCost: 10.0}}
	c := registrarMock(t, offer, &posted)
	conf := goodConf()
	conf.AllowPremium = true
	if _, err := c.Register(context.Background(), "", "example.com", conf, RegisterOptions{}); err != nil {
		t.Fatalf("premium with allowPremium should succeed, got %v", err)
	}
	if !posted {
		t.Fatal("expected POST")
	}
}

func TestRegister_NoAccountID(t *testing.T) {
	c := &Client{httpClient: http.DefaultClient, baseURL: "http://unused", token: "t"}
	if _, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{}); err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("expected account-id error, got %v", err)
	}
}

func TestCheckDomains_TokenRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"bad cf-tok"}]}`))
	}))
	defer srv.Close()
	c := &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "cf-tok", accountID: "a", redactions: []string{"Bearer cf-tok", "cf-tok"}}
	_, err := c.CheckDomains(context.Background(), "", []string{"example.com"})
	if err == nil || strings.Contains(err.Error(), "cf-tok") {
		t.Fatalf("token leak or no error: %v", err)
	}
}

func TestRegistrationStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("status must be GET (never re-POST)")
		}
		_, _ = w.Write([]byte(ok(RegistrationResult{DomainName: "example.com", State: StateSucceeded, Completed: true})))
	}))
	defer srv.Close()
	c := &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "t", accountID: "a", redactions: []string{"t"}}
	res, err := c.RegistrationStatus(context.Background(), "", "example.com")
	if err != nil || res.State != StateSucceeded || !res.Completed {
		t.Fatalf("status = %+v err=%v", res, err)
	}
}

// guard against accidental JSON shape drift in DomainOffer. Cloudflare returns
// price as a STRING ("9.50"); decode must yield the numeric value.
func TestDomainOffer_JSON_StringPrice(t *testing.T) {
	var o DomainOffer
	if err := json.Unmarshal([]byte(`{"name":"x.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":"9.50","renewal_cost":"12.00"}}`), &o); err != nil {
		t.Fatalf("decode string price failed: %v", err)
	}
	if o.Pricing.RegistrationCost.Float() != 9.5 || o.Pricing.RenewalCost.Float() != 12.0 {
		t.Fatalf("string price not parsed: %+v", o.Pricing)
	}
}

// also accept a numeric price (some fields/responses may use a number).
func TestDomainOffer_JSON_NumberPrice(t *testing.T) {
	var o DomainOffer
	if err := json.Unmarshal([]byte(`{"name":"x.com","pricing":{"currency":"USD","registration_cost":9.5}}`), &o); err != nil {
		t.Fatalf("decode number price failed: %v", err)
	}
	if o.Pricing.RegistrationCost.Float() != 9.5 {
		t.Fatalf("number price not parsed: %+v", o.Pricing)
	}
}

// TestPrice_RejectsNonFinite: NaN/Inf must fail to decode (they would slip past
// the guard's comparisons and allow a buy on an invalid price).
func TestPrice_RejectsNonFinite(t *testing.T) {
	for _, bad := range []string{`"NaN"`, `"+Inf"`, `"-Inf"`, `"Infinity"`} {
		var p price
		if err := json.Unmarshal([]byte(bad), &p); err == nil {
			t.Fatalf("price %s should fail to decode, got %v", bad, float64(p))
		}
	}
	// sanity: a finite string still works.
	var p price
	if err := json.Unmarshal([]byte(`"10.46"`), &p); err != nil || p.Float() != 10.46 {
		t.Fatalf("finite price failed: %v / %v", err, p.Float())
	}
}

// TestRegister_NonFiniteStringPriceRejectsNoPost: live check returns "NaN" → the
// pre-buy check must fail and NO register POST may happen.
func TestRegister_NonFiniteStringPriceRejectsNoPost(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domains":[{"name":"example.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":"NaN"}}]}}`))
			return
		}
		posted = true
		_, _ = w.Write([]byte(ok(RegistrationResult{})))
	}))
	defer srv.Close()
	c := &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "t", accountID: "a", redactions: []string{"t"}}
	_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{})
	if err == nil {
		t.Fatal("expected rejection on NaN price")
	}
	if posted {
		t.Fatal("MONEY: must not POST on non-finite price")
	}
}

// TestRegister_StringPriceGuard: the LIVE check returns a string price; the guard
// must compare it correctly (not treat it as 0 or error).
func TestRegister_StringPriceGuard(t *testing.T) {
	t.Run("string price within max → buy", func(t *testing.T) {
		posted := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") {
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domains":[{"name":"example.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":"10.46"}}]}}`))
				return
			}
			posted = true
			_, _ = w.Write([]byte(ok(RegistrationResult{DomainName: "example.com", State: StateInProgress})))
		}))
		defer srv.Close()
		c := &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "t", accountID: "a", redactions: []string{"t"}}
		conf := goodConf()
		conf.MaxRegistrationCost = 12.0 // covers 10.46
		if _, err := c.Register(context.Background(), "", "example.com", conf, RegisterOptions{}); err != nil {
			t.Fatalf("string price 10.46 <= max 12 should buy, got %v", err)
		}
		if !posted {
			t.Fatal("expected POST")
		}
	})
	t.Run("string price exceeds max → reject", func(t *testing.T) {
		posted := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/registrar/domain-check") {
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"domains":[{"name":"example.com","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":"99.99"}}]}}`))
				return
			}
			posted = true
			_, _ = w.Write([]byte(ok(RegistrationResult{})))
		}))
		defer srv.Close()
		c := &Client{httpClient: srv.Client(), baseURL: srv.URL, token: "t", accountID: "a", redactions: []string{"t"}}
		_, err := c.Register(context.Background(), "", "example.com", goodConf(), RegisterOptions{}) // max 12
		if err == nil || !strings.Contains(err.Error(), "exceeds max") {
			t.Fatalf("string price 99.99 should exceed max, got %v", err)
		}
		if posted {
			t.Fatal("MONEY: must not POST when string price exceeds max")
		}
	})
}
