package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerGrantsAndEmailTools registers slice-5c additive side-effect tools:
// role-grant, service-account-create-key, email-send.
// NO revoke / delete-key / role-bind (those remove access ≈ delete, excluded).
func registerGrantsAndEmailTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerRoleGrant(server, adapter)
	// role-bind is intentionally NOT exposed: it has replace-all (revoke-by-
	// replacement) semantics — submitting a partial/empty role set would
	// implicitly revoke a user's other roles, which violates the "no revoke /
	// no delete" policy. See ψ/memory/learnings/role_bind_deferred_replace_semantics.md.
	registerServiceAccountCreateKey(server, adapter)
	registerEmailSend(server, adapter)
}

// ===== Role grant =====
//
// Annotation rationale: grant ADDs an access grant (no removal) → destructive:false.

type RoleGrantInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Role    string `json:"role" jsonschema:"role sid to grant"`
	Email   string `json:"email" jsonschema:"user email to grant the role to"`
}

func registerRoleGrant(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-grant",
		Description: "Grant a role to a user (additive; does not remove other roles).",
		// Grants real access — mark destructive so MCP clients confirm before the call.
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RoleGrantInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		role := strings.TrimSpace(in.Role)
		email := strings.TrimSpace(in.Email)
		if project == "" {
			return nil, ResourceActionOutput{}, errProjectRequired()
		}
		if role == "" {
			return nil, ResourceActionOutput{}, errArgRequired("role")
		}
		if email == "" {
			return nil, ResourceActionOutput{}, errArgRequired("email")
		}
		if _, err := adapter.Client().Role().Grant(ctx, &api.RoleGrant{Project: project, Role: role, Email: email}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		out := ResourceActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Resource:        "role", Name: role, Action: "grant", Success: true,
		}
		return textResult(fmt.Sprintf("granted role %s to %s [project=%s]", role, email, project)), out, nil
	})
}

// ===== ServiceAccount create-key =====
//
// NOTE on key material: the pinned upstream ServiceAccount.CreateKey returns
// *Empty — the client does NOT surface any generated key/secret. So there is no
// one-time key to reveal here; this tool only triggers key creation and reports
// success. (If upstream later returns the key, decide one-time-reveal handling
// then.) Annotation: additive (adds a key) → destructive:false.

type SACreateKeyInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	ID      string `json:"id" jsonschema:"service account sid"`
}

func registerServiceAccountCreateKey(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service-account-create-key",
		Description: "Create a new key for a service account. Note: the pinned API does not return the key material in the response.",
		// Mints a real credential — mark destructive so MCP clients confirm first.
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SACreateKeyInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		id := strings.TrimSpace(in.ID)
		if project == "" {
			return nil, ResourceActionOutput{}, errProjectRequired()
		}
		if id == "" {
			return nil, ResourceActionOutput{}, errArgRequired("id")
		}
		if _, err := adapter.Client().ServiceAccount().CreateKey(ctx, &api.ServiceAccountCreateKey{Project: project, ID: id}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		out := ResourceActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Resource:        "serviceAccountKey", Name: id, Action: "create", Success: true,
		}
		return textResult(fmt.Sprintf("created key for service account %s [project=%s]", id, project)), out, nil
	})
}

// ===== Email send =====
//
// Annotation rationale: sends a message (external side-effect) but removes
// nothing → destructive:false. openWorldHint omitted (defaults true), which is
// apt for outbound email.

type EmailAddrInput struct {
	Email string `json:"email" jsonschema:"email address"`
	Name  string `json:"name,omitempty" jsonschema:"display name"`
}

type EmailSendInput struct {
	Project     string           `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	From        EmailAddrInput   `json:"from" jsonschema:"sender address"`
	To          []EmailAddrInput `json:"to" jsonschema:"recipient addresses (at least one)"`
	Subject     string           `json:"subject" jsonschema:"email subject"`
	ContentType string           `json:"contentType" jsonschema:"body content type: text/plain or text/html"`
	Body        string           `json:"body" jsonschema:"email body content"`
}

type EmailSendOutput struct {
	ResolvedContext
	To      []string `json:"to" jsonschema:"recipient addresses"`
	Subject string   `json:"subject" jsonschema:"subject sent"`
	Success bool     `json:"success" jsonschema:"whether the send was accepted"`
}

func registerEmailSend(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "email-send",
		Description: "Send an email from a project. contentType is text/plain or text/html.",
		// Sends a real email (an outward side effect) — mark destructive so MCP
		// clients confirm before the call.
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailSendInput) (*mcp.CallToolResult, EmailSendOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, EmailSendOutput{}, errProjectRequired()
		}
		if strings.TrimSpace(in.From.Email) == "" {
			return nil, EmailSendOutput{}, fmt.Errorf("from.email required")
		}
		if len(in.To) == 0 {
			return nil, EmailSendOutput{}, fmt.Errorf("at least one recipient required")
		}
		to := make([]api.EmailAddr, 0, len(in.To))
		toEmails := make([]string, 0, len(in.To))
		for _, t := range in.To {
			to = append(to, api.EmailAddr{Email: t.Email, Name: t.Name})
			toEmails = append(toEmails, t.Email)
		}
		m := &api.EmailSend{
			Project: project,
			From:    api.EmailAddr{Email: in.From.Email, Name: in.From.Name},
			To:      to,
			Subject: in.Subject,
			Body:    api.EmailBody{Type: api.EmailType(in.ContentType), Content: in.Body},
		}
		if _, err := adapter.Client().Email().Send(ctx, m); err != nil {
			return nil, EmailSendOutput{}, adapter.Redact(err)
		}
		out := EmailSendOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			To:              toEmails,
			Subject:         in.Subject,
			Success:         true,
		}
		return textResult(fmt.Sprintf("sent email %q to %d recipient(s) [project=%s]", in.Subject, len(toEmails), project)), out, nil
	})
}
