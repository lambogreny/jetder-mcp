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
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
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

// Log-snapshot read bounds. These are vars (not consts) so tests can shrink them to
// avoid real waits; they are NOT exposed as tool arguments.
var (
	logIdleTimeout    = 2 * time.Second
	logOverallTimeout = 10 * time.Second
)

// buildLogRequest validates a Jetder-provided log URL and builds a GET request for
// it. SECURITY (shared by every log fetch path):
//   - NEVER attach Basic auth / any Authorization header — the URL's JWT is the auth.
//   - SSRF guard: only http(s) URLs whose host is the configured API host or a
//     *.jetder.com host are allowed.
//   - HTTPS is required for jetder hosts (the query carries a credential); plaintext
//     http is allowed only when JETDER_ENDPOINT is itself http and targets that host.
//   - The URL is never echoed by callers (the query carries a token).
func (a *Adapter) buildLogRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("invalid log URL (must be http(s) with a host)")
	}
	if !a.allowedLogHost(u.Host) {
		return nil, errors.New("refusing to fetch log from an unexpected host")
	}
	if u.Scheme != "https" && !a.isHTTPEndpointHost(u.Host) {
		return nil, errors.New("refusing to fetch log over plaintext http")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("could not build request")
	}
	req.Header.Set("Accept", "text/event-stream, text/plain")
	// NO Authorization header.
	return req, nil
}

// LogSnapshot is the result of a bounded log read.
type LogSnapshot struct {
	// Lines are the parsed, human-readable log lines (SSE framing/JSON unwrapped).
	Lines []string
	// Truncated is true when reading stopped at a cap (bytes/events) rather than EOF.
	Truncated bool
	// Stream is true when the source was an SSE stream (text/event-stream).
	Stream bool
}

// FetchLogSnapshot reads a BOUNDED snapshot of a log URL. The Jetder log server is
// an SSE stream that never closes, so we cannot download it — we read until the
// first of: maxBytes (raw wire bytes) reached, maxEvents reached, the stream goes
// idle (no new data for logIdleTimeout after at least one event), or the overall
// deadline (logOverallTimeout) — then close the connection ourselves.
//
// Routing is by Content-Type: text/event-stream → SSE parser (unwrap data: {json});
// anything else → bounded plain-text reader. Neither path sends credentials.
func (a *Adapter) FetchLogSnapshot(ctx context.Context, rawURL string, maxBytes int64, maxEvents int) (*LogSnapshot, error) {
	if maxBytes <= 0 {
		return nil, errors.New("maxBytes must be positive")
	}
	if maxEvents <= 0 {
		maxEvents = 200
	}

	// Overall deadline guards against a hang regardless of the caller's ctx.
	cctx, cancel := context.WithTimeout(ctx, logOverallTimeout)
	defer cancel()

	req, err := a.buildLogRequest(cctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, errors.New("request failed")
	}
	// Do NOT drain the body on exit — it may be a never-ending stream, so draining
	// would hang. Cancelling the request context + Close() tears the connection down.
	defer func() { cancel(); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Read a small capped slice of the error body (not the whole stream).
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return nil, fmt.Errorf("log server returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	isSSE := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	return readBoundedLog(cctx, resp.Body, isSSE, maxBytes, maxEvents, cancel)
}

// readBoundedLog reads lines from r until a cap (maxBytes raw wire bytes / maxEvents),
// the stream goes idle (no new line for logIdleTimeout after at least one), or ctx is
// done — then returns. SSE framing/JSON is unwrapped when sse is true. cancel is
// invoked to release the request once we stop reading.
func readBoundedLog(ctx context.Context, r io.Reader, sse bool, maxBytes int64, maxEvents int, cancel context.CancelFunc) (*LogSnapshot, error) {
	type lineMsg struct {
		raw  string
		err  error
		done bool // [DONE] sentinel
	}
	ch := make(chan lineMsg, 64)
	// stop signals the producer to exit when the consumer returns early, so the
	// producer never blocks forever on a channel send (goroutine-leak guard).
	stop := make(chan struct{})

	// Producer: scan lines, counting RAW wire bytes against maxBytes. For SSE it
	// accumulates the (possibly multi-line) data: payloads of one event and emits on
	// the blank dispatch line. Every send is select-guarded on stop.
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(io.LimitReader(r, maxBytes+1))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var read int64
		var event []string // accumulated data: lines for the current SSE event

		send := func(m lineMsg) bool {
			select {
			case ch <- m:
				return true
			case <-stop:
				return false
			}
		}
		// flush emits the accumulated SSE event (joined per spec) and resets it.
		flush := func() bool {
			if len(event) == 0 {
				return true
			}
			payload := strings.Join(event, "\n")
			event = event[:0]
			if strings.TrimSpace(payload) == "[DONE]" {
				send(lineMsg{done: true})
				return false
			}
			return send(lineMsg{raw: payload})
		}

		for sc.Scan() {
			line := sc.Text()
			read += int64(len(line)) + 1
			if sse {
				if line == "" { // blank line dispatches the event
					if !flush() {
						return
					}
				} else if payload, ok := sseData(line); ok {
					event = append(event, payload) // multi-line data: accumulate
				}
				// non-data framing (event:/id:/comments) is ignored
			} else {
				if !send(lineMsg{raw: line}) {
					return
				}
			}
			if read > maxBytes {
				send(lineMsg{err: errTruncated})
				return
			}
		}
		// EOF: flush any pending SSE event (a stream that ended without a final blank).
		if sse && !flush() {
			return
		}
		if err := sc.Err(); err != nil {
			send(lineMsg{err: err})
		}
	}()

	// Ensure the producer is always released, even on an early return path: closing
	// stop unblocks a producer parked on a channel send, and cancel() tears down the
	// connection to unblock one parked in sc.Scan() on a never-closing stream.
	defer func() { cancel(); close(stop) }()

	out := &LogSnapshot{Stream: sse}
	idle := time.NewTimer(logOverallTimeout) // before the first event, wait the overall budget
	defer idle.Stop()
	gotOne := false

	for {
		select {
		case <-ctx.Done():
			cancel()
			out.Truncated = out.Truncated || !gotOne
			return finishLog(out, gotOne)
		case <-idle.C:
			// Idle (or overall budget hit before any event) → stop, snapshot what we have.
			cancel()
			return finishLog(out, gotOne)
		case m, ok := <-ch:
			if !ok {
				return finishLog(out, gotOne) // EOF: stream closed on its own.
			}
			if m.done {
				return finishLog(out, gotOne)
			}
			if m.err != nil {
				if m.err == errTruncated {
					out.Truncated = true
				}
				return finishLog(out, gotOne)
			}
			out.Lines = append(out.Lines, normalizeLogLine(m.raw, sse))
			gotOne = true
			if len(out.Lines) >= maxEvents {
				out.Truncated = true
				cancel()
				return finishLog(out, gotOne)
			}
			// Reset to the (shorter) idle timeout now that we've seen an event.
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(logIdleTimeout)
		}
	}
}

var errTruncated = errors.New("truncated")

func finishLog(out *LogSnapshot, gotOne bool) (*LogSnapshot, error) {
	if !gotOne && len(out.Lines) == 0 {
		return nil, errors.New("no log events received before timeout")
	}
	return out, nil
}

// sseData returns the payload of a `data:` SSE line (false for non-data lines).
func sseData(line string) (string, bool) {
	if strings.HasPrefix(line, "data:") {
		return strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "), true
	}
	return "", false
}

// normalizeLogLine turns a raw line (an SSE data payload, or a plain log line) into
// a human-readable "[ts] log" form. If it's JSON with a known log field, the message
// is extracted (and timestamp/level prefixed when present); otherwise the raw payload
// is kept verbatim. The result is NOT yet sanitized — the caller does that.
func normalizeLogLine(raw string, sse bool) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Only attempt JSON unwrap when it looks like a JSON object.
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			// Recognized log envelope (has a log/message/... key, even if empty)?
			if _, hasMsg := firstKey(m, "log", "message", "msg", "text"); hasMsg {
				msg := firstString(m, "log", "message", "msg", "text")
				if strings.TrimSpace(msg) == "" {
					return "" // empty log frame → dropped by the tail filter.
				}
				ts := firstString(m, "timestamp", "time", "ts")
				lvl := firstString(m, "level")
				prefix := ""
				if ts != "" {
					prefix += "[" + ts + "] "
				}
				if lvl != "" {
					prefix += lvl + " "
				}
				return prefix + strings.TrimRight(msg, "\n")
			}
		}
	}
	return s
}

// firstKey reports the first present key among keys (regardless of value).
func firstKey(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return k, true
		}
	}
	return "", false
}

// firstString returns the first non-empty string value among the given keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// SetLogTimeoutsForTest shrinks the snapshot read deadlines so tests don't wait the
// real (seconds-long) idle/overall budgets. Tests should restore the originals.
func SetLogTimeoutsForTest(idle, overall time.Duration) (restore func()) {
	oi, oo := logIdleTimeout, logOverallTimeout
	logIdleTimeout, logOverallTimeout = idle, overall
	return func() { logIdleTimeout, logOverallTimeout = oi, oo }
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
