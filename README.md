# jetder-mcp

An [MCP](https://modelcontextprotocol.io) (Model Context Protocol) server that
exposes the [Jetder](https://jetder.com) API as MCP tools and resources, served
over **stdio**.

> **Status:** in progress. Read-only tools for Me, Location, Project, and
> Deployment are implemented. Deployment actions (deploy/pause/resume/rollback)
> and Domain/Route are added in subsequent slices.

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

All tools below are read-only (annotated `readOnlyHint`).

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
  internal/jetder/
    client.go                # adapter: client construction, bearer auth, redaction, defaults
  go.mod
  README.md
```
