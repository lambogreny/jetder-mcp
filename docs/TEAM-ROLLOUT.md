# Team rollout guide

A playbook for a DevOps/platform team adopting **jetder-mcp** across its engineers and
CI/CD. It is the team-scale companion to the single-person
[ONBOARDING.md](./ONBOARDING.md): where onboarding gets *one* person running, this gets
a *team* standardized — with shared conventions, CI integration, and a security model.

> New to jetder-mcp? It is an MCP server that lets an AI agent (Claude Code, Cursor,
> VS Code, …) operate Jetder — deploy apps, manage domains/DNS, read logs, and
> **diagnose** unhealthy deployments — by chatting. See the
> [README](../README.md) and [ARCHITECTURE.md](./ARCHITECTURE.md) for the full picture.

---

## 1. What your team gets

- **Deploy from chat** — `deployment-deploy`, pause/resume/rollback, revisions.
- **Observe** — `deployment-logs` (real log tails, secrets masked best-effort),
  `deployment-metrics`.
- **Diagnose ("Deploy Doctor")** — `deployment-diagnose` correlates status, pod health,
  Kubernetes events, and logs into likely causes + fixes, plus scaling/cost advice.
- **Domains & DNS** — buy/point domains, manage Cloudflare DNS; pointing a domain
  through Cloudflare (proxied) serves a valid cert at the edge right away.
- **Safe by design** — read-only tools are marked; destructive ones are flagged for
  confirmation; paid actions (domain registration) stop for explicit approval;
  the server's own credentials are kept out of output, and known secret patterns in
  logs are masked best-effort (see §5).

It runs locally for each engineer (over stdio) and in CI as part of your deploy
pipeline — the **same binary, same tools**.

---

## 2. Before you start (team prerequisites)

| Need | Who provides it | Notes |
| --- | --- | --- |
| A **Jetder account/project** | the internal jetder-mcp team (same owner as deecrm) | one project can hold many deployments |
| A **service-account email + API token** per environment | your team lead | used as HTTP Basic auth; see [CREDENTIALS.md](./CREDENTIALS.md) |
| An **MCP client** (one each) | each engineer | Claude Code / Claude Desktop / Cursor / VS Code |
| (optional) **Cloudflare API token** | team lead | only if you manage domains/DNS |

You do **not** need GitHub to install or connect jetder-mcp; deploying an
already-built image works without it. (Building/pushing a private GHCR image in CI is a
separate concern — see [DEPLOY.md](./DEPLOY.md).)

> **Token model: scope by environment, not by person.** Each token authenticates a
> *service account* (its email is `JETDER_AUTH_USER`; the token is the Basic-auth
> password — see [CREDENTIALS.md](./CREDENTIALS.md)). Request tokens from the internal
> jetder-mcp team (deecrm and jetder-mcp share an owner) and use a **distinct
> service-account token per environment** (dev / staging / prod) so each can be rotated
> or revoked independently. Engineers on the same environment share that environment's
> token — distribute it out of band; never commit it or paste it in chat.

---

## 3. Rollout in three phases

### Phase 1 — Pilot (1–2 engineers, ~1 day)

Goal: prove the end-to-end path on a non-critical deployment.

1. One engineer installs the binary (see §4) and asks the agent to run the
   **`check-setup`** tool — it verifies the Jetder auth and the resolved
   project/location, and reports anything missing (without echoing secrets).
2. Point the agent at a throwaway/dev deployment and try, in plain language:
   - "list my deployments"
   - "show me the logs for &lt;name&gt;"
   - "diagnose &lt;name&gt;" (Deploy Doctor)
3. Confirm the agent **asks before** anything destructive or paid, and that known
   credentials/secret patterns are **masked** in log output (best-effort — see §5).
4. Capture any friction in a short note for Phase 2.

**Exit criteria:** a deploy + a logs read + a diagnose all work against a real dev
deployment, and the team agrees the safety prompts are acceptable.

### Phase 2 — Standardize (whole team, ~2–3 days)

Goal: every engineer set up identically, with shared conventions.

1. Add the install + `claude mcp add` commands (from §4) to your team's dev-setup
   script or internal wiki, with your **service-account email** filled in; distribute
   the per-environment token out of band via your team secret process (never
   commit/paste it).
2. Set team defaults so nobody has to type project/location every time:
   `JETDER_DEFAULT_PROJECT` and `JETDER_DEFAULT_LOCATION` (see [CREDENTIALS.md](./CREDENTIALS.md)).
3. Agree on conventions:
   - which environments map to which Jetder deployments (e.g. `app-dev`, `app-staging`, `app-prod`);
   - that **prod changes go through review** (don't auto-approve destructive tool calls on prod);
   - who holds the prod token.
4. Have each engineer run the `check-setup` tool (via the agent) and confirm green.

**Exit criteria:** every engineer can deploy/observe/diagnose dev from their own client.

### Phase 3 — Scale to CI/CD (~2–3 days)

Goal: deploys flow through your pipeline, not just laptops.

1. Wire the deploy step into CI following [DEPLOY.md](./DEPLOY.md) — branch → environment
   → Jetder deployment (e.g. `dev → staging → prod`).
2. Store the per-environment `JETDER_TOKEN` as a CI **secret**, and the
   `JETDER_AUTH_USER` / project / location as CI **variables**.
3. Protect the production environment (required reviewers) so a prod deploy needs a human.
4. Keep `deployment-diagnose` handy for on-call: when a deploy is unhealthy, the agent
   can pull the diagnosis in seconds.

**Exit criteria:** a push to your dev branch deploys via CI, and prod is gated by review.

---

## 4. Engineer setup (copy into your wiki)

Install (macOS/Linux — pick one):

```sh
# Homebrew
brew install lambogreny/tap/jetder-mcp
```

```sh
# or the install script (review it first if you prefer: download, read, then run)
curl -fsSL https://raw.githubusercontent.com/lambogreny/jetder-mcp/main/install.sh | sh
```

The installer prints where it put the binary (usually `~/.local/bin` or
`/usr/local/bin`). Then register it with your MCP client — Claude Code shown here:

```sh
claude mcp add \
  -e JETDER_AUTH_USER=<svc>@<project>.serviceaccount.jetder.com \
  -e JETDER_TOKEN=<your-jetder-api-token> \
  --scope user jetder-mcp -- jetder-mcp
```

To skip typing the project/location on every request, add the two optional `-e` flags
to the **same** command (before the `--scope` flag):

```sh
claude mcp add \
  -e JETDER_AUTH_USER=<svc>@<project>.serviceaccount.jetder.com \
  -e JETDER_TOKEN=<your-jetder-api-token> \
  -e JETDER_DEFAULT_PROJECT=<project> \
  -e JETDER_DEFAULT_LOCATION=<location> \
  --scope user jetder-mcp -- jetder-mcp
```

Restart the client, then ask the agent to run the **`check-setup`** tool. Other clients
(Claude Desktop one-click `.mcpb`, Cursor, VS Code) are covered in the
[README](../README.md#add-to-your-mcp-client).

---

## 5. Security & governance

- **One token per environment**, issued out of band; rotate on a schedule and on
  offboarding. Revoking a token instantly cuts off that environment.
- **Least privilege**: the Cloudflare token (if used) only needs DNS edit + zone read,
  and Registrar write *only* if you buy domains — see [CREDENTIALS.md](./CREDENTIALS.md)
  and [CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md).
- **Secret masking (best-effort)**: logs and diagnostic evidence are sanitized on a
  best-effort basis (known credentials + common secret patterns masked). Treat it as defense in
  depth, not a guarantee — don't deliberately print secrets to app logs.
- **Approve deliberately**: destructive tools are flagged so your client can prompt for
  confirmation, and paid domain registration always stops first. Keep **prod**
  approvals manual; review each tool call before approving.
- **CI isolation**: the deploy step needs only the Jetder credentials in its
  environment — see the security model in [DEPLOY.md](./DEPLOY.md#security-model).

---

## 6. Troubleshooting

- **`check-setup` reports auth failure** → wrong `JETDER_AUTH_USER`/`JETDER_TOKEN`, or
  the token was rotated. Re-issue and re-register — ask the internal jetder-mcp team
  for a fresh token (external fallback: [thunder.in.th](https://thunder.in.th/)).
- **Agent can't find the tools** → make sure `claude mcp add` ran cleanly and restart
  the client; if `jetder-mcp` isn't on PATH, use the full path the installer printed.
- **"no project" / empty lists** → set `JETDER_DEFAULT_PROJECT` or pass `project` in the
  request; confirm the service account has access to the project.
- **Domain/DNS tools say Cloudflare isn't configured** → that's expected until you set a
  Cloudflare token; see [CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md).
- **A deploy is unhealthy** → ask the agent to **"diagnose &lt;name&gt;"**; Deploy Doctor
  surfaces the likely cause (crash loop, image pull, OOM, port, health check…) with the
  evidence and a suggested fix.

---

## 7. Rollout checklist

- [ ] Jetder project + per-environment service-account tokens issued
- [ ] Pilot: deploy + logs + diagnose verified on a dev deployment
- [ ] Install + `claude mcp add` documented in the team wiki (with service-account email)
- [ ] Team defaults (`JETDER_DEFAULT_PROJECT`/`LOCATION`) agreed and set
- [ ] Every engineer's `check-setup` is green
- [ ] CI deploy wired per [DEPLOY.md](./DEPLOY.md); tokens stored as CI secrets
- [ ] Production environment gated by required reviewers
- [ ] Token rotation + offboarding process written down

---

Questions about access or the platform: ask the **internal jetder-mcp team** (same
owner as deecrm) — external fallback **[thunder.in.th](https://thunder.in.th/)**.
For the tool catalog and diagrams, see the [README](../README.md) and
[ARCHITECTURE.md](./ARCHITECTURE.md).
