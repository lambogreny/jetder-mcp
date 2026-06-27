package main

import (
	"regexp"
	"testing"
)

// TestServerVersion_Format guards the default serverVersion against drift: it must
// be a vMAJOR.MINOR.PATCH tag (release builds override it via -ldflags to the exact
// release tag, but the source default must still be a valid, current-looking tag).
func TestServerVersion_Format(t *testing.T) {
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+`).MatchString(serverVersion) {
		t.Fatalf("serverVersion %q is not a vX.Y.Z tag", serverVersion)
	}
}

// TestServerVersion_ReportedOnInitialize confirms the server advertises
// serverVersion as its implementation version over the MCP handshake (so clients
// see the right version). It checks against whatever serverVersion currently is,
// which a release build stamps to the release tag.
func TestServerVersion_ReportedOnInitialize(t *testing.T) {
	a := newTestAdapter(t, `{"ok":true,"result":{}}`, "p", "l")
	cs := connectInMemory(t, a)
	res := cs.InitializeResult()
	if res == nil || res.ServerInfo == nil {
		t.Fatalf("no InitializeResult/ServerInfo")
	}
	if res.ServerInfo.Name != serverName {
		t.Fatalf("server name = %q, want %q", res.ServerInfo.Name, serverName)
	}
	if res.ServerInfo.Version != serverVersion {
		t.Fatalf("server version = %q, want %q", res.ServerInfo.Version, serverVersion)
	}
}
