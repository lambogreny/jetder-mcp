// Package cloudflare is a small adapter around the Cloudflare REST API
// (api.cloudflare.com/client/v4) used by jetder-mcp to manage DNS records (and,
// later, domain registration) as part of the point-a-domain flow.
//
// It is intentionally SEPARATE from package jetder: Cloudflare uses Bearer-token
// auth (not Jetder's Basic auth), a different base URL, and a different response
// envelope ({success, result, errors}). The adapter is LAZY/OPTIONAL — a
// jetder-only server starts fine without any Cloudflare env set; CF tools simply
// report that Cloudflare is not configured.
//
// Security: CLOUDFLARE_API_TOKEN and the full "Bearer <token>" header value are
// redacted from every error/log this package produces.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.cloudflare.com/client/v4"

// Env vars.
const (
	EnvToken     = "CLOUDFLARE_API_TOKEN"
	EnvAccountID = "CLOUDFLARE_ACCOUNT_ID" // required only for Registrar (account-scoped)
	EnvBaseURL   = "CLOUDFLARE_API_BASE"   // optional override (testing)
)

// ErrNotConfigured is returned when Cloudflare env is not set. Callers (CF tools)
// surface this as a friendly "Cloudflare not configured" message; the server
// itself must NOT fail to start when CF is absent.
var ErrNotConfigured = errors.New("cloudflare not configured: set CLOUDFLARE_API_TOKEN")

// Client talks to the Cloudflare REST API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	accountID  string
	// redactions: token + the full Bearer header value.
	redactions []string
}

// New builds a Client from the environment. It returns (nil, nil) when CF is not
// configured, so the caller can treat Cloudflare as optional:
//
//	cf, err := cloudflare.New()
//	if err != nil { /* misconfig */ }
//	if cf == nil { /* CF tools disabled */ }
func New() (*Client, error) {
	token := strings.TrimSpace(getenv(EnvToken))
	if token == "" {
		return nil, nil // not configured → optional, not an error
	}
	base := strings.TrimSpace(getenv(EnvBaseURL))
	if base == "" {
		base = defaultBaseURL
	}
	base = strings.TrimSuffix(base, "/")

	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    base,
		token:      token,
		accountID:  strings.TrimSpace(getenv(EnvAccountID)),
		redactions: []string{"Bearer " + token, token},
	}, nil
}

// getenv is a tiny indirection so tests can stub env if needed.
var getenv = os.Getenv

// AccountID returns the configured account id (may be empty; needed for Registrar).
func (c *Client) AccountID() string { return c.accountID }

// Redact removes the token and Bearer header value from a string.
func (c *Client) Redact(s string) string {
	for _, r := range c.redactions {
		if r != "" {
			s = strings.ReplaceAll(s, r, "[REDACTED]")
		}
	}
	return s
}

// redactErr wraps an error with credentials redacted.
func (c *Client) redactErr(err error) error {
	if err == nil {
		return nil
	}
	red := c.Redact(err.Error())
	if red != err.Error() {
		return errors.New(red)
	}
	return err
}

// apiError represents one Cloudflare error entry.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// resultInfo carries Cloudflare list pagination metadata.
type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

// envelope is the standard Cloudflare response wrapper.
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Messages   []string        `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *resultInfo     `json:"result_info"`
}

// do performs a request and decodes result into out (out may be nil). It checks
// both transport status and the envelope's success flag; CF returns errors in
// the body, so HTTP 200 alone is not "ok". All errors are credential-redacted.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	_, err := c.doPaged(ctx, method, path, out, withBody(body))
	return err
}

type doOpt struct{ body any }

func withBody(b any) func(*doOpt) { return func(o *doOpt) { o.body = b } }

// doPaged performs a request, decodes result into out (may be nil), and returns
// the list pagination info (nil for non-list responses). Errors are redacted.
func (c *Client) doPaged(ctx context.Context, method, path string, out any, opts ...func(*doOpt)) (*resultInfo, error) {
	var o doOpt
	for _, f := range opts {
		f(&o)
	}

	var reqBody io.Reader
	if o.body != nil {
		b, err := json.Marshal(o.body)
		if err != nil {
			return nil, c.redactErr(err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, c.redactErr(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.redactErr(err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, c.redactErr(fmt.Errorf("cloudflare: decode response (status %d): %v", resp.StatusCode, err))
	}
	if !env.Success {
		return nil, c.redactErr(fmt.Errorf("cloudflare api error (status %d): %s", resp.StatusCode, formatAPIErrors(env.Errors)))
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, c.redactErr(fmt.Errorf("cloudflare: decode result: %v", err))
		}
	}
	return env.ResultInfo, nil
}

func formatAPIErrors(errs []apiError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

// ===== Zones =====

// Zone is a minimal view of a Cloudflare zone.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListZones returns zones, optionally filtered by exact name. It follows
// pagination (result_info) so callers see ALL matching zones, not just page 1.
func (c *Client) ListZones(ctx context.Context, name string) ([]Zone, error) {
	var all []Zone
	for page := 1; ; page++ {
		q := url.Values{}
		if name != "" {
			q.Set("name", name)
		}
		q.Set("per_page", "50")
		q.Set("page", fmt.Sprintf("%d", page))

		var zones []Zone
		info, err := c.doPaged(ctx, http.MethodGet, "/zones?"+q.Encode(), &zones)
		if err != nil {
			return nil, err
		}
		all = append(all, zones...)
		if info == nil || page >= info.TotalPages || len(zones) == 0 {
			break
		}
	}
	return all, nil
}

// FindZoneForName resolves the zone that owns a DNS name. It queries candidate
// zone names from MOST specific to the apex via exact-name lookup
// (GET /zones?name=<exact>), so it is correct regardless of how many zones the
// account has (no full-account scan / no pagination-window blind spot). The
// first (longest) candidate that exists wins. Returns ErrZoneNotFound if none
// exists — callers must NOT create records against a guessed zone.
func (c *Client) FindZoneForName(ctx context.Context, recordName string) (*Zone, error) {
	recordName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(recordName)), ".")
	if recordName == "" {
		return nil, errors.New("record name required")
	}
	labels := strings.Split(recordName, ".")
	// candidates: full name, then drop one leftmost label at a time, down to the
	// last two labels (a registrable apex needs >= 2 labels).
	for i := 0; i+1 < len(labels); i++ {
		candidate := strings.Join(labels[i:], ".")
		zones, err := c.ListZones(ctx, candidate) // exact-name query
		if err != nil {
			return nil, err
		}
		for j := range zones {
			if strings.EqualFold(zones[j].Name, candidate) {
				return &zones[j], nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no Cloudflare zone owns %q — add the zone (or register the domain) first", ErrZoneNotFound, recordName)
}

// ErrZoneNotFound indicates no configured zone owns the requested name.
var ErrZoneNotFound = errors.New("cloudflare zone not found")

// ===== DNS records =====

// DNSRecord is a Cloudflare DNS record (subset).
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	ZoneID  string `json:"zone_id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied bool   `json:"proxied"`
}

// ListDNSRecords lists records in a zone filtered by type+name, following
// pagination so ALL matches are returned (not just page 1).
func (c *Client) ListDNSRecords(ctx context.Context, zoneID, recType, name string) ([]DNSRecord, error) {
	var all []DNSRecord
	for page := 1; ; page++ {
		q := url.Values{}
		if recType != "" {
			q.Set("type", recType)
		}
		if name != "" {
			q.Set("name", name)
		}
		q.Set("per_page", "100")
		q.Set("page", fmt.Sprintf("%d", page))

		var recs []DNSRecord
		info, err := c.doPaged(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+q.Encode(), &recs)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if info == nil || page >= info.TotalPages || len(recs) == 0 {
			break
		}
	}
	return all, nil
}

// CreateDNSResult reports the outcome of an idempotent create.
type CreateDNSResult struct {
	Record        DNSRecord
	AlreadyExists bool // a record with the same type+name+content already existed
}

// CreateDNSRecord idempotently creates a record. It lists existing records by
// type+name first:
//   - same type+name+content already present → AlreadyExists=true, no write.
//   - same type+name but DIFFERENT content → conflict error (NEVER overwrites).
//   - otherwise → POST a new record.
//
// ttl defaults to 1 (auto) and proxied defaults to false (appropriate for Jetder
// verification + pointing) unless the caller overrides.
func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*CreateDNSResult, error) {
	if rec.Type == "" || rec.Name == "" || rec.Content == "" {
		return nil, errors.New("dns record requires type, name and content")
	}
	if rec.TTL == 0 {
		rec.TTL = 1 // auto
	}

	// Preflight: list ALL records at this name (every type, paginated) and apply
	// Cloudflare's same-name conflict rules BEFORE writing — never overwrite.
	atName, err := c.ListDNSRecords(ctx, zoneID, "", rec.Name)
	if err != nil {
		return nil, err
	}
	for i := range atName {
		e := atName[i]
		if !strings.EqualFold(e.Name, rec.Name) {
			continue
		}
		if conflictReason := dnsConflict(e, rec); conflictReason != "" {
			// idempotent no-op: identical record already present.
			if strings.EqualFold(e.Type, rec.Type) && e.Content == rec.Content {
				return &CreateDNSResult{Record: e, AlreadyExists: true}, nil
			}
			return nil, fmt.Errorf("conflict: cannot create %s %q: %s (existing %s %q); not overwriting",
				rec.Type, rec.Name, conflictReason, e.Type, e.Content)
		}
	}

	var created DNSRecord
	if err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", rec, &created); err != nil {
		return nil, err
	}
	return &CreateDNSResult{Record: created}, nil
}

// dnsConflict reports why an existing record e conflicts with a record we want to
// create (want), per Cloudflare's same-name rules. Empty string = no conflict.
// Note: identical (same type+content) returns a reason here too; the caller turns
// that specific case into an idempotent AlreadyExists.
func dnsConflict(e, want DNSRecord) string {
	et, wt := strings.ToUpper(e.Type), strings.ToUpper(want.Type)
	// CNAME cannot coexist with any other record at the same name (and vice versa).
	if et == "CNAME" || wt == "CNAME" {
		if et != wt {
			return "CNAME cannot coexist with other record types at the same name"
		}
		// both CNAME: only one allowed; same content = idempotent, diff = conflict.
		if e.Content == want.Content {
			return "record already exists"
		}
		return "a different CNAME already exists at this name"
	}
	// NS records cannot coexist with non-NS at the same name.
	if (et == "NS") != (wt == "NS") {
		return "NS cannot coexist with other record types at the same name"
	}
	// Same type: same content = idempotent, different content = conflict.
	if et == wt {
		if e.Content == want.Content {
			return "record already exists"
		}
		return "a record of this type with different content already exists"
	}
	// Different non-special types at the same name (e.g. A + TXT) are allowed.
	return ""
}
