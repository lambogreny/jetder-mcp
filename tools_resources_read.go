package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerResourceReadTools registers slice-5a read-only tools across the
// remaining resources: billing, disk, service-account, role, secret (REDACTED),
// pull-secret (REDACTED), workload-identity, organization.
func registerResourceReadTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerBillingRead(server, adapter)
	registerDiskRead(server, adapter)
	registerServiceAccountRead(server, adapter)
	registerRoleRead(server, adapter)
	registerSecretRead(server, adapter)
	registerPullSecretRead(server, adapter)
	registerWorkloadIdentityRead(server, adapter)
	registerOrganizationRead(server, adapter)
}

// ===== Billing (read) =====

type BillingItem struct {
	ID         string `json:"id" jsonschema:"billing account id"`
	Name       string `json:"name" jsonschema:"billing account name"`
	TaxID      string `json:"taxId,omitempty" jsonschema:"tax id"`
	TaxName    string `json:"taxName,omitempty" jsonschema:"tax name"`
	TaxAddress string `json:"taxAddress,omitempty" jsonschema:"tax address"`
	Active     bool   `json:"active" jsonschema:"whether the account is active"`
}

func toBillingItem(x *api.BillingItem) BillingItem {
	return BillingItem{
		ID:         fmt.Sprintf("%d", x.ID),
		Name:       x.Name,
		TaxID:      x.TaxID,
		TaxName:    x.TaxName,
		TaxAddress: x.TaxAddress,
		Active:     x.Active,
	}
}

type BillingListOutput struct {
	Items []BillingItem `json:"items" jsonschema:"billing accounts"`
}

type BillingGetInput struct {
	ID string `json:"id" jsonschema:"billing account id"`
}

type BillingProjectPriceInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
}

type BillingProjectPriceOutput struct {
	ResolvedContext
	Price float64 `json:"price" jsonschema:"current accrued price for the project"`
}

func registerBillingRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "billing-list",
		Description: "List billing accounts accessible to the user.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, BillingListOutput, error) {
		res, err := adapter.Client().Billing().List(ctx, nil)
		if err != nil {
			return nil, BillingListOutput{}, adapter.Redact(err)
		}
		out := BillingListOutput{Items: make([]BillingItem, 0, len(res.Items))}
		for _, x := range res.Items {
			out.Items = append(out.Items, toBillingItem(x))
		}
		return textResult(fmt.Sprintf("%d billing account(s)", len(out.Items))), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "billing-get",
		Description: "Get a billing account by id.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in BillingGetInput) (*mcp.CallToolResult, BillingItem, error) {
		id, err := parseID(in.ID)
		if err != nil {
			return nil, BillingItem{}, err
		}
		res, err := adapter.Client().Billing().Get(ctx, &api.BillingGet{ID: id})
		if err != nil {
			return nil, BillingItem{}, adapter.Redact(err)
		}
		out := toBillingItem(res)
		return textResult(fmt.Sprintf("billing %s (%s)", out.ID, out.Name)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "billing-project-price",
		Description: "Get the current accrued price for a project.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in BillingProjectPriceInput) (*mcp.CallToolResult, BillingProjectPriceOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, BillingProjectPriceOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Billing().Project(ctx, &api.BillingProject{Project: project})
		if err != nil {
			return nil, BillingProjectPriceOutput{}, adapter.Redact(err)
		}
		out := BillingProjectPriceOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Price:           res.Price,
		}
		return textResult(fmt.Sprintf("price for %s: %.2f", project, out.Price)), out, nil
	})
}

// ===== Disk (read) =====

type DiskItem struct {
	Project  string `json:"project" jsonschema:"project sid"`
	Location string `json:"location" jsonschema:"location id"`
	Name     string `json:"name" jsonschema:"disk name"`
	Size     int64  `json:"size" jsonschema:"disk size (bytes)"`
	Status   string `json:"status" jsonschema:"disk status"`
}

func toDiskItem(x *api.DiskItem) DiskItem {
	return DiskItem{
		Project:  x.Project,
		Location: x.Location,
		Name:     x.Name,
		Size:     x.Size,
		Status:   x.Status.Text(),
	}
}

type DiskListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"optional location filter (falls back to JETDER_DEFAULT_LOCATION)"`
}

type DiskListOutput struct {
	ResolvedContext
	Items []DiskItem `json:"items" jsonschema:"disks in the project"`
}

type DiskGetInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"disk name"`
}

type DiskGetOutput struct {
	ResolvedContext
	DiskItem
}

func registerDiskRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "disk-list",
		Description: "List disks in a project (optionally filtered by location).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiskListInput) (*mcp.CallToolResult, DiskListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, DiskListOutput{}, fmt.Errorf("project required")
		}
		location := adapter.ResolveLocation(in.Location)
		res, err := adapter.Client().Disk().List(ctx, &api.DiskList{Project: project, Location: location})
		if err != nil {
			return nil, DiskListOutput{}, adapter.Redact(err)
		}
		out := DiskListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]DiskItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toDiskItem(x))
		}
		return textResult(fmt.Sprintf("%d disk(s) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "disk-get",
		Description: "Get a single disk by name.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiskGetInput) (*mcp.CallToolResult, DiskGetOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, DiskGetOutput{}, err
		}
		res, err := adapter.Client().Disk().Get(ctx, &api.DiskGet{Project: project, Location: location, Name: name})
		if err != nil {
			return nil, DiskGetOutput{}, adapter.Redact(err)
		}
		out := DiskGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			DiskItem:        toDiskItem(res),
		}
		return textResult(fmt.Sprintf("disk %s status=%s [project=%s location=%s]", out.Name, out.Status, project, location)), out, nil
	})
}

// ===== ServiceAccount (read) =====

type ServiceAccountItem struct {
	SID         string `json:"sid" jsonschema:"service account sid"`
	Email       string `json:"email" jsonschema:"service account email"`
	Name        string `json:"name" jsonschema:"name"`
	Description string `json:"description,omitempty" jsonschema:"description"`
}

type SAListInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
}

type SAListOutput struct {
	ResolvedContext
	Items []ServiceAccountItem `json:"items" jsonschema:"service accounts in the project"`
}

type SAGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	ID      string `json:"id" jsonschema:"service account sid"`
}

// SAGetOutput includes key metadata (key ids/created), never key secrets.
type SAGetOutput struct {
	ResolvedContext
	ServiceAccountItem
	KeyCount int `json:"keyCount" jsonschema:"number of keys on this service account"`
}

func registerServiceAccountRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service-account-list",
		Description: "List service accounts in a project.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SAListInput) (*mcp.CallToolResult, SAListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, SAListOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().ServiceAccount().List(ctx, &api.ServiceAccountList{Project: project})
		if err != nil {
			return nil, SAListOutput{}, adapter.Redact(err)
		}
		out := SAListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Items:           make([]ServiceAccountItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, ServiceAccountItem{SID: x.SID, Email: x.Email, Name: x.Name, Description: x.Description})
		}
		return textResult(fmt.Sprintf("%d service account(s) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "service-account-get",
		Description: "Get a service account by sid (returns key metadata only, never key secrets).",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SAGetInput) (*mcp.CallToolResult, SAGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, SAGetOutput{}, fmt.Errorf("project required")
		}
		if strings.TrimSpace(in.ID) == "" {
			return nil, SAGetOutput{}, fmt.Errorf("id required")
		}
		res, err := adapter.Client().ServiceAccount().Get(ctx, &api.ServiceAccountGet{Project: project, ID: in.ID})
		if err != nil {
			return nil, SAGetOutput{}, adapter.Redact(err)
		}
		out := SAGetOutput{
			ResolvedContext:    ResolvedContext{ResolvedProject: project},
			ServiceAccountItem: ServiceAccountItem{SID: res.SID, Email: res.Email, Name: res.Name, Description: res.Description},
			KeyCount:           len(res.Keys),
		}
		return textResult(fmt.Sprintf("service account %s (%s) keys=%d [project=%s]", out.SID, out.Email, out.KeyCount, project)), out, nil
	})
}

// ===== Role (read) =====

type RoleItem struct {
	Role        string   `json:"role" jsonschema:"role sid"`
	Name        string   `json:"name" jsonschema:"role name"`
	Permissions []string `json:"permissions" jsonschema:"granted permissions"`
}

type RoleProjectInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
}

type RoleListOutput struct {
	ResolvedContext
	Items []RoleItem `json:"items" jsonschema:"roles in the project"`
}

type RoleGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Role    string `json:"role" jsonschema:"role sid"`
}

type RoleGetOutput struct {
	ResolvedContext
	RoleItem
}

type RoleUserItem struct {
	Email string   `json:"email" jsonschema:"user email"`
	Roles []string `json:"roles" jsonschema:"roles granted to the user"`
}

type RoleUsersOutput struct {
	ResolvedContext
	Users []RoleUserItem `json:"users" jsonschema:"users and their roles"`
}

type PermissionsOutput struct {
	Permissions []string `json:"permissions" jsonschema:"all assignable permissions"`
}

func registerRoleRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-list",
		Description: "List roles in a project.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RoleProjectInput) (*mcp.CallToolResult, RoleListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, RoleListOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Role().List(ctx, &api.RoleList{Project: project})
		if err != nil {
			return nil, RoleListOutput{}, adapter.Redact(err)
		}
		out := RoleListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Items:           make([]RoleItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, RoleItem{Role: x.Role, Name: x.Name, Permissions: x.Permissions})
		}
		return textResult(fmt.Sprintf("%d role(s) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-get",
		Description: "Get a single role by sid.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RoleGetInput) (*mcp.CallToolResult, RoleGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, RoleGetOutput{}, fmt.Errorf("project required")
		}
		if strings.TrimSpace(in.Role) == "" {
			return nil, RoleGetOutput{}, fmt.Errorf("role required")
		}
		res, err := adapter.Client().Role().Get(ctx, &api.RoleGet{Project: project, Role: in.Role})
		if err != nil {
			return nil, RoleGetOutput{}, adapter.Redact(err)
		}
		out := RoleGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			RoleItem:        RoleItem{Role: res.Role, Name: res.Name, Permissions: res.Permissions},
		}
		return textResult(fmt.Sprintf("role %s (%s) [project=%s]", out.Role, out.Name, project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-users",
		Description: "List users in a project and the roles granted to each.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RoleProjectInput) (*mcp.CallToolResult, RoleUsersOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, RoleUsersOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Role().Users(ctx, &api.RoleUsers{Project: project})
		if err != nil {
			return nil, RoleUsersOutput{}, adapter.Redact(err)
		}
		out := RoleUsersOutput{ResolvedContext: ResolvedContext{ResolvedProject: project}}
		// Upstream RoleUsersResult exposes both Items (canonical, json:"items")
		// and Users (json:"users"); prefer Items, fall back to Users.
		rows := res.Items
		if len(rows) == 0 {
			rows = res.Users
		}
		for _, x := range rows {
			out.Users = append(out.Users, RoleUserItem{Email: x.Email, Roles: x.Roles})
		}
		return textResult(fmt.Sprintf("%d user(s) [project=%s]", len(out.Users), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "role-permissions",
		Description: "List all assignable permissions.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, PermissionsOutput, error) {
		perms, err := adapter.Client().Role().Permissions(ctx, nil)
		if err != nil {
			return nil, PermissionsOutput{}, adapter.Redact(err)
		}
		return textResult(fmt.Sprintf("%d permission(s)", len(perms))), PermissionsOutput{Permissions: perms}, nil
	})
}

// ===== Secret (read, REDACTED) =====

// SecretMetadata deliberately omits the secret value. Value is NEVER returned.
type SecretMetadata struct {
	Name      string `json:"name" jsonschema:"secret name"`
	CreatedBy string `json:"createdBy,omitempty" jsonschema:"who created it"`
	CreatedAt string `json:"createdAt,omitempty" jsonschema:"creation time"`
}

type SecretListInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
}

type SecretListOutput struct {
	ResolvedContext
	Items []SecretMetadata `json:"items" jsonschema:"secret metadata (values are never returned)"`
}

type SecretGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Name    string `json:"name" jsonschema:"secret name"`
}

type SecretGetOutput struct {
	ResolvedContext
	SecretMetadata
}

func registerSecretRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "secret-list",
		Description: "List secret metadata in a project. Secret VALUES are never returned.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SecretListInput) (*mcp.CallToolResult, SecretListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, SecretListOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().Secret().List(ctx, &api.SecretList{Project: project})
		if err != nil {
			return nil, SecretListOutput{}, adapter.Redact(err)
		}
		out := SecretListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Items:           make([]SecretMetadata, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, secretMeta(x)) // Value intentionally dropped
		}
		return textResult(fmt.Sprintf("%d secret(s) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "secret-get",
		Description: "Get a secret's metadata by name. The secret VALUE is never returned.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SecretGetInput) (*mcp.CallToolResult, SecretGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, SecretGetOutput{}, fmt.Errorf("project required")
		}
		if strings.TrimSpace(in.Name) == "" {
			return nil, SecretGetOutput{}, fmt.Errorf("name required")
		}
		res, err := adapter.Client().Secret().Get(ctx, &api.SecretGet{Project: project, Name: in.Name})
		if err != nil {
			return nil, SecretGetOutput{}, adapter.Redact(err)
		}
		out := SecretGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			SecretMetadata:  secretMeta(res), // Value intentionally dropped
		}
		return textResult(fmt.Sprintf("secret %s [project=%s] (value redacted)", out.Name, project)), out, nil
	})
}

// secretMeta maps an api.SecretItem to redacted metadata. It NEVER copies Value.
func secretMeta(x *api.SecretItem) SecretMetadata {
	m := SecretMetadata{Name: x.Name, CreatedBy: x.CreatedBy}
	if !x.CreatedAt.IsZero() {
		m.CreatedAt = x.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return m
}

// ===== PullSecret (read, REDACTED value) =====

type PullSecretMetadata struct {
	Name      string `json:"name" jsonschema:"pull secret name"`
	Location  string `json:"location,omitempty" jsonschema:"location id"`
	Status    string `json:"status,omitempty" jsonschema:"status"`
	CreatedBy string `json:"createdBy,omitempty" jsonschema:"who created it"`
}

type PullSecretListOutput struct {
	ResolvedContext
	Items []PullSecretMetadata `json:"items" jsonschema:"pull secret metadata (values never returned)"`
}

type PullSecretGetInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"pull secret name"`
}

type PullSecretGetOutput struct {
	ResolvedContext
	PullSecretMetadata
}

func pullSecretMeta(x *api.PullSecretItem) PullSecretMetadata {
	return PullSecretMetadata{Name: x.Name, Location: x.Location, Status: x.Status.Text(), CreatedBy: x.CreatedBy}
}

type PullSecretListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
}

func registerPullSecretRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pull-secret-list",
		Description: "List pull-secret metadata in a project. Credential VALUES are never returned.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PullSecretListInput) (*mcp.CallToolResult, PullSecretListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		location := adapter.ResolveLocation(in.Location)
		if project == "" {
			return nil, PullSecretListOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().PullSecret().List(ctx, &api.PullSecretList{Project: project, Location: location})
		if err != nil {
			return nil, PullSecretListOutput{}, adapter.Redact(err)
		}
		out := PullSecretListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]PullSecretMetadata, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, pullSecretMeta(x)) // Value dropped
		}
		return textResult(fmt.Sprintf("%d pull secret(s) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "pull-secret-get",
		Description: "Get a pull-secret's metadata by name. The credential VALUE is never returned.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PullSecretGetInput) (*mcp.CallToolResult, PullSecretGetOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, PullSecretGetOutput{}, err
		}
		res, err := adapter.Client().PullSecret().Get(ctx, &api.PullSecretGet{Project: project, Location: location, Name: name})
		if err != nil {
			return nil, PullSecretGetOutput{}, adapter.Redact(err)
		}
		out := PullSecretGetOutput{
			ResolvedContext:    ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			PullSecretMetadata: pullSecretMeta(res), // Value dropped
		}
		return textResult(fmt.Sprintf("pull secret %s [project=%s location=%s] (value redacted)", out.Name, project, location)), out, nil
	})
}

// ===== WorkloadIdentity (read) =====

type WorkloadIdentityItem struct {
	Project  string `json:"project" jsonschema:"project sid"`
	Location string `json:"location" jsonschema:"location id"`
	Name     string `json:"name" jsonschema:"workload identity name"`
	GSA      string `json:"gsa,omitempty" jsonschema:"bound Google service account"`
	Status   string `json:"status,omitempty" jsonschema:"status"`
}

func toWorkloadIdentityItem(x *api.WorkloadIdentityItem) WorkloadIdentityItem {
	return WorkloadIdentityItem{Project: x.Project, Location: x.Location, Name: x.Name, GSA: x.GSA, Status: x.Status.Text()}
}

type WIListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
}

type WIListOutput struct {
	ResolvedContext
	Items []WorkloadIdentityItem `json:"items" jsonschema:"workload identities"`
}

type WIGetInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Name     string `json:"name" jsonschema:"workload identity name"`
}

type WIGetOutput struct {
	ResolvedContext
	WorkloadIdentityItem
}

func registerWorkloadIdentityRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "workload-identity-list",
		Description: "List workload identities in a project.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WIListInput) (*mcp.CallToolResult, WIListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		location := adapter.ResolveLocation(in.Location)
		if project == "" {
			return nil, WIListOutput{}, fmt.Errorf("project required")
		}
		res, err := adapter.Client().WorkloadIdentity().List(ctx, &api.WorkloadIdentityList{Project: project, Location: location})
		if err != nil {
			return nil, WIListOutput{}, adapter.Redact(err)
		}
		out := WIListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]WorkloadIdentityItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toWorkloadIdentityItem(x))
		}
		return textResult(fmt.Sprintf("%d workload identity(ies) [project=%s]", len(out.Items), project)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "workload-identity-get",
		Description: "Get a single workload identity by name.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WIGetInput) (*mcp.CallToolResult, WIGetOutput, error) {
		project, location, name, err := resolveDeploymentTarget(adapter, in.Project, in.Location, in.Name)
		if err != nil {
			return nil, WIGetOutput{}, err
		}
		res, err := adapter.Client().WorkloadIdentity().Get(ctx, &api.WorkloadIdentityGet{Project: project, Location: location, Name: name})
		if err != nil {
			return nil, WIGetOutput{}, adapter.Redact(err)
		}
		out := WIGetOutput{
			ResolvedContext:      ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			WorkloadIdentityItem: toWorkloadIdentityItem(res),
		}
		return textResult(fmt.Sprintf("workload identity %s [project=%s location=%s]", out.Name, project, location)), out, nil
	})
}

// ===== Organization (read) =====

type OrganizationItem struct {
	ID   string `json:"id" jsonschema:"organization id"`
	Name string `json:"name" jsonschema:"organization name"`
}

type OrgGetInput struct {
	ID string `json:"id" jsonschema:"organization id"`
}

type OrgListOutput struct {
	Items []OrganizationItem `json:"items" jsonschema:"organizations"`
}

type OrgProjectsOutput struct {
	Items []ProjectItem `json:"items" jsonschema:"projects in the organization"`
}

func registerOrganizationRead(server *mcp.Server, adapter *jetder.Adapter) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "organization-list",
		Description: "List organizations accessible to the user.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, OrgListOutput, error) {
		res, err := adapter.Client().Organization().List(ctx, nil)
		if err != nil {
			return nil, OrgListOutput{}, adapter.Redact(err)
		}
		out := OrgListOutput{Items: make([]OrganizationItem, 0, len(res.Items))}
		for _, x := range res.Items {
			out.Items = append(out.Items, OrganizationItem{ID: x.ID, Name: x.Name})
		}
		return textResult(fmt.Sprintf("%d organization(s)", len(out.Items))), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "organization-get",
		Description: "Get an organization by id.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OrgGetInput) (*mcp.CallToolResult, OrganizationItem, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, OrganizationItem{}, fmt.Errorf("id required")
		}
		res, err := adapter.Client().Organization().Get(ctx, &api.OrganizationGet{ID: in.ID})
		if err != nil {
			return nil, OrganizationItem{}, adapter.Redact(err)
		}
		return textResult(fmt.Sprintf("organization %s (%s)", res.ID, res.Name)), OrganizationItem{ID: res.ID, Name: res.Name}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "organization-projects",
		Description: "List projects belonging to an organization.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OrgGetInput) (*mcp.CallToolResult, OrgProjectsOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, OrgProjectsOutput{}, fmt.Errorf("id required")
		}
		res, err := adapter.Client().Organization().Projects(ctx, &api.OrganizationProjects{ID: in.ID})
		if err != nil {
			return nil, OrgProjectsOutput{}, adapter.Redact(err)
		}
		out := OrgProjectsOutput{Items: make([]ProjectItem, 0, len(res.Items))}
		for _, x := range res.Items {
			out.Items = append(out.Items, toProjectItem(x))
		}
		return textResult(fmt.Sprintf("%d project(s) in org %s", len(out.Items), in.ID)), out, nil
	})
}

// parseID strictly parses a positive decimal int64 id. It rejects empty input,
// trailing junk ("42abc"), embedded spaces ("42 abc"), and non-positive values
// — strconv.ParseInt requires the WHOLE string to be a valid base-10 integer.
func parseID(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("id required")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return v, nil
}
