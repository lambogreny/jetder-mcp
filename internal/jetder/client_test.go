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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
