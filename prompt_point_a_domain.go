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
// prompt is the prompt name, used only for a clear error message.
func validateArg(prompt, field, v string, required bool, pat *regexp.Regexp) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return "", fmt.Errorf("%s requires the %q argument", prompt, field)
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
			{Name: "dnsHost", Description: "where the domain's DNS is managed: \"cloudflare\" (this server sets records automatically) or \"other\" (you add the records at your own DNS provider). Defaults to cloudflare when registering, else asks."},
		},
	}, pointADomainHandler)
}

func pointADomainHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := map[string]string{}
	if req != nil && req.Params != nil && req.Params.Arguments != nil {
		args = req.Params.Arguments
	}

	const pn = "point-a-domain"
	domain, err := validateArg(pn, "domain", args["domain"], true, reDomain)
	if err != nil {
		return nil, err
	}
	// Canonicalize to lowercase so confirmText ("REGISTER <domain>") matches the
	// registrar guard, which lowercases the domain before comparing.
	domain = strings.ToLower(domain)
	deployment, err := validateArg(pn, "deployment", args["deployment"], true, reName)
	if err != nil {
		return nil, err
	}
	project, err := validateArg(pn, "project", args["project"], false, reName)
	if err != nil {
		return nil, err
	}
	location, err := validateArg(pn, "location", args["location"], false, reName)
	if err != nil {
		return nil, err
	}
	path, err := validateArg(pn, "path", args["path"], false, rePathSafe)
	if err != nil {
		return nil, err
	}
	register := strings.EqualFold(strings.TrimSpace(args["registerDomain"]), "true")

	// dnsHost: where DNS records get created. "cloudflare" → this server sets them
	// via cf-dns-create; "other" → the user adds them at their own DNS provider
	// (this server can't). Registering a domain via Cloudflare implies cloudflare.
	dnsHost := strings.ToLower(strings.TrimSpace(args["dnsHost"]))
	switch dnsHost {
	case "", "cloudflare", "other":
	default:
		return nil, fmt.Errorf("point-a-domain: dnsHost must be \"cloudflare\" or \"other\"")
	}
	if register {
		dnsHost = "cloudflare"
	}

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
		// Registrant contact: registration needs a legal owner (WHOIS) contact. Guide
		// the assistant to obtain it WITHOUT punting the user to a dashboard and
		// WITHOUT placing PII in this prompt — the contact goes only to the
		// cf-domain-register tool args or the CLOUDFLARE_REGISTRANT_* env.
		b.WriteString("   c. REGISTRANT CONTACT (the legal domain owner's WHOIS details). Cloudflare requires this to\n")
		b.WriteString("      register. This server cannot inspect your environment, so ASK the user which applies — do\n")
		b.WriteString("      NOT send them to the Cloudflare dashboard:\n")
		b.WriteString("      - If the user CONFIRMS they have already set the CLOUDFLARE_REGISTRANT_* environment\n")
		b.WriteString("        variables (name, email, phone, street, city, state, postalCode, countryCode) correctly,\n")
		b.WriteString("        register WITHOUT a registrant argument (the env contact is used automatically).\n")
		b.WriteString("      - Otherwise, ask the user for: full legal name, email, phone (E.164 with a dot, e.g.\n")
		b.WriteString("        +<countryCode>.<number>), street, city, state, postalCode, and countryCode (ISO 3166-1\n")
		b.WriteString("        alpha-2); organization is optional. Pass these as the cf-domain-register `registrant`\n")
		b.WriteString("        argument. Provide the contact ONLY through the tool argument (or the user's own\n")
		b.WriteString("        CLOUDFLARE_REGISTRANT_* env) — do NOT paste it back into the chat or store it in shared\n")
		b.WriteString("        config; it goes to the domain registry. To avoid re-asking next time, the user can set\n")
		b.WriteString("        the CLOUDFLARE_REGISTRANT_* env once.\n")
		b.WriteString("   d. CONFIRM ACCURACY. Registrant data is legally binding — inaccurate WHOIS details can get the\n")
		b.WriteString("      domain SUSPENDED. Ask the user to confirm the contact is accurate and current. Only AFTER\n")
		b.WriteString("      that confirmation, set acceptRegistrantAccuracy=true. Never set it just because the env is\n")
		b.WriteString("      populated.\n")
		fmt.Fprintf(&b, "   e. Only after approval (price) and accuracy confirmation, call cf-domain-register with: domain=%q,\n", domain)
		fmt.Fprintf(&b, "      confirmText=%q, maxRegistrationCost and currency matching the quote, acceptNonRefundable=true,\n", "REGISTER "+domain)
		b.WriteString("      acceptRegistrantAccuracy=true, and the registrant argument (unless using the env contact).\n")
		b.WriteString("   f. If it returns a non-completed state, poll cf-registration-status until completed; stop on failed/action_required/blocked.\n\n")
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

	switch dnsHost {
	case "other":
		fmt.Fprintf(&b, "%d. ADD THOSE RECORDS AT YOUR OWN DNS PROVIDER (manual):\n", step)
		b.WriteString("   The domain's DNS is NOT on Cloudflare, so this server cannot set the records.\n")
		b.WriteString("   Show the user EACH record from the step above and ask them to add it at their DNS\n")
		b.WriteString("   provider (GoDaddy, Namecheap, etc.): the ownershipRecord (TXT), every sslRecords\n")
		b.WriteString("   entry (TXT), and every pointTo entry (A/AAAA/CNAME) — exact type, name, and value.\n")
		b.WriteString("   Do NOT use the Cloudflare DNS tool here. Wait for the user to confirm they've added them.\n\n")
	case "cloudflare":
		fmt.Fprintf(&b, "%d. CREATE THOSE RECORDS IN CLOUDFLARE:\n", step)
		b.WriteString("   For EACH record from the step above, call cf-dns-create with its type, name, and content (value),\n")
		b.WriteString("   setting proxied EXPLICITLY by the record's role:\n")
		b.WriteString("   - The pointTo records (the traffic targets, A/AAAA/CNAME) → pass proxied:true, so the site gets a\n")
		b.WriteString("     valid Cloudflare certificate immediately (no browser TLS warning while the origin cert provisions).\n")
		b.WriteString("   - The ownershipRecord and every sslRecords entry (verification records) → pass proxied:false. These\n")
		b.WriteString("     MUST resolve directly (DNS-only); never proxy a verification record, even a CNAME one.\n")
		b.WriteString("   cf-dns-create is idempotent (reports alreadyExists for identical records, errors on a real conflict,\n")
		b.WriteString("   never overwrites); if a pointTo record already exists but is DNS-only, it updates it to proxied\n")
		b.WriteString("   (proxiedUpdated=true). (Leave zoneId empty to auto-resolve the zone, or first call cf-zone-lookup.)\n\n")
	default: // unset — ask which path applies
		fmt.Fprintf(&b, "%d. CREATE THOSE RECORDS (choose based on where the domain's DNS is hosted):\n", step)
		b.WriteString("   - If the DNS is on CLOUDFLARE: for each record above, call cf-dns-create (idempotent;\n")
		b.WriteString("     never overwrites; leave zoneId empty to auto-resolve).\n")
		b.WriteString("   - If the DNS is at ANOTHER provider (GoDaddy/Namecheap/etc.): this server cannot set\n")
		b.WriteString("     them — show the user each record (type/name/value) to add at their provider, and wait.\n")
		b.WriteString("   (Re-run this prompt with dnsHost=\"cloudflare\" or dnsHost=\"other\" to get a single path.)\n\n")
	}
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
