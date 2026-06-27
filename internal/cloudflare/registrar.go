package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// price is a monetary amount that Cloudflare returns as a JSON STRING (e.g.
// "10.46"), though some fields/responses may use a number. It decodes both so the
// purchase guard always has a real numeric value (a wrong/zero price = money
// danger). An empty/absent value decodes to 0.
type price float64

func (p *price) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` || s == "" {
		*p = 0
		return nil
	}
	// strip surrounding quotes if it's a JSON string.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("invalid price %q: %v", s, err)
	}
	// Reject non-finite values: NaN/Inf would slip past the guard's comparisons
	// (every comparison with NaN is false), so the purchase could proceed on an
	// invalid price. Fail closed on money data.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("invalid price %q: not a finite number", s)
	}
	*p = price(f)
	return nil
}

// Float returns the amount as a float64.
func (p price) Float() float64 { return float64(p) }

// Registrar (beta) endpoints are account-scoped and require a token with the
// Registrar Write permission. See
// ψ/memory/learnings/cloudflare_registrar_api_endpoints.md.

// ErrNoAccountID is returned when a Registrar call needs an account id but none
// is configured (env CLOUDFLARE_ACCOUNT_ID or an explicit arg).
var ErrNoAccountID = errors.New("cloudflare: account id required for Registrar (set CLOUDFLARE_ACCOUNT_ID)")

// account resolves the account id: explicit arg wins, else the configured env.
func (c *Client) account(arg string) (string, error) {
	if a := strings.TrimSpace(arg); a != "" {
		return a, nil
	}
	if c.accountID != "" {
		return c.accountID, nil
	}
	return "", ErrNoAccountID
}

// Pricing mirrors the Cloudflare Registrar pricing object.
type Pricing struct {
	Currency         string `json:"currency"`
	RegistrationCost price  `json:"registration_cost"` // CF returns this as a string
	RenewalCost      price  `json:"renewal_cost"`
}

// DomainOffer is one search/check result.
type DomainOffer struct {
	Name        string  `json:"name"`
	Registrable bool    `json:"registrable"`
	Tier        string  `json:"tier"`
	Pricing     Pricing `json:"pricing"`
	Reason      string  `json:"reason,omitempty"` // when not registrable
}

// SearchDomains returns domain ideas for a keyword (readOnly).
func (c *Client) SearchDomains(ctx context.Context, accountArg, q string, limit int) ([]DomainOffer, error) {
	acct, err := c.account(accountArg)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("q", q)
	if limit > 0 {
		v.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out struct {
		Domains []DomainOffer `json:"domains"`
	}
	path := "/accounts/" + url.PathEscape(acct) + "/registrar/domain-search?" + v.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}

// CheckDomains checks availability + price for exact domains (readOnly). This is
// the SOURCE OF TRUTH for price and registrability immediately before a buy.
func (c *Client) CheckDomains(ctx context.Context, accountArg string, domains []string) ([]DomainOffer, error) {
	acct, err := c.account(accountArg)
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, errors.New("at least one domain required")
	}
	body := map[string]any{"domains": domains}
	var out struct {
		Domains []DomainOffer `json:"domains"`
	}
	path := "/accounts/" + url.PathEscape(acct) + "/registrar/domain-check"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}

// RegistrationResult is the register/status response.
type RegistrationResult struct {
	DomainName string `json:"domain_name"`
	State      string `json:"state"`
	Completed  bool   `json:"completed"`
}

// Registration workflow states.
const (
	StatePending        = "pending"
	StateInProgress     = "in_progress"
	StateActionRequired = "action_required"
	StateBlocked        = "blocked"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
)

// RegisterOptions are the optional registration parameters.
type RegisterOptions struct {
	AutoRenew   bool
	PrivacyMode string // e.g. "redaction"
	Years       int    // 1..10

	// Registrant, when non-nil, supplies the registrant contact inline (sent as
	// contacts.registrant in the registration body). When nil, the registration
	// falls back to the Cloudflare account's default address book — the original
	// behavior. CF derives the admin/tech/billing contacts from the registrant.
	Registrant *RegistrantContact
}

// RegistrantContact is the registrant (legal domain owner) contact submitted to
// the registry. These are real, legally-binding WHOIS details — inaccurate data
// can lead to domain suspension. The fields mirror CF's contacts.registrant shape.
type RegistrantContact struct {
	Name         string // full legal name (required)
	Organization string // optional (companies only)
	Email        string // required, email format
	Phone        string // required, E.164 with dot: +{cc}.{number}
	Fax          string // optional, same format as Phone
	Street       string // required
	City         string // required
	State        string // required (standard abbreviation where applicable)
	PostalCode   string // required
	CountryCode  string // required, ISO 3166-1 alpha-2 (canonicalized to upper)
}

// field length caps — generous but bounded to avoid sending absurd payloads.
const (
	maxContactNameLen   = 255
	maxContactEmailLen  = 254
	maxContactPhoneLen  = 32
	maxContactStreetLen = 255
	maxContactCityLen   = 128
	maxContactStateLen  = 128
	maxContactPostalLen = 32
	maxContactOrgLen    = 255
)

var (
	// reContactEmail is a pragmatic email check (not full RFC 5322).
	reContactEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	// reContactPhone is CF's dotted E.164: +{country}.{number}.
	reContactPhone = regexp.MustCompile(`^\+\d{1,3}\.\d{4,15}$`)
	// reContactCountry is an ISO 3166-1 alpha-2 code (canonicalized to upper).
	reContactCountry = regexp.MustCompile(`^[A-Z]{2}$`)
)

// normalize trims every field and uppercases the country code. It does NOT
// otherwise massage values (e.g. it won't reformat a phone number).
func (rc *RegistrantContact) normalize() {
	rc.Name = strings.TrimSpace(rc.Name)
	rc.Organization = strings.TrimSpace(rc.Organization)
	rc.Email = strings.TrimSpace(rc.Email)
	rc.Phone = strings.TrimSpace(rc.Phone)
	rc.Fax = strings.TrimSpace(rc.Fax)
	rc.Street = strings.TrimSpace(rc.Street)
	rc.City = strings.TrimSpace(rc.City)
	rc.State = strings.TrimSpace(rc.State)
	rc.PostalCode = strings.TrimSpace(rc.PostalCode)
	rc.CountryCode = strings.ToUpper(strings.TrimSpace(rc.CountryCode))
}

// Validate fail-closes on any missing/malformed required field. Error messages
// name the offending FIELD, never its value (PII never appears in errors). It
// normalizes (trims, uppercases country) in place as a side effect.
func (rc *RegistrantContact) Validate() error {
	rc.normalize()
	type fld struct {
		name  string
		value string
		max   int
	}
	required := []fld{
		{"registrant.name", rc.Name, maxContactNameLen},
		{"registrant.email", rc.Email, maxContactEmailLen},
		{"registrant.phone", rc.Phone, maxContactPhoneLen},
		{"registrant.street", rc.Street, maxContactStreetLen},
		{"registrant.city", rc.City, maxContactCityLen},
		{"registrant.state", rc.State, maxContactStateLen},
		{"registrant.postalCode", rc.PostalCode, maxContactPostalLen},
		{"registrant.countryCode", rc.CountryCode, 2},
	}
	for _, f := range required {
		if f.value == "" {
			return fmt.Errorf("registrant contact incomplete: %s is required", f.name)
		}
		if len(f.value) > f.max {
			return fmt.Errorf("registrant contact invalid: %s exceeds %d chars", f.name, f.max)
		}
	}
	if len(rc.Organization) > maxContactOrgLen {
		return fmt.Errorf("registrant contact invalid: registrant.organization exceeds %d chars", maxContactOrgLen)
	}
	if !reContactEmail.MatchString(rc.Email) {
		return errors.New("registrant contact invalid: registrant.email is not a valid email")
	}
	if !reContactPhone.MatchString(rc.Phone) {
		return errors.New("registrant contact invalid: registrant.phone must be E.164 with a dot, e.g. +1.5555555555")
	}
	if rc.Fax != "" {
		if len(rc.Fax) > maxContactPhoneLen || !reContactPhone.MatchString(rc.Fax) {
			return errors.New("registrant contact invalid: registrant.fax must be E.164 with a dot, e.g. +1.5555555555")
		}
	}
	if !reContactCountry.MatchString(rc.CountryCode) {
		return errors.New("registrant contact invalid: registrant.countryCode must be an ISO 3166-1 alpha-2 code, e.g. US")
	}
	return nil
}

// body builds the CF contacts.registrant JSON object from a validated contact.
func (rc *RegistrantContact) body() map[string]any {
	addr := map[string]any{
		"street":       rc.Street,
		"city":         rc.City,
		"state":        rc.State,
		"postal_code":  rc.PostalCode,
		"country_code": rc.CountryCode,
	}
	postal := map[string]any{
		"name":    rc.Name,
		"address": addr,
	}
	if rc.Organization != "" {
		postal["organization"] = rc.Organization
	}
	reg := map[string]any{
		"email":       rc.Email,
		"phone":       rc.Phone,
		"postal_info": postal,
	}
	if rc.Fax != "" {
		reg["fax"] = rc.Fax
	}
	return map[string]any{"registrant": reg}
}

// piiValues returns the contact's sensitive substrings worth redacting from any
// error/log surface. Short, high-collision fields (country code, state) are
// excluded — replacing "US"/"TX" everywhere would corrupt unrelated text.
func (rc *RegistrantContact) piiValues() []string {
	if rc == nil {
		return nil
	}
	cands := []string{rc.Name, rc.Organization, rc.Email, rc.Phone, rc.Fax, rc.Street, rc.City, rc.PostalCode}
	out := make([]string, 0, len(cands))
	for _, v := range cands {
		// Only redact reasonably distinctive values (>=4 chars) to avoid noise.
		if len(strings.TrimSpace(v)) >= 4 {
			out = append(out, v)
		}
	}
	return out
}

// redactRegistrantErr strips the contact's PII (and CF credentials) from err.
// It is per-call (the contact is a parameter, not global mutable state).
func (c *Client) redactRegistrantErr(err error, rc *RegistrantContact) error {
	if err == nil {
		return nil
	}
	s := c.Redact(err.Error()) // credentials first
	for _, v := range rc.piiValues() {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	if s != err.Error() {
		return errors.New(s)
	}
	return err
}

// PurchaseConfirmation carries the explicit, fail-closed confirmation a caller
// MUST provide to spend money. Every field is checked by Register.
type PurchaseConfirmation struct {
	// ConfirmText must equal exactly "REGISTER <domain>".
	ConfirmText string
	// MaxRegistrationCost / Currency: the buy is rejected if the live TOTAL cost
	// (registration_cost × years) exceeds this, or the currency differs.
	MaxRegistrationCost float64
	Currency            string
	// AcceptNonRefundable must be true — domain registration is non-refundable.
	AcceptNonRefundable bool
	// AllowPremium must be true to permit a non-"standard" tier.
	AllowPremium bool
	// AcceptAutoRenew must be true to enable auto-renew (a recurring future
	// billing commitment). Required only when RegisterOptions.AutoRenew is set.
	AcceptAutoRenew bool
	// AcceptRegistrantAccuracy must be true to submit an inline/env registrant
	// contact: the data is legally binding and inaccurate WHOIS details can lead
	// to domain suspension. Required ONLY when RegisterOptions.Registrant is set;
	// the account-default (no inline contact) path does not need a fresh ack.
	AcceptRegistrantAccuracy bool
}

// ConfirmTextFor returns the exact phrase the caller must supply.
func ConfirmTextFor(domain string) string { return "REGISTER " + domain }

// Register registers (BUYS) a single domain. It is DESTRUCTIVE and BILLABLE, and
// fail-closed: it re-checks the live price/registrability immediately before
// buying and rejects unless every guard in conf passes. It registers exactly one
// domain per call and never auto-retries a 202 (callers poll RegistrationStatus).
func (c *Client) Register(ctx context.Context, accountArg, domain string, conf PurchaseConfirmation, opts RegisterOptions) (*RegistrationResult, error) {
	acct, err := c.account(accountArg)
	if err != nil {
		return nil, err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("domain required")
	}

	// ---- purchase guard (fail-closed; check BEFORE any spend) ----
	if conf.ConfirmText != ConfirmTextFor(domain) {
		return nil, fmt.Errorf("purchase rejected: confirmText must be exactly %q", ConfirmTextFor(domain))
	}
	if !conf.AcceptNonRefundable {
		return nil, errors.New("purchase rejected: acceptNonRefundable must be true (registration is non-refundable)")
	}
	if conf.Currency == "" || conf.MaxRegistrationCost <= 0 {
		return nil, errors.New("purchase rejected: maxRegistrationCost and currency are required")
	}
	if opts.Years != 0 && (opts.Years < 1 || opts.Years > 10) {
		return nil, errors.New("purchase rejected: years must be 1..10")
	}
	// AutoRenew is a recurring future billing commitment — require explicit ack.
	if opts.AutoRenew && !conf.AcceptAutoRenew {
		return nil, errors.New("purchase rejected: autoRenew requires acceptAutoRenew=true (recurring future billing)")
	}

	// ---- registrant contact (inline/env) validation — BEFORE any network call,
	// so malformed PII is never sent to CF (not even to the price check). ----
	if opts.Registrant != nil {
		if !conf.AcceptRegistrantAccuracy {
			return nil, errors.New("purchase rejected: a registrant contact requires acceptRegistrantAccuracy=true (legally-binding WHOIS data; inaccuracy can cause suspension)")
		}
		if err := opts.Registrant.Validate(); err != nil {
			// validate() never includes field VALUES, only field names.
			return nil, fmt.Errorf("purchase rejected: %v", err)
		}
	}

	// fresh price/availability check — SOURCE OF TRUTH right before buying.
	offers, err := c.CheckDomains(ctx, acct, []string{domain})
	if err != nil {
		return nil, fmt.Errorf("purchase rejected: pre-buy check failed: %v", err)
	}
	var offer *DomainOffer
	for i := range offers {
		if strings.EqualFold(offers[i].Name, domain) {
			offer = &offers[i]
			break
		}
	}
	if offer == nil {
		return nil, fmt.Errorf("purchase rejected: domain %q not found in check response", domain)
	}
	if !offer.Registrable {
		reason := offer.Reason
		if reason == "" {
			reason = "not registrable"
		}
		return nil, fmt.Errorf("purchase rejected: %q is not registrable (%s)", domain, reason)
	}
	if offer.Pricing.Currency == "" || !strings.EqualFold(offer.Pricing.Currency, conf.Currency) {
		return nil, fmt.Errorf("purchase rejected: currency drift/missing — live %q vs accepted %q", offer.Pricing.Currency, conf.Currency)
	}
	// Fail-closed on missing/garbage price data — never spend on incomplete data.
	regCost := offer.Pricing.RegistrationCost.Float()
	if regCost <= 0 {
		return nil, fmt.Errorf("purchase rejected: live registration cost missing/invalid (%.2f)", regCost)
	}
	// Fail-closed on missing tier — do NOT assume "standard" from an empty field.
	if offer.Tier == "" {
		return nil, errors.New("purchase rejected: domain tier unknown (missing from check); refusing to assume standard")
	}
	if !strings.EqualFold(offer.Tier, "standard") && !conf.AllowPremium {
		return nil, fmt.Errorf("purchase rejected: %q is a %q-tier (premium) domain; set allowPremium to proceed", domain, offer.Tier)
	}
	// domain-check returns a 1-year quote (no years param); the buy can be multi-
	// year, so compare TOTAL cost (registration_cost × years) against the accepted
	// max. Otherwise a years=N buy would spend N× the confirmed amount.
	years := opts.Years
	if years == 0 {
		years = 1
	}
	totalCost := regCost * float64(years)
	if totalCost > conf.MaxRegistrationCost {
		return nil, fmt.Errorf("purchase rejected: total cost %.2f %s (%.2f × %d yr) exceeds max %.2f %s",
			totalCost, offer.Pricing.Currency, regCost, years, conf.MaxRegistrationCost, conf.Currency)
	}

	// ---- guards passed: spend ----
	body := map[string]any{"domain_name": domain}
	if opts.AutoRenew {
		body["auto_renew"] = true
	}
	if opts.PrivacyMode != "" {
		body["privacy_mode"] = opts.PrivacyMode
	}
	if opts.Years != 0 {
		body["years"] = opts.Years
	}
	// Inline registrant contact (already validated above). Omitted when nil, so
	// the account-default address book is used — unchanged legacy behavior.
	if opts.Registrant != nil {
		body["contacts"] = opts.Registrant.body()
	}

	var res RegistrationResult
	path := "/accounts/" + url.PathEscape(acct) + "/registrar/registrations"
	if err := c.do(ctx, http.MethodPost, path, body, &res); err != nil {
		// Redact the contact PII (and credentials) from any CF error surface.
		return nil, c.redactRegistrantErr(err, opts.Registrant)
	}
	if res.DomainName == "" {
		res.DomainName = domain
	}
	return &res, nil
}

// RegistrationStatus polls a registration workflow (readOnly). Never re-POSTs.
func (c *Client) RegistrationStatus(ctx context.Context, accountArg, domain string) (*RegistrationResult, error) {
	acct, err := c.account(accountArg)
	if err != nil {
		return nil, err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("domain required")
	}
	var res RegistrationResult
	path := "/accounts/" + url.PathEscape(acct) + "/registrar/registrations/" + url.PathEscape(domain) + "/registration-status"
	if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	if res.DomainName == "" {
		res.DomainName = domain
	}
	return &res, nil
}
