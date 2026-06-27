# Homebrew formula for jetder-mcp — installs the prebuilt release binary (no build
# from source). Lives in the tap repo `lambogreny/homebrew-tap` at
# `Formula/jetder-mcp.rb`, so users install with:
#
#     brew install lambogreny/tap/jetder-mcp
#
# To bump: update `version`, then update every `url` + `sha256` from the release's
# SHA256SUMS (https://github.com/lambogreny/jetder-mcp/releases/download/v<VER>/SHA256SUMS).
class JetderMcp < Formula
  desc "MCP server exposing the Jetder API to AI agents (deploy, domains, DNS, more)"
  homepage "https://github.com/lambogreny/jetder-mcp"
  version "0.1.1"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/lambogreny/jetder-mcp/releases/download/v0.1.1/jetder-mcp_darwin_arm64"
      sha256 "d2a7cb39c269e9283d205e4aca81bbf842b8811dde035aeda8825942bc2238b4"
    end
    on_intel do
      url "https://github.com/lambogreny/jetder-mcp/releases/download/v0.1.1/jetder-mcp_darwin_amd64"
      sha256 "fa84c896303cece634033118322607b90c565d1dbfaea13d3556820ec0848571"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/lambogreny/jetder-mcp/releases/download/v0.1.1/jetder-mcp_linux_arm64"
      sha256 "b55f09175ab9baf3778fafbb12afdcbb5657805e3dcb0a83cc4ca2ceac2dbb49"
    end
    on_intel do
      url "https://github.com/lambogreny/jetder-mcp/releases/download/v0.1.1/jetder-mcp_linux_amd64"
      sha256 "7b9ca68d2fabab6a23419bd2a999034e0aff04542c8e1109202e7d9ad2e75029"
    end
  end

  def install
    # The release asset is the bare binary named per-arch; install it as `jetder-mcp`.
    bin.install Dir["jetder-mcp_*"].first => "jetder-mcp"
  end

  test do
    # The server speaks MCP over stdio and needs Jetder credentials to do anything
    # real, so just assert the binary is present and executable.
    assert_predicate bin/"jetder-mcp", :exist?
    assert_predicate bin/"jetder-mcp", :executable?
  end
end
