package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

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
	Name    string `json:"name" jsonschema:"deployment name acted upon"`
	Action  string `json:"action" jsonschema:"the action performed"`
	Success bool   `json:"success" jsonschema:"whether the action was accepted"`
	Detail  string `json:"detail,omitempty" jsonschema:"extra detail (e.g. target revision)"`
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
		_, err = adapter.Client().Deployment().Deploy(ctx, &api.DeploymentDeploy{
			Project:     project,
			Location:    location,
			Name:        name,
			Branch:      in.Branch,
			Image:       strings.TrimSpace(in.Image),
			MinReplicas: in.MinReplicas,
			MaxReplicas: in.MaxReplicas,
		})
		if err != nil {
			return nil, ActionResult{}, adapter.Redact(err)
		}
		return actionResult(project, location, name, "deploy", "image="+strings.TrimSpace(in.Image))
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
