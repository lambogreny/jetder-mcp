package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// check-setup is a read-only "doctor" tool: an agent calls it to confirm whether
// the user's environment is ready to deploy / point a domain. It inspects ONLY
// what the MCP server itself can see — Jetder auth, the resolved project/location,
// Cloudflare configuration, and (optionally) a named pull-secret. It deliberately
// does NOT probe local CLIs (docker/nixpacks/gh): those live on the caller's
// machine, out of the server's reach, and are covered by the getting-started docs.
//
// Design invariants (codex-reviewed):
//   - Every prerequisite *failure* is a structured check result, never a tool
//     error. The handler returns a tool error only for a genuine internal bug.
//   - No secret value is ever returned: me-get exposes only email/KYC, the
//     pull-secret check reports presence (never the credential), and Cloudflare
//     is reported as configured/not without echoing the token.
//   - Defaults are not hidden: the resolved project/location are echoed.

// checkStatus is the outcome of a single preflight check.
type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
)

// ownerContactURL is where a user requests Jetder access (a token / a project).
// Surfaced when auth fails or no project is configured, so the user knows how to
// get onboarded rather than being left stuck.
const ownerContactURL = "https://thunder.in.th/"

// contactOwner appends the owner-contact hint to a remediation string.
func contactOwner(remediation string) string {
	return remediation + " To get access (a Jetder token or a project), contact the owner: " + ownerContactURL
}

// SetupCheck is one line item in the doctor report.
type SetupCheck struct {
	Name        string      `json:"name" jsonschema:"the check identifier (e.g. jetder-auth, project-location)"`
	Status      checkStatus `json:"status" jsonschema:"ok | warn | fail"`
	Detail      string      `json:"detail" jsonschema:"human-readable result (never contains secrets)"`
	Remediation string      `json:"remediation,omitempty" jsonschema:"how to fix a warn/fail (command, env var, or prompt to run)"`
}

// CheckSetupInput selects the deploy target to validate. All fields are optional;
// project/location fall back to the JETDER_DEFAULT_* env vars, and pullSecret
// defaults to "ghcr-pull" (the name the deploy docs bootstrap for private images).
type CheckSetupInput struct {
	Project    string `json:"project,omitempty" jsonschema:"project sid to validate (falls back to JETDER_DEFAULT_PROJECT)"`
	Location   string `json:"location,omitempty" jsonschema:"location id to validate (falls back to JETDER_DEFAULT_LOCATION)"`
	PullSecret string `json:"pullSecret,omitempty" jsonschema:"pull-secret NAME to check for (default \"ghcr-pull\"); needed for private-image deploys"`
}

// CheckSetupOutput is the full doctor report.
type CheckSetupOutput struct {
	ResolvedContext
	Checks       []SetupCheck `json:"checks" jsonschema:"the individual preflight checks"`
	OverallReady bool         `json:"overallReady" jsonschema:"true when no check failed (warnings do not block readiness)"`
	Fails        int          `json:"fails" jsonschema:"number of failing checks"`
	Warns        int          `json:"warns" jsonschema:"number of warning checks"`
}

const defaultPullSecretName = "ghcr-pull"

// registerCheckSetup adds the "check-setup" doctor tool. cf may be nil (Cloudflare
// not configured) — that is reported as a warning, not a failure.
// buildSetupReport runs every preflight check and returns the structured report.
// It is shared by the check-setup tool and the jetder://status resource so the two
// can never drift. Every prerequisite failure is a structured check (never an
// error), and credentials are redacted; note that the jetder-auth OK detail
// includes the authenticated email, so callers that may expose the report to a
// client (e.g. the resource) must render a MASKED view — see renderStatusMarkdown.
func buildSetupReport(ctx context.Context, adapter *jetder.Adapter, cf *cloudflare.Client, in CheckSetupInput) CheckSetupOutput {
	project := adapter.ResolveProject(in.Project)
	location := adapter.ResolveLocation(in.Location)

	out := CheckSetupOutput{
		ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
	}

	// 1. Jetder auth — call me.get (a safe GET). Note: the server cannot even
	// start without JETDER_AUTH_USER/JETDER_TOKEN, so this validates that the
	// configured credentials actually WORK after startup, not first-run setup.
	authOK := false
	kyc := false
	if item, err := adapter.Client().Me().Get(ctx, nil); err != nil {
		out.add(SetupCheck{
			Name:        "jetder-auth",
			Status:      statusFail,
			Detail:      "Jetder auth check failed: " + adapter.Redact(err).Error(),
			Remediation: contactOwner("Verify JETDER_AUTH_USER (service-account email) and JETDER_TOKEN are correct and the account is active."),
		})
	} else {
		authOK = true
		kyc = item.KYC
		out.add(SetupCheck{
			Name:   "jetder-auth",
			Status: statusOK,
			Detail: "authenticated as " + item.Email,
		})
		// 1b. KYC — informational. Some operations (e.g. billing/registrar)
		// require a completed KYC; surface it as a warning, not a blocker.
		if kyc {
			out.add(SetupCheck{Name: "jetder-kyc", Status: statusOK, Detail: "KYC completed"})
		} else {
			out.add(SetupCheck{
				Name:        "jetder-kyc",
				Status:      statusWarn,
				Detail:      "KYC not completed — some operations (billing, domain registration) may be unavailable.",
				Remediation: "Complete KYC in the Jetder console if you need paid/registrar features.",
			})
		}
	}

	// 2. Project / location — both are required to deploy. Empty => fail.
	if project == "" {
		out.add(SetupCheck{
			Name:        "project",
			Status:      statusFail,
			Detail:      "no project resolved",
			Remediation: contactOwner("Set JETDER_DEFAULT_PROJECT or pass a project argument."),
		})
	} else {
		out.add(SetupCheck{Name: "project", Status: statusOK, Detail: "project=" + project})
	}
	if location == "" {
		out.add(SetupCheck{
			Name:        "location",
			Status:      statusFail,
			Detail:      "no location resolved",
			Remediation: "Set JETDER_DEFAULT_LOCATION or pass a location argument.",
		})
	} else {
		out.add(SetupCheck{Name: "location", Status: statusOK, Detail: "location=" + location})
	}

	// 3. Cloudflare — config presence only (no live API call; lazy/optional).
	switch {
	case cf == nil:
		out.add(SetupCheck{
			Name:        "cloudflare",
			Status:      statusWarn,
			Detail:      "Cloudflare not configured — domain tools are unavailable.",
			Remediation: fmt.Sprintf("Set %s (and %s for buying domains via Registrar) to enable domain tools.", cloudflare.EnvToken, cloudflare.EnvAccountID),
		})
	case strings.TrimSpace(cf.AccountID()) == "":
		out.add(SetupCheck{
			Name:        "cloudflare",
			Status:      statusWarn,
			Detail:      "Cloudflare token configured, but no account id — DNS/zone tools work; the Registrar (buy domain) is unavailable.",
			Remediation: fmt.Sprintf("Set %s to enable domain registration.", cloudflare.EnvAccountID),
		})
	default:
		out.add(SetupCheck{Name: "cloudflare", Status: statusOK, Detail: "Cloudflare configured (token + account id)"})
	}

	// 4. Pull-secret — only meaningful once auth works and a project/location
	// is known. Needed for private-image deploys; absence is a warning (public
	// images still deploy). The credential VALUE is never read or returned.
	out.add(checkPullSecret(ctx, adapter, project, location, authOK, in.PullSecret))

	out.finalize()
	return out
}

func registerCheckSetup(server *mcp.Server, adapter *jetder.Adapter, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in CheckSetupInput) (*mcp.CallToolResult, CheckSetupOutput, error) {
		out := buildSetupReport(ctx, adapter, cf, in)
		summary := fmt.Sprintf("ready=%t fail=%d warn=%d", out.OverallReady, out.Fails, out.Warns)
		return textResult(summary), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "check-setup",
		Description: "Preflight doctor: report whether this environment is ready to deploy / point a domain. " +
			"Checks Jetder auth, the resolved project/location, Cloudflare configuration, and a pull-secret " +
			"(default \"ghcr-pull\") for private images. Each result is ok/warn/fail with remediation. " +
			"It cannot see local tools (docker/nixpacks/gh) — for those, see the getting-started guide.",
		Annotations: readOnly(),
	}, handler)
}

// checkPullSecret validates that the named pull-secret exists. It is gated behind
// a working auth + a resolved project/location (a lookup is pointless otherwise).
// A genuine "not found" is a warning (public images don't need a pull-secret); any
// other error (permission/network) is a failure, because a private-image deploy
// would then behave unpredictably. The name is validated first so a credential
// accidentally passed in JETDER pull-secret slot is rejected without echoing it.
func checkPullSecret(ctx context.Context, adapter *jetder.Adapter, project, location string, authOK bool, requested string) SetupCheck {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = defaultPullSecretName
	}
	// Reject a credential-shaped value WITHOUT echoing it (PAT-leak guard).
	validName, err := validatePullSecretName(name)
	if err != nil {
		return SetupCheck{
			Name:        "pull-secret",
			Status:      statusFail,
			Detail:      "invalid pullSecret: must be a pull-secret NAME (3-25 chars), not a credential.",
			Remediation: "Pass the pull-secret's NAME (e.g. ghcr-pull), never a token.",
		}
	}

	if !authOK {
		return SetupCheck{
			Name:        "pull-secret",
			Status:      statusWarn,
			Detail:      "skipped: Jetder auth must pass first.",
			Remediation: "Fix the jetder-auth check, then re-run.",
		}
	}
	if project == "" || location == "" {
		return SetupCheck{
			Name:        "pull-secret",
			Status:      statusWarn,
			Detail:      "skipped: a project and location are required to look up a pull-secret.",
			Remediation: "Resolve project/location, then re-run.",
		}
	}

	_, err = adapter.Client().PullSecret().Get(ctx, &api.PullSecretGet{Project: project, Location: location, Name: validName})
	switch {
	case err == nil:
		return SetupCheck{Name: "pull-secret", Status: statusOK, Detail: fmt.Sprintf("pull-secret %q present", validName)}
	case errors.Is(err, api.ErrPullSecretNotFound):
		return SetupCheck{
			Name:        "pull-secret",
			Status:      statusWarn,
			Detail:      fmt.Sprintf("pull-secret %q not found — required only for PRIVATE images.", validName),
			Remediation: "For a private image, bootstrap the pull-secret (see the getting-started guide). Public images need no pull-secret.",
		}
	default:
		return SetupCheck{
			Name:        "pull-secret",
			Status:      statusFail,
			Detail:      "pull-secret lookup failed: " + adapter.Redact(err).Error(),
			Remediation: "Check Jetder permissions/network, then re-run.",
		}
	}
}

// add appends a check to the report.
func (o *CheckSetupOutput) add(c SetupCheck) { o.Checks = append(o.Checks, c) }

// finalize tallies warn/fail counts and computes readiness (ready = no fail).
func (o *CheckSetupOutput) finalize() {
	for _, c := range o.Checks {
		switch c.Status {
		case statusFail:
			o.Fails++
		case statusWarn:
			o.Warns++
		}
	}
	o.OverallReady = o.Fails == 0
}
