# jetder-mcp

An [MCP](https://modelcontextprotocol.io) (Model Context Protocol) server that
exposes the [Jetder](https://jetder.com) API as MCP tools and resources, served
over **stdio**.

> **Status:** skeleton slice. Currently ships one tool (`me-get`) to prove the
> end-to-end path. More tools/resources (Location, Project, Deployment,
> Domain/Route, …) are added in subsequent slices.

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

| Env var           | Required | Default                     | Notes                                  |
|-------------------|----------|-----------------------------|----------------------------------------|
| `JETDER_TOKEN`    | yes      | —                           | Bearer token; never logged or echoed.  |
| `JETDER_ENDPOINT` | no       | `https://api.jetder.com/`   | Override the API base URL (testing).   |

The token is injected as `Authorization: Bearer <token>` on every request and is
redacted from any error message before it reaches the client.

## Tools

| Tool     | Description                                                  |
|----------|-------------------------------------------------------------|
| `me-get` | Get the authenticated Jetder user's profile (email, KYC).   |

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
  main.go                  # server wiring + tool registration
  internal/jetder/
    client.go              # adapter: client construction, bearer auth, redaction
  go.mod
  README.md
```
