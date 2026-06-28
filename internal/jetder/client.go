// Package jetder is a thin adapter around the github.com/jetder-core/api client.
//
// It centralizes:
//   - construction of the underlying *client.Client
//   - HTTP Basic auth (Jetder uses Basic auth: service-account email as the
//     username, an API token as the password) pulled from the environment
//   - error translation / redaction so the credentials never leak into MCP output
//
// The adapter intentionally exposes only the high-level api.* interfaces the MCP
// server needs; it does not re-implement any API behavior.
package jetder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jetder-core/api/client"
)

// Auth env vars. Jetder authenticates with HTTP Basic auth:
//   - JETDER_AUTH_USER = service-account email (the Basic-auth username)
//   - JETDER_TOKEN     = the API token (the Basic-auth password)
//
// JETDER_AUTH_PASS is accepted as an alias for JETDER_TOKEN.
const (
	EnvAuthUser = "JETDER_AUTH_USER"
	EnvToken    = "JETDER_TOKEN"
	EnvAuthPass = "JETDER_AUTH_PASS"
)

// EnvEndpoint optionally overrides the Jetder API endpoint (mainly for testing).
const EnvEndpoint = "JETDER_ENDPOINT"

// Default-context env vars. When a tool's project/location arg is empty, these
// supply a fallback. Per-tool args remain the source of truth and always win.
const (
	EnvDefaultProject  = "JETDER_DEFAULT_PROJECT"
	EnvDefaultLocation = "JETDER_DEFAULT_LOCATION"
)

// ErrNoUser is returned when JETDER_AUTH_USER is not set.
var ErrNoUser = errors.New("JETDER_AUTH_USER environment variable is required (Jetder uses Basic auth: service-account email as username)")

// ErrNoToken is returned when no password (JETDER_TOKEN / JETDER_AUTH_PASS) is set.
var ErrNoToken = errors.New("JETDER_TOKEN (or JETDER_AUTH_PASS) environment variable is required")

// Adapter wraps a configured Jetder API client.
type Adapter struct {
	client          *client.Client
	user            string
	token           string // the Basic-auth password (API token)
	basicB64        string // base64(user:token); redacted from output too
	defaultProject  string
	defaultLocation string
}

// DefaultProject returns the configured fallback project (may be empty).
func (a *Adapter) DefaultProject() string { return a.defaultProject }

// DefaultLocation returns the configured fallback location (may be empty).
func (a *Adapter) DefaultLocation() string { return a.defaultLocation }

// ResolveProject returns arg if non-empty (trimmed), else the env default.
func (a *Adapter) ResolveProject(arg string) string {
	if v := strings.TrimSpace(arg); v != "" {
		return v
	}
	return a.defaultProject
}

// ResolveLocation returns arg if non-empty (trimmed), else the env default.
func (a *Adapter) ResolveLocation(arg string) string {
	if v := strings.TrimSpace(arg); v != "" {
		return v
	}
	return a.defaultLocation
}

// New builds an Adapter from environment configuration.
//
// Jetder uses HTTP Basic auth: JETDER_AUTH_USER (service-account email) as the
// username and JETDER_TOKEN (or JETDER_AUTH_PASS) as the password. Both are
// required. Credentials are applied via the client's Auth hook (r.SetBasicAuth)
// and are never logged or returned in errors.
func New() (*Adapter, error) {
	user := strings.TrimSpace(os.Getenv(EnvAuthUser))
	// password: JETDER_TOKEN preferred, JETDER_AUTH_PASS accepted as an alias.
	token := strings.TrimSpace(os.Getenv(EnvToken))
	if token == "" {
		token = strings.TrimSpace(os.Getenv(EnvAuthPass))
	}
	if token == "" {
		return nil, ErrNoToken
	}
	// Require an explicit username — do NOT silently fall back to Bearer (Jetder
	// rejects Bearer, so a fallback would just fail confusingly).
	if user == "" {
		return nil, ErrNoUser
	}

	c := &client.Client{
		Endpoint: strings.TrimSpace(os.Getenv(EnvEndpoint)), // "" => default https://api.jetder.com/
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		Auth: func(r *http.Request) {
			r.SetBasicAuth(user, token)
		},
	}

	return &Adapter{
		client:          c,
		user:            user,
		token:           token,
		basicB64:        base64.StdEncoding.EncodeToString([]byte(user + ":" + token)),
		defaultProject:  strings.TrimSpace(os.Getenv(EnvDefaultProject)),
		defaultLocation: strings.TrimSpace(os.Getenv(EnvDefaultLocation)),
	}, nil
}

// Client returns the underlying api client. Callers use its resource accessors
// (Me(), Location(), Project(), Deployment(), Domain(), Route(), ...).
func (a *Adapter) Client() *client.Client {
	return a.client
}

// Creds returns the Basic-auth credential strings to scrub from any surfaced text
// (token, username, base64 header). Used by the log sanitizer so a log line that
// echoed our own credentials never reaches the client.
func (a *Adapter) Creds() []string {
	return []string{a.basicB64, a.token, a.user}
}

// apiHost returns the host of the configured Jetder API endpoint (used for the
// same-origin SSRF check before attaching Basic auth to an outbound request).
func (a *Adapter) apiHost() string {
	ep := strings.TrimSpace(os.Getenv(EnvEndpoint))
	if ep == "" {
		ep = "https://api.jetder.com/"
	}
	if u, err := url.Parse(ep); err == nil {
		return u.Host
	}
	return ""
}

// FetchURL performs a bounded GET of a Jetder-provided URL (e.g. a deployment's
// logUrl, which carries its own short-lived JWT in the query string) and returns up
// to maxBytes of the body plus whether it was truncated.
//
// Security:
//   - It NEVER sends Basic auth (or any Authorization header). The log URL is
//     self-authenticating via the JWT in its query string; sending our credentials
//     to the log server (or anywhere) would be wrong and a leak risk.
//   - SSRF guard: only http(s) URLs whose host is the configured API host or a
//     *.jetder.com host are allowed; anything else is rejected. (Tests point
//     JETDER_ENDPOINT at an httptest server, so that host is allowed too.)
//   - The body is read through an io.LimitReader, so an enormous log can never be
//     fully buffered. maxBytes must be > 0.
//   - It never returns/echoes the URL — callers must not surface it (the query
//     carries a token).
func (a *Adapter) FetchURL(ctx context.Context, rawURL string, maxBytes int64) (body []byte, truncated bool, err error) {
	if maxBytes <= 0 {
		return nil, false, errors.New("maxBytes must be positive")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		// Never echo the URL (it can carry a token in the query).
		return nil, false, errors.New("invalid log URL (must be http(s) with a host)")
	}
	if !a.allowedLogHost(u.Host) {
		return nil, false, errors.New("refusing to fetch log from an unexpected host")
	}
	// The URL's query carries a credential — require HTTPS so it isn't sent in the
	// clear. The ONLY exception is when the configured API endpoint is itself http
	// (a local/test endpoint) and the URL targets that same host.
	if u.Scheme != "https" && !a.isHTTPEndpointHost(u.Host) {
		return nil, false, errors.New("refusing to fetch log over plaintext http")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false, errors.New("could not build request")
	}
	req.Header.Set("Accept", "text/plain")
	// NO Authorization header: the URL's JWT is the auth. Do not attach credentials.

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, false, errors.New("request failed")
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("log server returned status %d", resp.StatusCode)
	}

	// Read at most maxBytes+1 so we can detect truncation without buffering more.
	limited := io.LimitReader(resp.Body, maxBytes+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, errors.New("could not read response")
	}
	if int64(len(b)) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}

// AllowedLogHostForTest exposes allowedLogHost for tests in the parent package.
func (a *Adapter) AllowedLogHostForTest(host string) bool { return a.allowedLogHost(host) }

// isHTTPEndpointHost reports whether the configured JETDER_ENDPOINT uses http (not
// https) AND its host matches the given host. This is the only case where a
// plaintext-http log fetch is permitted — a local/httptest endpoint.
func (a *Adapter) isHTTPEndpointHost(host string) bool {
	ep := strings.TrimSpace(os.Getenv(EnvEndpoint))
	if ep == "" {
		return false // default endpoint is https
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// allowedLogHost reports whether host is acceptable for a log fetch: the configured
// API host (so httptest endpoints work in tests) or any *.jetder.com host.
func (a *Adapter) allowedLogHost(host string) bool {
	h := strings.ToLower(host)
	if ah := a.apiHost(); ah != "" && h == strings.ToLower(ah) {
		return true
	}
	// Strip a port if present for the suffix check.
	if i := strings.LastIndex(h, ":"); i != -1 {
		h = h[:i]
	}
	return h == "jetder.com" || strings.HasSuffix(h, ".jetder.com")
}

// httpClient returns the adapter's HTTP client (or a default).
func (a *Adapter) httpClient() *http.Client {
	if a.client != nil && a.client.HTTPClient != nil {
		return a.client.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Redact removes any occurrence of the Basic-auth credentials — the password
// (token), the username, and the base64(user:token) header value — from an error
// message so leaked credentials can never reach MCP clients or logs. Returns nil
// for nil errors.
func (a *Adapter) Redact(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	changed := false
	// Order: longest/most-sensitive first. base64 contains neither user nor token
	// as a substring, so order between them doesn't matter, but redact the token
	// before the username (token is the real secret).
	for _, cred := range []string{a.basicB64, a.token, a.user} {
		if cred == "" {
			continue
		}
		if strings.Contains(msg, cred) {
			msg = strings.ReplaceAll(msg, cred, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		return errors.New(msg)
	}
	return err
}

// RedactValues redacts the Basic-auth credentials (token, username, base64 header
// value — via Redact) AND any of the given secret values (e.g. a submitted
// secret/password) from an error message. Use this on the error path of any tool
// that accepts secret material as input, so an upstream error that echoes the
// submitted value can never leak it to MCP clients. Returns nil for nil errors.
// Empty values are ignored.
func (a *Adapter) RedactValues(err error, secrets ...string) error {
	err = a.Redact(err)
	if err == nil {
		return nil
	}
	msg := err.Error()
	changed := false
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(msg, s) {
			msg = strings.ReplaceAll(msg, s, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		return errors.New(msg)
	}
	return err
}
