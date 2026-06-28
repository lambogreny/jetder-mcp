package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// *-plan tools are READ-ONLY previews ("dry run") of a high-risk action: they show
// what WOULD happen — the resolved context, the side effects, the confirmations the
// real tool will require, and any missing prerequisites — WITHOUT performing it. They
// never POST/PUT/DELETE; at most they do read-only validation GETs. They never return
// success:true (nothing succeeded — nothing was done).

// PlanOutput is the common shape for every *-plan tool.
type PlanOutput struct {
	ResolvedContext
	// PlanOnly / WillExecute are always true / false — a plan never executes.
	PlanOnly    bool   `json:"planOnly" jsonschema:"always true — this is a preview"`
	WillExecute bool   `json:"willExecute" jsonschema:"always false — no changes are applied"`
	NextTool    string `json:"nextTool" jsonschema:"the tool that would actually perform this action"`
	Action      string `json:"action" jsonschema:"short human description of the planned action"`

	WouldBeDestructive bool    `json:"wouldBeDestructive" jsonschema:"true if executing would change/replace live state"`
	SpendsMoney        bool    `json:"spendsMoney" jsonschema:"true if executing would incur a charge"`
	EstimatedCost      float64 `json:"estimatedCost,omitempty" jsonschema:"estimated total cost if it spends money"`
	Currency           string  `json:"currency,omitempty" jsonschema:"currency of estimatedCost"`

	// RequestSummary is the exact (sanitized) request the real tool would send, so the
	// user can review the precise parameters before approving. Never contains secrets.
	RequestSummary        []string `json:"requestSummary" jsonschema:"the exact (sanitized) parameters the real call would use"`
	SideEffectsIfExecuted []string `json:"sideEffectsIfExecuted" jsonschema:"what executing would change"`
	RequiredConfirmations []string `json:"requiredConfirmations" jsonschema:"confirmations/guards the real tool requires"`
	ReadChecksPerformed   []string `json:"readChecksPerformed" jsonschema:"read-only checks this plan performed"`
	Warnings              []string `json:"warnings" jsonschema:"things to be aware of before executing"`
	MissingPrereqs        []string `json:"missingPrereqs" jsonschema:"prerequisites that appear to be missing"`
}

// newPlanOutput seeds a PlanOutput with the invariant fields + non-nil slices (so the
// output is never null arrays).
func newPlanOutput(project, location, nextTool, action string) PlanOutput {
	return PlanOutput{
		ResolvedContext:       ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
		PlanOnly:              true,
		WillExecute:           false,
		NextTool:              nextTool,
		Action:                action,
		RequestSummary:        []string{},
		SideEffectsIfExecuted: []string{},
		RequiredConfirmations: []string{},
		ReadChecksPerformed:   []string{},
		Warnings:              []string{},
		MissingPrereqs:        []string{},
	}
}

const planOnlyText = "PLAN ONLY — no changes applied"

// --- deployment-deploy-plan ---------------------------------------------------

// DeploymentDeployPlanInput mirrors DeploymentDeployInput (so the preview matches the
// real call exactly).
type DeploymentDeployPlanInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
	// Image is optional HERE (unlike deployment-deploy) so the plan can preview a call
	// and flag a missing image as a prerequisite rather than rejecting it outright.
	Image       string `json:"image,omitempty" jsonschema:"container image to deploy"`
	MinReplicas *int   `json:"minReplicas,omitempty" jsonschema:"minimum replicas"`
	MaxReplicas *int   `json:"maxReplicas,omitempty" jsonschema:"maximum replicas"`
	PullSecret  string `json:"pullSecret,omitempty" jsonschema:"name of an existing pull secret for private images"`
	// Env mirrors deployment-deploy. VALUES ARE NEVER previewed — only key names.
	AddEnv    map[string]string `json:"addEnv,omitempty" jsonschema:"env to add/merge (values are not previewed — only key names)"`
	Env       map[string]string `json:"env,omitempty" jsonschema:"env to REPLACE all with (values are not previewed — only key names)"`
	RemoveEnv []string          `json:"removeEnv,omitempty" jsonschema:"env var names to remove"`
	EnvGroups []string          `json:"envGroups,omitempty" jsonschema:"env group names to attach"`
}

func registerDeploymentDeployPlan(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentDeployPlanInput) (*mcp.CallToolResult, PlanOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, PlanOutput{}, err
		}

		out := newPlanOutput(project, location, "deployment-deploy",
			fmt.Sprintf("deploy image to deployment %q", name))
		out.WouldBeDestructive = true // a deploy replaces the running revision
		out.SideEffectsIfExecuted = append(out.SideEffectsIfExecuted,
			fmt.Sprintf("create a new revision of %q and roll it out (replacing the current running pods)", name))
		out.RequiredConfirmations = append(out.RequiredConfirmations,
			"this is a destructive action — your client should confirm before deployment-deploy")

		// Input validation (no network needed): image required, pull-secret name shape.
		if strings.TrimSpace(in.Image) == "" {
			out.MissingPrereqs = append(out.MissingPrereqs, "image is required")
		} else {
			out.Action = fmt.Sprintf("deploy %s to %q", in.Image, name)
		}
		pullSecret, perr := validatePullSecretName(in.PullSecret)
		if perr != nil {
			out.Warnings = append(out.Warnings, "pullSecret name looks invalid: "+perr.Error())
		}

		// Exact (sanitized) request the real deploy would send — for the user to review.
		// Only the pull-secret NAME is shown (never any secret value).
		out.RequestSummary = append(out.RequestSummary,
			"deployment="+name,
			"image="+strings.TrimSpace(in.Image))
		if b := strings.TrimSpace(in.Branch); b != "" {
			out.RequestSummary = append(out.RequestSummary, "branch="+b)
		}
		if in.MinReplicas != nil {
			out.RequestSummary = append(out.RequestSummary, fmt.Sprintf("minReplicas=%d", *in.MinReplicas))
		}
		if in.MaxReplicas != nil {
			out.RequestSummary = append(out.RequestSummary, fmt.Sprintf("maxReplicas=%d", *in.MaxReplicas))
		}
		if pullSecret != "" {
			out.RequestSummary = append(out.RequestSummary, "pullSecret(name)="+pullSecret)
		}
		// Env preview: KEY NAMES only — values are secrets and are never shown.
		if len(in.AddEnv) > 0 && len(in.Env) > 0 {
			out.Warnings = append(out.Warnings, "addEnv and env cannot be combined — the real deploy would reject this")
		}
		if len(in.AddEnv) > 0 {
			keys, bad := safeEnvKeys(sortedKeys(in.AddEnv))
			out.RequestSummary = append(out.RequestSummary, "addEnv keys=["+strings.Join(keys, ",")+"]")
			out.SideEffectsIfExecuted = append(out.SideEffectsIfExecuted,
				fmt.Sprintf("add/update %d environment variable(s)", len(in.AddEnv)))
			if bad {
				out.MissingPrereqs = append(out.MissingPrereqs, "an env name is malformed (e.g. contains '=') — the real deploy would reject it")
			}
		}
		if len(in.Env) > 0 {
			keys, bad := safeEnvKeys(sortedKeys(in.Env))
			out.RequestSummary = append(out.RequestSummary, "env(replace) keys=["+strings.Join(keys, ",")+"]")
			out.Warnings = append(out.Warnings, "env replaces ALL existing environment variables (use addEnv to merge instead)")
			if bad {
				out.MissingPrereqs = append(out.MissingPrereqs, "an env name is malformed (e.g. contains '=') — the real deploy would reject it")
			}
		}
		if len(in.RemoveEnv) > 0 {
			keys, _ := safeEnvKeys(in.RemoveEnv)
			out.RequestSummary = append(out.RequestSummary, "removeEnv=["+strings.Join(keys, ",")+"]")
		}
		if len(in.EnvGroups) > 0 {
			out.RequestSummary = append(out.RequestSummary, "envGroups=["+strings.Join(in.EnvGroups, ",")+"]")
		}

		// Read-only validation GET: does the named pull secret actually exist? (No
		// mutation.) Only when one is specified.
		if pullSecret != "" {
			_, gerr := adapter.Client().PullSecret().Get(ctx, &api.PullSecretGet{
				Project: project, Location: location, Name: pullSecret,
			})
			out.ReadChecksPerformed = append(out.ReadChecksPerformed,
				fmt.Sprintf("checked that pull secret %q exists", pullSecret))
			if gerr != nil {
				// Never echo the underlying error verbatim (could carry detail); a
				// redacted, plain note is enough.
				out.MissingPrereqs = append(out.MissingPrereqs,
					fmt.Sprintf("pull secret %q was not found (create it before deploying a private image)", pullSecret))
			}
		}

		if in.MinReplicas != nil && in.MaxReplicas != nil && *in.MinReplicas > *in.MaxReplicas {
			out.Warnings = append(out.Warnings, "minReplicas is greater than maxReplicas")
		}

		return textResult(planOnlyText + ": " + out.Action), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "deployment-deploy-plan",
		Description: "Preview ('dry run') a deployment-deploy WITHOUT applying it. Shows the resolved " +
			"project/location, what would change, the confirmations the real tool needs, and any missing " +
			"prerequisites (e.g. a missing pull secret). Read-only: it performs no writes. Use deployment-deploy " +
			"to actually deploy.",
		Annotations: readOnly(),
	}, handler)
}

// --- cf-domain-register-plan --------------------------------------------------

// CFDomainRegisterPlanInput mirrors the buy inputs (so the preview matches), but a
// plan never spends money, so the money-guard fields are previewed, not enforced.
type CFDomainRegisterPlanInput struct {
	Domain    string `json:"domain" jsonschema:"the single domain you are considering registering (BUY)"`
	AccountID string `json:"accountId,omitempty" jsonschema:"Cloudflare account id (falls back to CLOUDFLARE_ACCOUNT_ID)"`
	Years     int    `json:"years,omitempty" jsonschema:"registration years 1..10 (default 1)"`
	// Optional: if provided, the plan checks them against the live quote and warns on
	// mismatch — but it NEVER buys.
	MaxRegistrationCost float64 `json:"maxRegistrationCost,omitempty" jsonschema:"the max total cost you would accept (checked against the live quote)"`
	Currency            string  `json:"currency,omitempty" jsonschema:"the currency you would accept"`
	// Parity with cf-domain-register: previewed so the user sees what the real buy
	// will require. The plan NEVER applies any of these.
	AcceptPremium            bool             `json:"acceptPremium,omitempty" jsonschema:"would be required to register a premium-tier domain"`
	AutoRenew                bool             `json:"autoRenew,omitempty" jsonschema:"enable auto-renew (recurring billing) on the real buy"`
	AcceptAutoRenew          bool             `json:"acceptAutoRenew,omitempty" jsonschema:"acknowledge recurring auto-renew billing"`
	PrivacyMode              string           `json:"privacyMode,omitempty" jsonschema:"WHOIS privacy mode on the real buy, e.g. 'redaction'"`
	AcceptRegistrantAccuracy bool             `json:"acceptRegistrantAccuracy,omitempty" jsonschema:"acknowledge the registrant contact details are accurate (required only when you supply a registrant)"`
	Registrant               *RegistrantInput `json:"registrant,omitempty" jsonschema:"optional registrant contact; if omitted, inline/env fields or the Cloudflare account default are used"`
}

func registerCFDomainRegisterPlan(server *mcp.Server, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in CFDomainRegisterPlanInput) (*mcp.CallToolResult, PlanOutput, error) {
		if cf == nil {
			return nil, PlanOutput{}, errCFNotConfigured()
		}
		domain := strings.ToLower(strings.TrimSpace(in.Domain))
		if domain == "" {
			return nil, PlanOutput{}, fmt.Errorf("domain is required")
		}
		// years: <0 is invalid (don't silently default a negative to 1); 0 means "default
		// 1"; >10 is out of the registrar's accepted 1..10 range.
		years := in.Years
		negativeYears := years < 0
		if years <= 0 {
			years = 1
		}

		out := newPlanOutput("", "", "cf-domain-register",
			fmt.Sprintf("register (BUY) %s for %d year(s)", domain, years))
		out.WouldBeDestructive = true
		out.SpendsMoney = true
		out.SideEffectsIfExecuted = append(out.SideEffectsIfExecuted,
			fmt.Sprintf("purchase %s via Cloudflare Registrar (non-refundable)", domain))
		out.RequiredConfirmations = append(out.RequiredConfirmations,
			fmt.Sprintf("confirmText must equal exactly 'REGISTER %s'", domain),
			"maxRegistrationCost must be >= the live total price",
			"acceptNonRefundable must be true",
		)
		out.Warnings = append(out.Warnings, "registration spends money and is NON-REFUNDABLE")

		// years bound: the real registrar accepts 1..10. Flag an out-of-range value as
		// a missing prereq (the real buy would reject it) so years=-1 / years=99 aren't
		// shown valid.
		if negativeYears {
			out.MissingPrereqs = append(out.MissingPrereqs,
				fmt.Sprintf("years=%d is invalid — registration accepts 1..10 years", in.Years))
		} else if years > 10 {
			out.MissingPrereqs = append(out.MissingPrereqs,
				fmt.Sprintf("years=%d is out of range — registration accepts 1..10 years", years))
		}

		// Registrant ack parity: the real Register() ONLY requires acceptRegistrantAccuracy
		// when a registrant contact is actually supplied (inline arg or env). With no
		// contact, the Cloudflare account default is used. Detect via resolveRegistrant —
		// the SAME logic the real tool uses — WITHOUT echoing any PII.
		registrant, hasRegistrant := resolveRegistrant(in.Registrant)
		registrantSource := "Cloudflare account default"
		if hasRegistrant {
			registrantSource = "inline/env registrant contact"
			out.RequiredConfirmations = append(out.RequiredConfirmations,
				"acceptRegistrantAccuracy must be true (you are supplying a registrant contact)")
			if !in.AcceptRegistrantAccuracy {
				out.MissingPrereqs = append(out.MissingPrereqs,
					"a registrant contact is supplied but acceptRegistrantAccuracy is not set — the real buy would reject it")
			}
			// Validate the supplied contact the SAME way the real buy does. Validate()
			// returns field-name-only errors (never the value), so it is PII-safe to
			// surface. A partial/invalid contact would be rejected even with ack=true.
			if verr := registrant.Validate(); verr != nil {
				out.MissingPrereqs = append(out.MissingPrereqs, verr.Error())
			}
		} else {
			out.Warnings = append(out.Warnings,
				"no registrant contact supplied — the Cloudflare account default would be used (a default may be required on the account)")
		}

		// Parity previews: surface the recurring-billing acks the real buy needs.
		if in.AutoRenew {
			out.SideEffectsIfExecuted = append(out.SideEffectsIfExecuted,
				"enable AUTO-RENEW — recurring annual billing until cancelled")
			out.RequiredConfirmations = append(out.RequiredConfirmations,
				"acceptAutoRenew must be true to enable auto-renew")
			if !in.AcceptAutoRenew {
				out.Warnings = append(out.Warnings,
					"autoRenew requested but acceptAutoRenew is not set — the real buy would reject it")
			}
		}
		if pm := strings.TrimSpace(in.PrivacyMode); pm != "" {
			out.SideEffectsIfExecuted = append(out.SideEffectsIfExecuted,
				fmt.Sprintf("set WHOIS privacy mode = %q", pm))
		}

		// Exact (sanitized) request the real buy would send — NO PII (registrant is
		// reported only by SOURCE, never its field values).
		out.RequestSummary = append(out.RequestSummary,
			"domain="+domain, fmt.Sprintf("years=%d", years))
		if in.MaxRegistrationCost > 0 {
			out.RequestSummary = append(out.RequestSummary, fmt.Sprintf("maxRegistrationCost=%.2f", in.MaxRegistrationCost))
		}
		if c := strings.TrimSpace(in.Currency); c != "" {
			out.RequestSummary = append(out.RequestSummary, "currency="+c)
		}
		out.RequestSummary = append(out.RequestSummary,
			fmt.Sprintf("acceptPremium=%t", in.AcceptPremium),
			fmt.Sprintf("autoRenew=%t", in.AutoRenew),
			fmt.Sprintf("acceptAutoRenew=%t", in.AcceptAutoRenew))
		if pm := strings.TrimSpace(in.PrivacyMode); pm != "" {
			out.RequestSummary = append(out.RequestSummary, "privacyMode="+pm)
		}
		out.RequestSummary = append(out.RequestSummary, "registrantSource="+registrantSource)

		// Read-only: fresh availability + price from Cloudflare (the source of truth).
		res, err := cf.CheckDomains(ctx, in.AccountID, []string{domain})
		if err != nil {
			return nil, PlanOutput{}, err
		}
		out.ReadChecksPerformed = append(out.ReadChecksPerformed,
			"fetched live availability + price from Cloudflare Registrar")
		if len(res) == 0 {
			out.MissingPrereqs = append(out.MissingPrereqs, "no price/availability returned for this domain")
			return textResult(planOnlyText + ": " + out.Action), out, nil
		}
		offer := toCFOffer(res[0])
		unit := offer.RegistrationCost
		total := unit * float64(years)
		out.EstimatedCost = total
		out.Currency = offer.Currency
		out.Action = fmt.Sprintf("register %s for %d year(s) — est. %.2f %s total", domain, years, total, offer.Currency)

		if !offer.Registrable {
			note := "domain does not appear to be registrable"
			if offer.Reason != "" {
				note += " (" + offer.Reason + ")"
			}
			out.MissingPrereqs = append(out.MissingPrereqs, note)
		}
		if offer.Tier != "" && !strings.EqualFold(offer.Tier, "standard") {
			out.RequiredConfirmations = append(out.RequiredConfirmations,
				fmt.Sprintf("acceptPremium must be true (this is a %s-tier domain)", offer.Tier))
			if !in.AcceptPremium {
				out.MissingPrereqs = append(out.MissingPrereqs,
					fmt.Sprintf("this is a %s-tier domain but acceptPremium is not set — the real buy would reject it", offer.Tier))
			}
		}
		// If the caller previewed a budget, sanity-check it against the live quote.
		if in.MaxRegistrationCost > 0 && total > in.MaxRegistrationCost {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("live total (%.2f %s) exceeds your maxRegistrationCost (%.2f) — the buy would be rejected",
					total, offer.Currency, in.MaxRegistrationCost))
		}
		if in.Currency != "" && !strings.EqualFold(in.Currency, offer.Currency) {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("live currency is %s, not %s — the buy would be rejected", offer.Currency, in.Currency))
		}

		return textResult(planOnlyText + ": " + out.Action), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "cf-domain-register-plan",
		Description: "Preview ('dry run') a domain registration WITHOUT buying it. Fetches the live price/" +
			"availability from Cloudflare (read-only) and shows the estimated total, the money/confirmation " +
			"guards the real tool requires, and any issues — but never spends money. Use cf-domain-register to " +
			"actually buy.",
		Annotations: readOnly(),
	}, handler)
}
