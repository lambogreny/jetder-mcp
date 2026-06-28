package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// deployment-diagnose ("Deploy Doctor") is a READ-ONLY tool that correlates the
// signals a deployment exposes — its status, pod health (statusUrl), Kubernetes
// events (eventUrl), and recent logs (logUrl) — into a structured diagnosis: a list
// of likely causes, each with a confidence and the evidence it was inferred from.
//
// It does NOT mutate anything and does NOT auto-run fixes; "suggestion" fields are
// plain advice. When no rule matches, it returns the raw (sanitized) evidence and an
// "unknown" assessment rather than guessing — better honest than confidently wrong.

const (
	diagLogTail      = 60
	diagLogMaxBytes  = 64 * 1024
	diagJSONMaxBytes = 64 * 1024
)

// DeploymentDiagnoseInput selects the deployment to diagnose.
type DeploymentDiagnoseInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
}

// PodHealth mirrors the deployment statusUrl payload (pod counts).
type PodHealth struct {
	Count     int `json:"count" jsonschema:"total pods"`
	Ready     int `json:"ready" jsonschema:"ready pods"`
	Succeeded int `json:"succeeded" jsonschema:"succeeded pods"`
	Failed    int `json:"failed" jsonschema:"failed pods"`
}

// DiagnosisCause is one likely explanation with its confidence and evidence.
type DiagnosisCause struct {
	Cause      string `json:"cause" jsonschema:"short name of the likely cause (e.g. CrashLoopBackOff)"`
	Confidence string `json:"confidence" jsonschema:"high | medium | low"`
	Evidence   string `json:"evidence" jsonschema:"the (sanitized) log line / event that this was inferred from"`
	Suggestion string `json:"suggestion" jsonschema:"plain-language suggested fix (advice only, not auto-applied)"`
}

// DeploymentDiagnoseOutput is the structured diagnosis.
type DeploymentDiagnoseOutput struct {
	ResolvedContext
	Name string `json:"name" jsonschema:"deployment name"`
	// Status is the coarse deployment status (Pending/Success/Error/Cancelled).
	Status string `json:"status" jsonschema:"deployment status"`
	// Health is the pod-level snapshot (nil if statusUrl was unavailable).
	Health *PodHealth `json:"health,omitempty" jsonschema:"pod readiness snapshot"`
	// Healthy is a quick boolean: status Success and all pods ready, no failures.
	Healthy bool `json:"healthy" jsonschema:"true if the deployment looks healthy"`
	// Assessment: "healthy", "unhealthy", or "unknown" (no rule matched).
	Assessment string           `json:"assessment" jsonschema:"healthy | unhealthy | unknown"`
	Causes     []DiagnosisCause `json:"causes" jsonschema:"likely causes, most confident first (empty if healthy)"`
	// Advisories are optimization/scaling suggestions (resource utilization, replica
	// readiness) — separate from causes, since a healthy deployment can still be
	// over-/under-provisioned. Always an array (never null).
	Advisories []DiagnosisCause `json:"advisories" jsonschema:"scaling/resource suggestions (advice only, not applied)"`
	// EventsSummary / RecentLogs are sanitized evidence for the caller (or an LLM) to
	// reason further. RecentLogs is the tail of the app log.
	EventsSummary string `json:"eventsSummary,omitempty" jsonschema:"sanitized summary of recent Kubernetes events"`
	RecentLogs    string `json:"recentLogs,omitempty" jsonschema:"sanitized tail of the app log"`
}

func registerDeploymentDiagnose(server *mcp.Server, adapter *jetder.Adapter, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentDiagnoseInput) (*mcp.CallToolResult, DeploymentDiagnoseOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentDiagnoseOutput{}, err
		}

		dep, err := adapter.Client().Deployment().Get(ctx, &api.DeploymentGet{
			Project: project, Location: location, Name: name, Branch: in.Branch,
		})
		if err != nil {
			return nil, DeploymentDiagnoseOutput{}, adapter.Redact(err)
		}

		out := DeploymentDiagnoseOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Name:            name,
			Status:          dep.Status.Text(),
		}

		// 1) Pod health snapshot (statusUrl) — the clearest single signal.
		out.Health = fetchPodHealth(ctx, adapter, dep.StatusURL)

		// 2) Events (eventUrl, JSON) + logs (logUrl, SSE) — sanitized evidence.
		events := fetchEvents(ctx, adapter, dep.EventURL)
		out.EventsSummary = summarizeEvents(adapter, events)
		out.RecentLogs = fetchRecentLogs(ctx, adapter, dep.LogURL)

		// 3) Decide healthy/unhealthy + run the rule-based pattern matcher.
		out.Healthy = isHealthy(dep.Status.Text(), out.Health)
		out.Causes = []DiagnosisCause{} // never nil — the output schema wants an array
		if out.Healthy {
			out.Assessment = "healthy"
		} else {
			if c := diagnose(adapter, events, out.RecentLogs, out.Health, dep.Status.Text()); len(c) > 0 {
				out.Causes = c
				out.Assessment = "unhealthy"
			} else {
				// Not healthy, but no rule matched: be honest, hand back evidence.
				out.Assessment = "unknown"
			}
		}

		// 4) Scaling/resource advisories (optimization, separate from causes). Pulls
		// recent metrics; advice only, never auto-scales. Best-effort (skipped on error).
		out.Advisories = scalingAdvisories(ctx, adapter, project, location, name, in.Branch, out.Health, out.Healthy)

		summary := fmt.Sprintf("diagnose %s: %s", name, out.Assessment)
		if len(out.Causes) > 0 {
			summary += " — " + out.Causes[0].Cause
		}
		if len(out.Advisories) > 0 {
			summary += fmt.Sprintf(" (%d advisory)", len(out.Advisories))
		}
		return textResult(summary), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "deployment-diagnose",
		Description: "Diagnose a deployment's health (\"Deploy Doctor\"). Correlates status, pod " +
			"readiness, Kubernetes events, and recent logs into likely causes — each with a " +
			"confidence and the evidence it came from — plus plain-language suggestions. It is " +
			"read-only and never applies changes; if nothing matches a known pattern it returns " +
			"the raw evidence and an \"unknown\" assessment rather than guessing.",
		Annotations: readOnly(),
	}, handler)
}

// --- evidence fetchers (all sanitized, all best-effort/non-fatal) -------------

func fetchPodHealth(ctx context.Context, adapter *jetder.Adapter, statusURL string) *PodHealth {
	if strings.TrimSpace(statusURL) == "" {
		return nil
	}
	b, _, err := adapter.FetchJSON(ctx, statusURL, diagJSONMaxBytes)
	if err != nil {
		return nil
	}
	var h PodHealth
	if err := json.Unmarshal(b, &h); err != nil {
		return nil
	}
	return &h
}

// eventItem + fetchEvents now live in tools_deployment_events_shared.go (shared with
// the deployment-events tool). diagJSONMaxBytes (status fetch) stays here.

func summarizeEvents(adapter *jetder.Adapter, events []eventItem) string {
	if len(events) == 0 {
		return ""
	}
	var lines []string
	for _, e := range events {
		if t := e.text(); t != "" {
			lines = append(lines, t) // already sanitized at fetch time
		}
	}
	return strings.Join(lines, "\n")
}

func fetchRecentLogs(ctx context.Context, adapter *jetder.Adapter, logURL string) string {
	if strings.TrimSpace(logURL) == "" {
		return ""
	}
	snap, err := adapter.FetchLogSnapshot(ctx, logURL, diagLogMaxBytes, diagLogTail*2)
	if err != nil || snap == nil {
		return ""
	}
	clean := make([]string, 0, len(snap.Lines))
	for _, ln := range snap.Lines {
		clean = append(clean, sanitizeLog(adapter, ln, logURL))
	}
	logs, _ := tailLinesSlice(clean, diagLogTail)
	return logs
}

// --- health + diagnosis -------------------------------------------------------

func isHealthy(status string, h *PodHealth) bool {
	if !strings.EqualFold(status, "success") {
		return false
	}
	if h == nil {
		return false // can't confirm health without the pod snapshot
	}
	return h.Failed == 0 && h.Ready >= 1 && h.Ready >= h.Count
}

// diagPattern is one rule: if any needle appears (case-insensitive) in the haystack
// (events + logs), it yields a cause.
type diagPattern struct {
	cause      string
	needles    []string
	confidence string
	suggestion string
}

var diagPatterns = []diagPattern{
	{
		// FIRST so it wins over the generic CrashLoopBackOff it usually co-occurs with:
		// an arch mismatch (e.g. an arm64 image built on Apple Silicon running on the
		// amd64 cluster) surfaces as "exec format error" and then crash-loops. The arch
		// fix is the actionable root cause.
		cause:      "ArchitectureMismatch",
		needles:    []string{"exec format error", "cannot execute binary file", "no matching manifest for linux/amd64", "no match for platform", "exec user process caused"},
		confidence: "high",
		suggestion: "The image's CPU architecture does not match the cluster (linux/amd64). If you built on Apple Silicon (M1/M2/M3), rebuild for amd64: `docker build --platform linux/amd64 ...` (or `docker buildx build --platform linux/amd64`), push, and redeploy.",
	},
	{
		cause:      "ImagePullBackOff",
		needles:    []string{"ImagePullBackOff", "ErrImagePull", "pull access denied", "manifest unknown", "not found: manifest"},
		confidence: "high",
		suggestion: "Check the image name/tag and that the deployment's pull secret can access the registry (private images need a valid pull secret).",
	},
	{
		cause:      "CrashLoopBackOff",
		needles:    []string{"CrashLoopBackOff", "Back-off restarting failed container", "exit code 1", "exited with code"},
		confidence: "high",
		suggestion: "The container starts then exits. Check recentLogs for the startup error (panic, missing config, failed dependency) and fix the app, then redeploy.",
	},
	{
		cause:      "OOMKilled",
		needles:    []string{"OOMKilled", "out of memory", "memory limit", "cannot allocate memory"},
		confidence: "high",
		suggestion: "The container exceeded its memory limit. Raise the memory limit or reduce the app's memory use.",
	},
	{
		cause:      "PortMismatch",
		needles:    []string{"address already in use", "bind: ", "listen tcp", "connection refused"},
		confidence: "medium",
		suggestion: "The app may be listening on a different port than the deployment expects. Make the app listen on $PORT (or align the route's target port).",
	},
	{
		cause:      "HealthCheckFailing",
		needles:    []string{"Unhealthy", "Liveness probe failed", "Readiness probe failed", "probe failed"},
		confidence: "medium",
		suggestion: "Liveness/readiness probes are failing. Verify the health endpoint path/port responds 200 and the app is ready before the probe runs.",
	},
	{
		cause:      "FailedScheduling",
		needles:    []string{"FailedScheduling", "Insufficient cpu", "Insufficient memory", "no nodes available"},
		confidence: "medium",
		suggestion: "The pod can't be scheduled (not enough cluster resources). Lower the requested CPU/memory or reduce replicas.",
	},
	{
		cause:      "AppStartupError",
		needles:    []string{"panic:", "fatal error", "Traceback (most recent call last)", "Error: Cannot find module", "undefined environment", "no such file or directory"},
		confidence: "medium",
		suggestion: "The app crashed on startup. Check recentLogs for the specific error (often a missing env var, file, or dependency).",
	},
}

// diagnose runs the rule-based matcher over events + logs (+ health/status) and
// returns causes most-confident-first. Each cause cites the evidence line it matched.
// recentLogs is already sanitized by the caller; event text is sanitized HERE before
// it can become cited evidence (an event message can carry a secret too).
func diagnose(adapter *jetder.Adapter, events []eventItem, recentLogs string, h *PodHealth, status string) []DiagnosisCause {
	// Build a searchable corpus that keeps line provenance for evidence citing.
	type line struct{ src, text string }
	var corpus []line
	for _, e := range events {
		if t := e.text(); t != "" { // already sanitized at fetch time (incl. eventUrl/JWT)
			corpus = append(corpus, line{"event", t})
		}
	}
	for _, l := range strings.Split(recentLogs, "\n") {
		if strings.TrimSpace(l) != "" {
			corpus = append(corpus, line{"log", l})
		}
	}

	var causes []DiagnosisCause
	seen := map[string]bool{}
	for _, p := range diagPatterns {
		for _, l := range corpus {
			if containsAnyFold(l.text, p.needles) {
				if seen[p.cause] {
					break
				}
				seen[p.cause] = true
				causes = append(causes, DiagnosisCause{
					Cause:      p.cause,
					Confidence: p.confidence,
					Evidence:   l.src + ": " + strings.TrimSpace(l.text),
					Suggestion: p.suggestion,
				})
				break
			}
		}
	}

	// A status of Error with no specific match still deserves a low-confidence note.
	if len(causes) == 0 && strings.EqualFold(status, "error") {
		causes = append(causes, DiagnosisCause{
			Cause:      "DeploymentError",
			Confidence: "low",
			Evidence:   "deployment status = Error",
			Suggestion: "The deployment is in an error state but no specific pattern matched. Review eventsSummary and recentLogs for details.",
		})
	}
	return causes
}

func containsAnyFold(haystack string, needles []string) bool {
	h := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(h, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// --- scaling / resource advisories --------------------------------------------

const (
	diagMetricsRange = "1h"
	highUtilPct      = 0.80 // >=80% of limit (avg) → near-limit advisory
	lowCPUPct        = 0.10 // <10% cpu AND
	lowMemPct        = 0.20 // <20% mem (avg) → over-provisioned advisory
)

// scalingAdvisories returns optimization suggestions from recent metrics + replica
// readiness. Advice only (never scales). Best-effort: returns [] (never nil), and
// skips silently if metrics are unavailable. Confidence is capped at "medium" for
// metrics-derived advice (a snapshot can't prove a sustained pattern); replica
// readiness — a deterministic count — can be "high".
func scalingAdvisories(ctx context.Context, adapter *jetder.Adapter, project, location, name, branch string, h *PodHealth, healthy bool) []DiagnosisCause {
	advisories := []DiagnosisCause{}

	// Replica readiness (from statusUrl counts) — deterministic, can be high.
	if h != nil && h.Count > 0 && h.Ready < h.Count {
		advisories = append(advisories, DiagnosisCause{
			Cause:      "ReplicasNotReady",
			Confidence: "high",
			Evidence:   fmt.Sprintf("%d/%d replicas ready", h.Ready, h.Count),
			Suggestion: "Some replicas are not ready. Check causes above (crash/health/scheduling); if persistent, the desired replica count may not be schedulable.",
		})
	}

	m, err := adapter.Client().Deployment().Metrics(ctx, &api.DeploymentMetrics{
		Project: project, Location: location, Name: name, Branch: branch,
		TimeRange: api.DeploymentMetricsTimeRange(diagMetricsRange),
	})
	if err != nil || m == nil {
		return advisories // metrics unavailable → just the replica advisory (if any)
	}

	cpuUtil, cpuOK := avgUtil(firstPoints(m.CPUUsage), firstPoints(m.CPULimit))
	memUtil, memOK := avgUtil(firstPoints(m.MemoryUsage), firstPoints(m.MemoryLimit))

	if (cpuOK && cpuUtil >= highUtilPct) || (memOK && memUtil >= highUtilPct) {
		advisories = append(advisories, DiagnosisCause{
			Cause:      "ResourceNearLimit",
			Confidence: "medium",
			Evidence:   utilEvidence(cpuOK, cpuUtil, memOK, memUtil),
			Suggestion: "Resource usage is close to the limit over the last hour. Consider raising the CPU/memory limit or adding replicas to avoid throttling/OOM under load.",
		})
	} else if healthy && cpuOK && memOK && cpuUtil < lowCPUPct && memUtil < lowMemPct {
		// Only advise scaling DOWN on a HEALTHY deployment — low usage on a broken
		// one (crash-loop, not serving) is expected and "scale down" would be wrong.
		advisories = append(advisories, DiagnosisCause{
			Cause:      "OverProvisioned",
			Confidence: "medium",
			Evidence:   utilEvidence(cpuOK, cpuUtil, memOK, memUtil),
			Suggestion: "Resource usage has been very low over the last hour. If this is steady, you could lower the CPU/memory limit or reduce replicas to save cost (verify against peak/expected load first).",
		})
	}

	return advisories
}

// firstPoints returns the points of the first series (nil-safe).
func firstPoints(series []*api.DeploymentMetricsLine) [][2]float64 {
	if len(series) == 0 || series[0] == nil {
		return nil
	}
	return series[0].Points
}

// avgUtil returns the average of (usage/limit) over the overlapping points and
// whether a valid ratio could be computed (both present, limit > 0).
func avgUtil(usage, limit [][2]float64) (float64, bool) {
	if len(usage) == 0 || len(limit) == 0 {
		return 0, false
	}
	avgU, okU := avgValue(usage)
	avgL, okL := avgValue(limit)
	if !okU || !okL || avgL <= 0 {
		return 0, false
	}
	return avgU / avgL, true
}

// avgValue averages the value component of [ts,value] points.
func avgValue(points [][2]float64) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	var sum float64
	for _, p := range points {
		sum += p[1]
	}
	return sum / float64(len(points)), true
}

func utilEvidence(cpuOK bool, cpuUtil float64, memOK bool, memUtil float64) string {
	var parts []string
	if cpuOK {
		parts = append(parts, fmt.Sprintf("cpu avg %.0f%% of limit", cpuUtil*100))
	}
	if memOK {
		parts = append(parts, fmt.Sprintf("mem avg %.0f%% of limit", memUtil*100))
	}
	return strings.Join(parts, ", ") + " over 1h"
}
