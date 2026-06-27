# Getting started — for newcomers

Welcome! This is a friendly, start-from-zero guide. You don't need to know Jetder,
Cloudflare, or GitHub — just follow the steps.

## What is this?

**jetder-mcp** lets you **deploy apps and point domains at them by chatting with an
AI agent** (Claude, Cursor, etc.). You say "deploy this app" or "buy this domain and
point it at my app," and the agent does it for you — no dashboards, no copy-pasting
API commands.

## What you need first

- **A Jetder API token.** This is your key to deploy. You get it from the owner —
  request one at **<https://thunder.in.th/>**. (Two values: a *service-account email*
  and a *token*.)
- **An AI client** that supports MCP — **Claude Desktop**, **Claude Code**, **Cursor**,
  or **VS Code**. (If you have Claude Desktop, that's the easiest.)

That's it. You do **not** need GitHub to install or connect jetder-mcp, and deploying
an already-built image works without it. (If you later build and push your *own*
private GHCR image, you may need local registry credentials for that — but not for
getting started.) Cloudflare is only needed later **if** you want to buy/manage
domains — you can skip it for now.

## Install in 3 steps

> These are for **Claude Code** (the terminal one). For **Claude Desktop** one-click,
> see the [README](../README.md#claude-desktop) — download the `.mcpb` file and it
> walks you through it.

**1. Install the program.** Open a terminal and paste:

```sh
curl -fsSL https://raw.githubusercontent.com/lambogreny/jetder-mcp/main/install.sh | sh
```

(macOS or Linux. It downloads the program safely and **prints where it installed it**
— usually `~/.local/bin` or `/usr/local/bin`.)

**2. Connect it to Claude Code.** Paste this, replacing the two `<...>` parts with the
email and token you got from the owner:

```sh
claude mcp add -e JETDER_AUTH_USER=<your-service-account-email> \
  -e JETDER_TOKEN=<your-token> --scope user jetder-mcp -- jetder-mcp
```

**3. Done.** Restart Claude Code. The Jetder tools are now available to the agent.

## Your first time

**Check you're ready.** In the chat, just say:

> Run check-setup

The agent runs a quick health check and tells you what's ready (auth, project,
Cloudflare, etc.) and what's missing — with a fix for anything that isn't right. If
it says you need access, contact the owner at <https://thunder.in.th/>.

**Then try something real**, in plain words:

- *"Deploy the image `ghcr.io/me/my-app:latest` as a service called `my-app`."*
- *"What deployments do I have?"*
- *"Buy the domain `example.com` and point it at my-app."* (needs Cloudflare — see below)

The guided flow stops and asks before paid domain registration, and destructive
tools are marked so your client can ask you to confirm them. Still, **review each
tool call before you approve it** — you're in control.

## If you get stuck

- **"project required" / "no access"** — you need a Jetder token and a project.
  Get them from the owner: <https://thunder.in.th/>.
- **The agent can't find the tools** — make sure step 2 ran without errors, then
  restart your AI client. If `jetder-mcp` isn't found, use the full path the
  installer printed (e.g. `~/.local/bin/jetder-mcp`) in the command.
- **Buying/pointing a domain** — that needs a Cloudflare API token + account ID. See
  **[CLOUDFLARE-SETUP.md](./CLOUDFLARE-SETUP.md)** for a step-by-step walkthrough.
- **Want every setting?** The full list of environment variables is in
  **[CREDENTIALS.md](./CREDENTIALS.md)**.
- **Still stuck?** Contact the owner: <https://thunder.in.th/>.

---

More depth when you want it: the [README](../README.md) (overview + every client),
[GETTING-STARTED.md](./GETTING-STARTED.md) (a longer beginner guide), and
[ARCHITECTURE.md](./ARCHITECTURE.md) (how it all fits together, with diagrams).
