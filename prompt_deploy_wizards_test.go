package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrompts_ListHasThree(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	lp, err := cs.ListPrompts(context.Background(), &mcp.ListPromptsParams{})
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	got := map[string]bool{}
	for _, p := range lp.Prompts {
		got[p.Name] = true
	}
	for _, want := range []string{"point-a-domain", "deploy-an-app", "bootstrap-pull-secret"} {
		if !got[want] {
			t.Fatalf("prompt %q missing; have %v", want, got)
		}
	}
}

func TestDeployAnApp_Playbook(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	gp, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "deploy-an-app",
		Arguments: map[string]string{"deployment": "hello", "image": "ghcr.io/lambogreny/app:abc123", "project": "p1"},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	text := gp.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"pull-secret-get", "deployment-deploy", "deployment-get",
		"ghcr.io/lambogreny/app:abc123", "hello", "ghcr-pull", "project=\"p1\"",
		"do not deploy again", "scripts/deploy.sh",
		// arch/amd64 gotcha precondition (guards against regression).
		"linux/amd64", "exec format error", "--platform linux/amd64"} {
		if !strings.Contains(text, want) {
			t.Fatalf("deploy-an-app playbook missing %q:\n%s", want, text)
		}
	}
	// step order: pull-secret-get before deployment-deploy before deployment-get.
	if !(strings.Index(text, "pull-secret-get") < strings.Index(text, "deployment-deploy") &&
		strings.Index(text, "deployment-deploy") < strings.Index(text, "deployment-get")) {
		t.Fatalf("steps out of order:\n%s", text)
	}
}

func TestDeployAnApp_ImageValidation(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	bad := []string{
		"nginx:latest",              // not ghcr.io
		"ghcr.io/owner/app",         // no tag/digest
		"ghcr.io/owner/app:la test", // space
		"ghcr.io/owner/app:l\"x",    // quote
		"ghcr.io/Owner/App:tag",     // uppercase path
	}
	for _, img := range bad {
		_, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
			Name:      "deploy-an-app",
			Arguments: map[string]string{"deployment": "hello", "image": img},
		})
		if err == nil {
			t.Fatalf("image %q should be rejected", img)
		}
	}
	// a valid digest ref is accepted.
	if _, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "deploy-an-app",
		Arguments: map[string]string{"deployment": "hello", "image": "ghcr.io/lambogreny/app@sha256:" + strings.Repeat("a", 64)},
	}); err != nil {
		t.Fatalf("valid digest image rejected: %v", err)
	}
}

// ⭐ The bootstrap prompt must never accept or render the PAT.
func TestBootstrap_NoSecretSurface(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))

	// schema must NOT have a password/token argument.
	lp, _ := cs.ListPrompts(context.Background(), &mcp.ListPromptsParams{})
	for _, p := range lp.Prompts {
		if p.Name != "bootstrap-pull-secret" {
			continue
		}
		for _, a := range p.Arguments {
			low := strings.ToLower(a.Name)
			if strings.Contains(low, "password") || strings.Contains(low, "token") || strings.Contains(low, "secret") || low == "pat" {
				t.Fatalf("bootstrap prompt must not have a secret arg, found %q", a.Name)
			}
		}
	}

	gp, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "bootstrap-pull-secret",
		Arguments: map[string]string{"githubUsername": "lambogreny"},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	text := gp.Messages[0].Content.(*mcp.TextContent).Text
	// must guide via the tool's password field + pre-filled link, with no PAT placeholder.
	for _, want := range []string{"pull-secret-create", "read:packages", "password field", "ghcr-pull", "lambogreny"} {
		if !strings.Contains(text, want) {
			t.Fatalf("bootstrap playbook missing %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"ghp_", "<PAT>", "<token>", "password=\""} {
		if strings.Contains(text, leak) {
			t.Fatalf("bootstrap playbook must not invite pasting a PAT (found %q):\n%s", leak, text)
		}
	}
}

func TestBootstrap_UsernameValidation(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	for _, u := range []string{"bad user", "has\"quote", "x\ny", "-leading", "way-too-long-username-way-too-long-username-x"} {
		_, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
			Name:      "bootstrap-pull-secret",
			Arguments: map[string]string{"githubUsername": u},
		})
		if err == nil {
			t.Fatalf("username %q should be rejected", u)
		}
	}
}

func TestDeployWizards_ErrorNamesPrompt(t *testing.T) {
	cs := connectWithCF(t, cfOK(`{"success":true,"result":[]}`))
	_, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "deploy-an-app",
		Arguments: map[string]string{"image": "ghcr.io/o/a:t"}, // missing deployment
	})
	if err == nil || !strings.Contains(err.Error(), "deploy-an-app") {
		t.Fatalf("error should name the prompt 'deploy-an-app', got %v", err)
	}
}
