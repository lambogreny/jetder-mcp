package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Validation patterns specific to the deploy wizards (stricter than reName where
// a value maps to a constrained identifier or an image ref). All also go through
// validateArg's control/quote/newline rejection.
var (
	rePromptPullSecret = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}[a-z0-9]$`)
	reGithubUser       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	reDeployName       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}[a-z0-9]$`)
	reGHCRImage        = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)+(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}|@sha256:[A-Fa-f0-9]{64})$`)
)

func registerDeployWizardPrompts(server *mcp.Server) {
	registerDeployAnAppPrompt(server)
	registerBootstrapPullSecretPrompt(server)
}

// ===== deploy-an-app =====

func registerDeployAnAppPrompt(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "deploy-an-app",
		Title:       "Deploy an already-built image to Jetder",
		Description: "Wizard for the MCP-side of deploying a container image that you have ALREADY built and pushed to a private registry: confirm the pull secret exists, deploy, and read the URL. (Building/pushing the image is done by your local script, outside MCP.)",
		Arguments: []*mcp.PromptArgument{
			{Name: "deployment", Description: "the Jetder deployment name", Required: true},
			{Name: "image", Description: "the pushed image ref, e.g. ghcr.io/owner/app:tag", Required: true},
			{Name: "project", Description: "Jetder project sid (optional; falls back to JETDER_DEFAULT_PROJECT)"},
			{Name: "location", Description: "Jetder location id (optional; falls back to JETDER_DEFAULT_LOCATION)"},
			{Name: "pullSecret", Description: "name of the pull secret for the private image (optional; default ghcr-pull)"},
		},
	}, deployAnAppHandler)
}

func deployAnAppHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := promptArgs(req)
	const pn = "deploy-an-app"

	deployment, err := validateArg(pn, "deployment", args["deployment"], true, reDeployName)
	if err != nil {
		return nil, err
	}
	image, err := validateArg(pn, "image", args["image"], true, reGHCRImage)
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
	pullSecret, err := validateArg(pn, "pullSecret", args["pullSecret"], false, rePromptPullSecret)
	if err != nil {
		return nil, err
	}
	if pullSecret == "" {
		pullSecret = "ghcr-pull"
	}
	projectArg := argOrOmit("project", project)
	locationArg := argOrOmit("location", location)

	var b strings.Builder
	fmt.Fprintf(&b, "Goal: deploy the already-built image %q to the Jetder deployment %q.\n", image, deployment)
	b.WriteString("Use ONLY this server's tools. Stop and report if any step errors.\n\n")
	b.WriteString("PRECONDITION — the image must already be built, pushed to a PRIVATE registry,\n")
	b.WriteString("and verified private. That happens OUTSIDE this MCP (your local build/push, e.g.\n")
	b.WriteString("the sample's scripts/deploy.sh — see docs/GETTING-STARTED.md). If you do NOT yet\n")
	b.WriteString("have a pushed image ref, STOP and do the local build/push first.\n")
	b.WriteString("If you ran scripts/deploy.sh, it ALREADY deployed — do not deploy again here;\n")
	b.WriteString("skip to the verify step and just read the URL.\n")
	b.WriteString("ARCHITECTURE — the image MUST be built for linux/amd64 (the cluster's arch).\n")
	b.WriteString("If you built on Apple Silicon (Mac M1/M2/M3), Docker defaults to arm64 and the\n")
	b.WriteString("pod will crash-loop with \"exec format error\". Build with\n")
	b.WriteString("`docker build --platform linux/amd64 ...` (or buildx) before pushing.\n\n")

	fmt.Fprintf(&b, "1. CONFIRM THE PULL SECRET EXISTS:\n")
	fmt.Fprintf(&b, "   Call pull-secret-get with %s%s name=%q.\n", projectArg, locationArg, pullSecret)
	fmt.Fprintf(&b, "   If it is NOT found, STOP and run the \"bootstrap-pull-secret\" prompt to create %q first\n", pullSecret)
	b.WriteString("   (do not try to create it here).\n\n")

	fmt.Fprintf(&b, "2. DEPLOY:\n")
	fmt.Fprintf(&b, "   Call deployment-deploy with %s%s name=%q, image=%q, pullSecret=%q.\n\n",
		projectArg, locationArg, deployment, image, pullSecret)

	fmt.Fprintf(&b, "3. READ THE URL:\n")
	fmt.Fprintf(&b, "   Call deployment-get with %s%s name=%q and report its url to the user\n", projectArg, locationArg, deployment)
	b.WriteString("   (deployment-deploy returns status/context, not the URL).\n")

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Deploy %s to %s", image, deployment),
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: b.String()}}},
	}, nil
}

// ===== bootstrap-pull-secret =====
//
// SECURITY: this prompt NEVER accepts or renders the PAT. It takes only
// non-secret args and instructs the user to supply the token directly to the
// pull-secret-create TOOL's password field.

func registerBootstrapPullSecretPrompt(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "bootstrap-pull-secret",
		Title:       "Bootstrap a GHCR pull secret in Jetder (one-time)",
		Description: "One-time wizard to let Jetder pull your private images: create a read:packages GitHub token and store it in Jetder as a pull secret. The token is NEVER entered into this prompt — you provide it directly to the pull-secret-create tool.",
		Arguments: []*mcp.PromptArgument{
			{Name: "githubUsername", Description: "your GitHub username (the pull secret's registry username)", Required: true},
			{Name: "name", Description: "pull secret name to create (optional; default ghcr-pull)"},
			{Name: "project", Description: "Jetder project sid (optional; falls back to JETDER_DEFAULT_PROJECT)"},
			{Name: "location", Description: "Jetder location id (optional; falls back to JETDER_DEFAULT_LOCATION)"},
		},
	}, bootstrapPullSecretHandler)
}

func bootstrapPullSecretHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := promptArgs(req)
	const pn = "bootstrap-pull-secret"

	githubUser, err := validateArg(pn, "githubUsername", args["githubUsername"], true, reGithubUser)
	if err != nil {
		return nil, err
	}
	name, err := validateArg(pn, "name", args["name"], false, rePromptPullSecret)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "ghcr-pull"
	}
	project, err := validateArg(pn, "project", args["project"], false, reName)
	if err != nil {
		return nil, err
	}
	location, err := validateArg(pn, "location", args["location"], false, reName)
	if err != nil {
		return nil, err
	}
	projectArg := argOrOmit("project", project)
	locationArg := argOrOmit("location", location)

	var b strings.Builder
	b.WriteString("Goal: give Jetder a read-only token so it can pull your PRIVATE images.\n")
	b.WriteString("Do this ONCE. Use ONLY this server's tools.\n\n")

	b.WriteString("1. CREATE A READ-ONLY GITHUB TOKEN:\n")
	b.WriteString("   Open this pre-filled link (scope read:packages + name already set), then click\n")
	b.WriteString("   Generate token and copy it:\n")
	b.WriteString("   https://github.com/settings/tokens/new?scopes=read:packages&description=jetder-mcp-pull\n")
	b.WriteString("   STOP here until the user has generated and copied their token.\n\n")

	b.WriteString("2. STORE IT IN JETDER:\n")
	fmt.Fprintf(&b, "   Call pull-secret-create with %s%s name=%q, server=\"ghcr.io\", username=%q,\n",
		projectArg, locationArg, name, githubUser)
	b.WriteString("   and pass the token the user generated as the tool's `password` field.\n")
	b.WriteString("   - Provide the token ONLY as the tool's password field — do NOT type it into this\n")
	b.WriteString("     conversation, repeat it, quote it, or summarize it.\n")
	b.WriteString("   - Only do this in a trusted local client. The server never echoes the token back.\n\n")

	fmt.Fprintf(&b, "Once created, deploys can pull private images by referencing the pull secret name %q.\n", name)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Bootstrap pull secret %s", name),
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: b.String()}}},
	}, nil
}

// promptArgs safely extracts the arguments map.
func promptArgs(req *mcp.GetPromptRequest) map[string]string {
	if req != nil && req.Params != nil && req.Params.Arguments != nil {
		return req.Params.Arguments
	}
	return map[string]string{}
}
