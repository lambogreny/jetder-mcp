package jetder

import (
	"errors"
	"net/http"
	"testing"
)

func TestNew_NoToken(t *testing.T) {
	t.Setenv(EnvToken, "")

	a, err := New()
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("New() error = %v, want ErrNoToken", err)
	}
	if a != nil {
		t.Fatalf("New() adapter = %v, want nil on error", a)
	}
}

func TestNew_TrimsWhitespaceToken(t *testing.T) {
	t.Setenv(EnvToken, "   \t ")

	if _, err := New(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New() with whitespace-only token error = %v, want ErrNoToken", err)
	}
}

func TestNew_AuthInjectsBearer(t *testing.T) {
	const token = "secret-token-123"
	t.Setenv(EnvToken, token)

	a, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	auth := a.Client().Auth
	if auth == nil {
		t.Fatal("Client().Auth is nil, want auth hook")
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.test/", nil)
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	auth(req)

	got := req.Header.Get("Authorization")
	want := "Bearer " + token
	if got != want {
		t.Fatalf("Authorization header = %q, want %q", got, want)
	}
}

func TestRedact_NilError(t *testing.T) {
	a := &Adapter{token: "abc"}
	if err := a.Redact(nil); err != nil {
		t.Fatalf("Redact(nil) = %v, want nil", err)
	}
}

func TestRedact_RemovesToken(t *testing.T) {
	const token = "super-secret-token"
	a := &Adapter{token: token}

	in := errors.New("request failed with " + token + " denied")
	out := a.Redact(in)
	if out == nil {
		t.Fatal("Redact returned nil for non-nil error")
	}
	msg := out.Error()
	if contains(msg, token) {
		t.Fatalf("Redact result %q still contains token", msg)
	}
	if !contains(msg, "[REDACTED]") {
		t.Fatalf("Redact result %q missing [REDACTED] marker", msg)
	}
}

func TestRedact_PassthroughWhenNoToken(t *testing.T) {
	a := &Adapter{token: ""}
	in := errors.New("some error")
	if out := a.Redact(in); out != in {
		t.Fatalf("Redact with empty token = %v, want passthrough %v", out, in)
	}
}

func TestRedact_NoTokenInMessage(t *testing.T) {
	a := &Adapter{token: "tok"}
	in := errors.New("unrelated failure")
	if out := a.Redact(in); out != in {
		t.Fatalf("Redact with token absent from message = %v, want original error", out)
	}
}

func TestRedactValues(t *testing.T) {
	a := &Adapter{token: "tok-123"}

	// redacts token + each provided secret; leaves other text.
	in := errors.New("failed: secret=PLAINTEXT and token tok-123 in msg")
	out := a.RedactValues(in, "PLAINTEXT")
	msg := out.Error()
	if contains(msg, "PLAINTEXT") {
		t.Fatalf("value not redacted: %q", msg)
	}
	if contains(msg, "tok-123") {
		t.Fatalf("token not redacted: %q", msg)
	}

	// nil error → nil.
	if a.RedactValues(nil, "x") != nil {
		t.Fatal("RedactValues(nil) should be nil")
	}

	// empty secret ignored, no false changes.
	orig := errors.New("plain error")
	if got := a.RedactValues(orig, ""); got != orig {
		t.Fatalf("empty secret should passthrough original error, got %v", got)
	}

	// secret not present → original returned.
	ne := errors.New("nothing sensitive")
	if got := a.RedactValues(ne, "absent"); got != ne {
		t.Fatalf("absent secret should passthrough, got %v", got)
	}
}

func TestResolveProjectLocation(t *testing.T) {
	a := &Adapter{defaultProject: "dp", defaultLocation: "dl"}

	if got := a.ResolveProject("explicit"); got != "explicit" {
		t.Fatalf("ResolveProject(explicit) = %q, want explicit", got)
	}
	if got := a.ResolveProject("  spaced  "); got != "spaced" {
		t.Fatalf("ResolveProject trims = %q, want spaced", got)
	}
	if got := a.ResolveProject(""); got != "dp" {
		t.Fatalf("ResolveProject(empty) = %q, want default dp", got)
	}
	if got := a.ResolveProject("   "); got != "dp" {
		t.Fatalf("ResolveProject(whitespace) = %q, want default dp", got)
	}
	if got := a.ResolveLocation(""); got != "dl" {
		t.Fatalf("ResolveLocation(empty) = %q, want default dl", got)
	}
	if got := a.ResolveLocation("here"); got != "here" {
		t.Fatalf("ResolveLocation(here) = %q, want here", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
