package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
)

// cf-verify is a READ-ONLY tool that confirms the Cloudflare credentials actually
// work — it makes one cheap live call (list zones) to prove the CLOUDFLARE_API_TOKEN
// is valid and has the DNS/zone scope the domain tools need, and reports which
// capabilities are unlocked. It is the live counterpart to check-setup (which only
// checks config presence). It NEVER echoes the token: errors are redacted, and the
// token is read from the environment, never an argument.

// CFVerifyInput takes no credentials — the token/account come from the environment
// (CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID), read at server start.
type CFVerifyInput struct{}

// CFVerifyOutput reports connectivity + capabilities (never any secret).
type CFVerifyOutput struct {
	Configured        bool     `json:"configured" jsonschema:"true if a Cloudflare token is set in the environment"`
	Connected         bool     `json:"connected" jsonschema:"true if a live Cloudflare API call succeeded with the token"`
	AccountConfigured bool     `json:"accountConfigured" jsonschema:"true if CLOUDFLARE_ACCOUNT_ID is set (required for the Registrar/buy-domain tools)"`
	ZoneCount         int      `json:"zoneCount" jsonschema:"number of zones the token can see (proof the token works)"`
	Capabilities      []string `json:"capabilities" jsonschema:"the tool groups this configuration unlocks"`
	Remediation       string   `json:"remediation,omitempty" jsonschema:"what to fix when not connected/configured"`
}

func registerCFVerify(server *mcp.Server, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, _ CFVerifyInput) (*mcp.CallToolResult, CFVerifyOutput, error) {
		out := CFVerifyOutput{Capabilities: []string{}}

		// Not configured: the env token wasn't set when the server started.
		if cf == nil {
			out.Remediation = fmt.Sprintf(
				"Cloudflare is not configured. Set %s (and %s for buying domains) in your MCP client, "+
					"then RESTART the client — the server reads these at startup — and run cf-verify again. "+
					"See the connect-cloudflare prompt for step-by-step setup.",
				cloudflare.EnvToken, cloudflare.EnvAccountID)
			return textResult("Cloudflare not configured (set the token + restart, then cf-verify)"), out, nil
		}
		out.Configured = true
		out.AccountConfigured = cf.AccountID() != ""

		// Live check: list zones. A success proves the token is valid and has the
		// Zone:Read scope the DNS tools rely on. The client redacts its own errors;
		// we additionally never surface the raw error text with the token.
		zones, err := cf.ListZones(ctx, "")
		if err != nil {
			out.Connected = false
			out.Remediation = "Cloudflare token is set but the API call failed. Check the token is valid and " +
				"has Zone:Read (and Zone:DNS:Edit for DNS changes). Reason: " + cf.Redact(err.Error())
			return textResult("Cloudflare configured but NOT connected — check the token/scopes"), out, nil
		}

		out.Connected = true
		out.ZoneCount = len(zones)
		out.Capabilities = append(out.Capabilities, "DNS records (cf-dns-* tools)", "zone lookup (cf-zone-lookup)", "point a domain at a deployment (point-a-domain)")
		if out.AccountConfigured {
			out.Capabilities = append(out.Capabilities, "buy/manage domains via Registrar (cf-domain-check, cf-domain-register)")
		} else {
			out.Remediation = fmt.Sprintf(
				"Connected. DNS/zone tools are ready. To also BUY domains via Registrar, set %s and restart.",
				cloudflare.EnvAccountID)
		}

		summary := fmt.Sprintf("Cloudflare connected ✓ (%d zone(s) visible)", out.ZoneCount)
		if !out.AccountConfigured {
			summary += " — set an account id to enable the Registrar"
		}
		return textResult(summary), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "cf-verify",
		Description: "Verify the Cloudflare connection (read-only): makes one live call to confirm the " +
			"CLOUDFLARE_API_TOKEN works and has the right scopes, then reports which domain/DNS tools are " +
			"unlocked. Run this after setting the token (and restarting the client). The token is never echoed.",
		Annotations: readOnly(),
	}, handler)
}
