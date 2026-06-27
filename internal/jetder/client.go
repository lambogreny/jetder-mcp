// Package jetder is a thin adapter around the github.com/jetder-core/api client.
//
// It centralizes:
//   - construction of the underlying *client.Client
//   - bearer-token auth pulled from the JETDER_TOKEN environment variable
//   - error translation / redaction so the raw token never leaks into MCP output
//
// The adapter intentionally exposes only the high-level api.* interfaces the MCP
// server needs; it does not re-implement any API behavior.
package jetder

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jetder-core/api/client"
)

// EnvToken is the environment variable that holds the Jetder API bearer token.
const EnvToken = "JETDER_TOKEN"

// EnvEndpoint optionally overrides the Jetder API endpoint (mainly for testing).
const EnvEndpoint = "JETDER_ENDPOINT"

// Default-context env vars. When a tool's project/location arg is empty, these
// supply a fallback. Per-tool args remain the source of truth and always win.
const (
	EnvDefaultProject  = "JETDER_DEFAULT_PROJECT"
	EnvDefaultLocation = "JETDER_DEFAULT_LOCATION"
)

// ErrNoToken is returned when JETDER_TOKEN is not set.
var ErrNoToken = errors.New("JETDER_TOKEN environment variable is required")

// Adapter wraps a configured Jetder API client.
type Adapter struct {
	client          *client.Client
	token           string
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
// It reads JETDER_TOKEN (required) and JETDER_ENDPOINT (optional). The token is
// injected as an "Authorization: Bearer <token>" header on every request via the
// client's Auth hook; it is never logged or returned in errors.
func New() (*Adapter, error) {
	token := strings.TrimSpace(os.Getenv(EnvToken))
	if token == "" {
		return nil, ErrNoToken
	}

	c := &client.Client{
		Endpoint: strings.TrimSpace(os.Getenv(EnvEndpoint)), // "" => default https://api.jetder.com/
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		Auth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		},
	}

	return &Adapter{
		client:          c,
		token:           token,
		defaultProject:  strings.TrimSpace(os.Getenv(EnvDefaultProject)),
		defaultLocation: strings.TrimSpace(os.Getenv(EnvDefaultLocation)),
	}, nil
}

// Client returns the underlying api client. Callers use its resource accessors
// (Me(), Location(), Project(), Deployment(), Domain(), Route(), ...).
func (a *Adapter) Client() *client.Client {
	return a.client
}

// Redact removes any occurrence of the bearer token from an error message so a
// leaked token can never reach MCP clients or logs. Returns nil for nil errors.
func (a *Adapter) Redact(err error) error {
	if err == nil {
		return nil
	}
	if a.token == "" {
		return err
	}
	msg := err.Error()
	if strings.Contains(msg, a.token) {
		return errors.New(strings.ReplaceAll(msg, a.token, "[REDACTED]"))
	}
	return err
}
