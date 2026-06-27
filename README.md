# jetder-mcp

An [MCP](https://modelcontextprotocol.io) (Model Context Protocol) server that
exposes the [Jetder](https://jetder.com) API to AI agents — **62 tools** and
**3 guided prompts** for deployments, domains, DNS, billing, secrets, and more,
served over **stdio**.

## Quick start

**1. Get access.** You need a Jetder API token (and a project) from the owner.
Request one here: **<https://thunder.in.th/>**.

**2. Install** (macOS / Linux, amd64 / arm64). The script downloads a release
binary, verifies its SHA-256 checksum, and installs it to `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/lambogreny/jetder-mcp/main/install.sh | sh
```

> Prefer to review first? Download `install.sh`, read it, then run `sh install.sh`.
> Pin a version with `JETDER_MCP_VERSION=v1.2.3`, or set `INSTALL_DIR`.
>
> Or build from source: `go build -o jetder-mcp .` (Go 1.23+).

**3. Add it to your MCP client.** Point the client at the installed binary and
supply your token via `env`:

- **Claude Desktop** (`claude_desktop_config.json`), **Cursor** (`.cursor/mcp.json`),
  **Claude Code** (`.mcp.json`) — all use the `mcpServers` shape:

  ```json
  {
    "mcpServers": {
      "jetder": {
        "command": "jetder-mcp",
        "env": {
          "JETDER_AUTH_USER": "<svc>@<project>.serviceaccount.jetder.com",
          "JETDER_TOKEN": "<your-jetder-api-token>"
        }
      }
    }
  }
  ```

- **VS Code** (`.vscode/mcp.json`) uses a `servers` key instead of `mcpServers`;
  the `command`/`env` block is identical.

Use the absolute path (e.g. `~/.local/bin/jetder-mcp`) for `command` if the bin
dir is not on your client's `PATH`. Optional env for domain tools and defaults:
`CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `JETDER_DEFAULT_PROJECT`,
`JETDER_DEFAULT_LOCATION`. The full list of every variable the server reads is in
**[docs/CREDENTIALS.md](./docs/CREDENTIALS.md)**.

**4. Verify.** Ask the agent to run the **`check-setup`** tool — it reports
whether your auth, project/location, Cloudflare config, and pull-secret are ready
to deploy, with a remediation (and the owner contact) for anything missing.

## Getting access

jetder-mcp talks to a Jetder account you must be granted access to. To get a
service-account email + API token (and a project to deploy into), contact the
owner: **<https://thunder.in.th/>**. Set the token as `JETDER_TOKEN` (and the
service-account email as `JETDER_AUTH_USER`) in your MCP client config above.

## What's included

- **62 tools** across Me, Location, Project, Deployment (read + deploy / pause /
  resume / rollback), Domain / Route, Billing (read), Disk, ServiceAccount, Role,
  Secret / PullSecret (values redacted), WorkloadIdentity, Organization, Email,
  Cloudflare DNS + Registrar (buy a domain, with a registrant contact), and a
  `check-setup` preflight doctor.
- **3 guided prompts:** `deploy-an-app`, `bootstrap-pull-secret`, `point-a-domain`.

Deferred for safety until an explicit opt-in: `role-bind` (replace-all role set =
implicit revoke) and route `forwardAuth`. `*delete`, role revoke, and
service-account key deletion are intentionally not exposed.

The server reads JSON-RPC from stdin and writes to stdout, so it is launched by an
MCP client rather than run interactively.

### Configuration

| Env var                   | Required | Default                   | Notes                                                          |
|---------------------------|----------|---------------------------|----------------------------------------------------------------|
| `JETDER_AUTH_USER`        | yes      | —                         | Basic-auth username = service-account email.                   |
| `JETDER_TOKEN`            | yes      | —                         | Basic-auth password = API token. Alias: `JETDER_AUTH_PASS`. Never logged/echoed. |
| `JETDER_ENDPOINT`         | no       | `https://api.jetder.com/` | Override the API base URL (testing).                           |
| `JETDER_DEFAULT_PROJECT`  | no       | —                         | Fallback project sid when a tool's `project` arg is empty.     |
| `JETDER_DEFAULT_LOCATION` | no       | —                         | Fallback location when a tool's `location` arg is empty.       |

Credentials are applied as HTTP **Basic auth** (`r.SetBasicAuth(user, token)`) on
every request. The username, token, and the base64 `user:token` header value are
all redacted from any error message before it reaches the client.

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
| `billing-list`         | List billing accounts.                                           |
| `billing-get`          | Get a billing account by id.                                     |
| `billing-project-price`| Get the current accrued price for a project.                    |
| `disk-list` / `disk-get`             | List / get disks.                                  |
| `service-account-list` / `-get`      | List / get service accounts (key metadata only).   |
| `role-list` / `-get` / `-users` / `-permissions` | Roles, role members, assignable perms. |
| `secret-list` / `secret-get`         | Secret **metadata only** — values are never returned. |
| `pull-secret-list` / `-get`          | Pull-secret **metadata only** — values never returned. |
| `workload-identity-list` / `-get`    | List / get workload identities.                    |
| `organization-list` / `-get` / `-projects` | Organizations and their projects.            |

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
| `disk-create` / `disk-update`       | Create a disk / resize (grow-only).   | `destructiveHint:false`|
| `secret-create`       | Create a secret (value write-only, never echoed).    | `destructiveHint:false`|
| `pull-secret-create`  | Create a registry pull secret (password write-only). | `destructiveHint:false`|
| `workload-identity-create`          | Create a workload identity.           | `destructiveHint:false`|
| `service-account-create` / `-update`| Create / rename a service account.    | `destructiveHint:false`|
| `organization-create` / `-update`   | Create / rename an organization.      | `destructiveHint:false`|
| `role-create`         | Create a role with permissions.                      | `destructiveHint:false`|
| `role-grant`          | Grant a role to a user (additive).                   | `destructiveHint:true` |
| `service-account-create-key` | Create a key for a service account.           | `destructiveHint:true` |
| `email-send`          | Send an email from a project.                        | `destructiveHint:true` |

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
`/client` subpackage). That repo is **public** and fetchable without auth, so
`go.mod` simply **pins it to a remote pseudo-version** — there is **no committed
`replace`** and **no `GOPRIVATE`/credentials** needed (CI runners fetch it
directly):

```
require github.com/jetder-core/api v0.0.0-20251222122510-e4c734d325f7
```

Pin a semver tag instead of the pseudo-version once `jetder-core/api` publishes
one.

**Local development against uncommitted `jetder-core/api` changes:** temporarily
add a `replace github.com/jetder-core/api => ../` (or use a `go.work` file) — but
**do not commit it**, or external `go run github.com/lambogreny/jetder-mcp@<ref>`
will break.

## Layout

```
mcp/
  main.go                    # server wiring + me-get tool
  tools_read.go              # location-*, project-* read tools
  tools_deployment_read.go   # deployment-* read tools
  tools_deployment_action.go # deployment deploy/pause/resume/rollback
  tools_domain.go            # domain create/get/list/purge-cache
  tools_route.go             # route create-v2/get/list
  tools_resources_read.go    # billing/disk/sa/role/secret/pullsecret/wi/org reads
  tools_resources_write.go   # disk/secret/pullsecret/wi/sa/org/role create+update
  tools_grants_email.go      # role grant/bind, sa create-key, email send
  tools_cloudflare.go        # cf-* tools (DNS + Registrar, registrant contact)
  tools_check_setup.go       # "check-setup" preflight doctor tool
  prompt_point_a_domain.go   # "point-a-domain" guided MCP prompt
  prompt_deploy_wizards.go   # "deploy-an-app" + "bootstrap-pull-secret" prompts
  install.sh                 # one-line installer (release binary + checksum verify)
  .github/workflows/release.yml # tag v* -> cross-platform binaries + SHA256SUMS
  internal/jetder/
    client.go                # adapter: jetder basic auth, redaction, defaults
  internal/cloudflare/
    client.go                # Cloudflare zone + DNS client (Bearer, idempotent)
    registrar.go             # Cloudflare Registrar (search/check/register + purchase guard)
  scripts/mcp-deploy/        # CI deploy helper (drives the server)
  docs/DEPLOY.md             # CI/CD deploy guide
  docs/CLOUDFLARE-SETUP.md   # Cloudflare token/account setup + cf tools + point-a-domain
  go.mod
  README.md
```

## More docs

- **[docs/GETTING-STARTED.md](./docs/GETTING-STARTED.md) — start here:** zero-to-deployed
  beginner guide (install tools, get credentials, connect any MCP client, deploy).
- **[docs/CREDENTIALS.md](./docs/CREDENTIALS.md) — credential reference:** every env
  var the server reads (Jetder, Cloudflare, registrant contact), required vs optional.
- [docs/DEPLOY.md](./docs/DEPLOY.md) — deploy details: build → GHCR → deploy via MCP.
- [docs/CLOUDFLARE-SETUP.md](./docs/CLOUDFLARE-SETUP.md) — Cloudflare DNS + domain
  registration, the step-by-step token/Account-ID walkthrough, and the
  `point-a-domain` guided prompt.
