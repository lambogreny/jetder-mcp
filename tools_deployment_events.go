package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/cloudflare"
	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// deployment-events is a READ-ONLY tool that returns a deployment's recent Kubernetes
// events (from its eventUrl — a finite JSON endpoint, not the SSE log stream). Useful
// on its own and as the lower-level companion to deployment-diagnose: events show
// scheduling, image pulls, restarts, OOM kills, probe failures, etc.

const (
	eventsLimitDefault          = 50
	eventsLimitMax              = 200
	eventsMaxBytesDefault int64 = 64 * 1024
	eventsMaxBytesHardMax int64 = 1024 * 1024
)

// DeploymentEventsInput selects the deployment and how many events to return.
type DeploymentEventsInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
	Revision int    `json:"revision,omitempty" jsonschema:"deployment revision (default 0 = latest)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max events to return (default 50, max 200)"`
	MaxBytes int64  `json:"maxBytes,omitempty" jsonschema:"max bytes to read from the event endpoint (default 65536, max 1048576)"`
}

// EventOutItem is the safe, sanitized DTO returned to the client.
type EventOutItem struct {
	Type           string `json:"type,omitempty" jsonschema:"event type (Normal/Warning)"`
	Reason         string `json:"reason,omitempty" jsonschema:"short reason (e.g. BackOff, Pulled, OOMKilling)"`
	Message        string `json:"message,omitempty" jsonschema:"the (sanitized) event message"`
	Count          int    `json:"count,omitempty" jsonschema:"how many times this event occurred"`
	LastTimestamp  string `json:"lastTimestamp,omitempty" jsonschema:"when the event last occurred"`
	InvolvedKind   string `json:"involvedObjectKind,omitempty" jsonschema:"kind of the object (e.g. Pod)"`
	InvolvedName   string `json:"involvedObjectName,omitempty" jsonschema:"name of the object"`
	InvolvedNsName string `json:"involvedObjectNamespace,omitempty" jsonschema:"namespace of the object"`
}

// DeploymentEventsOutput is the event list.
type DeploymentEventsOutput struct {
	ResolvedContext
	Name      string         `json:"name" jsonschema:"deployment name"`
	Events    []EventOutItem `json:"events" jsonschema:"recent Kubernetes events, oldest-to-newest as provided"`
	Count     int            `json:"count" jsonschema:"number of events returned"`
	Truncated bool           `json:"truncated" jsonschema:"true if the event data was capped by maxBytes or by limit"`
}

func registerDeploymentEvents(server *mcp.Server, adapter *jetder.Adapter, cf *cloudflare.Client) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentEventsInput) (*mcp.CallToolResult, DeploymentEventsOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentEventsOutput{}, err
		}

		dep, err := adapter.Client().Deployment().Get(ctx, &api.DeploymentGet{
			Project: project, Location: location, Name: name, Branch: in.Branch, Revision: in.Revision,
		})
		if err != nil {
			return nil, DeploymentEventsOutput{}, adapter.Redact(err)
		}

		// A standalone tool must NOT pretend "no events = healthy" when it actually
		// failed to fetch. No event URL is an honest error.
		eventURL := strings.TrimSpace(dep.EventURL)
		if eventURL == "" {
			return nil, DeploymentEventsOutput{}, fmt.Errorf("deployment %q has no event URL yet (is it deployed?)", name)
		}

		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = eventsMaxBytesDefault
		}
		if maxBytes > eventsMaxBytesHardMax {
			maxBytes = eventsMaxBytesHardMax
		}

		// fetchEventsResult distinguishes a fetch/parse error from genuinely-no-events.
		events, truncated, err := fetchEventsResult(ctx, adapter, eventURL, maxBytes)
		if err != nil {
			// FetchJSON already scrubs the URL; errEventsUnparseable carries no secret.
			return nil, DeploymentEventsOutput{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = eventsLimitDefault
		}
		if limit > eventsLimitMax {
			limit = eventsLimitMax
		}
		if len(events) > limit {
			// Keep the most recent `limit` events (the tail) and flag truncation.
			events = events[len(events)-limit:]
			truncated = true
		}

		out := DeploymentEventsOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Name:            name,
			Events:          make([]EventOutItem, 0, len(events)),
			Truncated:       truncated,
		}
		for _, e := range events {
			// Build the message from the sanitized text (reason is part of it); prefer
			// the sanitized clean text for Message so nothing raw slips through.
			out.Events = append(out.Events, EventOutItem{
				Type:           sanitizeLog(adapter, e.Type, dep.EventURL),
				Reason:         sanitizeLog(adapter, e.Reason, dep.EventURL),
				Message:        e.text(), // already sanitized at fetch (reason+message)
				Count:          e.Count,
				LastTimestamp:  e.when(), // prefers lastSeen (live field), then lastTimestamp
				InvolvedKind:   e.InvolvedObject.Kind,
				InvolvedName:   e.InvolvedObject.Name,
				InvolvedNsName: e.InvolvedObject.Namespace,
			})
		}
		out.Count = len(out.Events)

		summary := fmt.Sprintf("%d event(s) for %s [project=%s]", out.Count, name, project)
		if out.Count == 0 {
			summary += " (no recent events reported)"
		}
		if out.Truncated {
			summary += " (truncated)"
		}
		return textResult(summary), out, nil
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "deployment-events",
		Description: "List a deployment's recent Kubernetes events (scheduling, image pulls, restarts, " +
			"OOM kills, probe failures, …). Read-only; returns the most recent events (default 50, max 200). " +
			"Event messages are sanitized best-effort. For an interpreted view of what's wrong, use " +
			"deployment-diagnose.",
		Annotations: readOnly(),
	}, handler)
}
