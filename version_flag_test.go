package main

import (
	"bytes"
	"strings"
	"testing"
)

// --version / -version / -v print "jetder-mcp <version>" and report handled=true.
func TestHandleVersionFlag_Prints(t *testing.T) {
	for _, arg := range []string{"--version", "-version", "-v"} {
		var buf bytes.Buffer
		if !handleVersionFlag([]string{arg}, &buf) {
			t.Fatalf("%s: handled should be true", arg)
		}
		got := strings.TrimSpace(buf.String())
		want := serverName + " " + versionString()
		if got != want {
			t.Fatalf("%s: printed %q, want %q", arg, got, want)
		}
	}
}

// A normal launch (no version flag) is NOT handled — the server starts as usual.
func TestHandleVersionFlag_NoFlag(t *testing.T) {
	var buf bytes.Buffer
	if handleVersionFlag([]string{}, &buf) {
		t.Fatal("no args: must not be handled")
	}
	if handleVersionFlag([]string{"--something-else"}, &buf) {
		t.Fatal("unrelated arg: must not be handled")
	}
	if buf.Len() != 0 {
		t.Fatalf("nothing should be printed, got %q", buf.String())
	}
}

// versionString falls back to "dev" when the injected version is empty — never
// panics, never returns an empty string.
func TestVersionString_EmptyFallback(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })

	for _, empty := range []string{"", "   "} {
		serverVersion = empty
		if got := versionString(); got != "dev" {
			t.Fatalf("empty version %q → %q, want dev", empty, got)
		}
		// And the flag output uses the fallback.
		var buf bytes.Buffer
		handleVersionFlag([]string{"--version"}, &buf)
		if got := strings.TrimSpace(buf.String()); got != serverName+" dev" {
			t.Fatalf("empty version flag output = %q, want %q dev", got, serverName)
		}
	}
}

// A non-empty version is reported verbatim.
func TestVersionString_NonEmpty(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })
	serverVersion = "v9.9.9"
	if got := versionString(); got != "v9.9.9" {
		t.Fatalf("versionString = %q, want v9.9.9", got)
	}
}
