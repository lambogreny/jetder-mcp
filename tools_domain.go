package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jetder-core/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// registerDomainTools registers domain tools: create, get, list, purge-cache.
// (domain-delete is intentionally NOT exposed.)
func registerDomainTools(server *mcp.Server, adapter *jetder.Adapter) {
	registerDomainCreate(server, adapter)
	registerDomainGet(server, adapter)
	registerDomainList(server, adapter)
	registerDomainPurgeCache(server, adapter)
}

// ---- domain view types ----

// DNSRecordHint is a DNS record the user must create at their DNS provider.
type DNSRecordHint struct {
	Type  string `json:"type" jsonschema:"record type (A, AAAA, CNAME, TXT)"`
	Name  string `json:"name,omitempty" jsonschema:"record name/host"`
	Value string `json:"value" jsonschema:"record value to set"`
}

// DomainItem is the MCP-facing view of a domain. It surfaces everything an agent
// needs to tell a user HOW to point their domain: ownership-verification TXT,
// SSL/DCV records, and the A/AAAA/CNAME targets.
type DomainItem struct {
	Project  string `json:"project" jsonschema:"project sid"`
	Location string `json:"location" jsonschema:"location id"`
	Domain   string `json:"domain" jsonschema:"the domain name"`
	Wildcard bool   `json:"wildcard" jsonschema:"whether this is a wildcard domain"`
	CDN      bool   `json:"cdn" jsonschema:"whether CDN is enabled"`
	Status   string `json:"status" jsonschema:"domain status: pending, verify, success, error"`

	// Pointing instructions.
	OwnershipRecord *DNSRecordHint  `json:"ownershipRecord,omitempty" jsonschema:"TXT record to prove domain ownership"`
	SSLPending      bool            `json:"sslPending" jsonschema:"whether SSL issuance is still pending"`
	SSLRecords      []DNSRecordHint `json:"sslRecords,omitempty" jsonschema:"TXT/DCV records required to issue the SSL certificate"`
	PointTo         []DNSRecordHint `json:"pointTo,omitempty" jsonschema:"A/AAAA/CNAME records to point the domain at Jetder"`

	OwnershipErrors []string `json:"ownershipErrors,omitempty" jsonschema:"ownership verification errors, if any"`
	SSLErrors       []string `json:"sslErrors,omitempty" jsonschema:"ssl verification errors, if any"`
}

func toDomainItem(x *api.DomainItem) DomainItem {
	d := DomainItem{
		Project:         x.Project,
		Location:        x.Location,
		Domain:          x.Domain,
		Wildcard:        x.Wildcard,
		CDN:             x.CDN,
		Status:          x.Status.String(),
		SSLPending:      x.Verification.SSL.Pending,
		OwnershipErrors: x.Verification.Ownership.Errors,
		SSLErrors:       x.Verification.SSL.Errors,
	}

	// Ownership TXT record.
	if ow := x.Verification.Ownership; ow.Name != "" || ow.Value != "" {
		typ := ow.Type
		if typ == "" {
			typ = "TXT"
		}
		d.OwnershipRecord = &DNSRecordHint{Type: typ, Name: ow.Name, Value: ow.Value}
	}

	// SSL records: DCV + any TXT records.
	if dcv := x.Verification.SSL.DCV; dcv.Name != "" || dcv.Value != "" {
		d.SSLRecords = append(d.SSLRecords, DNSRecordHint{Type: "TXT", Name: dcv.Name, Value: dcv.Value})
	}
	for _, r := range x.Verification.SSL.Records {
		d.SSLRecords = append(d.SSLRecords, DNSRecordHint{Type: "TXT", Name: r.TxtName, Value: r.TxtValue})
	}

	// Point-to records: A (ipv4), AAAA (ipv6), CNAME.
	for _, ip := range x.DNSConfig.IPv4 {
		d.PointTo = append(d.PointTo, DNSRecordHint{Type: "A", Name: x.Domain, Value: ip})
	}
	for _, ip := range x.DNSConfig.IPv6 {
		d.PointTo = append(d.PointTo, DNSRecordHint{Type: "AAAA", Name: x.Domain, Value: ip})
	}
	for _, cn := range x.DNSConfig.CName {
		d.PointTo = append(d.PointTo, DNSRecordHint{Type: "CNAME", Name: x.Domain, Value: cn})
	}
	return d
}

// ---- create ----

type DomainCreateInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"location id (falls back to JETDER_DEFAULT_LOCATION)"`
	Domain   string `json:"domain" jsonschema:"domain name to add (must not end with .jetder.com)"`
	Wildcard bool   `json:"wildcard,omitempty" jsonschema:"create as a wildcard domain"`
	CDN      bool   `json:"cdn,omitempty" jsonschema:"enable CDN"`
}

// DomainActionOutput is the resolved-context-aware result for domain mutations.
type DomainActionOutput struct {
	ResolvedContext
	Domain  string `json:"domain" jsonschema:"the domain acted upon"`
	Action  string `json:"action" jsonschema:"the action performed"`
	Success bool   `json:"success" jsonschema:"whether the action was accepted"`
}

func registerDomainCreate(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DomainCreateInput) (*mcp.CallToolResult, DomainActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		location := adapter.ResolveLocation(in.Location)
		domain := strings.TrimSpace(in.Domain)
		if project == "" {
			return nil, DomainActionOutput{}, fmt.Errorf("project required")
		}
		if location == "" {
			return nil, DomainActionOutput{}, fmt.Errorf("location required")
		}
		if domain == "" {
			return nil, DomainActionOutput{}, fmt.Errorf("domain required")
		}
		_, err := adapter.Client().Domain().Create(ctx, &api.DomainCreate{
			Project:  project,
			Location: location,
			Domain:   domain,
			Wildcard: in.Wildcard,
			CDN:      in.CDN,
		})
		if err != nil {
			return nil, DomainActionOutput{}, adapter.Redact(err)
		}
		out := DomainActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Domain:          domain,
			Action:          "create",
			Success:         true,
		}
		return textResult(fmt.Sprintf("created domain %s [project=%s location=%s]; call domain-get for DNS pointing records", domain, project, location)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "domain-create",
		Description: "Add a custom domain to a project. After creating, call domain-get to retrieve the DNS records needed to verify ownership and point the domain.",
		Annotations: nonReadOnly(),
	}, handler)
}

// ---- get ----

type DomainGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Domain  string `json:"domain" jsonschema:"domain name"`
}

// DomainGetOutput wraps the domain with resolved context.
type DomainGetOutput struct {
	ResolvedContext
	DomainItem
}

func registerDomainGet(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DomainGetInput) (*mcp.CallToolResult, DomainGetOutput, error) {
		project := adapter.ResolveProject(in.Project)
		domain := strings.TrimSpace(in.Domain)
		if project == "" {
			return nil, DomainGetOutput{}, fmt.Errorf("project required")
		}
		if domain == "" {
			return nil, DomainGetOutput{}, fmt.Errorf("domain required")
		}
		res, err := adapter.Client().Domain().Get(ctx, &api.DomainGet{Project: project, Domain: domain})
		if err != nil {
			return nil, DomainGetOutput{}, adapter.Redact(err)
		}
		out := DomainGetOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			DomainItem:      toDomainItem(res),
		}
		return textResult(fmt.Sprintf("domain %s status=%s (%d point-to record(s)) [project=%s]", out.Domain, out.Status, len(out.PointTo), project)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "domain-get",
		Description: "Get a domain with the DNS records required to point it: ownership TXT, SSL/DCV records, and the A/AAAA/CNAME targets to set at your DNS provider.",
		Annotations: readOnly(),
	}, handler)
}

// ---- list ----

type DomainListInput struct {
	Project  string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Location string `json:"location,omitempty" jsonschema:"optional location filter (falls back to JETDER_DEFAULT_LOCATION)"`
}

type DomainListOutput struct {
	ResolvedContext
	Items []DomainItem `json:"items" jsonschema:"domains in the project"`
}

func registerDomainList(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DomainListInput) (*mcp.CallToolResult, DomainListOutput, error) {
		project := adapter.ResolveProject(in.Project)
		if project == "" {
			return nil, DomainListOutput{}, fmt.Errorf("project required")
		}
		location := adapter.ResolveLocation(in.Location)
		res, err := adapter.Client().Domain().List(ctx, &api.DomainList{Project: project, Location: location})
		if err != nil {
			return nil, DomainListOutput{}, adapter.Redact(err)
		}
		out := DomainListOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project, ResolvedLocation: location},
			Items:           make([]DomainItem, 0, len(res.Items)),
		}
		for _, x := range res.Items {
			out.Items = append(out.Items, toDomainItem(x))
		}
		return textResult(fmt.Sprintf("%d domain(s) [project=%s]", len(out.Items), project)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "domain-list",
		Description: "List custom domains in a project.",
		Annotations: readOnly(),
	}, handler)
}

// ---- purge cache ----

type DomainPurgeCacheInput struct {
	Project string `json:"project,omitempty" jsonschema:"project sid (falls back to JETDER_DEFAULT_PROJECT)"`
	Domain  string `json:"domain" jsonschema:"domain name"`
	File    string `json:"file,omitempty" jsonschema:"specific file path to purge"`
	Prefix  string `json:"prefix,omitempty" jsonschema:"path prefix to purge"`
}

func registerDomainPurgeCache(server *mcp.Server, adapter *jetder.Adapter) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, in DomainPurgeCacheInput) (*mcp.CallToolResult, DomainActionOutput, error) {
		project := adapter.ResolveProject(in.Project)
		domain := strings.TrimSpace(in.Domain)
		if project == "" {
			return nil, DomainActionOutput{}, fmt.Errorf("project required")
		}
		if domain == "" {
			return nil, DomainActionOutput{}, fmt.Errorf("domain required")
		}
		_, err := adapter.Client().Domain().PurgeCache(ctx, &api.DomainPurgeCache{
			Project: project,
			Domain:  domain,
			File:    in.File,
			Prefix:  in.Prefix,
		})
		if err != nil {
			return nil, DomainActionOutput{}, adapter.Redact(err)
		}
		out := DomainActionOutput{
			ResolvedContext: ResolvedContext{ResolvedProject: project},
			Domain:          domain,
			Action:          "purgeCache",
			Success:         true,
		}
		return textResult(fmt.Sprintf("purged CDN cache for %s [project=%s]", domain, project)), out, nil
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "domain-purge-cache",
		Description: "Purge the CDN cache for a domain (optionally a specific file or prefix). Removes cached content (affecting serving / origin load); does not delete the domain.",
		Annotations: destructive(),
	}, handler)
}
