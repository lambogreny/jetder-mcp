package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// deployment-logs reads a snapshot of a deployment's logs. Jetder has no logs API
// method — a deployment carries a self-authenticating `logUrl` (a log-server URL
// with a short-lived JWT in its query). So this tool: resolves the deployment,
// reads its logUrl via deployment-get, fetches a BOUNDED snapshot of that URL
// (NEVER sending our own credentials — the URL's JWT is the auth), sanitizes the
// text, and returns the tail. It is a snapshot (not a live stream/tail): an MCP
// call must be finite.

const (
	logsTailDefault = 200
	logsTailMax     = 1000
	// 128 KiB default cap, 1 MiB hard max, read via io.LimitReader (never buffers
	// the whole log).
	logsMaxBytesDefault int64 = 128 * 1024
	logsMaxBytesMax     int64 = 1024 * 1024
)

// DeploymentLogsInput selects the deployment and how much log to return.
type DeploymentLogsInput struct {
	Project   string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location  string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name      string `json:"name" jsonschema:"deployment name"`
	Branch    string `json:"branch,omitempty" jsonschema:"branch"`
	Revision  int    `json:"revision,omitempty" jsonschema:"deployment revision (default 0 = latest)"`
	TailLines int    `json:"tailLines,omitempty" jsonschema:"how many trailing log lines to return (default 200, max 1000)"`
	MaxBytes  int64  `json:"maxBytes,omitempty" jsonschema:"max bytes to read from the log before truncating (default 131072, max 1048576)"`
}

// DeploymentLogsOutput is the sanitized log snapshot. It NEVER includes the logUrl
// (the query carries a token).
type DeploymentLogsOutput struct {
	ResolvedContext
	Name string `json:"name" jsonschema:"deployment name"`
	// Source is always "logUrl" — a label, never the URL itself.
	Source    string `json:"source" jsonschema:"where the logs came from (always \"logUrl\")"`
	Logs      string `json:"logs" jsonschema:"the (best-effort sanitized) log text, last tailLines lines"`
	LineCount int    `json:"lineCount" jsonschema:"number of log lines returned"`
	Truncated bool   `json:"truncated" jsonschema:"true if the log exceeded the byte cap and was cut"`
}

func registerDeploymentLogs(server *mcp.Server, adapter *jetder.Adapter, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentLogsInput) (*mcp.CallToolResult, DeploymentLogsOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentLogsOutput{}, err
		}

		// 1) Resolve the deployment to get its logUrl (the URL is never surfaced).
		dep, err := adapter.Client().Deployment().Get(ctx, &api.DeploymentGet{
			Project: project, Location: location, Name: name, Branch: in.Branch, Revision: in.Revision,
		})
		if err != nil {
			return nil, DeploymentLogsOutput{}, adapter.Redact(err)
		}
		logURL := strings.TrimSpace(dep.LogURL)
		if logURL == "" {
			return nil, DeploymentLogsOutput{}, fmt.Errorf("deployment %q has no log URL yet (is it deployed and running?)", name)
		}

		// 2) Fetch a bounded snapshot (no credentials sent; host-guarded; capped).
		// maxBytes: <=0 => default; clamp to the hard max. The LimitReader still
		// enforces the cap inside FetchURL.
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = logsMaxBytesDefault
		}
		if maxBytes > logsMaxBytesMax {
			maxBytes = logsMaxBytesMax
		}
		raw, truncated, err := adapter.FetchURL(ctx, logURL, maxBytes)
		if err != nil {
			// Defense in depth: scrub the logUrl/JWT from any fetch error too.
			return nil, DeploymentLogsOutput{}, sanitizeErr(adapter, logURL, err)
		}

		// 3) Sanitize (best-effort) + take the tail. Pass logURL so the body can
		// never echo our own log URL / its JWT back to the client.
		clean := sanitizeLog(adapter, string(raw), logURL)
		tail := in.TailLines
		if tail <= 0 {
			tail = logsTailDefault
		}
		if tail > logsTailMax {
			tail = logsTailMax
		}
		logs, lineCount := tailLines(clean, tail)

		out := DeploymentLogsOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Name:            name,
			Source:          "logUrl",
			Logs:            logs,
			LineCount:       lineCount,
			Truncated:       truncated,
		}
		summary := fmt.Sprintf("%d log line(s) for %s [project=%s]", lineCount, name, project)
		if truncated {
			summary += " (truncated)"
		}
		return textResult(summary), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "deployment-logs",
		Description: "Read a snapshot of a deployment's recent logs (last tailLines lines, default 200). " +
			"This is a one-shot read, not a live tail. The logs are sanitized on a best-effort basis " +
			"(known credentials and common secret patterns are masked) — but logs can contain anything " +
			"your app prints, so do not treat them as guaranteed secret-free.",
		Annotations: readOnly(),
	}, handler)
}

// --- log sanitizer (separate from error redaction) ---------------------------

// reLogSecret matches common secret-bearing lines/tokens in log text. Best-effort:
// it masks the value after a key like token=/password:/"api_key": (incl. JSON-style
// quoted keys), plus a few well-known token shapes. It is NOT a guarantee — see
// sanitizeLog's doc.
var reLogSecret = regexp.MustCompile(
	// key : value / key = value — key may have prefixes/suffixes (DB_PASSWORD,
	// X-Api-Key) and an optional surrounding quote (JSON: "password":"x"). The value
	// may include an auth scheme word (Bearer/Basic) before the credential, so consume
	// the rest of the line.
	`(?i)["']?[A-Za-z0-9_-]*(authorization|password|passwd|secret|api[_-]?key|access[_-]?key|client[_-]?secret|token|cookie)[A-Za-z0-9_-]*["']?\s*[:=]\s*\S.*` +
		// bare auth-scheme tokens: "Bearer <x>" / "Basic <x>"
		`|(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}` +
		// well-known token shapes
		`|ghp_[A-Za-z0-9]{20,}` +
		`|github_pat_[A-Za-z0-9_]{20,}` +
		`|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`, // JWT
)

// sanitizeLog masks secrets in log text. It is BEST-EFFORT, not a guarantee: it
// removes our own known credentials (Jetder Basic auth, any CLOUDFLARE_*/GHCR/
// GitHub-token env values), the log URL and its embedded token (in case the body
// echoes the request URL), then applies regex masking for common secret patterns.
// Logs can contain arbitrary application output, so callers must not rely on this to
// make logs provably secret-free.
func sanitizeLog(adapter *jetder.Adapter, s string, logURL string) string {
	if s == "" {
		return s
	}
	// 0) The log URL itself (and its query token) — the body must never echo it.
	for _, v := range logURLSecrets(logURL) {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	// 1) Exact known values: our Basic-auth creds + secret-ish env values.
	for _, v := range adapter.Creds() {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	for _, v := range secretEnvValues() {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	// 2) Pattern masking for common secret shapes.
	s = reLogSecret.ReplaceAllString(s, "[REDACTED]")
	return s
}

// logURLSecrets returns the distinctive strings of a log URL that must be scrubbed
// from any surfaced text: the full URL, its raw query string, and each query VALUE
// (e.g. the `t` JWT). Returns nothing for an unparseable/empty URL.
func logURLSecrets(logURL string) []string {
	logURL = strings.TrimSpace(logURL)
	if logURL == "" {
		return nil
	}
	out := []string{logURL}
	if u, err := url.Parse(logURL); err == nil {
		if u.RawQuery != "" {
			out = append(out, u.RawQuery)
		}
		for _, vals := range u.Query() {
			for _, v := range vals {
				if v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// secretEnvValues returns the VALUES of env vars likely to hold a credential, so
// the sanitizer can scrub them from log text if an app echoed them.
func secretEnvValues() []string {
	keys := []string{
		jetder.EnvToken, jetder.EnvAuthPass,
		cloudflare.EnvToken,
		"GHCR_TOKEN", "GHCR_PAT", "GITHUB_TOKEN", "GITHUB_PAT", "GH_TOKEN",
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// sanitizeErr scrubs the logUrl (and its JWT) plus known creds from a fetch error.
func sanitizeErr(adapter *jetder.Adapter, logURL string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", sanitizeLog(adapter, err.Error(), logURL))
}

// tailLines returns the last n lines of s (and the count returned).
func tailLines(s string, n int) (string, int) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return "", 0
	}
	lines := strings.Split(s, "\n")
	if n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), len(lines)
}
