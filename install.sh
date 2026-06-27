#!/usr/bin/env sh
# jetder-mcp installer — downloads a release binary from GitHub, verifies its
# SHA-256 against the published SHA256SUMS, and installs it to a bin directory.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/lambogreny/jetder-mcp/main/install.sh | sh
#   # or pin a version / choose a dir:
#   JETDER_MCP_VERSION=v1.2.3 INSTALL_DIR=$HOME/.local/bin sh install.sh
#
# Security notes:
#   - Public repo: no token/auth needed.
#   - The binary is verified against SHA256SUMS (also from the release) BEFORE it
#     is made executable or moved into place. A mismatch aborts with no install.
#   - For the security-conscious: download and read this script first, then run it
#     (instead of piping curl straight into sh).
set -eu

REPO="lambogreny/jetder-mcp"
BIN="jetder-mcp"
VERSION="${JETDER_MCP_VERSION:-latest}"

err() { printf 'install: %s\n' "$1" >&2; exit 1; }
info() { printf 'install: %s\n' "$1" >&2; }

# Require the tools we use up front, with a clear message.
need() { command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"; }
need uname
need curl

# A SHA-256 tool: prefer sha256sum, fall back to shasum -a 256.
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  err "need sha256sum or shasum to verify the download"
fi

# --- detect OS / arch ---------------------------------------------------------
os="$(uname -s)"
case "$os" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) err "unsupported OS: $os (supported: Linux, macOS)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) err "unsupported architecture: $arch (supported: amd64, arm64)" ;;
esac

ASSET="${BIN}_${OS}_${ARCH}"
info "platform: ${OS}/${ARCH} -> ${ASSET}"

# --- resolve the release base URL --------------------------------------------
if [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/${REPO}/releases/latest/download"
else
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
fi

# --- download into a private temp dir (cleaned up on exit) --------------------
TMP="$(mktemp -d "${TMPDIR:-/tmp}/jetder-mcp.XXXXXX")" || err "could not create temp dir"
trap 'rm -rf "$TMP"' EXIT INT TERM

info "downloading ${ASSET} (${VERSION})"
curl -fSL --proto '=https' --tlsv1.2 -o "${TMP}/${ASSET}" "${BASE}/${ASSET}" \
  || err "download failed: ${BASE}/${ASSET} (is the version/asset published?)"

info "downloading checksums"
curl -fSL --proto '=https' --tlsv1.2 -o "${TMP}/SHA256SUMS" "${BASE}/SHA256SUMS" \
  || err "could not fetch SHA256SUMS — refusing to install unverified binary"

# --- verify checksum BEFORE making executable or installing ------------------
# SHA256SUMS lines are "<hex>␣␣<filename>" (sha256sum format). Match the asset by
# exact filename (awk field compare, not a regex) to avoid metacharacter issues
# and partial matches (e.g. jetder-mcp_* vs mcp-deploy_*).
want="$(awk -v f="$ASSET" '$2==f {print $1; exit}' "${TMP}/SHA256SUMS")"
[ -n "$want" ] || err "no checksum for ${ASSET} in SHA256SUMS"
# Expect a 64-hex-char SHA-256 — guard against a malformed/empty checksum line.
case "$want" in
  *[!0-9a-fA-F]* | "") err "malformed checksum for ${ASSET} in SHA256SUMS" ;;
esac
[ "${#want}" -eq 64 ] || err "unexpected checksum length for ${ASSET} (not SHA-256)"
got="$(sha256 "${TMP}/${ASSET}")"
if [ "$want" != "$got" ]; then
  err "checksum mismatch for ${ASSET}: expected ${want}, got ${got} — NOT installing"
fi
info "checksum verified"

# --- choose an install dir (no sudo by default) ------------------------------
if [ -n "${INSTALL_DIR:-}" ]; then
  DEST="$INSTALL_DIR"
elif [ -w "/usr/local/bin" ]; then
  DEST="/usr/local/bin"
else
  DEST="${HOME}/.local/bin"
fi
mkdir -p "$DEST" || err "could not create install dir: $DEST"

chmod +x "${TMP}/${ASSET}"
mv "${TMP}/${ASSET}" "${DEST}/${BIN}" || err "could not install to ${DEST} (try INSTALL_DIR=\$HOME/.local/bin)"
info "installed ${BIN} to ${DEST}/${BIN}"

# --- post-install hints -------------------------------------------------------
case ":${PATH}:" in
  *":${DEST}:"*) ;;
  *) info "note: ${DEST} is not on your PATH — add it: export PATH=\"${DEST}:\$PATH\"" ;;
esac

cat >&2 <<EOF

Next steps:
  1. Get a Jetder token from the owner: https://thunder.in.th/
  2. Add jetder-mcp to your MCP client config, e.g.:

     {
       "mcpServers": {
         "jetder": {
           "command": "${DEST}/${BIN}",
           "env": {
             "JETDER_AUTH_USER": "<svc>@<project>.serviceaccount.jetder.com",
             "JETDER_TOKEN": "<your-jetder-api-token>"
           }
         }
       }
     }

  See the README for Cloudflare/domain env vars and per-client config.
EOF
