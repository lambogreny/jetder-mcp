# Credentials & environment reference

Every environment variable the `jetder-mcp` server reads, what it's for, and
whether it's required. Set these in your **MCP client config** (under the server's
`env` block) or your shell — see the [README](../README.md) Quick start.

> The server only needs **Jetder** credentials to start. **Cloudflare** variables
> are read lazily and are only needed for the domain/DNS tools.

## Jetder (required to start)

Jetder uses HTTP **Basic auth**: a service-account email (username) + an API token
(password). Get them from the owner — see [Getting access](#getting-access).

| Env var                   | Required | Default                   | What it does                                                                 |
|---------------------------|----------|---------------------------|------------------------------------------------------------------------------|
| `JETDER_AUTH_USER`        | **yes**  | —                         | Basic-auth **username** = service-account email (e.g. `ai-dev@<project>.serviceaccount.jetder.com`). |
| `JETDER_TOKEN`            | **yes**\*| —                         | Basic-auth **password** = your Jetder API token.                             |
| `JETDER_AUTH_PASS`        | no       | —                         | Alias for `JETDER_TOKEN` (used only if `JETDER_TOKEN` is unset).             |
| `JETDER_DEFAULT_PROJECT`  | no       | —                         | Fallback project sid when a tool's `project` argument is omitted.            |
| `JETDER_DEFAULT_LOCATION` | no       | —                         | Fallback location id when a tool's `location` argument is omitted.           |
| `JETDER_ENDPOINT`         | no       | `https://api.jetder.com/` | Override the Jetder API base URL (mainly for testing).                       |

\* The password may come from **either** `JETDER_TOKEN` (preferred) **or**
`JETDER_AUTH_PASS`; one of the two is required. The server will not start without
`JETDER_AUTH_USER` and a token. The token, username, and the base64 `user:token`
header value are all redacted from any error the server returns.

Per-tool `project`/`location` arguments always override the `JETDER_DEFAULT_*`
env. Each tool echoes the resolved project/location in its result.

## GitHub — no env var needed

There is **no `GITHUB_TOKEN`** (or any GitHub env) read by the server.

- **Installing** the binary pulls from public GitHub Releases over
  `raw.githubusercontent.com` / `github.com` — no auth.
- **Pulling a private container image** at deploy time does **not** use a token on
  your machine. Jetder pulls the image using a **pull secret stored in the Jetder
  cluster** (named `ghcr-pull` by convention). Create it once with the
  `bootstrap-pull-secret` prompt — you provide a `read:packages` GitHub token
  directly to the `pull-secret-create` tool, and it is stored on the Jetder side,
  never in your shell. See [DEPLOY.md](./DEPLOY.md).

## Cloudflare (optional — only for domain / DNS tools)

Needed only for the `cf-*` tools (DNS records, zone lookup, and buying domains via
Registrar). A Jetder-only server runs fine without any of these. Cloudflare auth is
**Bearer** (separate from Jetder's Basic auth); the token and `Bearer <token>`
header value are redacted from errors.

### Core

| Env var                 | Required for…                  | What it does                                                                 |
|-------------------------|--------------------------------|------------------------------------------------------------------------------|
| `CLOUDFLARE_API_TOKEN`  | all `cf-*` tools               | Cloudflare API token (Bearer). If unset, the `cf-*` tools report "not configured" and the rest of the server is unaffected. |
| `CLOUDFLARE_ACCOUNT_ID` | the Registrar (`cf-domain-*`)  | Account ID, required for account-scoped Registrar calls (search/check/register). DNS/zone tools don't need it. |
| `CLOUDFLARE_API_BASE`   | no (testing)                   | Override the Cloudflare API base URL. Niche; leave unset in normal use.      |

### Registrant contact (only when buying a domain with `cf-domain-register`)

These supply the **registrant** (legal domain owner) WHOIS contact so the server
can register a domain without you visiting the Cloudflare dashboard. They are an
alternative to passing the contact inline as the `cf-domain-register` `registrant`
argument; set them **once** to avoid re-entering the contact each time.

> ⚠️ This is **PII** and is **legally binding** — inaccurate WHOIS details can get
> a domain **suspended**. Put these in a **user-level** secret store, never commit
> them to a project/shared config.

| Env var                              | Required | What it does                                                            |
|--------------------------------------|----------|-------------------------------------------------------------------------|
| `CLOUDFLARE_REGISTRANT_NAME`         | yes\*\*  | Full legal name of the registrant.                                      |
| `CLOUDFLARE_REGISTRANT_EMAIL`        | yes\*\*  | Contact email.                                                          |
| `CLOUDFLARE_REGISTRANT_PHONE`        | yes\*\*  | Phone in E.164 **with a dot**: `+<countryCode>.<number>` (e.g. `+1.5555550123`). |
| `CLOUDFLARE_REGISTRANT_STREET`       | yes\*\*  | Street address (incl. building/suite).                                  |
| `CLOUDFLARE_REGISTRANT_CITY`         | yes\*\*  | City / locality.                                                        |
| `CLOUDFLARE_REGISTRANT_STATE`        | yes\*\*  | State / province / region (standard abbreviation where applicable).     |
| `CLOUDFLARE_REGISTRANT_POSTAL_CODE`  | yes\*\*  | Postal / ZIP code.                                                      |
| `CLOUDFLARE_REGISTRANT_COUNTRY_CODE` | yes\*\*  | ISO 3166-1 alpha-2 country code (e.g. `US`, `GB`, `TH`).                |
| `CLOUDFLARE_REGISTRANT_ORG`          | no       | Organization / company name (optional for individuals).                 |
| `CLOUDFLARE_REGISTRANT_FAX`          | no       | Fax in the same E.164-with-dot format (optional).                       |

\*\* Required **only if you register a domain via the env contact**. If you don't
register, or you pass the contact inline as the tool's `registrant` argument, these
are unused. A **partial** contact is rejected — supply all required fields or none.

## Getting access

To get a Jetder service-account email + API token (and a project to deploy into),
contact the owner: **<https://thunder.in.th/>**.

For the detailed Cloudflare token + Account ID walkthrough, see
[CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md).
