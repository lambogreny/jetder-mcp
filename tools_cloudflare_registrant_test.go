package main

import (
	"strings"
	"testing"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

// fullArg is a complete inline registrant input.
func fullArg() *RegistrantInput {
	return &RegistrantInput{
		Name: "Arg Name", Email: "arg@example.com", Phone: "+1.5555550001",
		Street: "1 Arg St", City: "ArgCity", State: "AS", PostalCode: "11111", CountryCode: "US",
	}
}

func TestResolveRegistrant_NoneIsNil(t *testing.T) {
	// No arg, no env => nil (use account default).
	rc, intent := resolveRegistrant(nil)
	if rc != nil || intent {
		t.Fatalf("expected (nil,false), got (%v,%v)", rc, intent)
	}
}

func TestResolveRegistrant_EnvFull(t *testing.T) {
	t.Setenv(cloudflare.EnvRegistrantName, "Env Name")
	t.Setenv(cloudflare.EnvRegistrantEmail, "env@example.com")
	t.Setenv(cloudflare.EnvRegistrantPhone, "+44.2071234567")
	t.Setenv(cloudflare.EnvRegistrantStreet, "1 Env Rd")
	t.Setenv(cloudflare.EnvRegistrantCity, "EnvCity")
	t.Setenv(cloudflare.EnvRegistrantState, "EC")
	t.Setenv(cloudflare.EnvRegistrantPostalCode, "EC1A1BB")
	t.Setenv(cloudflare.EnvRegistrantCountryCode, "GB")

	rc, intent := resolveRegistrant(nil)
	if !intent || rc == nil {
		t.Fatal("expected a contact from env")
	}
	if rc.Name != "Env Name" || rc.Email != "env@example.com" || rc.CountryCode != "GB" {
		t.Fatalf("env not picked up: %+v", rc)
	}
	if err := rc.Validate(); err != nil {
		t.Fatalf("env contact should validate: %v", err)
	}
}

func TestResolveRegistrant_ArgOverridesEnv(t *testing.T) {
	// Env present but the arg overrides per-field.
	t.Setenv(cloudflare.EnvRegistrantName, "Env Name")
	t.Setenv(cloudflare.EnvRegistrantEmail, "env@example.com")
	t.Setenv(cloudflare.EnvRegistrantCountryCode, "GB")

	rc, intent := resolveRegistrant(fullArg())
	if !intent || rc == nil {
		t.Fatal("expected a contact")
	}
	if rc.Name != "Arg Name" || rc.Email != "arg@example.com" || rc.CountryCode != "US" {
		t.Fatalf("arg should win over env: %+v", rc)
	}
}

func TestResolveRegistrant_ArgFillsEnvGaps(t *testing.T) {
	// Mixed: arg gives some fields, env fills the rest (per-field merge).
	t.Setenv(cloudflare.EnvRegistrantStreet, "1 Env Rd")
	t.Setenv(cloudflare.EnvRegistrantCity, "EnvCity")
	t.Setenv(cloudflare.EnvRegistrantState, "EC")
	t.Setenv(cloudflare.EnvRegistrantPostalCode, "EC1A1BB")
	t.Setenv(cloudflare.EnvRegistrantCountryCode, "GB")

	rc, intent := resolveRegistrant(&RegistrantInput{
		Name: "Mixed Name", Email: "mix@example.com", Phone: "+1.5555550002",
	})
	if !intent || rc == nil {
		t.Fatal("expected a contact")
	}
	if rc.Name != "Mixed Name" || rc.Street != "1 Env Rd" || rc.CountryCode != "GB" {
		t.Fatalf("merge wrong: %+v", rc)
	}
}

// A partial contact (some fields, missing required ones) still signals intent —
// so the caller requires accuracy ack and validation fails (no silent default).
func TestResolveRegistrant_PartialSignalsIntent(t *testing.T) {
	rc, intent := resolveRegistrant(&RegistrantInput{Email: "only@example.com"})
	if !intent || rc == nil {
		t.Fatal("a partial contact must signal intent (not fall through to default)")
	}
	if err := rc.Validate(); err == nil {
		t.Fatal("partial contact must fail validation")
	} else if strings.Contains(err.Error(), "only@example.com") {
		t.Fatalf("validation error leaked PII: %v", err)
	}
}
