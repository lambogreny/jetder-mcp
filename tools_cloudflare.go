package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

// registerCloudflareTools registers the cf-* tools. cf may be nil (Cloudflare not
// configured) — the tools still register but return a clear error when invoked,
// so a jetder-only server is unaffected.
func registerCloudflareTools(server *mcp.Server, cf *cloudflare.Client) {
	registerCFDomainSearch(server, cf)
	registerCFDomainCheck(server, cf)
	registerCFZoneLookup(server, cf)
	registerCFDNSList(server, cf)
	registerCFDNSCreate(server, cf)
	registerCFDomainRegister(server, cf)
	registerCFRegistrationStatus(server, cf)
}

// errCFNotConfigured is the user-facing message when CF env is missing.
func errCFNotConfigured() error {
	return fmt.Errorf("Cloudflare not configured: set %s (and %s for Registrar)", cloudflare.EnvToken, cloudflare.EnvAccountID)
}

// ===== read-only =====

type CFDomainSearchInput struct {
	Query     string `json:"query" jsonschema:"keyword to search domain ideas for"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max results"`
	AccountID string `json:"accountId,omitempty" jsonschema:"Cloudflare account id (falls back to CLOUDFLARE_ACCOUNT_ID)"`
}

type CFDomainOffer struct {
	Name             string  `json:"name"`
	Registrable      bool    `json:"registrable"`
	Tier             string  `json:"tier,omitempty"`
	Currency         string  `json:"currency,omitempty"`
	RegistrationCost float64 `json:"registrationCost,omitempty"`
	RenewalCost      float64 `json:"renewalCost,omitempty"`
	Reason           string  `json:"reason,omitempty"`
}

func toCFOffer(o cloudflare.DomainOffer) CFDomainOffer {
	return CFDomainOffer{
		Name: o.Name, Registrable: o.Registrable, Tier: o.Tier,
		Currency: o.Pricing.Currency, RegistrationCost: o.Pricing.RegistrationCost.Float(),
		RenewalCost: o.Pricing.RenewalCost.Float(), Reason: o.Reason,
	}
}

type CFOffersOutput struct {
	Domains []CFDomainOffer `json:"domains"`
}

func registerCFDomainSearch(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-domain-search",
		Description: "Search Cloudflare Registrar for available domain ideas by keyword (read-only).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFDomainSearchInput) (*mcp.CallToolResult, CFOffersOutput, error) {
		if cf == nil {
			return nil, CFOffersOutput{}, errCFNotConfigured()
		}
		res, err := cf.SearchDomains(ctx, in.AccountID, in.Query, in.Limit)
		if err != nil {
			return nil, CFOffersOutput{}, err
		}
		out := CFOffersOutput{Domains: make([]CFDomainOffer, 0, len(res))}
		for _, o := range res {
			out.Domains = append(out.Domains, toCFOffer(o))
		}
		return textResult(fmt.Sprintf("%d domain idea(s)", len(out.Domains))), out, nil
	})
}

type CFDomainCheckInput struct {
	Domains   []string `json:"domains" jsonschema:"exact domain names to check availability + price"`
	AccountID string   `json:"accountId,omitempty" jsonschema:"Cloudflare account id (falls back to CLOUDFLARE_ACCOUNT_ID)"`
}

func registerCFDomainCheck(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-domain-check",
		Description: "Check exact domains for availability and current price via Cloudflare Registrar (read-only). This is the source of truth for price before registering.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFDomainCheckInput) (*mcp.CallToolResult, CFOffersOutput, error) {
		if cf == nil {
			return nil, CFOffersOutput{}, errCFNotConfigured()
		}
		if len(in.Domains) == 0 {
			return nil, CFOffersOutput{}, fmt.Errorf("at least one domain required")
		}
		res, err := cf.CheckDomains(ctx, in.AccountID, in.Domains)
		if err != nil {
			return nil, CFOffersOutput{}, err
		}
		out := CFOffersOutput{Domains: make([]CFDomainOffer, 0, len(res))}
		for _, o := range res {
			out.Domains = append(out.Domains, toCFOffer(o))
		}
		return textResult(fmt.Sprintf("checked %d domain(s)", len(out.Domains))), out, nil
	})
}

type CFZoneLookupInput struct {
	Domain string `json:"domain" jsonschema:"a domain/record name; the owning zone is resolved by longest matching suffix"`
}

type CFZoneOutput struct {
	ZoneID   string `json:"zoneId"`
	ZoneName string `json:"zoneName"`
}

func registerCFZoneLookup(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-zone-lookup",
		Description: "Resolve the Cloudflare zone that owns a domain/record name (read-only).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFZoneLookupInput) (*mcp.CallToolResult, CFZoneOutput, error) {
		if cf == nil {
			return nil, CFZoneOutput{}, errCFNotConfigured()
		}
		z, err := cf.FindZoneForName(ctx, in.Domain)
		if err != nil {
			return nil, CFZoneOutput{}, err
		}
		return textResult(fmt.Sprintf("zone %s (%s)", z.Name, z.ID)), CFZoneOutput{ZoneID: z.ID, ZoneName: z.Name}, nil
	})
}

type CFDNSListInput struct {
	ZoneID string `json:"zoneId" jsonschema:"Cloudflare zone id"`
	Type   string `json:"type,omitempty" jsonschema:"filter by record type (A, AAAA, CNAME, TXT, ...)"`
	Name   string `json:"name,omitempty" jsonschema:"filter by record name"`
}

type CFDNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied bool   `json:"proxied"`
}

func toCFRecord(r cloudflare.DNSRecord) CFDNSRecord {
	return CFDNSRecord{ID: r.ID, Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Proxied: r.Proxied}
}

type CFDNSListOutput struct {
	Records []CFDNSRecord `json:"records"`
}

func registerCFDNSList(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-dns-list",
		Description: "List Cloudflare DNS records in a zone, optionally filtered by type/name (read-only).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFDNSListInput) (*mcp.CallToolResult, CFDNSListOutput, error) {
		if cf == nil {
			return nil, CFDNSListOutput{}, errCFNotConfigured()
		}
		if in.ZoneID == "" {
			return nil, CFDNSListOutput{}, fmt.Errorf("zoneId required")
		}
		res, err := cf.ListDNSRecords(ctx, in.ZoneID, in.Type, in.Name)
		if err != nil {
			return nil, CFDNSListOutput{}, err
		}
		out := CFDNSListOutput{Records: make([]CFDNSRecord, 0, len(res))}
		for _, r := range res {
			out.Records = append(out.Records, toCFRecord(r))
		}
		return textResult(fmt.Sprintf("%d record(s)", len(out.Records))), out, nil
	})
}

type CFRegistrationStatusInput struct {
	Domain    string `json:"domain" jsonschema:"domain whose registration workflow to poll"`
	AccountID string `json:"accountId,omitempty" jsonschema:"Cloudflare account id (falls back to CLOUDFLARE_ACCOUNT_ID)"`
}

type CFRegistrationOutput struct {
	Domain    string `json:"domain"`
	State     string `json:"state"`
	Completed bool   `json:"completed"`
}

func registerCFRegistrationStatus(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-registration-status",
		Description: "Poll the status of a Cloudflare domain registration workflow (read-only).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFRegistrationStatusInput) (*mcp.CallToolResult, CFRegistrationOutput, error) {
		if cf == nil {
			return nil, CFRegistrationOutput{}, errCFNotConfigured()
		}
		res, err := cf.RegistrationStatus(ctx, in.AccountID, in.Domain)
		if err != nil {
			return nil, CFRegistrationOutput{}, err
		}
		return textResult(fmt.Sprintf("%s state=%s completed=%t", res.DomainName, res.State, res.Completed)),
			CFRegistrationOutput{Domain: res.DomainName, State: res.State, Completed: res.Completed}, nil
	})
}

// ===== mutations =====

type CFDNSCreateInput struct {
	ZoneID  string `json:"zoneId,omitempty" jsonschema:"Cloudflare zone id; if empty, resolved from the record name"`
	Type    string `json:"type" jsonschema:"record type (A, AAAA, CNAME, TXT)"`
	Name    string `json:"name" jsonschema:"record name (fully-qualified)"`
	Content string `json:"content" jsonschema:"record content/value"`
}

type CFDNSCreateOutput struct {
	ZoneID        string      `json:"zoneId"`
	Record        CFDNSRecord `json:"record"`
	AlreadyExists bool        `json:"alreadyExists" jsonschema:"true if an identical record already existed (no change made)"`
}

func registerCFDNSCreate(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-dns-create",
		Description: "Create a Cloudflare DNS record (idempotent: skips if identical, errors on conflict; never overwrites). Defaults ttl=auto, proxied=false.",
		Annotations: destructive(), // public DNS change can route/break traffic
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFDNSCreateInput) (*mcp.CallToolResult, CFDNSCreateOutput, error) {
		if cf == nil {
			return nil, CFDNSCreateOutput{}, errCFNotConfigured()
		}
		if in.Type == "" || in.Name == "" || in.Content == "" {
			return nil, CFDNSCreateOutput{}, fmt.Errorf("type, name and content are required")
		}
		zoneID := in.ZoneID
		if zoneID == "" {
			z, err := cf.FindZoneForName(ctx, in.Name)
			if err != nil {
				return nil, CFDNSCreateOutput{}, err
			}
			zoneID = z.ID
		}
		res, err := cf.CreateDNSRecord(ctx, zoneID, cloudflare.DNSRecord{Type: in.Type, Name: in.Name, Content: in.Content})
		if err != nil {
			return nil, CFDNSCreateOutput{}, err
		}
		out := CFDNSCreateOutput{ZoneID: zoneID, Record: toCFRecord(res.Record), AlreadyExists: res.AlreadyExists}
		verb := "created"
		if res.AlreadyExists {
			verb = "already exists"
		}
		return textResult(fmt.Sprintf("%s %s %s [zone=%s]", verb, in.Type, in.Name, zoneID)), out, nil
	})
}

type CFDomainRegisterInput struct {
	Domain    string `json:"domain" jsonschema:"the single domain to register (BUY)"`
	AccountID string `json:"accountId,omitempty" jsonschema:"Cloudflare account id (falls back to CLOUDFLARE_ACCOUNT_ID)"`

	// Purchase guard (all required to spend money) — see cf-domain-check for price.
	ConfirmText         string  `json:"confirmText" jsonschema:"must equal exactly 'REGISTER <domain>'"`
	MaxRegistrationCost float64 `json:"maxRegistrationCost" jsonschema:"max TOTAL cost you accept (registrationCost x years)"`
	Currency            string  `json:"currency" jsonschema:"currency you accept, must match the live quote"`
	AcceptNonRefundable bool    `json:"acceptNonRefundable" jsonschema:"must be true; registration is non-refundable"`

	// Optional.
	Years           int    `json:"years,omitempty" jsonschema:"registration years 1..10 (default 1)"`
	AcceptPremium   bool   `json:"acceptPremium,omitempty" jsonschema:"required to register a premium-tier domain"`
	AutoRenew       bool   `json:"autoRenew,omitempty" jsonschema:"enable auto-renew (requires acceptAutoRenew)"`
	AcceptAutoRenew bool   `json:"acceptAutoRenew,omitempty" jsonschema:"must be true to enable autoRenew (recurring future billing)"`
	PrivacyMode     string `json:"privacyMode,omitempty" jsonschema:"e.g. 'redaction'"`

	// Registrant contact (optional). When provided (or any CLOUDFLARE_REGISTRANT_*
	// env var is set), the registrant is submitted to the registry and
	// acceptRegistrantAccuracy must be true. When omitted entirely, the Cloudflare
	// account's default address book is used. The contact is NEVER echoed back.
	Registrant               *RegistrantInput `json:"registrant,omitempty" jsonschema:"registrant (legal domain owner) contact; if omitted, falls back to CLOUDFLARE_REGISTRANT_* env, then the CF account default"`
	AcceptRegistrantAccuracy bool             `json:"acceptRegistrantAccuracy,omitempty" jsonschema:"must be true when supplying a registrant contact (data is legally binding; inaccurate WHOIS can cause suspension)"`
}

// RegistrantInput is the tool-facing registrant contact. Field VALUES are PII and
// are never returned in any output or error.
type RegistrantInput struct {
	Name         string `json:"name,omitempty" jsonschema:"full legal name of the registrant"`
	Organization string `json:"organization,omitempty" jsonschema:"organization/company name (optional)"`
	Email        string `json:"email,omitempty" jsonschema:"contact email"`
	Phone        string `json:"phone,omitempty" jsonschema:"phone in E.164 with a dot: +{countryCode}.{number} e.g. +1.5555555555"`
	Fax          string `json:"fax,omitempty" jsonschema:"fax in E.164 with a dot (optional)"`
	Street       string `json:"street,omitempty" jsonschema:"street address incl. building/suite"`
	City         string `json:"city,omitempty" jsonschema:"city or locality"`
	State        string `json:"state,omitempty" jsonschema:"state/province/region (standard abbreviation where applicable)"`
	PostalCode   string `json:"postalCode,omitempty" jsonschema:"postal or ZIP code"`
	CountryCode  string `json:"countryCode,omitempty" jsonschema:"ISO 3166-1 alpha-2 country code, e.g. US"`
}

// resolveRegistrant builds the registrant contact for a register call, preferring
// the inline arg fields over CLOUDFLARE_REGISTRANT_* env (per-field override). It
// returns (nil, false) when NEITHER arg nor env supplies any field — meaning "use
// the account default address book" (legacy behavior). The bool reports whether a
// contact intent was detected (so the handler can require acceptRegistrantAccuracy
// and the validator can flag a partial contact as an error, not a silent default).
func resolveRegistrant(in *RegistrantInput) (*cloudflare.RegistrantContact, bool) {
	pick := func(arg, env string) string {
		if v := strings.TrimSpace(arg); v != "" {
			return v
		}
		return strings.TrimSpace(os.Getenv(env))
	}
	var a RegistrantInput
	if in != nil {
		a = *in
	}
	rc := cloudflare.RegistrantContact{
		Name:         pick(a.Name, cloudflare.EnvRegistrantName),
		Organization: pick(a.Organization, cloudflare.EnvRegistrantOrg),
		Email:        pick(a.Email, cloudflare.EnvRegistrantEmail),
		Phone:        pick(a.Phone, cloudflare.EnvRegistrantPhone),
		Fax:          pick(a.Fax, cloudflare.EnvRegistrantFax),
		Street:       pick(a.Street, cloudflare.EnvRegistrantStreet),
		City:         pick(a.City, cloudflare.EnvRegistrantCity),
		State:        pick(a.State, cloudflare.EnvRegistrantState),
		PostalCode:   pick(a.PostalCode, cloudflare.EnvRegistrantPostalCode),
		CountryCode:  pick(a.CountryCode, cloudflare.EnvRegistrantCountryCode),
	}
	intent := rc.Name != "" || rc.Organization != "" || rc.Email != "" || rc.Phone != "" ||
		rc.Fax != "" || rc.Street != "" || rc.City != "" || rc.State != "" ||
		rc.PostalCode != "" || rc.CountryCode != ""
	if !intent {
		return nil, false
	}
	return &rc, true
}

func registerCFDomainRegister(server *mcp.Server, cf *cloudflare.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cf-domain-register",
		Description: "Register (BUY) a single domain via Cloudflare Registrar. SPENDS MONEY and is non-refundable. Requires an exact confirmText 'REGISTER <domain>', a max cost, currency, and acceptNonRefundable=true; the live price is re-checked and the buy is rejected on any drift. Returns the workflow state — poll cf-registration-status (do not call again).",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CFDomainRegisterInput) (*mcp.CallToolResult, CFRegistrationOutput, error) {
		if cf == nil {
			return nil, CFRegistrationOutput{}, errCFNotConfigured()
		}
		// Resolve the registrant contact from the arg (per-field) over the
		// CLOUDFLARE_REGISTRANT_* env. nil => use the account default address book.
		// Register() only consults AcceptRegistrantAccuracy when a contact is set.
		registrant, _ := resolveRegistrant(in.Registrant)

		conf := cloudflare.PurchaseConfirmation{
			ConfirmText:              in.ConfirmText,
			MaxRegistrationCost:      in.MaxRegistrationCost,
			Currency:                 in.Currency,
			AcceptNonRefundable:      in.AcceptNonRefundable,
			AllowPremium:             in.AcceptPremium,
			AcceptAutoRenew:          in.AcceptAutoRenew,
			AcceptRegistrantAccuracy: in.AcceptRegistrantAccuracy,
		}
		opts := cloudflare.RegisterOptions{AutoRenew: in.AutoRenew, PrivacyMode: in.PrivacyMode, Years: in.Years, Registrant: registrant}
		res, err := cf.Register(ctx, in.AccountID, in.Domain, conf, opts)
		if err != nil {
			return nil, CFRegistrationOutput{}, err
		}
		return textResult(fmt.Sprintf("register %s state=%s completed=%t (poll cf-registration-status)", res.DomainName, res.State, res.Completed)),
			CFRegistrationOutput{Domain: res.DomainName, State: res.State, Completed: res.Completed}, nil
	})
}
