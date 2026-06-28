# Deploying via jetder-mcp (CI/CD)

This guide explains how to deploy an application to **Jetder** from CI by driving
the `jetder-mcp` server. It is written both for humans setting up a repo and for
AI agents (Claude) that need to understand and trigger deploys.

## How it works (the flow)

```
 your app repo                         jetder-mcp                 Jetder API
 ┌───────────┐   build+push   ┌──────────────────────┐   deploy   ┌─────────┐
 │ Dockerfile│ ─────────────▶ │ ghcr.io/lambogreny/  │            │         │
 └───────────┘                │   <app>:<sha>        │            │         │
      │                       └──────────────────────┘            │         │
      │ deploy job (gated)               ▲                         │         │
      ▼                                  │ deployment-deploy       │         │
 ┌────────────┐  spawns (stdio)  ┌───────┴────────┐  Basic auth   │         │
 │ mcp-deploy │ ───────────────▶ │ jetder-mcp     │ ────────────▶ │         │
 │  helper    │ ◀─ result ─────  │ (MCP server)   │ ◀─ result ──  │         │
 └────────────┘                  └────────────────┘               └─────────┘
```

1. **build-push** — the workflow builds your app's Docker image and pushes it to
   `ghcr.io/lambogreny/<app>:<git-sha>`.
2. **deploy** (gated) — pulls the `mcp-deploy` helper straight from the jetder-mcp
   Go module (`go run github.com/lambogreny/jetder-mcp/scripts/mcp-deploy@<ver>`)
   and runs it. The helper launches the MCP server over stdio
   (`CommandTransport`), performs the handshake, and calls the
   **`deployment-deploy`** tool with the pushed image. It exits non-zero on any
   error, so the CI job fails loudly.

> A complete, working example is the separate repo
> [`jetder-sample-app`](https://github.com/lambogreny/jetder-sample-app) — copy
> its `.github/workflows/deploy.yml` as your starting point.

The MCP server authenticates to the Jetder API with **HTTP Basic auth** —
`JETDER_AUTH_USER` (service-account email) as the username and `JETDER_TOKEN` as
the password. Credentials are **never** tool arguments and are masked/redacted
everywhere (username, token, and the base64 header value).

## Branch strategy (dev → staging → prod)

The template uses a three-tier flow. All three deploy to the **same Jetder
project** but to **different deployments** (distinguished by name):

| Branch    | GitHub Environment | Jetder deployment     | Image tag             |
|-----------|--------------------|-----------------------|-----------------------|
| `dev`     | `dev`              | `<APPNAME>-dev`       | `dev-<sha>`           |
| `staging` | `staging`          | `<APPNAME>-staging`   | `staging-<sha>`       |
| `main`    | `production`       | `<APPNAME>-prod`      | `prod-<sha>`          |

`<APPNAME>` is the app name you set in the workflow's *Resolve branch → …* step
(`APP="..."`). `production` should be a **protected** environment (approval gate);
`dev`/`staging` can be unprotected.

**Promotion:** merge `dev → staging → main`. Each merge's push triggers a deploy
to that tier; the `main` (production) deploy waits for environment approval.

## Setup in your app repo

1. Copy the deploy workflow from the
   [`jetder-sample-app`](https://github.com/lambogreny/jetder-sample-app) repo
   (`.github/workflows/deploy.yml`) into your app repo and set the `APP` env value
   and Dockerfile path/context for your project.
2. In Jetder, create three deployments in your project:
   `<APPNAME>-dev`, `<APPNAME>-staging`, `<APPNAME>-prod`.
3. Create three **Environments** (Settings → Environments → New environment):
   `dev`, `staging`, `production`. For each, **restrict deployment branches** to
   the matching branch (`dev`→`dev`, `staging`→`staging`, `production`→`main`).
   On **`production`** also add **required reviewers**.
4. Add `JETDER_TOKEN` as an **environment-level Secret on each environment**
   (Environments → \<env\> → Add secret) — **not** a repo-wide secret. This way
   each tier can hold a least-privilege token scoped to that tier's deployment,
   and the token is only available to jobs bound to that environment.
5. Add **Variables** — `JETDER_AUTH_USER` (the service-account email = Basic-auth
   username), `JETDER_PROJECT`, and `JETDER_LOCATION` (the location id; see
   `location-list`). These are shared, so repo-level Variables are fine. (The
   username is not secret; only `JETDER_TOKEN` is a Secret.)

The deploy job sets `environment:` from the branch, so it picks up that env's
`JETDER_TOKEN` and (for production) waits for approval, while dev/staging flow
through.

## Security model

- `JETDER_TOKEN` is exposed **only** in the deploy job's deploy step.
- `GITHUB_TOKEN` is used **only** to push to GHCR (`packages: write`).
- The deploy job runs **only** on `dev`/`staging`/`main` pushes or manual
  `workflow_dispatch`, and binds to the branch's GitHub Environment — the
  `production` environment carries the required-reviewer protection. **Pull
  requests and forks never run the deploy job**, so the Jetder token is never
  exposed to untrusted code.
- The step calls `::add-mask::` on the token and never uses `set -x`. The helper
  redacts the token from any error output (server stderr, tool errors, SDK errors).

## For agents / Claude: triggering and understanding deploys

- **What tool deploys?** `deployment-deploy` on the jetder-mcp server. It is
  annotated `destructiveHint:true` (it changes the active revision).
- **Required args:** `project`, `location`, `name`, `image` (all explicit — the CI
  path does NOT rely on `JETDER_DEFAULT_*` env defaults). Optional: `branch`,
  `minReplicas`, `maxReplicas`.
- **Result:** structured output with `success`, `resolvedProject`,
  `resolvedLocation`, `name`. The helper asserts the resolved context equals the
  requested args before reporting success.
- **Manual deploy:** run the `deploy` workflow via **Run workflow**
  (workflow_dispatch) and pick the branch (`dev`/`staging`/`main`) — it deploys
  that tier's image to that tier's deployment, honoring the environment gate.
- **Local / manual (outside CI):** from a clone of jetder-mcp, build the server
  and helper and run the helper directly:
  ```sh
  cd mcp
  go build -o jetder-mcp .
  go build -o mcp-deploy ./scripts/mcp-deploy
  JETDER_AUTH_USER=<svc>@<project>.serviceaccount.jetder.com \
  JETDER_TOKEN=*** ./mcp-deploy \
    -server ./jetder-mcp \
    -project <sid> -location <loc> -name <deployment> \
    -image ghcr.io/lambogreny/<app>:<tag>
  ```
  (For local interactive use you may instead set `JETDER_DEFAULT_PROJECT` /
  `JETDER_DEFAULT_LOCATION` and call the MCP tools directly — but CI always uses
  explicit args.)

## Build for the right architecture (linux/amd64)

The Jetder cluster runs **linux/amd64**. Your container image MUST be built for that
architecture. This bites people who build on **Apple Silicon (Mac M1/M2/M3)**: Docker
defaults to **arm64** there, so the image builds and pushes fine but the pod
**crash-loops with `exec format error`** at runtime.

Always build for amd64:

```sh
docker build --platform linux/amd64 -t <image> .
# or, with buildx:
docker buildx build --platform linux/amd64 -t <image> --push .
```

If a deployment is crash-looping, run **`deployment-diagnose`** — the Deploy Doctor
flags `ArchitectureMismatch` (from `exec format error`) and tells you exactly this.

## Notes

- `jetder-mcp` is the deploy *driver*; it does not need to be containerized to run
  in CI (it's a Go binary).
- **How the helper finds the server:** `mcp-deploy` launches the MCP server via
  `-server-command` (a command split into argv and exec'd directly — no shell, no
  injection). In CI both the helper and the server are pulled from the same pinned
  ref, so the adopter needs nothing from jetder-mcp on disk:
  ```sh
  go run github.com/lambogreny/jetder-mcp/scripts/mcp-deploy@$REF \
    -server-command "go run github.com/lambogreny/jetder-mcp@$REF" -timeout 5m ...
  ```
  (`-server <binary-path>` is the alternative for a prebuilt server, e.g. local.)
- **Distribution:** `jetder-mcp` and its `github.com/jetder-core/api` dependency
  are **public** — CI runners fetch them with no auth (no `GOPRIVATE`/PAT needed).
  `mcp/go.mod` pins the api dependency to a remote pseudo-version (no local
  `replace`), so `go run …@<ref>` resolves out of the box. Pin `@<tag>` instead of
  `@main` once jetder-mcp tags a release.
- **Local dev:** to develop against uncommitted `jetder-core/api` changes, add
  `replace github.com/jetder-core/api => ../` to `mcp/go.mod` temporarily — but do
  not commit it.
