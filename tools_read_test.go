package main

import (
	"testing"

	"github.com/jetder-core/api"
)

func TestToLocationItem(t *testing.T) {
	in := &api.LocationItem{
		ID:           "loc-1",
		Provider:     "gcp",
		Region:       "asia",
		CPUType:      "amd",
		DomainSuffix: ".jetder.com",
		Endpoint:     "https://loc-1",
		CName:        "loc-1.cname",
		FreeTier:     true,
	}
	got := toLocationItem(in)
	if got.ID != "loc-1" || got.Provider != "gcp" || got.Region != "asia" ||
		got.CPUType != "amd" || got.DomainSuffix != ".jetder.com" ||
		got.Endpoint != "https://loc-1" || got.CName != "loc-1.cname" || !got.FreeTier {
		t.Fatalf("toLocationItem mismatch: %+v", got)
	}
}

func TestToProjectItem_IDsStringified(t *testing.T) {
	in := &api.ProjectItem{
		ID:             1234567890123,
		Project:        "my-proj",
		Name:           "My Project",
		BillingAccount: 987654321,
	}
	got := toProjectItem(in)
	// int64 ids must be rendered as decimal strings (no scientific notation / precision loss).
	if got.ID != "1234567890123" {
		t.Fatalf("ID = %q, want 1234567890123", got.ID)
	}
	if got.BillingAccount != "987654321" {
		t.Fatalf("BillingAccount = %q, want 987654321", got.BillingAccount)
	}
	if got.Project != "my-proj" || got.Name != "My Project" {
		t.Fatalf("project/name mismatch: %+v", got)
	}
}
