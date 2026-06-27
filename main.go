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
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

const (
	serverName    = "jetder-mcp"
	serverVersion = "v0.0.1"
)

func main() {
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

	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{
			// Advertise the tools capability but disable tool-list-changed
			// notifications: our tool set is static, so emitting
			// "notifications/tools/list_changed" (the inferred default when
			// tools are added) is noise. ToolCapabilities{} => ListChanged:false.
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{},
			},
		},
	)

	registerMeGet(server, adapter)
	registerReadTools(server, adapter)

	// Serve over stdin/stdout until the client disconnects.
	return server.Run(context.Background(), &mcp.StdioTransport{})
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
	}, handler)
}
