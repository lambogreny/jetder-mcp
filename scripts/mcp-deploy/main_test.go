package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinaries compiles the jetder-mcp server and this mcp-deploy helper into a
// temp dir and returns their paths. Skips if the Go toolchain isn't available.
func buildBinaries(t *testing.T) (server, helper string) {
	t.Helper()
	goBin := goTool(t)
	dir := t.TempDir()
	server = filepath.Join(dir, "jetder-mcp")
	helper = filepath.Join(dir, "mcp-deploy")

	// module root is two levels up from scripts/mcp-deploy.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	build := func(out, pkg string) {
		cmd := exec.Command(goBin, "build", "-o", out, pkg)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s failed: %v\n%s", pkg, err, b)
		}
	}
	build(server, ".")
	build(helper, "./scripts/mcp-deploy")
	return server, helper
}

func goTool(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/local/go/bin/go", "go"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	t.Skip("go toolchain not found")
	return ""
}

// fakeJetder returns an httptest server replying with the given JSON envelope.
func fakeJetder(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runHelper(t *testing.T, helper, server, endpoint, token string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{
		"-server", server,
		"-project", "p1", "-location", "l1", "-name", "web",
		"-image", "ghcr.io/lambogreny/app:sha123",
	}, extra...)
	cmd := exec.Command(helper, args...)
	cmd.Env = append(os.Environ(),
		"JETDER_TOKEN="+token,
		"JETDER_ENDPOINT="+endpoint,
		// ensure no hidden defaults influence the run
		"JETDER_DEFAULT_PROJECT=", "JETDER_DEFAULT_LOCATION=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDeploy_Success(t *testing.T) {
	server, helper := buildBinaries(t)
	// upstream deploy returns Empty; the MCP server synthesizes the structured result.
	fake := fakeJetder(t, `{"ok":true,"result":{}}`)

	out, err := runHelper(t, helper, server, fake.URL, "tok-success")
	if err != nil {
		t.Fatalf("helper should exit 0 on success, got err=%v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "deploy accepted") {
		t.Fatalf("expected success message, got:\n%s", out)
	}
}

func TestDeploy_ToolError_ExitsNonZero(t *testing.T) {
	server, helper := buildBinaries(t)
	fake := fakeJetder(t, `{"ok":false,"error":{"message":"api: unauthorized"}}`)

	out, err := runHelper(t, helper, server, fake.URL, "tok-unauth")
	if err == nil {
		t.Fatalf("helper must exit non-zero on tool error; output:\n%s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit, got %v", err)
	}
	if !strings.Contains(out, "error") {
		t.Fatalf("expected error summary, got:\n%s", out)
	}
}

func TestDeploy_TokenNeverPrinted(t *testing.T) {
	server, helper := buildBinaries(t)
	const token = "SUPER-SECRET-CI-TOKEN"
	// error message even echoes the token; helper must not surface it.
	fake := fakeJetder(t, `{"ok":false,"error":{"message":"denied for SUPER-SECRET-CI-TOKEN"}}`)

	out, _ := runHelper(t, helper, server, fake.URL, token)
	if strings.Contains(out, token) {
		t.Fatalf("TOKEN LEAK in helper output:\n%s", out)
	}
}

// TestDeploy_HelperRedactsServerStderr: a stub "server" that prints the token to
// its OWN stderr before any handshake, then exits. The helper must redact the
// token from the connect-failure summary it surfaces (helper-side sanitize, not
// relying on the real server's redaction).
func TestDeploy_HelperRedactsServerStderr(t *testing.T) {
	_, helper := buildBinaries(t)
	const token = "STDERR-LEAK-TOKEN-999"

	// stub server: emit token to stderr, exit 1 (never speaks MCP).
	stub := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"boot error with $JETDER_TOKEN\" 1>&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cmd := exec.Command(helper,
		"-server", stub, "-project", "p1", "-location", "l1", "-name", "web",
		"-image", "img:1", "-timeout", "5s")
	cmd.Env = append(os.Environ(), "JETDER_TOKEN="+token)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure when server can't handshake; output:\n%s", out)
	}
	if strings.Contains(string(out), token) {
		t.Fatalf("TOKEN LEAK from server stderr via helper:\n%s", out)
	}
	if !strings.Contains(string(out), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got:\n%s", out)
	}
}

// TestDeploy_WhitespaceFlagsTrimmed: leading/trailing spaces on args must be
// trimmed so the submitted args equal what verify() compares against the
// server's (trimmed) resolved context — otherwise verify would falsely fail.
func TestDeploy_WhitespaceFlagsTrimmed(t *testing.T) {
	server, helper := buildBinaries(t)
	fake := fakeJetder(t, `{"ok":true,"result":{}}`)

	cmd := exec.Command(helper,
		"-server", server,
		"-project", "  p1  ", "-location", " l1 ", "-name", " web ",
		"-image", " ghcr.io/lambogreny/app:sha ")
	cmd.Env = append(os.Environ(),
		"JETDER_TOKEN=tok", "JETDER_ENDPOINT="+fake.URL,
		"JETDER_DEFAULT_PROJECT=", "JETDER_DEFAULT_LOCATION=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("whitespace flags should succeed after trim, got err=%v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "deploy accepted") {
		t.Fatalf("expected success, got:\n%s", out)
	}
}

func TestDeploy_MissingRequiredFlag(t *testing.T) {
	server, helper := buildBinaries(t)
	cmd := exec.Command(helper, "-server", server, "-project", "p1") // missing location/name/image
	cmd.Env = append(os.Environ(), "JETDER_TOKEN=tok")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for missing flags; output:\n%s", out)
	}
	if !strings.Contains(string(out), "missing required") {
		t.Fatalf("expected missing-required message, got:\n%s", out)
	}
}

func TestServerArgv(t *testing.T) {
	// -server-command split into argv (no shell), overrides -server.
	c := config{server: "./jetder-mcp", serverCmd: "go run mod@v1 -x"}
	argv, err := c.serverArgv()
	if err != nil {
		t.Fatalf("serverArgv: %v", err)
	}
	want := []string{"go", "run", "mod@v1", "-x"}
	if len(argv) != len(want) {
		t.Fatalf("argv=%v want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, argv[i], want[i])
		}
	}

	// falls back to -server binary path when no command.
	c2 := config{server: "./jetder-mcp"}
	argv2, err := c2.serverArgv()
	if err != nil || len(argv2) != 1 || argv2[0] != "./jetder-mcp" {
		t.Fatalf("server path argv=%v err=%v", argv2, err)
	}

	// neither set → error.
	if _, err := (config{}).serverArgv(); err == nil {
		t.Fatal("expected error when neither -server nor -server-command set")
	}
}

// TestDeploy_ServerCommand_Success: launch the real server via -server-command
// (a multi-arg command) instead of -server, against a fake jetder endpoint.
func TestDeploy_ServerCommand_Success(t *testing.T) {
	server, helper := buildBinaries(t)
	fake := fakeJetder(t, `{"ok":true,"result":{}}`)

	// Use a 2-token command ("<server> -- ") to exercise argv splitting; the
	// server ignores unknown trailing args. (Server takes no flags, so keep it
	// to just the path here — splitting is unit-tested separately.)
	cmd := exec.Command(helper,
		"-server-command", server,
		"-project", "p1", "-location", "l1", "-name", "web",
		"-image", "ghcr.io/lambogreny/app:sha")
	cmd.Env = append(os.Environ(),
		"JETDER_TOKEN=tok", "JETDER_ENDPOINT="+fake.URL,
		"JETDER_DEFAULT_PROJECT=", "JETDER_DEFAULT_LOCATION=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("-server-command deploy should succeed, err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "deploy accepted") {
		t.Fatalf("expected success, got:\n%s", out)
	}
}

func TestDeploy_MissingToken(t *testing.T) {
	server, helper := buildBinaries(t)
	cmd := exec.Command(helper,
		"-server", server, "-project", "p1", "-location", "l1", "-name", "web", "-image", "img:1")
	// explicitly clear token
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "JETDER_TOKEN=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for missing token; output:\n%s", out)
	}
	if !strings.Contains(string(out), "JETDER_TOKEN") {
		t.Fatalf("expected token-required message, got:\n%s", out)
	}
}
