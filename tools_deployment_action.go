package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// rePullSecretName matches a valid jetder pull-secret NAME (same shape as the
// upstream api.PullSecretCreate name: lowercase, 3..25 chars). Validating this
// BEFORE any deploy ensures a misplaced credential (e.g. a GHCR PAT pasted into
// the pull-secret arg) is rejected up front and never sent/echoed/surfaced.
var rePullSecretName = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)

// validatePullSecretName trims and validates a pull-secret name. Empty → ("",nil)
// (omit). Invalid (incl. anything that looks like a credential) → error.
func validatePullSecretName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if n := len(v); n < 3 || n > 25 {
		return "", fmt.Errorf("invalid pullSecret: must be a pull-secret NAME (3-25 chars), not a credential")
	}
	if !rePullSecretName.MatchString(v) {
		return "", fmt.Errorf("invalid pullSecret: must be a pull-secret NAME matching %s (a name, never a token/URL)", rePullSecretName.String())
	}
	return v, nil
}

// validateEnvKeys checks env-var KEYS only (never values — they may be secrets). The
// API does its own reEnvName validation; here we catch the obvious mistakes early and
// WITHOUT echoing any value: empty key, a key containing '=' (likely "KEY=val" pasted
// into the key), or control characters. Error messages name the offending KEY only.
func validateEnvKeys(env map[string]string) error {
	for k := range env {
		if err := validateEnvKey(k); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvKeyList(keys []string) error {
	for _, k := range keys {
		if err := validateEnvKey(k); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvKey(k string) error {
	if strings.TrimSpace(k) == "" {
		return fmt.Errorf("invalid env: a variable name is empty")
	}
	// A '=' in the KEY usually means a "KEY=value" pair was pasted into the key slot —
	// and that value may be a SECRET. NEVER echo the raw key here (it would leak the
	// value after '='). A generic message is enough.
	if strings.ContainsRune(k, '=') {
		return fmt.Errorf("invalid env name: a variable name must not contain '=' (put the value in the map value, not the key)")
	}
	for _, r := range k {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid env name: a variable name contains a control character")
		}
	}
	return nil
}

// envValues returns all VALUES across the given env maps, so they can be passed to
// RedactValues and scrubbed from any error (never logged or echoed otherwise).
func envValues(maps ...map[string]string) []string {
	var vals []string
	for _, m := range maps {
		for _, v := range m {
			if v != "" {
				vals = append(vals, v)
			}
		}
	}
	return vals
}

// envKeySummary returns a " envAdd=[K1,K2] envReplace=N removeEnv=[..] envGroups=[..]"
// fragment for the action summary — KEY NAMES and counts only, NEVER values.
func envKeySummary(in DeploymentDeployInput) string {
	var b strings.Builder
	if len(in.AddEnv) > 0 {
		fmt.Fprintf(&b, " addEnv=[%s]", strings.Join(sortedKeys(in.AddEnv), ","))
	}
	if len(in.Env) > 0 {
		fmt.Fprintf(&b, " env(replace)=[%s]", strings.Join(sortedKeys(in.Env), ","))
	}
	if len(in.RemoveEnv) > 0 {
		fmt.Fprintf(&b, " removeEnv=[%s]", strings.Join(in.RemoveEnv, ","))
	}
	if len(in.EnvGroups) > 0 {
		fmt.Fprintf(&b, " envGroups=[%s]", strings.Join(in.EnvGroups, ","))
	}
	return b.String()
}

// safeEnvKeys returns the keys safe to display: any key that fails validateEnvKey
// (empty, contains '=', or control chars) is replaced with a generic "[invalid]"
// placeholder — NEVER the raw key, since a "KEY=secret" paste would otherwise leak the
// value. Returns the rendered list and whether any key was masked.
func safeEnvKeys(keys []string) ([]string, bool) {
	out := make([]string, 0, len(keys))
	bad := false
	for _, k := range keys {
		if validateEnvKey(k) != nil {
			out = append(out, "[invalid]")
			bad = true
			continue
		}
		out = append(out, k)
	}
	return out, bad
}

// sortedKeys returns the map's keys in sorted order (deterministic summary).
func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// registerDeploymentActionTools registers the state-changing deployment tools:
// deploy, pause, resume, rollback. (delete is intentionally NOT exposed.)
//
// All are annotated ReadOnlyHint:false. rollback additionally carries
// DestructiveHint:true (it reverts state to a prior revision).
func registerDeploymentActionTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerDeploymentDeploy(server, adapter)
	registerDeploymentPause(server, adapter)
	registerDeploymentResume(server, adapter)
	registerDeploymentRollback(server, adapter)
}

// ActionResult is the common output for deployment actions: the resolved context
// plus a short human-readable summary of what was performed.
type ActionResult struct {
	ResolvedContext
	Name       string `json:"name" jsonschema:"deployment name acted upon"`
	Action     string `json:"action" jsonschema:"the action performed"`
	Success    bool   `json:"success" jsonschema:"whether the action was accepted"`
	Detail     string `json:"detail,omitempty" jsonschema:"extra detail (e.g. target revision)"`
	PullSecret string `json:"pullSecret,omitempty" jsonschema:"the pull secret name applied (deploy only), echoed for verification"`
}

func actionResult(project, location, name, action, detail string) (*mcp.CallToolResult, ActionResult, error) {
	out := ActionResult{
		ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
		Name:            name,
		Action:          action,
		Success:         true,
		Detail:          detail,
	}
	msg := fmt.Sprintf("%s %s [project=%s location=%s]", action, name, project, location)
	if detail != "" {
		msg += " " + detail
	}
	return textResult(msg), out, nil
}

// nonReadOnly returns annotations for a state-changing but restorative/additive
// tool — one whose effect only restores or adds, never degrades existing state
// (e.g. deployment-resume). DestructiveHint:false.
func nonReadOnly() *mcp.ToolAnnotations {
	ro := false
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &ro}
}

// destructive returns annotations for a state-changing tool that can degrade or
// replace existing running state (deploy changes the active revision, pause
// stops serving, rollback reverts). DestructiveHint:true.
func destructive() *mcp.ToolAnnotations {
	d := true
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &d}
}

// ---- deploy ----

// DeploymentDeployInput captures the common fields for triggering a deploy.
// Image is required by the API; env/replicas/etc. are optional overrides.
type DeploymentDeployInput struct {
	Project     string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location    string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name        string `json:"name" jsonschema:"deployment name"`
	Branch      string `json:"branch,omitempty" jsonschema:"branch"`
	Image       string `json:"image" jsonschema:"container image to deploy"`
	MinReplicas *int   `json:"minReplicas,omitempty" jsonschema:"minimum replicas"`
	MaxReplicas *int   `json:"maxReplicas,omitempty" jsonschema:"maximum replicas"`
	// PullSecret is the NAME of an existing pull secret (created out-of-band) used
	// to pull a private image. Only the name is accepted here — never credentials.
	PullSecret string `json:"pullSecret,omitempty" jsonschema:"name of an existing pull secret for private images (credentials are NOT accepted here)"`
	// Runtime environment variables. VALUES MAY BE SECRETS — they are write-only and
	// are NEVER echoed back (only key names appear in output/errors).
	AddEnv    map[string]string `json:"addEnv,omitempty" jsonschema:"environment variables to ADD/UPDATE, merged onto the deployment's existing env (the safe default; values are write-only and never returned)"`
	Env       map[string]string `json:"env,omitempty" jsonschema:"REPLACE the deployment's entire env with this set (destructive — drops existing vars; prefer addEnv. Cannot be combined with addEnv. values are write-only)"`
	RemoveEnv []string          `json:"removeEnv,omitempty" jsonschema:"names of environment variables to remove"`
	EnvGroups []string          `json:"envGroups,omitempty" jsonschema:"names of existing env groups to attach to the deployment"`
}

func registerDeploymentDeploy(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentDeployInput) (*mcp.CallToolResult, ActionResult, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ActionResult{}, err
		}
		if strings.TrimSpace(in.Image) == "" {
			return nil, ActionResult{}, fmt.Errorf("image required")
		}
		// env: addEnv (merge) and env (replace-all) are mutually exclusive. Validate the
		// KEYS before any POST so a malformed request is rejected up front (nothing
		// echoed). Values are never validated/echoed — they may be secrets.
		if len(in.AddEnv) > 0 && len(in.Env) > 0 {
			return nil, ActionResult{}, fmt.Errorf("addEnv and env cannot be combined: addEnv merges onto existing env, env replaces all of it")
		}
		if err := validateEnvKeys(in.AddEnv); err != nil {
			return nil, ActionResult{}, err
		}
		if err := validateEnvKeys(in.Env); err != nil {
			return nil, ActionResult{}, err
		}
		if err := validateEnvKeyList(in.RemoveEnv); err != nil {
			return nil, ActionResult{}, err
		}

		m := &api.DeploymentDeploy{
			Project:     project,
			Location:    location,
			Name:        name,
			Branch:      in.Branch,
			Image:       strings.TrimSpace(in.Image),
			MinReplicas: in.MinReplicas,
			MaxReplicas: in.MaxReplicas,
			AddEnv:      in.AddEnv,
			Env:         in.Env,
			RemoveEnv:   in.RemoveEnv,
			EnvGroups:   in.EnvGroups,
		}
		// pullSecret: NAME only (never credentials); validate BEFORE the deploy so a
		// misplaced token/URL is rejected up front (no POST, nothing echoed). Omit
		// when empty so the API keeps the deployment's existing value.
		pullSecret, err := validatePullSecretName(in.PullSecret)
		if err != nil {
			return nil, ActionResult{}, err
		}
		if pullSecret != "" {
			m.PullSecret = &pullSecret
		}
		if _, err = adapter.Client().Deployment().Deploy(ctx, m); err != nil {
			// Defense in depth: scrub any env VALUE that an API error might reflect back.
			return nil, ActionResult{}, adapter.RedactValues(err, envValues(in.AddEnv, in.Env)...)
		}
		// Summary echoes only KEY NAMES + counts — never any env value.
		summary := "image=" + strings.TrimSpace(in.Image) + envKeySummary(in)
		res, out, err := actionResult(project, location, name, "deploy", summary)
		out.PullSecret = pullSecret // echo for verification (empty if not set)
		return res, out, err
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-deploy",
		Description: "Deploy or redeploy a service with the given image (changes the active revision).",
		Annotations: destructive(),
	}, handler)
}

// ---- pause / resume ----

// DeploymentLifecycleInput is shared by pause/resume/rollback target selection.
type DeploymentLifecycleInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
}

func registerDeploymentPause(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentLifecycleInput) (*mcp.CallToolResult, ActionResult, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ActionResult{}, err
		}
		_, err = adapter.Client().Deployment().Pause(ctx, &api.DeploymentPause{
			Project: project, Location: location, Name: name, Branch: in.Branch,
		})
		if err != nil {
			return nil, ActionResult{}, adapter.Redact(err)
		}
		return actionResult(project, location, name, "pause", "")
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-pause",
		Description: "Pause a running deployment (scales it down so the service stops serving; reverse with deployment-resume).",
		Annotations: destructive(),
	}, handler)
}

func registerDeploymentResume(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentLifecycleInput) (*mcp.CallToolResult, ActionResult, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ActionResult{}, err
		}
		_, err = adapter.Client().Deployment().Resume(ctx, &api.DeploymentResume{
			Project: project, Location: location, Name: name, Branch: in.Branch,
		})
		if err != nil {
			return nil, ActionResult{}, adapter.Redact(err)
		}
		return actionResult(project, location, name, "resume", "")
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-resume",
		Description: "Resume a paused deployment.",
		Annotations: nonReadOnly(),
	}, handler)
}

// ---- rollback (destructive) ----

type DeploymentRollbackInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
	Revision int    `json:"revision" jsonschema:"revision number to roll back to (must be >= 1)"`
}

func registerDeploymentRollback(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentRollbackInput) (*mcp.CallToolResult, ActionResult, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ActionResult{}, err
		}
		if in.Revision < 1 {
			return nil, ActionResult{}, fmt.Errorf("revision must be >= 1")
		}
		_, err = adapter.Client().Deployment().Rollback(ctx, &api.DeploymentRollback{
			Project: project, Location: location, Name: name, Branch: in.Branch, Revision: in.Revision,
		})
		if err != nil {
			return nil, ActionResult{}, adapter.Redact(err)
		}
		return actionResult(project, location, name, "rollback", fmt.Sprintf("revision=%d", in.Revision))
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-rollback",
		Description: "Roll a deployment back to a previous revision. This changes running state; use with care.",
		Annotations: destructive(),
	}, handler)
}
