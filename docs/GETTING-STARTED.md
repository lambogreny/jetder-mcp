# Getting started — from zero to a deployed app

This is a **beginner** guide. It assumes you have **no tools installed** and have
never used MCP. By the end you'll have an app running on [Jetder](https://jetder.com),
deployed by talking to an AI assistant.

> jetder-mcp is an **MCP server**: a small program your AI client (Claude, Cursor,
> VS Code, …) runs in the background so the AI can call real Jetder/Cloudflare
> actions ("deploy my app", "point my domain") on your behalf.

---

## Part 0 — Install the tools

You need **Go** (to run the server) and **git**. For the local image build/deploy
path you also need **Docker** and the **GitHub CLI (`gh`)**.

### macOS (Homebrew)
```sh
brew install go git gh
# Docker Desktop: https://www.docker.com/products/docker-desktop/
```

### Linux (Debian/Ubuntu)
```sh
sudo apt update && sudo apt install -y golang git
# gh:    https://github.com/cli/cli/blob/trunk/docs/install_linux.md
# docker: https://docs.docker.com/engine/install/
```

### Windows
- Install **WSL2** + Ubuntu, then follow the Linux steps inside WSL, **or**
- Install Go (go.dev/dl), Git for Windows, GitHub CLI, and Docker Desktop.

Verify:
```sh
go version && git --version && gh --version && docker --version
```

---

## Part 1 — Get your credentials

You authenticate to Jetder (always) and optionally to Cloudflare (for DNS/domains).

### Jetder (required)
Jetder uses **HTTP Basic auth**: a service-account email + an API token.

- `JETDER_AUTH_USER` — your service-account email, e.g.
  `ai-dev@<project>.serviceaccount.jetder.com`.
- `JETDER_TOKEN` — the API token (the "password").

Don't have access yet? Request a token and a project from the owner:
**<https://thunder.in.th/>**.

Once you have access, get the credentials from the Jetder console (Service
Accounts → create/select → create key). The service account needs an
**owner/deployer role** on your project. Note your **project sid** (e.g.
`dev-lambogreny`) and a **location** (e.g. `cluster-1`; see the `location-list`
tool).

### Cloudflare (optional — only for DNS / buying domains)
See **[docs/CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md)** for the step-by-step. In
short: create an API token (Zone:DNS Edit, Zone:Read, Account:Registrar Domains
Edit) → `CLOUDFLARE_API_TOKEN`; find your Account ID → `CLOUDFLARE_ACCOUNT_ID`.

### GitHub (optional — only for the private-image deploy path)
The private-image path uses **two separate** classic PATs (don't mix them — details
and the exact steps are in Part 3B):

- **pull** PAT — `read:packages`. Given to Jetder **once** so it can pull your
  private image. Not an MCP env var.
  Pre-filled link: <https://github.com/settings/tokens/new?scopes=read:packages&description=jetder-mcp-pull>
- **push** PAT — `write:packages`. Used by `docker login` on **your machine** to push
  the image. **Stays local — never goes to Jetder.**
  Pre-filled link: <https://github.com/settings/tokens/new?scopes=write:packages&description=jetder-mcp-push>

(GitHub requires you to click **Generate** yourself — the links only pre-select the
scope and name so you can't accidentally over-scope.)

---

## Part 2 — Connect the MCP server

Install the server:
```sh
go install github.com/lambogreny/jetder-mcp@latest   # provides the `jetder-mcp` command
```

MCP clients are configured with a small JSON block listing the server command and
its environment. The exact key differs by client — see below.

> ⚠️ **Where to put secrets:** prefer your client's **global/user** config for real
> tokens. If you use a **project** file (`.cursor/mcp.json`, `.vscode/mcp.json`,
> `.env`), it lives in your repo — add it to `.gitignore` and **never commit
> tokens/PATs**. VS Code can prompt for secrets via `inputs` (below) so they're
> not stored in the file at all.

### Claude Desktop / Cursor — `mcpServers`

Minimal (Jetder only):
```json
{
  "mcpServers": {
    "jetder": {
      "command": "jetder-mcp",
      "env": {
        "JETDER_AUTH_USER": "ai-dev@your-project.serviceaccount.jetder.com",
        "JETDER_TOKEN": "your-jetder-api-token"
      }
    }
  }
}
```

With Cloudflare (only if you use DNS/domains — otherwise **omit these keys
entirely**; a placeholder value makes the server think Cloudflare is configured):
```json
{
  "mcpServers": {
    "jetder": {
      "command": "jetder-mcp",
      "env": {
        "JETDER_AUTH_USER": "ai-dev@your-project.serviceaccount.jetder.com",
        "JETDER_TOKEN": "your-jetder-api-token",
        "CLOUDFLARE_API_TOKEN": "your-cf-token",
        "CLOUDFLARE_ACCOUNT_ID": "your-cf-account-id"
      }
    }
  }
}
```
- Claude Desktop: `claude_desktop_config.json` (Settings → Developer) or `claude mcp add`.
- Cursor: global Cursor settings, or `.cursor/mcp.json` (gitignore it if it holds secrets).

### VS Code — `servers` (note: different shape)

VS Code uses a top-level `servers` key with `type: "stdio"`, and can prompt for
secrets via `inputs` so they're never written to disk:
```json
{
  "inputs": [
    { "id": "jetderToken", "type": "promptString", "description": "Jetder API token", "password": true }
  ],
  "servers": {
    "jetder": {
      "type": "stdio",
      "command": "jetder-mcp",
      "env": {
        "JETDER_AUTH_USER": "ai-dev@your-project.serviceaccount.jetder.com",
        "JETDER_TOKEN": "${input:jetderToken}"
      }
    }
  }
}
```
Put it in `.vscode/mcp.json`. Add Cloudflare keys only if you use them.

After saving, restart/reload the client. The AI can now call jetder-mcp tools.

> Tip: `JETDER_DEFAULT_PROJECT` / `JETDER_DEFAULT_LOCATION` in `env` let you skip
> repeating project/location in every request.

---

## Part 3 — Deploy your first app

Two ways depending on how your image is built.

### A. You already have a public image (simplest)
Just ask the assistant, e.g.:

> **"Deploy nginx:latest to a deployment called hello in project dev-lambogreny, location cluster-1."**

The AI calls `deployment-deploy`. To get the live URL, ask it to **"show the URL of
the hello deployment"** — it calls `deployment-get` (the deploy call itself returns
status/context, not the URL).

### B. Your own private image (recommended for real apps)
Use the local deploy script from the sample repo
[`jetder-sample1`](https://github.com/lambogreny/jetder-sample1) — it builds, pushes
to **private** GHCR, verifies it's private, and deploys. (The repo may be private;
you'll need GitHub access to clone it, or copy its `scripts/deploy.sh` into your app.)

> 🔑 **Two separate credentials — do not mix them up:**
> | Credential | Scope | Used by | Goes to Jetder? |
> |------------|-------|---------|-----------------|
> | **push** PAT | `write:packages` | your local `docker login` / `deploy.sh` push | **No** — local only |
> | **pull** PAT | `read:packages`  | Jetder, via the `ghcr-pull` pull secret | **Yes** (once, see below) |
>
> The push credential stays on your machine. **Never put the write:packages token
> into Jetder.** Jetder only ever gets the least-privilege read:packages PAT.

First, get the sample and enter it:
```sh
git clone https://github.com/lambogreny/jetder-sample1.git
cd jetder-sample1
```

**GitHub auth for the local build/push** (the script pushes to GHCR and checks the
package is private, so you must be authenticated as the package owner):
```sh
gh auth login                     # log in as the package OWNER (e.g. lambogreny)
docker login ghcr.io -u <owner>   # password = a PAT with write:packages
```
Create the **push** PAT (note the different scope — `write:packages`) here:

> https://github.com/settings/tokens/new?scopes=write:packages&description=jetder-mcp-push

> The bundled `scripts/deploy.sh` uses `OWNER=lambogreny`. If that's not you, edit
> `OWNER`/`APP` in the script (or copy it into your own app) to point at a package
> your account owns — otherwise the visibility check runs against the wrong account.

One-time **pull-secret bootstrap** (so Jetder can pull your private image). First
create the PAT via the pre-filled link (scope `read:packages` + name already set —
click **Generate token**, copy it):

> https://github.com/settings/tokens/new?scopes=read:packages&description=jetder-mcp-pull

Then ask the assistant:

> **"Create a pull secret named ghcr-pull in project dev-lambogreny, location cluster-1,
> server ghcr.io, username `<your-github-username>`, password `<your GHCR PAT>`."**

(The AI calls `pull-secret-create`. The PAT is stored in Jetder; the value is never
echoed back, and later deploys only reference the **name** `ghcr-pull`.)

> 🔐 **Handling the PAT safely:** use a **least-privilege** classic PAT
> (`read:packages` only). Only paste it into a **trusted local** AI client — never
> into shared chats, issues, screenshots, or docs. The server redacts it from
> outputs, but whatever you type may be retained by the client. **Rotate** it if it
> is ever exposed.

Then deploy (from the `jetder-sample1` directory):
```sh
export JETDER_AUTH_USER=... JETDER_TOKEN=... JETDER_PROJECT=dev-lambogreny JETDER_LOCATION=cluster-1
scripts/deploy.sh dev
```

### Point a custom domain
Ask the assistant (it follows the `point-a-domain` prompt):

> **"Buy coolapp-demo.com and point it at my deployment hello."**

The AI checks the price, **asks you to approve the purchase**, registers the domain
via Cloudflare, creates the DNS records, waits for verification, and routes the
domain to your deployment.

---

## Part 4 — Troubleshooting

| Symptom                                   | Likely cause / fix                                              |
|-------------------------------------------|----------------------------------------------------------------|
| `api: unauthorized` on every call         | Wrong/expired `JETDER_TOKEN`, or `JETDER_AUTH_USER` not set (Jetder needs **Basic** auth, not a bearer token). |
| `iam: forbidden`                          | The service account lacks a role on that project — grant it.   |
| `Cloudflare not configured`               | Set `CLOUDFLARE_API_TOKEN` (+ `CLOUDFLARE_ACCOUNT_ID` for domains). |
| Deploy can't pull image / image not found | Image is private and no pull secret — bootstrap `ghcr-pull` (Part 3B) and pass it. |
| `deploy.sh` says package is PUBLIC        | The script refuses to push a public image. Make the GHCR package private, then re-run. |
| The AI doesn't see the tools              | MCP config not loaded — check the file path/JSON and restart the client. |

For deploy/CI details see **[docs/DEPLOY.md](./DEPLOY.md)**; for Cloudflare see
**[docs/CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md)**.
