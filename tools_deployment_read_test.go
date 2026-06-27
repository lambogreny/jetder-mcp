package main

import (
	"testing"

	"github.com/jetder-core/api"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

func TestToDeploymentItem(t *testing.T) {
	in := &api.DeploymentItem{
		Project:     "proj",
		Location:    "loc",
		Name:        "web",
		Type:        api.DeploymentTypeWebService,
		Revision:    7,
		Image:       "nginx:latest",
		MinReplicas: 1,
		MaxReplicas: 3,
		Port:        8080,
		URL:         "https://web.example",
	}
	got := toDeploymentItem(in)
	if got.Name != "web" || got.Type != "WebService" || got.Revision != 7 ||
		got.Image != "nginx:latest" || got.MinReplicas != 1 || got.MaxReplicas != 3 ||
		got.Port != 8080 || got.URL != "https://web.example" {
		t.Fatalf("toDeploymentItem mismatch: %+v", got)
	}
}

func TestResolveDeploymentTarget_Defaults(t *testing.T) {
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvDefaultProject, "def-proj")
	t.Setenv(jetder.EnvDefaultLocation, "def-loc")

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// empty project/location -> env defaults; name explicit.
	p, l, n, err := resolveDeploymentTarget(a, "", "", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != "def-proj" || l != "def-loc" || n != "web" {
		t.Fatalf("resolved = (%q,%q,%q), want (def-proj,def-loc,web)", p, l, n)
	}

	// explicit args override defaults.
	p, l, _, _ = resolveDeploymentTarget(a, "p2", "l2", "web")
	if p != "p2" || l != "l2" {
		t.Fatalf("override resolved = (%q,%q), want (p2,l2)", p, l)
	}
}

func TestResolveDeploymentTarget_MissingName(t *testing.T) {
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvDefaultProject, "p")
	t.Setenv(jetder.EnvDefaultLocation, "l")

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, _, err := resolveDeploymentTarget(a, "", "", ""); err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
}

func TestResolveDeploymentTarget_MissingProject(t *testing.T) {
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	// no defaults set
	t.Setenv(jetder.EnvDefaultProject, "")
	t.Setenv(jetder.EnvDefaultLocation, "")

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, _, err := resolveDeploymentTarget(a, "", "loc", "web"); err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
}

func TestResolveDeploymentTarget_MissingLocation(t *testing.T) {
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvDefaultProject, "")
	t.Setenv(jetder.EnvDefaultLocation, "")

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, _, err := resolveDeploymentTarget(a, "proj", "", "web"); err == nil {
		t.Fatal("expected error for missing location, got nil")
	}
}

func TestResolveDeploymentTarget_WhitespaceName(t *testing.T) {
	t.Setenv(jetder.EnvAuthUser, "ci@test.example")
	t.Setenv(jetder.EnvToken, "tok")
	t.Setenv(jetder.EnvDefaultProject, "p")
	t.Setenv(jetder.EnvDefaultLocation, "l")

	a, err := jetder.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, _, err := resolveDeploymentTarget(a, "", "", "   "); err == nil {
		t.Fatal("expected error for whitespace-only name, got nil")
	}
	// non-empty name gets trimmed.
	_, _, n, err := resolveDeploymentTarget(a, "", "", "  web  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != "web" {
		t.Fatalf("name = %q, want trimmed 'web'", n)
	}
}
