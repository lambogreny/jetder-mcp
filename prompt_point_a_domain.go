package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Validation patterns for prompt args. These bound what gets interpolated into the
// playbook so a malicious argument cannot inject instructions (esp. on the money
// path). Anything with control chars / newlines / quotes is rejected outright.
var (
	reDomain     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)
	reName       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`) // deployment/project/location ids
	rePathSafe   = regexp.MustCompile(`^/[a-zA-Z0-9._~!$&'()*+,;=:@%/-]*$`)
	reControlChr = regexp.MustCompile(`[\x00-\x1f\x7f"'` + "`" + `]`)
)

// validateArg trims and validates a prompt argument against pat. Empty input is
// allowed only when required is false (returns ""). On any control char/quote or
// pattern mismatch it errors — never lets unvalidated text reach the playbook.
func validateArg(field, v string, required bool, pat *regexp.Regexp) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return "", fmt.Errorf("point-a-domain requires the %q argument", field)
		}
		return "", nil
	}
	if reControlChr.MatchString(v) {
		return "", fmt.Errorf("invalid %q: must not contain quotes, newlines or control characters", field)
	}
	if pat != nil && !pat.MatchString(v) {
		return "", fmt.Errorf("invalid %q: %q is not a valid value", field, v)
	}
	return v, nil
}

// registerPointADomainPrompt adds the "point-a-domain" MCP Prompt: a single
// user-role playbook that guides an agent to point a custom domain at a Jetder
// deployment end-to-end using THIS server's own tools (jetder + cf-*).
func registerPointADomainPrompt(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "point-a-domain",
		Title:       "Point a custom domain at a Jetder deployment",
		Description: "Step-by-step playbook to point (and optionally register) a custom domain at a Jetder deployment: optional Cloudflare registration, jetder domain-create, DNS records via Cloudflare, verification, and routing.",
		Arguments: []*mcp.PromptArgument{
			{Name: "domain", Description: "the custom domain to point (e.g. app.example.com)", Required: true},
			{Name: "deployment", Description: "the Jetder deployment name to route to", Required: true},
			{Name: "project", Description: "Jetder project sid (optional; falls back to JETDER_DEFAULT_PROJECT)"},
			{Name: "location", Description: "Jetder location id (optional; falls back to JETDER_DEFAULT_LOCATION)"},
			{Name: "path", Description: "optional route path prefix (must start with /)"},
			{Name: "registerDomain", Description: "set to \"true\" to include buying the domain via Cloudflare Registrar first (default false)"},
		},
	}, pointADomainHandler)
}

func pointADomainHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := map[string]string{}
	if req != nil && req.Params != nil && req.Params.Arguments != nil {
		args = req.Params.Arguments
	}

	domain, err := validateArg("domain", args["domain"], true, reDomain)
	if err != nil {
		return nil, err
	}
	// Canonicalize to lowercase so confirmText ("REGISTER <domain>") matches the
	// registrar guard, which lowercases the domain before comparing.
	domain = strings.ToLower(domain)
	deployment, err := validateArg("deployment", args["deployment"], true, reName)
	if err != nil {
		return nil, err
	}
	project, err := validateArg("project", args["project"], false, reName)
	if err != nil {
		return nil, err
	}
	location, err := validateArg("location", args["location"], false, reName)
	if err != nil {
		return nil, err
	}
	path, err := validateArg("path", args["path"], false, rePathSafe)
	if err != nil {
		return nil, err
	}
	register := strings.EqualFold(strings.TrimSpace(args["registerDomain"]), "true")

	// projectArg/locationArg render either an explicit `key="value"` or an
	// instruction to OMIT the arg (so the tool's env default applies). Never emit
	// a placeholder literal as if it were a real value.
	projectArg := argOrOmit("project", project)
	locationArg := argOrOmit("location", location)
	pathArg := ""
	if path != "" {
		pathArg = fmt.Sprintf(" path=%q,", path)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Goal: point %s at the Jetder deployment %q.\n", domain, deployment)
	if project == "" || location == "" {
		b.WriteString("(For any of project/location not given below, OMIT that argument so the server's configured default is used.)\n")
	}
	b.WriteString("\nUse ONLY this server's tools. Follow the steps in order; stop and report if any step errors.\n\n")

	step := 1
	if register {
		fmt.Fprintf(&b, "%d. REGISTER THE DOMAIN (optional, costs money):\n", step)
		fmt.Fprintf(&b, "   a. Call cf-domain-check with domains=[%q] to get live availability + price.\n", domain)
		b.WriteString("   b. STOP. Show the user the exact domain, price, and currency, and ask them to approve the purchase.\n")
		b.WriteString("      Do NOT buy without explicit user approval.\n")
		fmt.Fprintf(&b, "   c. Only after approval, call cf-domain-register with: domain=%q, confirmText=%q,\n", domain, "REGISTER "+domain)
		b.WriteString("      maxRegistrationCost and currency matching the quote, acceptNonRefundable=true.\n")
		b.WriteString("   d. If it returns a non-completed state, poll cf-registration-status until completed; stop on failed/action_required/blocked.\n\n")
		step++
	}

	fmt.Fprintf(&b, "%d. CREATE THE DOMAIN IN JETDER:\n", step)
	fmt.Fprintf(&b, "   Call domain-create with %s%s domain=%q.\n\n", projectArg, locationArg, domain)
	step++

	fmt.Fprintf(&b, "%d. GET THE DNS RECORDS JETDER NEEDS:\n", step)
	fmt.Fprintf(&b, "   Call domain-get with %s domain=%q. Read its output:\n", projectArg, domain)
	b.WriteString("   - ownershipRecord (a TXT record proving ownership)\n")
	b.WriteString("   - sslRecords (TXT/DCV records to issue the certificate)\n")
	b.WriteString("   - pointTo (the A/AAAA/CNAME records that point the domain at Jetder)\n\n")
	step++

	fmt.Fprintf(&b, "%d. CREATE THOSE RECORDS IN CLOUDFLARE:\n", step)
	b.WriteString("   For EACH record from the step above (ownershipRecord, every sslRecords entry, every pointTo entry),\n")
	b.WriteString("   call cf-dns-create with its type, name, and content (value). cf-dns-create is idempotent:\n")
	b.WriteString("   it reports alreadyExists for identical records and errors on a real conflict — never overwrites.\n")
	b.WriteString("   (Leave zoneId empty to auto-resolve the zone, or first call cf-zone-lookup.)\n\n")
	step++

	fmt.Fprintf(&b, "%d. WAIT FOR VERIFICATION:\n", step)
	fmt.Fprintf(&b, "   Poll domain-get (%s domain=%q) until status is \"success\"\n", projectArg, domain)
	b.WriteString("   (it progresses pending → verify → success). Stop and report if it reaches \"error\".\n\n")
	step++

	fmt.Fprintf(&b, "%d. ROUTE THE DOMAIN TO THE DEPLOYMENT:\n", step)
	fmt.Fprintf(&b, "   Call route-create-v2 with %s%s domain=%q,%s target=%q.\n\n",
		projectArg, locationArg, domain, pathArg, "deployment://"+deployment)

	fmt.Fprintf(&b, "When done, confirm to the user that %s now routes to %s.", domain, deployment)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Playbook to point %s at deployment %s", domain, deployment),
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: b.String()}},
		},
	}, nil
}

// argOrOmit returns `key="value", ` when v is set, or an explicit omit
// instruction when empty — never a placeholder literal.
func argOrOmit(key, v string) string {
	if v == "" {
		return fmt.Sprintf("(omit %s to use the default) ", key)
	}
	return fmt.Sprintf("%s=%q, ", key, v)
}
