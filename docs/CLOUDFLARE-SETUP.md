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

### Registrant contact (who legally owns the domain)

Every registration needs a **registrant contact** — the legal owner's WHOIS
details that go to the registry. You can either let Cloudflare use the account's
default address book, or have jetder-mcp supply the contact so you never have to
touch the Cloudflare dashboard.

> ⚠️ This data is **legally binding**. Inaccurate WHOIS details can get a domain
> **suspended**. When you supply a contact you must also pass
> `acceptRegistrantAccuracy: true`.

Two ways to provide it (an explicit arg wins over env, per field):

1. **Inline arg** on `cf-domain-register`:

   ```json
   {
     "domain": "example.com",
     "confirmText": "REGISTER example.com",
     "maxRegistrationCost": 12.20, "currency": "USD",
     "acceptNonRefundable": true,
     "acceptRegistrantAccuracy": true,
     "registrant": {
       "name": "Ada Lovelace",
       "email": "ada@example.com",
       "phone": "+1.5555550123",
       "street": "12 Analytical Engine Way",
       "city": "London", "state": "LDN",
       "postalCode": "EC1A 1BB", "countryCode": "GB"
     }
   }
   ```

2. **Environment** (so you set it once and reuse it). These are **PII** — put them
   in your **user-level** MCP config/secret store, **never** commit them to a
   project config:

   ```bash
   export CLOUDFLARE_REGISTRANT_NAME="Ada Lovelace"
   export CLOUDFLARE_REGISTRANT_EMAIL="ada@example.com"
   export CLOUDFLARE_REGISTRANT_PHONE="+1.5555550123"   # E.164 with a dot
   export CLOUDFLARE_REGISTRANT_STREET="12 Analytical Engine Way"
   export CLOUDFLARE_REGISTRANT_CITY="London"
   export CLOUDFLARE_REGISTRANT_STATE="LDN"
   export CLOUDFLARE_REGISTRANT_POSTAL_CODE="EC1A 1BB"
   export CLOUDFLARE_REGISTRANT_COUNTRY_CODE="GB"
   # optional: CLOUDFLARE_REGISTRANT_ORG, CLOUDFLARE_REGISTRANT_FAX
   ```

Required fields: `name`, `email`, `phone`, `street`, `city`, `state`,
`postalCode`, `countryCode`. `organization` and `fax` are optional. `phone`/`fax`
must be **E.164 with a dot**: `+{countryCode}.{number}` (e.g. `+1.5555550123`).
`countryCode` is an ISO 3166-1 alpha-2 code (e.g. `US`, `GB`). A **partial**
contact is rejected before any purchase — supply all required fields or none
(none = use the account default). If you supply none and the account has no
default address book entry, Cloudflare rejects the registration.

The contact is sent only to Cloudflare/the registry — jetder-mcp never echoes it
back, and redacts it (along with credentials) from any error message.

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

## DNS proxying & instant SSL (`cf-dns-create` `proxied`)

When you point a domain at a deployment, you want a **valid HTTPS certificate
immediately** — not a browser "your connection is not private" warning while the
origin's certificate provisions. `cf-dns-create` handles this for you:

- **`proxied` defaults to AUTO.** `A`/`AAAA`/`CNAME` records are created **proxied**
  (orange cloud), so Cloudflare's Universal SSL serves a valid edge certificate
  right away. `TXT`/`MX`/other types stay **DNS-only** (only `A`/`AAAA`/`CNAME` can
  be proxied at all).
- Pass `proxied: false` to force a record **DNS-only** (grey cloud) — e.g. a
  record that must resolve straight to the origin. Passing `proxied: true` on a
  non-proxiable type (like `TXT`) is rejected with a clear error.
- If a record already exists but with the **wrong** proxy status (e.g. an old
  DNS-only `A` record), `cf-dns-create` **updates it** to match
  (`proxiedUpdated: true`) instead of silently reporting "already exists".

> ⚠️ **SSL/TLS encryption mode.** Proxying gives the *visitor* a valid Cloudflare
> certificate. Cloudflare → origin uses the zone's **SSL/TLS mode**. While the
> origin still serves a self-signed/placeholder certificate (e.g. right after a
> Jetder deployment, before its Let's Encrypt cert issues), the mode must be
> **Full** or **Automatic** — **Full (strict)** will return a `526` until the
> origin certificate is valid. Domains bought through Cloudflare Registrar default
> to **Automatic SSL/TLS**, which is fine. This server cannot read or set the SSL
> mode (the API token would need Zone Settings scope); set it in the Cloudflare
> dashboard if needed.

## Security

- Use the **narrowest** token scope you need; prefer per-zone over all-zones.
- **Rotate** the token periodically and on any suspected exposure.
- Never commit the token. The server keeps it out of logs and errors.

See also: the main [README](../README.md) and the deploy guide
[docs/DEPLOY.md](./DEPLOY.md).
