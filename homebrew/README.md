# Homebrew tap formula

`Formula/jetder-mcp.rb` is the Homebrew formula for installing the prebuilt
`jetder-mcp` release binary (it does **not** build from source).

## Publishing

This file lives in a **separate tap repository**, `lambogreny/homebrew-tap`, at the
path `Formula/jetder-mcp.rb`. Once published there, users install with:

```sh
brew install lambogreny/tap/jetder-mcp
```

(`lambogreny/tap` is Homebrew shorthand for the `lambogreny/homebrew-tap` repo.)
The copy here is the source of truth; copy it into the tap repo when it changes.

## Bumping to a new release

1. Cut the new release (tag `vX.Y.Z`) so its assets + `SHA256SUMS` are published.
2. Fetch the checksums:
   `curl -fsSL https://github.com/lambogreny/jetder-mcp/releases/download/vX.Y.Z/SHA256SUMS`
3. In `Formula/jetder-mcp.rb`, update `version` and every `url` + `sha256`
   (`jetder-mcp_darwin_arm64`, `_darwin_amd64`, `_linux_arm64`, `_linux_amd64`).
4. Copy the updated formula into `lambogreny/homebrew-tap` and push.

The checksums in the committed formula are verified against the release's published
`SHA256SUMS` — keep them in sync on every bump.
