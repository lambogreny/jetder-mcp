// Command mcp-deploy drives the jetder-mcp server to perform a single
// deployment-deploy from CI (or locally).
//
// It launches the MCP server as a subprocess over stdio (mcp.CommandTransport),
// performs the MCP handshake, calls the deployment-deploy tool with EXPLICIT
// project/location/name/image arguments, and verifies the result. It exits 0 on
// a confirmed success and non-zero on any transport error, tool error, or a
// mismatch between the resolved context and the explicit arguments.
//
// Security:
//   - Jetder uses HTTP Basic auth. JETDER_AUTH_USER (service-account email) and
//     JETDER_TOKEN (API token) are read from this process's environment and
//     inherited by the child server only. They are NEVER passed as tool arguments
//     and NEVER printed — the user, the token, and the base64(user:token) header
//     value are all redacted from any surfaced output.
//   - The server's stderr is captured and only a sanitized one-line summary is
//     surfaced on failure (never raw JSON-RPC or the environment).
//
// Usage:
//
//	# prebuilt server binary:
//	mcp-deploy -server ./jetder-mcp -project P -location L -name N -image IMG [-branch B]
//
//	# launch the server via a command (e.g. from CI with `go run`):
//	mcp-deploy -server-command "go run github.com/lambogreny/jetder-mcp@v1" \
//	  -project P -location L -name N -image IMG
//
// -server-command is split into argv on whitespace and exec'd directly (no shell,
// so no command injection). project/location/name/image are required and explicit
// (no hidden env defaults).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		// err is already sanitized (no token, no raw payload).
		fmt.Fprintln(os.Stderr, "mcp-deploy: "+err.Error())
		os.Exit(1)
	}
	fmt.Println("mcp-deploy: deploy accepted")
}

type config struct {
	server    string // path to a prebuilt server binary (compat)
	serverCmd string // full command to launch the server, e.g. "go run <module>@<ver>"
	project   string
	location  string
	name      string
	image     string
	branch    string
	timeout   time.Duration
	// Basic-auth credentials, for sanitization only (never printed). authUser =
	// JETDER_AUTH_USER, token = JETDER_TOKEN/JETDER_AUTH_PASS, basicB64 =
	// base64(user:token) (the raw Basic header value).
	authUser string
	token    string
	basicB64 string
}

// serverArgv returns the command + args used to launch the MCP server. If
// -server-command is set it is split on whitespace into argv (NO shell, so no
// injection); otherwise the -server binary path is used as a single argv.
func (c config) serverArgv() ([]string, error) {
	if strings.TrimSpace(c.serverCmd) != "" {
		argv := strings.Fields(c.serverCmd)
		if len(argv) == 0 {
			return nil, errors.New("-server-command is empty")
		}
		return argv, nil
	}
	if strings.TrimSpace(c.server) == "" {
		return nil, errors.New("either -server or -server-command is required")
	}
	return []string{c.server}, nil
}

// sanitize removes the Basic-auth credentials — the token, the username, and the
// base64(user:token) header value — from any text the helper is about to surface
// (server stderr, tool-error text, SDK error strings). Defense in depth so the
// helper itself can never leak a credential even if a child or the SDK echoes it.
func (c config) sanitize(s string) string {
	for _, cred := range []string{c.basicB64, c.token, c.authUser} {
		if cred != "" {
			s = strings.ReplaceAll(s, cred, "[REDACTED]")
		}
	}
	return s
}

func parseFlags() (config, error) {
	var c config
	flag.StringVar(&c.server, "server", "./jetder-mcp", "path to a prebuilt jetder-mcp server binary")
	flag.StringVar(&c.serverCmd, "server-command", "", "command to launch the server (argv, split on spaces, no shell), e.g. \"go run github.com/lambogreny/jetder-mcp@v1\"; overrides -server")
	flag.StringVar(&c.project, "project", "", "jetder project sid (required)")
	flag.StringVar(&c.location, "location", "", "jetder location id (required)")
	flag.StringVar(&c.name, "name", "", "deployment name (required)")
	flag.StringVar(&c.image, "image", "", "container image to deploy, e.g. ghcr.io/owner/app:sha (required)")
	flag.StringVar(&c.branch, "branch", "", "branch (optional)")
	flag.DurationVar(&c.timeout, "timeout", 60*time.Second, "overall timeout")
	flag.Parse()

	// Trim the values we send so the args we submit are EXACTLY the values we
	// later verify against the server's resolved context (the server trims too).
	c.project = strings.TrimSpace(c.project)
	c.location = strings.TrimSpace(c.location)
	c.name = strings.TrimSpace(c.name)
	c.image = strings.TrimSpace(c.image)
	c.branch = strings.TrimSpace(c.branch)

	var missing []string
	for k, v := range map[string]string{"project": c.project, "location": c.location, "name": c.name, "image": c.image} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	// Basic-auth creds: the child server enforces them; the helper only needs
	// them to sanitize its output. token = JETDER_TOKEN or JETDER_AUTH_PASS.
	c.token = strings.TrimSpace(os.Getenv("JETDER_TOKEN"))
	if c.token == "" {
		c.token = strings.TrimSpace(os.Getenv("JETDER_AUTH_PASS"))
	}
	if c.token == "" {
		return c, errors.New("JETDER_TOKEN (or JETDER_AUTH_PASS) must be set in the environment")
	}
	c.authUser = strings.TrimSpace(os.Getenv("JETDER_AUTH_USER"))
	if c.authUser != "" {
		c.basicB64 = base64.StdEncoding.EncodeToString([]byte(c.authUser + ":" + c.token))
	}
	return c, nil
}

func run() error {
	c, err := parseFlags()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	// Launch the server as a subprocess. It inherits our env (incl JETDER_TOKEN);
	// capture its stderr so we can surface a sanitized summary on failure only.
	argv, err := c.serverArgv()
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...) // argv, never `sh -c` (no injection)
	cmd.Env = os.Environ()
	var stderr strings.Builder
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-deploy", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("failed to start/connect MCP server: %s", c.sanitize(firstLine(c.sanitize(stderr.String())+" "+err.Error())))
	}

	out, callErr := callDeploy(ctx, session, c)

	// We have our answer. Close the session; ignore post-success EOF / exit-status
	// noise from the child shutting down — only the deploy result matters.
	_ = session.Close()

	if callErr != nil {
		return callErr
	}
	return verify(out, c)
}

// deployResult mirrors the deployment-deploy structured output.
type deployResult struct {
	ResolvedProject  string `json:"resolvedProject"`
	ResolvedLocation string `json:"resolvedLocation"`
	Name             string `json:"name"`
	Action           string `json:"action"`
	Success          bool   `json:"success"`
}

func callDeploy(ctx context.Context, session *mcp.ClientSession, c config) (*deployResult, error) {
	args := map[string]any{
		"project":  c.project,
		"location": c.location,
		"name":     c.name,
		"image":    c.image,
	}
	if c.branch != "" {
		args["branch"] = c.branch
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "deployment-deploy", Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("deployment-deploy call failed: %s", c.sanitize(err.Error()))
	}
	if res.IsError {
		return nil, fmt.Errorf("deployment-deploy returned an error: %s", c.sanitize(toolErrorText(res)))
	}

	// StructuredContent is the validated tool output (a generic value); round-trip
	// it through JSON into our typed struct.
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return nil, fmt.Errorf("could not encode deploy result: %v", err)
	}
	out := &deployResult{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("could not parse deploy result: %v", err)
	}
	return out, nil
}

// verify confirms success and that the server acted on the EXACT explicit args
// (defends against any silent env-default substitution).
func verify(out *deployResult, c config) error {
	if !out.Success {
		return errors.New("deploy not reported successful")
	}
	if out.ResolvedProject != c.project {
		return fmt.Errorf("resolved project %q != requested %q", out.ResolvedProject, c.project)
	}
	if out.ResolvedLocation != c.location {
		return fmt.Errorf("resolved location %q != requested %q", out.ResolvedLocation, c.location)
	}
	if out.Name != c.name {
		return fmt.Errorf("deployed name %q != requested %q", out.Name, c.name)
	}
	return nil
}

func toolErrorText(res *mcp.CallToolResult) string {
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok && tc.Text != "" {
			return firstLine(tc.Text)
		}
	}
	return "unknown tool error"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "(no detail)"
	}
	return s
}
