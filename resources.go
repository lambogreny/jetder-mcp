package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// MCP Resources expose read-only context a client can preload without calling a
// tool. We keep them minimal and PII-safe:
//
//   - jetder://status — current readiness (auth/project/location/Cloudflare/pull-secret).
//     Rendered from the SAME logic as the check-setup tool (buildSetupReport), but
//     through a MASKED markdown view: a client may auto-preload a resource, so the
//     content must never include the authenticated email, token, or any secret.
//   - jetder://help — static usage/quick-start text.
//   - jetder://projects — the projects accessible to the user, as a SAFE list
//     (id/project/name only). Deliberately drops billingAccount, webhookUrl (which
//     can carry a secret), quota, config, and createdAt — a resource may be
//     auto-preloaded, so the surface is kept minimal and secret-free.
const (
	statusResourceURI   = "jetder://status"
	helpResourceURI     = "jetder://help"
	projectsResourceURI = "jetder://projects"
)

// registerResources adds the read-only MCP resources. cf may be nil.
func registerResources(server *mcp.Server, adapter *jetder.Adapter, cf *cloudflare.Client) {
	server.AddResource(&mcp.Resource{
		URI:         statusResourceURI,
		Name:        "status",
		Title:       "Jetder setup status",
		Description: "Current readiness to deploy / point a domain: Jetder auth, the resolved project/location, Cloudflare configuration, and the pull-secret. Same checks as the check-setup tool, with secrets and the account email masked.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		report := buildSetupReport(ctx, adapter, cf, CheckSetupInput{})
		md := renderStatusMarkdown(report)
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      statusResourceURI,
				MIMEType: "text/markdown",
				Text:     md,
			}},
		}, nil
	})

	server.AddResource(&mcp.Resource{
		URI:         helpResourceURI,
		Name:        "help",
		Title:       "jetder-mcp help",
		Description: "Quick-start and usage notes for jetder-mcp: required environment, how to get access, and where to find the docs.",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      helpResourceURI,
				MIMEType: "text/markdown",
				Text:     helpMarkdown,
			}},
		}, nil
	})

	server.AddResource(&mcp.Resource{
		URI:         projectsResourceURI,
		Name:        "projects",
		Title:       "Jetder projects",
		Description: "The Jetder projects accessible to the authenticated user, as a safe list of id/project/name only (no billing, webhook, or other sensitive fields).",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      projectsResourceURI,
				MIMEType: "application/json",
				Text:     renderProjectsJSON(ctx, adapter),
			}},
		}, nil
	})
}

// ProjectResourceItem is the SAFE projection of a project for the resource — only
// non-sensitive identifiers. It is deliberately separate from ProjectItem (which
// carries billingAccount) so a sensitive field can never be added by accident.
type ProjectResourceItem struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Name    string `json:"name"`
}

// projectsResourcePayload is the JSON body of jetder://projects. On a backend
// failure it carries an empty list + a redacted error note (the resource exists;
// only the live read failed) — never a ReadResource transport error.
type projectsResourcePayload struct {
	Projects []ProjectResourceItem `json:"projects"`
	Count    int                   `json:"count"`
	Error    string                `json:"error,omitempty"`
}

// renderProjectsJSON lists the user's projects and marshals the safe payload. Any
// list error is redacted and returned as content (not an error), so an
// auto-preloading client gets a usable, secret-free body either way.
func renderProjectsJSON(ctx context.Context, adapter *jetder.Adapter) string {
	payload := projectsResourcePayload{Projects: []ProjectResourceItem{}}

	res, err := adapter.Client().Project().List(ctx, nil)
	if err != nil {
		payload.Error = adapter.Redact(err).Error()
		return marshalProjectsPayload(payload)
	}
	for _, x := range res.Items {
		// Copy ONLY the safe identifiers — never billingAccount/webhookUrl/etc.
		payload.Projects = append(payload.Projects, ProjectResourceItem{
			ID:      fmt.Sprintf("%d", x.ID),
			Project: x.Project,
			Name:    x.Name,
		})
	}
	payload.Count = len(payload.Projects)
	return marshalProjectsPayload(payload)
}

// marshalProjectsPayload renders the payload as indented JSON. On the (unlikely)
// marshal error it returns a minimal hand-built JSON object so the content is
// always valid JSON and never leaks anything.
func marshalProjectsPayload(p projectsResourcePayload) string {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return `{"projects":[],"count":0,"error":"failed to render projects"}`
	}
	return string(b)
}

// renderStatusMarkdown renders the readiness report as markdown WITHOUT leaking any
// secret or PII. It deliberately ignores SetupCheck.Detail (the auth-ok detail
// embeds the account email; other details are safe but we keep the rule simple:
// detail is never emitted) and prints only the check name, status, kyc, resolved
// project/location, counts, overall readiness, and the (secret-free) remediation.
func renderStatusMarkdown(r CheckSetupOutput) string {
	var b strings.Builder
	b.WriteString("# Jetder setup status\n\n")
	ready := "not ready"
	if r.OverallReady {
		ready = "ready"
	}
	fmt.Fprintf(&b, "**Overall:** %s — %d failing, %d warning.\n\n", ready, r.Fails, r.Warns)

	proj := r.ResolvedProject
	if proj == "" {
		proj = "(none)"
	}
	loc := r.ResolvedLocation
	if loc == "" {
		loc = "(none)"
	}
	fmt.Fprintf(&b, "- Project: `%s`\n- Location: `%s`\n\n", proj, loc)

	b.WriteString("## Checks\n\n")
	for _, c := range r.Checks {
		// name + status only — never the Detail (may contain the account email).
		fmt.Fprintf(&b, "- **%s**: %s\n", c.Name, c.Status)
		if c.Remediation != "" && (c.Status == statusFail || c.Status == statusWarn) {
			// Remediation text is static/secret-free (env var names, the owner
			// contact URL) — safe to surface and useful for fixing the issue.
			fmt.Fprintf(&b, "  - %s\n", c.Remediation)
		}
	}
	return b.String()
}

// helpMarkdown is the static jetder://help content. It contains only public info
// (env var names, the owner contact, doc links) — no secrets or paste-bait values.
const helpMarkdown = `# jetder-mcp

An MCP server that exposes the Jetder API to AI agents — deployments, domains,
DNS, billing, secrets, and more, over stdio.

## Required environment

- ` + "`JETDER_AUTH_USER`" + ` — your service-account email (the Basic-auth username).
- ` + "`JETDER_TOKEN`" + ` — your Jetder API token (the Basic-auth password).

Optional: ` + "`JETDER_DEFAULT_PROJECT`, `JETDER_DEFAULT_LOCATION`" + ` (fallbacks when
a tool's project/location is omitted), and the Cloudflare variables for the domain
tools. The full list is in docs/CREDENTIALS.md.

## Get access

You need a Jetder token (and a project) from the owner. Request one at
https://thunder.in.th/

## Verify your setup

Read the ` + "`jetder://status`" + ` resource, or run the ` + "`check-setup`" + ` tool,
to see whether your auth, project/location, Cloudflare config, and pull-secret are
ready — with a remediation for anything missing.

## Docs

- README — quick start and "Add to your MCP client".
- docs/CREDENTIALS.md — every environment variable the server reads.
- docs/CLOUDFLARE-SETUP.md — Cloudflare token/account setup and the point-a-domain prompt.
- docs/DEPLOY.md — build → push → deploy via MCP.
`
