package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerRouteTools registers route tools: create-v2, get, list.
// (V1 create is legacy and route-delete is intentionally NOT exposed.)
func registerRouteTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerRouteCreateV2(server, adapter)
	registerRouteGet(server, adapter)
	registerRouteList(server, adapter)
}

// RouteItem is the MCP-facing view of a route.
type RouteItem struct {
	Location   string `json:"location" jsonschema:"location id"`
	Domain     string `json:"domain" jsonschema:"domain"`
	Path       string `json:"path,omitempty" jsonschema:"path prefix"`
	Target     string `json:"target,omitempty" jsonschema:"route target (e.g. deployment://name)"`
	Deployment string `json:"deployment,omitempty" jsonschema:"bound deployment (legacy)"`
}

func toRouteItem(x *api.RouteItem) RouteItem {
	return RouteItem{
		Location:   x.Location,
		Domain:     x.Domain,
		Path:       x.Path,
		Target:     x.Target,
		Deployment: x.Deployment,
	}
}

// ---- create v2 ----

type RouteBasicAuth struct {
	User     string `json:"user" jsonschema:"basic-auth username"`
	Password string `json:"password" jsonschema:"basic-auth password"`
}

// NOTE: forwardAuth is intentionally NOT exposed. The pinned upstream
// RouteCreateV2.Valid() is self-contradictory for forwardAuth (it requires the
// route target to use a non-http scheme like deployment://, yet when forwardAuth
// is set it also forces the target to start with "http://"), so no forwardAuth
// route can pass upstream validation. See
// ψ/memory/learnings/upstream_route_forwardauth_contradiction.md. Re-enable once
// upstream is fixed.
type RouteCreateV2Input struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Domain   string `json:"domain" jsonschema:"domain to route from"`
	Path     string `json:"path,omitempty" jsonschema:"path prefix (must start with /)"`
	// Target prefixes: deployment://, redirect://, ipfs://, ipns://, dnslink://
	Target    string          `json:"target" jsonschema:"route target, e.g. deployment://my-service, redirect://https://..., ipfs://, ipns://, dnslink://"`
	BasicAuth *RouteBasicAuth `json:"basicAuth,omitempty" jsonschema:"optional HTTP basic auth protection"`
}

// RouteActionOutput is the resolved-context-aware result for route mutations.
type RouteActionOutput struct {
	ResolvedContext
	Domain  string `json:"domain" jsonschema:"domain acted upon"`
	Path    string `json:"path,omitempty" jsonschema:"path acted upon"`
	Target  string `json:"target,omitempty" jsonschema:"target set"`
	Action  string `json:"action" jsonschema:"the action performed"`
	Success bool   `json:"success" jsonschema:"whether the action was accepted"`
}

func registerRouteCreateV2(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in RouteCreateV2Input) (*mcp.CallToolResult, RouteActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		location := adapter.ResolveLocation(in.Location)
		domain := strings.TrimSpace(in.Domain)
		target := strings.TrimSpace(in.Target)
		if project == "" {
			return nil, RouteActionOutput{}, errProjectRequired()
		}
		if location == "" {
			return nil, RouteActionOutput{}, errLocationRequired()
		}
		if domain == "" {
			return nil, RouteActionOutput{}, errArgRequired("domain")
		}
		if target == "" {
			return nil, RouteActionOutput{}, errArgRequired("target")
		}

		m := &api.RouteCreateV2{
			Project:  project,
			Location: location,
			Domain:   domain,
			Path:     in.Path,
			Target:   target,
		}
		if in.BasicAuth != nil {
			m.Config.BasicAuth = &api.RouteConfigBasicAuth{User: in.BasicAuth.User, Password: in.BasicAuth.Password}
		}

		if _, err := adapter.Client().Route().CreateV2(ctx, m); err != nil {
			return nil, RouteActionOutput{}, adapter.Redact(err)
		}
		out := RouteActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Domain:          domain,
			Path:            in.Path,
			Target:          target,
			Action:          "create",
			Success:         true,
		}
		return textResult(fmt.Sprintf("routed %s%s -> %s [project=%s location=%s]", domain, in.Path, target, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "route-create-v2",
		Description: "Create a route mapping a domain (and optional path) to a target. Target prefixes: deployment://<name>, redirect://<url>, ipfs://, ipns://, dnslink://. Optionally protect with HTTP basicAuth.",
		Annotations: nonReadOnly(),
	}, handler)
}

// ---- get ----

type RouteGetInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Domain   string `json:"domain" jsonschema:"domain"`
	Path     string `json:"path,omitempty" jsonschema:"path prefix (must start with /)"`
}

// RouteGetOutput wraps the route with resolved context.
type RouteGetOutput struct {
	ResolvedContext
	RouteItem
}

func registerRouteGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in RouteGetInput) (*mcp.CallToolResult, RouteGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		location := adapter.ResolveLocation(in.Location)
		domain := strings.TrimSpace(in.Domain)
		if project == "" {
			return nil, RouteGetOutput{}, errProjectRequired()
		}
		if location == "" {
			return nil, RouteGetOutput{}, errLocationRequired()
		}
		if domain == "" {
			return nil, RouteGetOutput{}, errArgRequired("domain")
		}
		res, err := adapter.Client().Route().Get(ctx, &api.RouteGet{
			Project: project, Location: location, Domain: domain, Path: in.Path,
		})
		if err != nil {
			return nil, RouteGetOutput{}, adapter.Redact(err)
		}
		out := RouteGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			RouteItem:       toRouteItem(res),
		}
		return textResult(fmt.Sprintf("route %s%s -> %s [project=%s location=%s]", out.Domain, out.Path, out.Target, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "route-get",
		Description: "Get a single route by domain (and optional path).",
		Annotations: readOnly(),
	}, handler)
}

// ---- list ----

type RouteListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"optional location filter (falls back to JETDER_DEFAULT_LOCATION)"`
}

type RouteListOutput struct {
	ResolvedContext
	Items []RouteItem `json:"items" jsonschema:"routes in the project"`
}

func registerRouteList(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in RouteListInput) (*mcp.CallToolResult, RouteListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, RouteListOutput{}, errProjectRequired()
		}
		location := adapter.ResolveLocation(in.Location)
		res, err := adapter.Client().Route().List(ctx, &api.RouteList{Project: project, Location: location})
		if err != nil {
			return nil, RouteListOutput{}, adapter.Redact(err)
		}
		out := RouteListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]RouteItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toRouteItem(x))
		}
		return textResult(fmt.Sprintf("%d route(s) [project=%s]", len(out.Items), project)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "route-list",
		Description: "List routes in a project.",
		Annotations: readOnly(),
	}, handler)
}
