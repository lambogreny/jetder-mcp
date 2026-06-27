# Cloudflare setup (DNS + domain registration)

jetder-mcp can manage Cloudflare DNS and register domains directly, so the whole
"point a domain at a deployment" flow runs through this one server. Cloudflare is
**optional** — if you don't set the env below, the `cf-*` tools simply report that
Cloudflare is not configured and the rest of the server works normally.

## 1. Create a Cloudflare API token

Cloudflare dashboard → **My Profile → API Tokens → Create Token → Create Custom Token**.

Grant the **minimum** permissions for what you use:

| Permission                              | Needed for                            |
|-----------------------------------------|---------------------------------------|
| **Zone → DNS → Edit**                   | `cf-dns-create`, `cf-dns-list`        |
| **Zone → Zone → Read** (or Edit)        | `cf-zone-lookup` / resolving zones    |
| **Account → Registrar Domains → Edit**  | `cf-domain-*` (search/check/register) |

- **Account Resources:** include the account you'll use (or "All accounts").
- **Zone Resources:** the zones you'll manage (or "All zones" of the account).
- Keep the scope as narrow as you actually need.

Copy the token value (shown once).

## 2. Find your Account ID

Dashboard → pick your account → **Overview** (right sidebar shows *Account ID*), or
read it from the dashboard URL: `dash.cloudflare.com/<ACCOUNT_ID>/...`.
The Account ID is **required for the Registrar tools** (domain search/check/register).

## 3. Set the environment

```sh
export CLOUDFLARE_API_TOKEN="<your-token>"     # required for cf-* tools
export CLOUDFLARE_ACCOUNT_ID="<your-account>"  # required for cf-domain-* (Registrar)
# plus the Jetder auth from the main README:
export JETDER_AUTH_USER="<svc>@<project>.serviceaccount.jetder.com"
export JETDER_TOKEN="<jetder-api-token>"
```

Cloudflare auth is **Bearer** (separate from Jetder's Basic auth). The token and the
`Bearer` header value are redacted from every error/log.

## Cloudflare tools

| Tool                     | Kind        | Description                                              |
|--------------------------|-------------|---------------------------------------------------------|
| `cf-domain-search`       | read-only   | Search Registrar for domain ideas by keyword.           |
| `cf-domain-check`        | read-only   | Check exact domains' availability + price (price source of truth). |
| `cf-zone-lookup`         | read-only   | Resolve the zone that owns a name (longest suffix).     |
| `cf-dns-list`            | read-only   | List DNS records in a zone (filter by type/name).       |
| `cf-registration-status` | read-only   | Poll a domain registration workflow.                    |
| `cf-dns-create`          | **destructive** | Create a DNS record (idempotent; never overwrites). |
| `cf-domain-register`     | **destructive, billable** | Register (buy) one domain — guarded (see below). |

### Registering a domain safely (`cf-domain-register`)

Registration **spends money and is non-refundable**. The tool re-checks the live
price immediately before buying and rejects unless you pass all of:

- `confirmText` exactly equal to **`REGISTER <domain>`** (domain is lowercased),
- `maxRegistrationCost` and `currency` matching the live quote (total = cost × years),
- `acceptNonRefundable: true`,
- `acceptPremium: true` **if** the domain is a premium (non-standard) tier — a
  premium domain is rejected unless you explicitly approve it,
- `acceptAutoRenew: true` if you also set `autoRenew` (a recurring future charge).

It registers exactly one domain per call and never auto-retries; poll
`cf-registration-status` for the workflow result.

## The `point-a-domain` prompt

The `point-a-domain` MCP **prompt** gives an agent the full guided playbook
(using only this server's tools):

1. *(optional, `registerDomain=true`)* `cf-domain-check` → **stop and ask you to
   approve the domain + price** → `cf-domain-register`.
2. `domain-create` in Jetder.
3. `domain-get` → read the DNS records Jetder needs (ownership TXT, SSL/DCV, and the
   A/AAAA/CNAME `pointTo` records).
4. `cf-dns-create` for each of those records (idempotent).
5. Poll `domain-get` until `status: success`.
6. `route-create-v2` with `target: deployment://<deployment>`.

Arguments: `domain` + `deployment` (required), `project` / `location` / `path` /
`registerDomain` (optional). The prompt never buys a domain on its own — it always
stops for explicit approval before registering.

## Security

- Use the **narrowest** token scope you need; prefer per-zone over all-zones.
- **Rotate** the token periodically and on any suspected exposure.
- Never commit the token. The server keeps it out of logs and errors.

See also: the main [README](../README.md) and the deploy guide
[docs/DEPLOY.md](./DEPLOY.md).
