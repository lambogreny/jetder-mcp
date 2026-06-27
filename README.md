# jetder-mcp

An [MCP](https://modelcontextprotocol.io) (Model Context Protocol) server that
exposes the [Jetder](https://jetder.com) API as MCP tools and resources, served
over **stdio**.

> **Status:** in progress. Tools for Me, Location, Project, Deployment
> (read + deploy/pause/resume/rollback), and Domain/Route (custom domains and
> routing) are implemented. Other resources follow in subsequent slices.

## Requirements

- Go 1.23+
- A Jetder API token

## Setup

```sh
cd mcp
export JETDER_TOKEN="<your-jetder-api-token>"   # required
# export JETDER_ENDPOINT="https://api.jetder.com/"  # optional override

go build ./...
go run .            # starts the MCP server on stdin/stdout
```

The server reads JSON-RPC from stdin and writes to stdout, so it is meant to be
launched by an MCP client (e.g. Claude Code) rather than run interactively.

### Configuration

| Env var                   | Required | Default                   | Notes                                                      |
|---------------------------|----------|---------------------------|------------------------------------------------------------|
| `JETDER_TOKEN`            | yes      | —                         | Bearer token; never logged or echoed.                      |
| `JETDER_ENDPOINT`         | no       | `https://api.jetder.com/` | Override the API base URL (testing).                       |
| `JETDER_DEFAULT_PROJECT`  | no       | —                         | Fallback project sid when a tool's `project` arg is empty. |
| `JETDER_DEFAULT_LOCATION` | no       | —                         | Fallback location when a tool's `location` arg is empty.   |

The token is injected as `Authorization: Bearer <token>` on every request and is
redacted from any error message before it reaches the client.

Per-tool `project`/`location` arguments are the source of truth and always
override the env defaults. Each tool reports the resolved context in its result.

## Tools

### Read-only (annotated `readOnlyHint: true`)

| Tool                   | Description                                                       |
|------------------------|------------------------------------------------------------------|
| `me-get`               | Get the authenticated Jetder user's profile (email, KYC).        |
| `location-list`        | List available locations (optionally scoped to a project).       |
| `location-get`         | Get a single location by id.                                     |
| `project-list`         | List projects accessible to the user.                            |
| `project-get`          | Get a single project by sid.                                     |
| `project-usage`        | Get current resource usage for a project.                        |
| `deployment-list`      | List deployments in a project (optionally filtered by location). |
| `deployment-get`       | Get a deployment (latest or a specific revision).                |
| `deployment-revisions` | List revision history for a deployment.                          |
| `deployment-metrics`   | Time-series metrics (cpu, memory, requests, egress).             |
| `domain-get`           | Get a domain with the DNS records needed to point it.            |
| `domain-list`          | List custom domains in a project.                                |
| `route-get`            | Get a single route by domain (and optional path).               |
| `route-list`           | List routes in a project.                                        |

### State-changing (`readOnlyHint: false`)

| Tool                  | Description                                          | Annotation             |
|-----------------------|------------------------------------------------------|------------------------|
| `deployment-deploy`   | Deploy/redeploy a service (changes active revision). | `destructiveHint:true` |
| `deployment-pause`    | Pause a deployment (stops serving until resumed).    | `destructiveHint:true` |
| `deployment-resume`   | Resume a paused deployment (restorative).            | `destructiveHint:false`|
| `deployment-rollback` | Roll a deployment back to a previous revision.       | `destructiveHint:true` |
| `domain-create`       | Add a custom domain to a project.                    | `destructiveHint:false`|
| `domain-purge-cache`  | Purge the CDN cache for a domain (no resource delete).| `destructiveHint:true` |
| `route-create-v2`     | Map a domain/path to a target (deployment://, etc.). | `destructiveHint:false`|

> `deployment-delete`, `domain-delete`, and `route-delete` are intentionally not
> exposed. Route V1 create is superseded by `route-create-v2`.

### Pointing a custom domain ("ชี้โดเมน")

1. `domain-create` — add the domain to the project.
2. `domain-get` — returns `ownershipRecord` (TXT to prove ownership), `sslRecords`
   (TXT/DCV to issue the certificate), and `pointTo` (the A/AAAA/CNAME records to
   set at your DNS provider). Set these records, then poll `domain-get` until
   `status` is `success`.
3. `route-create-v2` — route the domain to a deployment, e.g.
   `target: "deployment://my-service"`.

## Development notes

This module imports the Jetder API package `github.com/jetder-core/api` (and its
`/client` subpackage). That repo has **no published tags yet** and is private, so
`go.mod` uses a **local replace** pointing at the parent directory:

```
replace github.com/jetder-core/api => ../
```

This keeps the MCP build in lockstep with the live parent module during
development. Once `jetder-core/api` publishes tags, switch to a pinned remote
version and set `GOPRIVATE=github.com/jetder-core/*` (plus CI git credentials).

## Layout

```
mcp/
  main.go                    # server wiring + me-get tool
  tools_read.go              # location-*, project-* read tools
  tools_deployment_read.go   # deployment-* read tools
  tools_deployment_action.go # deployment deploy/pause/resume/rollback
  tools_domain.go            # domain create/get/list/purge-cache
  tools_route.go             # route create-v2/get/list
  internal/jetder/
    client.go                # adapter: client construction, bearer auth, redaction, defaults
  go.mod
  README.md
```
