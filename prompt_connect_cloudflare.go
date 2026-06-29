package main

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerConnectCloudflarePrompt adds the "connect-cloudflare" MCP Prompt: a
// step-by-step playbook that walks a user through enabling the Cloudflare-backed
// tools (DNS, domain pointing, domain registration) WITHOUT them having to figure out
// the environment variables themselves. It deliberately takes NO arguments — a token
// must never be passed as a prompt argument (it could be logged). The token is set in
// the MCP client's env and the prompt explains exactly how.
func registerConnectCloudflarePrompt(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "connect-cloudflare",
		Title:       "Connect Cloudflare (enable DNS + domain tools)",
		Description: "Step-by-step setup to enable the Cloudflare-backed tools: which credentials to set, how to set them in your MCP client, why a restart is needed, how to verify the connection with cf-verify, and what each step unlocks. No token is ever entered into this prompt.",
		// No arguments — never accept a token via prompt args.
	}, connectCloudflareHandler)
}

func connectCloudflareHandler(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	var b strings.Builder

	b.WriteString("Goal: connect Cloudflare so the DNS, domain-pointing, and domain-registration\n")
	b.WriteString("tools work. Guide the user through this — do NOT ask them to paste their token\n")
	b.WriteString("into the chat; it goes in the MCP client's environment.\n\n")

	b.WriteString("WHAT YOU NEED:\n")
	b.WriteString("1. A Cloudflare API token with these scopes:\n")
	b.WriteString("   - Zone : Zone : Read\n")
	b.WriteString("   - Zone : DNS : Edit          (to create/update DNS records)\n")
	b.WriteString("   - Account : Registrar/Domains : Write   (ONLY if buying domains; not\n")
	b.WriteString("     \"API Gateway\" — that is a different product)\n")
	b.WriteString("   Create it at: https://dash.cloudflare.com/profile/api-tokens\n")
	b.WriteString("2. (Only for buying domains) your Cloudflare ACCOUNT ID, shown on the\n")
	b.WriteString("   account's Overview page.\n\n")

	b.WriteString("STEP 1 — SET THE CREDENTIALS IN YOUR MCP CLIENT (not in this chat).\n")
	b.WriteString("Add these env vars where the jetder-mcp server is configured. For Claude\n")
	b.WriteString("Code, re-run `claude mcp add` with the Cloudflare vars added. Each KEY=value\n")
	b.WriteString("is quoted so the <your-...> placeholders don't confuse the shell — replace\n")
	b.WriteString("them with your real values (the CLOUDFLARE_ACCOUNT_ID line is only needed if\n")
	b.WriteString("you will buy domains; drop it otherwise):\n\n")
	b.WriteString("   claude mcp add \\\n")
	b.WriteString("     -e \"JETDER_AUTH_USER=<your-service-account-email>\" \\\n")
	b.WriteString("     -e \"JETDER_TOKEN=<your-jetder-token>\" \\\n")
	b.WriteString("     -e \"CLOUDFLARE_API_TOKEN=<your-cloudflare-token>\" \\\n")
	b.WriteString("     -e \"CLOUDFLARE_ACCOUNT_ID=<your-account-id>\" \\\n")
	b.WriteString("     --scope user jetder-mcp -- jetder-mcp\n\n")
	b.WriteString("(For Claude Desktop / Cursor / VS Code, add the same two CLOUDFLARE_* keys\n")
	b.WriteString("to the server's \"env\" block — see docs/CLOUDFLARE-SETUP.md.)\n")
	b.WriteString("Use the real values in your client config — NEVER type the token here.\n\n")

	b.WriteString("STEP 2 — RESTART THE MCP CLIENT. ⚠️ IMPORTANT.\n")
	b.WriteString("The server reads CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID **once, at\n")
	b.WriteString("startup**. If you set the env while the client is running, the server will\n")
	b.WriteString("still see no token (Cloudflare \"not configured\"). Fully restart the client\n")
	b.WriteString("(or reload the MCP server) so it picks up the new environment.\n\n")

	b.WriteString("STEP 3 — VERIFY THE CONNECTION.\n")
	b.WriteString("Call the `cf-verify` tool. It makes one read-only Cloudflare call (listing\n")
	b.WriteString("zones) to confirm the token works and reports what is unlocked:\n")
	b.WriteString("   - configured=false  → the token isn't set / the client wasn't restarted.\n")
	b.WriteString("     Re-check STEP 1 and STEP 2.\n")
	b.WriteString("   - configured=true, connected=false → the token is set but the call failed.\n")
	b.WriteString("     The token is likely wrong or missing the Zone:Read scope — recreate it.\n")
	b.WriteString("   - connected=true → 🎉 Cloudflare is ready.\n\n")

	b.WriteString("WHAT THIS UNLOCKS once connected:\n")
	b.WriteString("   - DNS records:        cf-dns-list, cf-dns-create, cf-zone-lookup\n")
	b.WriteString("   - Point a domain:     the \"point-a-domain\" prompt (domain → deployment,\n")
	b.WriteString("                         with a valid cert at the edge)\n")
	b.WriteString("   - Buy domains:        cf-domain-check, cf-domain-register (needs the\n")
	b.WriteString("                         account id + the Registrar scope)\n\n")

	b.WriteString("Walk the user through STEP 1→3, then confirm with cf-verify. If cf-verify\n")
	b.WriteString("reports it is still not connected, help them re-check the scopes and the\n")
	b.WriteString("restart — do not ask for the token value itself.")

	return &mcp.GetPromptResult{
		Description: "Step-by-step setup to connect Cloudflare and verify it with cf-verify",
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: b.String()}},
		},
	}, nil
}
