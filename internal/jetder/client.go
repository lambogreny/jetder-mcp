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
	"encoding/base64"
	"errors"
	"net/http"
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
