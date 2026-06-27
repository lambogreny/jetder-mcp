// Command jetder-mcp is an MCP (Model Context Protocol) server that exposes the
// Jetder API as MCP tools and resources, served over stdio.
//
// Skeleton slice: wires up the server, the auth/adapter layer, and a single
// "me-get" tool to prove the end-to-end path. Further tools/resources are added
// in subsequent slices.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

const serverName = "jetder-mcp"

// serverVersion is reported as the MCP server's implementation version. It is a
// var (not a const) so a release build can stamp the exact tag via
// -ldflags "-X main.serverVersion=vX.Y.Z"; the default tracks the current release
// for source builds. Keep this in sync with the latest release tag.
var serverVersion = "v0.1.0"

// versionString returns the version to report, falling back to "dev" when the
// version is empty (e.g. a build that didn't inject -X main.serverVersion). It
// never panics.
func versionString() string {
	if v := strings.TrimSpace(serverVersion); v != "" {
		return v
	}
	return "dev"
}

// handleVersionFlag prints "jetder-mcp <version>" to out and returns true if any
// arg is a version flag (--version, -version, -v). Kept separate from main() so it
// can be tested without exiting the process. It does NOT touch stdio otherwise, so
// a normal no-arg launch (how an MCP client starts the server) is unaffected.
func handleVersionFlag(args []string, out io.Writer) bool {
	for _, a := range args {
		switch a {
		case "--version", "-version", "-v":
			fmt.Fprintf(out, "%s %s\n", serverName, versionString())
			return true
		}
	}
	return false
}

func main() {
	// Handle --version before starting the server (so it never blocks on stdio).
	if handleVersionFlag(os.Args[1:], os.Stdout) {
		os.Exit(0)
	}
	if err := run(); err != nil {
		// Errors are already token-redacted by the adapter; safe to log.
		log.Fatalf("jetder-mcp: %v", err)
	}
}

func run() error {
	adapter, err := jetder.New()
	if err != nil {
		return err
	}
	// Cloudflare is OPTIONAL: New() returns (nil,nil) when not configured, so a
	// jetder-only server starts fine. cf tools then report CF is not configured.
	cf, err := cloudflare.New()
	if err != nil {
		return err
	}

	server := buildServer(adapter, cf)

	// Serve over stdin/stdout until the client disconnects.
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

// buildServer constructs the MCP server with all tools registered. cf may be nil
// (Cloudflare not configured); cf-* tools still register but error when invoked.
func buildServer(adapter *jetder.Adapter, cf *cloudflare.Client) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: versionString()},
		&mcp.ServerOptions{
			// Advertise the tools capability but disable tool-list-changed
			// notifications: our tool set is static, so emitting
			// "notifications/tools/list_changed" (the inferred default when
			// tools are added) is noise. ToolCapabilities{} => ListChanged:false.
			Capabilities: &mcp.ServerCapabilities{
				Tools:     &mcp.ToolCapabilities{},
				Prompts:   &mcp.PromptCapabilities{},   // advertise prompts, suppress list_changed
				Resources: &mcp.ResourceCapabilities{}, // advertise resources; no list_changed/subscribe (static set)
			},
		},
	)

	registerMeGet(server, adapter)
	registerReadTools(server, adapter)
	registerDeploymentReadTools(server, adapter)
	registerDeploymentActionTools(server, adapter)
	registerDomainTools(server, adapter)
	registerRouteTools(server, adapter)
	registerResourceReadTools(server, adapter)
	registerResourceWriteTools(server, adapter)
	registerGrantsAndEmailTools(server, adapter)
	registerCloudflareTools(server, cf)
	registerCheckSetup(server, adapter, cf)
	registerResources(server, adapter, cf)
	registerPointADomainPrompt(server)
	registerDeployWizardPrompts(server)

	return server
}

// MeGetInput is the (empty) input for the me-get tool.
type MeGetInput struct{}

// MeGetOutput is the result of the me-get tool.
type MeGetOutput struct {
	Email string `json:"email" jsonschema:"the email of the authenticated user"`
	KYC   bool   `json:"kyc" jsonschema:"whether the user has completed KYC"`
}

// registerMeGet adds the "me-get" tool: returns the authenticated user's profile.
func registerMeGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, _ MeGetInput) (*mcp.CallToolResult, MeGetOutput, error) {
		item, err := adapter.Client().Me().Get(ctx, nil)
		if err != nil {
			return nil, MeGetOutput{}, adapter.Redact(err)
		}
		out := MeGetOutput{Email: item.Email, KYC: item.KYC}
		return textResult(fmt.Sprintf("email=%s kyc=%t", out.Email, out.KYC)), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "me-get",
		Description: "Get the authenticated Jetder user's profile (email, KYC status).",
		Annotations: readOnly(),
	}, handler)
}
