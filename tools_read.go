package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// ResolvedContext echoes the effective project/location a tool used after
// applying env defaults (JETDER_DEFAULT_PROJECT / JETDER_DEFAULT_LOCATION).
// Embedded in the output of any tool that resolves these, so defaults are never
// hidden state — the caller always sees what was actually used.
type ResolvedContext struct {
	ResolvedProject  string `json:"resolvedProject,omitempty" jsonschema:"project actually used after applying defaults"`
	ResolvedLocation string `json:"resolvedLocation,omitempty" jsonschema:"location actually used after applying defaults"`
}

// registerReadTools registers the read-only tools for Location and Project.
// (Me.Get is registered separately as me-get.)
func registerReadTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerLocationList(server, adapter)
	registerLocationGet(server, adapter)
	registerProjectList(server, adapter)
	registerProjectGet(server, adapter)
	registerProjectUsage(server, adapter)
}

// ---- Location ----

// LocationItem is the MCP-facing view of a Jetder location.
type LocationItem struct {
	ID           string `json:"id" jsonschema:"location id"`
	Provider     string `json:"provider" jsonschema:"infrastructure provider"`
	Region       string `json:"region" jsonschema:"region"`
	CPUType      string `json:"cpuType" jsonschema:"cpu type"`
	DomainSuffix string `json:"domainSuffix" jsonschema:"default domain suffix for deployments"`
	Endpoint     string `json:"endpoint" jsonschema:"location endpoint"`
	CName        string `json:"cname" jsonschema:"cname target for custom domains"`
	FreeTier     bool   `json:"freeTier" jsonschema:"whether this location offers a free tier"`
}

func toLocationItem(x *api.LocationItem) LocationItem {
	return LocationItem{
		ID:           x.ID,
		Provider:     x.Provider,
		Region:       x.Region,
		CPUType:      x.CPUType,
		DomainSuffix: x.DomainSuffix,
		Endpoint:     x.Endpoint,
		CName:        x.CName,
		FreeTier:     x.FreeTier,
	}
}

type LocationListInput struct {
	Project string `json:"project,omitempty" jsonschema:"optional project sid to scope locations"`
}

type LocationListOutput struct {
	ResolvedContext
	Items []LocationItem `json:"items" jsonschema:"available locations"`
}

func registerLocationList(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in LocationListInput) (*mcp.CallToolResult, LocationListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		res, err := adapter.Client().Location().List(ctx, &api.LocationList{Project: project})
		if err != nil {
			return nil, LocationListOutput{}, adapter.Redact(err)
		}
		out := LocationListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Items:           make([]LocationItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toLocationItem(x))
		}
		return textResult(fmt.Sprintf("%d location(s) [project=%s]", len(out.Items), project)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "location-list",
		Description: "List available Jetder locations (optionally scoped to a project).",
		Annotations: readOnly(),
	}, handler)
}

type LocationGetInput struct {
	ID string `json:"id" jsonschema:"location id"`
}

func registerLocationGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in LocationGetInput) (*mcp.CallToolResult, LocationItem, error) {
		res, err := adapter.Client().Location().Get(ctx, &api.LocationGet{ID: in.ID})
		if err != nil {
			return nil, LocationItem{}, adapter.Redact(err)
		}
		out := toLocationItem(res)
		return textResult(fmt.Sprintf("location %s (%s/%s)", out.ID, out.Provider, out.Region)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "location-get",
		Description: "Get a single Jetder location by id.",
		Annotations: readOnly(),
	}, handler)
}

// ---- Project ----

// ProjectItem is the MCP-facing view of a Jetder project.
type ProjectItem struct {
	ID             string `json:"id" jsonschema:"project numeric id"`
	Project        string `json:"project" jsonschema:"project sid"`
	Name           string `json:"name" jsonschema:"display name"`
	BillingAccount string `json:"billingAccount" jsonschema:"billing account id"`
}

func toProjectItem(x *api.ProjectItem) ProjectItem {
	return ProjectItem{
		ID:             fmt.Sprintf("%d", x.ID),
		Project:        x.Project,
		Name:           x.Name,
		BillingAccount: fmt.Sprintf("%d", x.BillingAccount),
	}
}

type ProjectListOutput struct {
	Items []ProjectItem `json:"items" jsonschema:"projects accessible to the user"`
}

func registerProjectList(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProjectListOutput, error) {
		res, err := adapter.Client().Project().List(ctx, nil)
		if err != nil {
			return nil, ProjectListOutput{}, adapter.Redact(err)
		}
		out := ProjectListOutput{Items: make([]ProjectItem, 0, len(res.Items))}
		names := make([]string, 0, len(res.Items))
		for _, x := range res.Items {
			out.Items = append(out.Items, toProjectItem(x))
			names = append(names, x.Project)
		}
		return textResult(fmt.Sprintf("%d project(s): %s", len(out.Items), strings.Join(names, ", "))), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project-list",
		Description: "List Jetder projects accessible to the authenticated user.",
		Annotations: readOnly(),
	}, handler)
}

type ProjectGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
}

// ProjectGetOutput wraps the project with the resolved context.
type ProjectGetOutput struct {
	ResolvedContext
	ProjectItem
}

func registerProjectGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in ProjectGetInput) (*mcp.CallToolResult, ProjectGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, ProjectGetOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Project().Get(ctx, &api.ProjectGet{Project: project})
		if err != nil {
			return nil, ProjectGetOutput{}, adapter.Redact(err)
		}
		out := ProjectGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			ProjectItem:     toProjectItem(res),
		}
		return textResult(fmt.Sprintf("project %s (%s)", out.Project, out.Name)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project-get",
		Description: "Get a single Jetder project by sid.",
		Annotations: readOnly(),
	}, handler)
}

// ProjectUsageOutput mirrors api.ProjectUsageResult (current resource usage).
type ProjectUsageOutput struct {
	ResolvedContext
	CPUUsage float64 `json:"cpuUsage" jsonschema:"current cpu usage"`
	CPU      float64 `json:"cpu" jsonschema:"allocated cpu"`
	Memory   float64 `json:"memory" jsonschema:"memory usage"`
	Egress   float64 `json:"egress" jsonschema:"egress usage"`
	Disk     float64 `json:"disk" jsonschema:"disk usage"`
	Replica  float64 `json:"replica" jsonschema:"replica count"`
}

func registerProjectUsage(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in ProjectGetInput) (*mcp.CallToolResult, ProjectUsageOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, ProjectUsageOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Project().Usage(ctx, &api.ProjectUsage{Project: project})
		if err != nil {
			return nil, ProjectUsageOutput{}, adapter.Redact(err)
		}
		out := ProjectUsageOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			CPUUsage:        res.CPUUsage,
			CPU:             res.CPU,
			Memory:          res.Memory,
			Egress:          res.Egress,
			Disk:            res.Disk,
			Replica:         res.Replica,
		}
		return textResult(fmt.Sprintf("usage for %s: cpu=%.2f mem=%.2f disk=%.2f", project, out.CPU, out.Memory, out.Disk)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project-usage",
		Description: "Get current resource usage for a Jetder project.",
		Annotations: readOnly(),
	}, handler)
}

// textResult builds a CallToolResult with a single text content block.
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

// readOnly returns annotations marking a tool as non-mutating.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}
