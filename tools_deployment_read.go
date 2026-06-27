package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerDeploymentReadTools registers read-only deployment tools:
// list, get, revisions, metrics.
func registerDeploymentReadTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerDeploymentList(server, adapter)
	registerDeploymentGet(server, adapter)
	registerDeploymentRevisions(server, adapter)
	registerDeploymentMetrics(server, adapter)
}

// DeploymentItem is the MCP-facing view of a deployment.
type DeploymentItem struct {
	Project     string `json:"project" jsonschema:"project sid"`
	Location    string `json:"location" jsonschema:"location id"`
	Name        string `json:"name" jsonschema:"deployment name"`
	Branch      string `json:"branch,omitempty" jsonschema:"branch"`
	Type        string `json:"type" jsonschema:"deployment type (WebService, Worker, CronJob, ...)"`
	Revision    int64  `json:"revision" jsonschema:"current revision number"`
	Image       string `json:"image" jsonschema:"container image"`
	Status      string `json:"status" jsonschema:"deployment status"`
	MinReplicas int    `json:"minReplicas" jsonschema:"minimum replicas"`
	MaxReplicas int    `json:"maxReplicas" jsonschema:"maximum replicas"`
	Port        int    `json:"port,omitempty" jsonschema:"service port"`
	URL         string `json:"url,omitempty" jsonschema:"public url"`
	InternalURL string `json:"internalUrl,omitempty" jsonschema:"internal url"`
	Address     string `json:"address,omitempty" jsonschema:"external tcp address"`
}

func toDeploymentItem(x *api.DeploymentItem) DeploymentItem {
	return DeploymentItem{
		Project:     x.Project,
		Location:    x.Location,
		Name:        x.Name,
		Branch:      x.Branch,
		Type:        x.Type.String(),
		Revision:    x.Revision,
		Image:       x.Image,
		Status:      x.Status.Text(),
		MinReplicas: x.MinReplicas,
		MaxReplicas: x.MaxReplicas,
		Port:        x.Port,
		URL:         x.URL,
		InternalURL: x.InternalURL,
		Address:     x.Address,
	}
}

// ---- list ----

type DeploymentListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"optional location filter (falls back to JETDER_DEFAULT_LOCATION)"`
}

type DeploymentListOutput struct {
	ResolvedContext
	Items []DeploymentItem `json:"items" jsonschema:"deployments in the project"`
}

func registerDeploymentList(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentListInput) (*mcp.CallToolResult, DeploymentListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, DeploymentListOutput{}, fmt.Errorf("project required")
		}
		location := adapter.ResolveLocation(in.Location)
		res, err := adapter.Client().Deployment().List(ctx, &api.DeploymentList{
			Project:  project,
			Location: location,
		})
		if err != nil {
			return nil, DeploymentListOutput{}, adapter.Redact(err)
		}
		out := DeploymentListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]DeploymentItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toDeploymentItem(x))
		}
		return textResult(fmt.Sprintf("%d deployment(s) [project=%s location=%s]", len(out.Items), project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-list",
		Description: "List deployments in a Jetder project (optionally filtered by location).",
		Annotations: readOnly(),
	}, handler)
}

// ---- get ----

type DeploymentGetInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
	Revision int    `json:"revision,omitempty" jsonschema:"revision number; 0 = latest"`
}

// DeploymentGetOutput wraps a deployment with the resolved context.
type DeploymentGetOutput struct {
	ResolvedContext
	DeploymentItem
}

func registerDeploymentGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentGetInput) (*mcp.CallToolResult, DeploymentGetOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentGetOutput{}, err
		}
		res, err := adapter.Client().Deployment().Get(ctx, &api.DeploymentGet{
			Project:  project,
			Location: location,
			Name:     name,
			Branch:   in.Branch,
			Revision: in.Revision,
		})
		if err != nil {
			return nil, DeploymentGetOutput{}, adapter.Redact(err)
		}
		out := DeploymentGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			DeploymentItem:  toDeploymentItem(res),
		}
		return textResult(fmt.Sprintf("%s (%s) rev=%d status=%s [project=%s location=%s]", out.Name, out.Type, out.Revision, out.Status, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-get",
		Description: "Get a single deployment (latest or a specific revision).",
		Annotations: readOnly(),
	}, handler)
}

// ---- revisions ----

type DeploymentRevisionsInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"deployment name"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch"`
}

func registerDeploymentRevisions(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentRevisionsInput) (*mcp.CallToolResult, DeploymentListOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentListOutput{}, err
		}
		res, err := adapter.Client().Deployment().Revisions(ctx, &api.DeploymentRevisions{
			Project:  project,
			Location: location,
			Name:     name,
			Branch:   in.Branch,
		})
		if err != nil {
			return nil, DeploymentListOutput{}, adapter.Redact(err)
		}
		out := DeploymentListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]DeploymentItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toDeploymentItem(x))
		}
		return textResult(fmt.Sprintf("%d revision(s) of %s [project=%s location=%s]", len(out.Items), name, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-revisions",
		Description: "List revision history for a deployment.",
		Annotations: readOnly(),
	}, handler)
}

// ---- metrics ----

type DeploymentMetricsInput struct {
	Project   string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location  string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name      string `json:"name" jsonschema:"deployment name"`
	Branch    string `json:"branch,omitempty" jsonschema:"branch"`
	TimeRange string `json:"timeRange" jsonschema:"time range: 1h,6h,12h,1d,1hagg,6hagg,12hagg,1dagg,2dagg,7dagg,30dagg"`
}

// MetricsSeries is one named metric line (timestamp/value point pairs).
type MetricsSeries struct {
	Name   string       `json:"name" jsonschema:"series name"`
	Points [][2]float64 `json:"points" jsonschema:"[timestamp, value] points"`
}

// DeploymentMetricsOutput mirrors api.DeploymentMetricsResult.
type DeploymentMetricsOutput struct {
	ResolvedContext
	TimeRange   string          `json:"timeRange" jsonschema:"the time range these metrics cover"`
	CPUUsage    []MetricsSeries `json:"cpuUsage"`
	CPULimit    []MetricsSeries `json:"cpuLimit"`
	MemoryUsage []MetricsSeries `json:"memoryUsage"`
	Memory      []MetricsSeries `json:"memory"`
	MemoryLimit []MetricsSeries `json:"memoryLimit"`
	Requests    []MetricsSeries `json:"requests"`
	RequestP95  []MetricsSeries `json:"requestP95"`
	Egress      []MetricsSeries `json:"egress"`
}

func toSeries(lines []*api.DeploymentMetricsLine) []MetricsSeries {
	out := make([]MetricsSeries, 0, len(lines))
	for _, l := range lines {
		out = append(out, MetricsSeries{Name: l.Name, Points: l.Points})
	}
	return out
}

func registerDeploymentMetrics(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DeploymentMetricsInput) (*mcp.CallToolResult, DeploymentMetricsOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DeploymentMetricsOutput{}, err
		}
		res, err := adapter.Client().Deployment().Metrics(ctx, &api.DeploymentMetrics{
			Project:   project,
			Location:  location,
			Name:      name,
			Branch:    in.Branch,
			TimeRange: api.DeploymentMetricsTimeRange(in.TimeRange),
		})
		if err != nil {
			return nil, DeploymentMetricsOutput{}, adapter.Redact(err)
		}
		out := DeploymentMetricsOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			TimeRange:       in.TimeRange,
			CPUUsage:        toSeries(res.CPUUsage),
			CPULimit:        toSeries(res.CPULimit),
			MemoryUsage:     toSeries(res.MemoryUsage),
			Memory:          toSeries(res.Memory),
			MemoryLimit:     toSeries(res.MemoryLimit),
			Requests:        toSeries(res.Requests),
			RequestP95:      toSeries(res.RequestP95),
			Egress:          toSeries(res.Egress),
		}
		return textResult(fmt.Sprintf("metrics for %s (%s) [project=%s location=%s]", name, in.TimeRange, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deployment-metrics",
		Description: "Get time-series metrics (cpu, memory, requests, egress) for a deployment.",
		Annotations: readOnly(),
	}, handler)
}

// resolveDeploymentTarget resolves project/location from args+env defaults and
// validates the required project/location/name triple shared by deployment tools.
func resolveDeploymentTarget(adapter *jetder.Adapter, project, location, name string) (string, string, string, error) {
	p := adapter.ResolveProject(project)
	l := adapter.ResolveLocation(location)
	n := strings.TrimSpace(name)
	if p == "" {
		return "", "", "", fmt.Errorf("project required")
	}
	if l == "" {
		return "", "", "", fmt.Errorf("location required")
	}
	if n == "" {
		return "", "", "", fmt.Errorf("name required")
	}
	return p, l, n, nil
}
