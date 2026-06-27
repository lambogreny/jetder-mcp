package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerResourceWriteTools registers slice-5b additive mutations:
// create + update across disk/secret/pull-secret/workload-identity/
// service-account/organization/role. NO delete.
func registerResourceWriteTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerDiskWrite(server, adapter)
	registerSecretCreate(server, adapter)
	registerPullSecretCreate(server, adapter)
	registerWorkloadIdentityCreate(server, adapter)
	registerServiceAccountWrite(server, adapter)
	registerOrganizationWrite(server, adapter)
	registerRoleCreate(server, adapter)
}

// ResourceActionOutput is the common output for resource create/update tools:
// resolved context + a short summary. It never echoes secret material.
type ResourceActionOutput struct {
	ResolvedContext
	Resource string `json:"resource" jsonschema:"resource kind acted upon"`
	Name     string `json:"name" jsonschema:"resource name/identifier"`
	Action   string `json:"action" jsonschema:"the action performed (create/update)"`
	Success  bool   `json:"success" jsonschema:"whether the action was accepted"`
}

// ===== Disk: create + update =====
//
// Annotation rationale (for codex):
//   disk-create — additive (new disk) → destructive:false.
//   disk-update — resizes an existing disk; jetder disks can only grow, so this
//     is a non-removing, monotonic change. Classified destructive:false; codex
//     to confirm (a shrink would be destructive, but the API does not shrink).

type DiskCreateInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"disk name"`
	Size     int64  `json:"size" jsonschema:"disk size in Gi (>= 1)"`
}

func registerDiskWrite(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "disk-create",
		Description: "Create a new disk in a project.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiskCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ResourceActionOutput{}, err
		}
		if in.Size < 1 {
			return nil, ResourceActionOutput{}, fmt.Errorf("size must be >= 1 Gi")
		}
		if _, err := adapter.Client().Disk().Create(ctx, &api.DiskCreate{
			Project: project, Location: location, Name: name, Size: in.Size,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResult(project, location, "disk", name, "create")
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "disk-update",
		Description: "Update a disk's size (disks grow only; cannot shrink).",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiskCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ResourceActionOutput{}, err
		}
		if in.Size < 1 {
			return nil, ResourceActionOutput{}, fmt.Errorf("size must be >= 1 Gi")
		}
		if _, err := adapter.Client().Disk().Update(ctx, &api.DiskUpdate{
			Project: project, Location: location, Name: name, Size: in.Size,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResult(project, location, "disk", name, "update")
	})
}

// ===== Secret: create (value is write-only; NEVER echoed back) =====

type SecretCreateInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Name    string `json:"name" jsonschema:"secret name"`
	Value   string `json:"value" jsonschema:"secret value (write-only; never returned in any response)"`
}

func registerSecretCreate(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "secret-create",
		Description: "Create a secret. The value is write-only and is never echoed back in the response.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SecretCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		name := strings.TrimSpace(in.Name)
		if project == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("project required")
		}
		if name == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("name required")
		}
		if in.Value == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("value required")
		}
		if _, err := adapter.Client().Secret().Create(ctx, &api.SecretCreate{
			Project: project, Name: name, Value: in.Value,
		}); err != nil {
			// Redact the submitted value: an upstream error could echo it back.
			return nil, ResourceActionOutput{}, adapter.RedactValues(err, in.Value)
		}
		// Output carries only name/context — NEVER the value.
		out := ResourceActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Resource:        "secret",
			Name:            name,
			Action:          "create",
			Success:         true,
		}
		return textResult(fmt.Sprintf("created secret %s [project=%s] (value not echoed)", name, project)), out, nil
	})
}

// ===== PullSecret: create (registry password is write-only) =====

type PullSecretCreateInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"pull secret name"`
	Server   string `json:"server" jsonschema:"registry server"`
	Username string `json:"username" jsonschema:"registry username"`
	Password string `json:"password" jsonschema:"registry password (write-only; never returned)"`
}

func registerPullSecretCreate(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pull-secret-create",
		Description: "Create a registry pull secret. The password is write-only and is never echoed back.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PullSecretCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ResourceActionOutput{}, err
		}
		if strings.TrimSpace(in.Server) == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("server required")
		}
		if _, err := adapter.Client().PullSecret().Create(ctx, &api.PullSecretCreate{
			Project:  project,
			Location: location,
			Name:     name,
			Spec:     api.PullSecretSpec{Server: in.Server, Username: in.Username, Password: in.Password},
		}); err != nil {
			// Redact the submitted password: an upstream error could echo it back.
			return nil, ResourceActionOutput{}, adapter.RedactValues(err, in.Password)
		}
		out := ResourceActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Resource:        "pullSecret",
			Name:            name,
			Action:          "create",
			Success:         true,
		}
		return textResult(fmt.Sprintf("created pull secret %s [project=%s location=%s] (password not echoed)", name, project, location)), out, nil
	})
}

// ===== WorkloadIdentity: create =====

type WICreateInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"workload identity name"`
	GSA      string `json:"gsa" jsonschema:"Google service account to bind"`
}

func registerWorkloadIdentityCreate(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "workload-identity-create",
		Description: "Create a workload identity binding a Google service account.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WICreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, ResourceActionOutput{}, err
		}
		if strings.TrimSpace(in.GSA) == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("gsa required")
		}
		if _, err := adapter.Client().WorkloadIdentity().Create(ctx, &api.WorkloadIdentityCreate{
			Project: project, Location: location, Name: name, GSA: in.GSA,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResult(project, location, "workloadIdentity", name, "create")
	})
}

// ===== ServiceAccount: create + update =====
//
// Annotation rationale: service-account-update changes name/description only
// (no credential removal) → destructive:false. codex to confirm.

type SACreateInput struct {
	Project     string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	SID         string `json:"sid" jsonschema:"service account sid"`
	Name        string `json:"name" jsonschema:"display name"`
	Description string `json:"description,omitempty" jsonschema:"description"`
}

func registerServiceAccountWrite(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service-account-create",
		Description: "Create a service account in a project.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SACreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		sid := strings.TrimSpace(in.SID)
		if project == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("project required")
		}
		if sid == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("sid required")
		}
		if _, err := adapter.Client().ServiceAccount().Create(ctx, &api.ServiceAccountCreate{
			Project: project, SID: sid, Name: in.Name, Description: in.Description,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResultProject(project, "serviceAccount", sid, "create")
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "service-account-update",
		Description: "Update a service account's name/description (does not remove keys).",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SACreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		sid := strings.TrimSpace(in.SID)
		if project == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("project required")
		}
		if sid == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("sid required")
		}
		if _, err := adapter.Client().ServiceAccount().Update(ctx, &api.ServiceAccountUpdate{
			Project: project, SID: sid, Name: in.Name, Description: in.Description,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResultProject(project, "serviceAccount", sid, "update")
	})
}

// ===== Organization: create + update =====
//
// Annotation rationale: organization-update renames only (reversible, no
// removal) → destructive:false. codex to confirm.

type OrgCreateInput struct {
	ID   string `json:"id" jsonschema:"organization id"`
	Name string `json:"name" jsonschema:"organization name"`
}

func registerOrganizationWrite(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "organization-create",
		Description: "Create an organization.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OrgCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("id required")
		}
		if _, err := adapter.Client().Organization().Create(ctx, &api.OrganizationCreate{ID: id, Name: in.Name}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResultBare("organization", id, "create")
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "organization-update",
		Description: "Update (rename) an organization.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OrgCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("id required")
		}
		if _, err := adapter.Client().Organization().Update(ctx, &api.OrganizationUpdate{ID: id, Name: in.Name}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResultBare("organization", id, "update")
	})
}

// ===== Role: create =====

type RoleCreateInput struct {
	Project     string   `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Role        string   `json:"role" jsonschema:"role sid"`
	Name        string   `json:"name" jsonschema:"role display name"`
	Permissions []string `json:"permissions,omitempty" jsonschema:"permissions to grant the role"`
}

func registerRoleCreate(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-create",
		Description: "Create a role with a set of permissions.",
		Annotations: nonReadOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RoleCreateInput) (*mcp.CallToolResult, ResourceActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		role := strings.TrimSpace(in.Role)
		if project == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("project required")
		}
		if role == "" {
			return nil, ResourceActionOutput{}, fmt.Errorf("role required")
		}
		if _, err := adapter.Client().Role().Create(ctx, &api.RoleCreate{
			Project: project, Role: role, Name: in.Name, Permissions: in.Permissions,
		}); err != nil {
			return nil, ResourceActionOutput{}, adapter.Redact(err)
		}
		return resourceResultProject(project, "role", role, "create")
	})
}

// ---- result helpers ----

func resourceResult(project, location, resource, name, action string) (*mcp.CallToolResult, ResourceActionOutput, error) {
	out := ResourceActionOutput{
		ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
		Resource:        resource, Name: name, Action: action, Success: true,
	}
	return textResult(fmt.Sprintf("%s %s %s [project=%s location=%s]", action, resource, name, project, location)), out, nil
}

func resourceResultProject(project, resource, name, action string) (*mcp.CallToolResult, ResourceActionOutput, error) {
	out := ResourceActionOutput{
		ResolvedContext: ResolvedContext{ResolvedProject: project},
		Resource:        resource, Name: name, Action: action, Success: true,
	}
	return textResult(fmt.Sprintf("%s %s %s [project=%s]", action, resource, name, project)), out, nil
}

func resourceResultBare(resource, name, action string) (*mcp.CallToolResult, ResourceActionOutput, error) {
	out := ResourceActionOutput{Resource: resource, Name: name, Action: action, Success: true}
	return textResult(fmt.Sprintf("%s %s %s", action, resource, name)), out, nil
}
