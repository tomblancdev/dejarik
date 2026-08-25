package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/store"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	c, err := loadFrom(t)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(c.Auth)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := arcade.New(c, st, log)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c, svc, au, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

// loadFrom writes the config out and reads it back, so the test exercises
// the same validation and ordering a real start does.
func loadFrom(t *testing.T) (*config.Config, error) {
	t.Helper()
	p := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(p, []byte(`
listen: ":0"
data_dir: `+t.TempDir()+`
auth:
  trusted_proxies: ["10.0.0.1/32"]
  admin_groups: [admins]
  player_groups: [players]
veilleur:
  url: "http://127.0.0.1:9"
  timeout: 200ms
projects:
  console:
    label: "the console"
    target: console
    wait_minutes: 10
    sunshine:
      probe_url: "http://127.0.0.1:9/serverinfo"
      timeout: 200ms
    connect:
      host: "10.10.50.21"
      tcp: [47989]
`), 0o600); err != nil {
		return nil, err
	}
	return config.Load(p)
}

func TestNobodyGetsNothing(t *testing.T) {
	h := testServer(t)
	for _, path := range []string{"/", "/api/projects", "/api/me", "/panel/console"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401 for an unidentified caller", path, r.Code)
		}
	}
}

// Identity headers are worth nothing unless the proxy itself is trusted —
// otherwise anybody who can reach the container is an admin.
func TestHeadersOnlyFromTheProxy(t *testing.T) {
	h := testServer(t)
	req := func(from string) int {
		r := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		q.RemoteAddr = from + ":4000"
		q.Header.Set("Remote-User", "mallory")
		q.Header.Set("Remote-Groups", "admins")
		h.ServeHTTP(r, q)
		return r.Code
	}
	if got := req("10.0.0.9"); got != http.StatusUnauthorized {
		t.Fatalf("forged headers from an untrusted address = %d, want 401", got)
	}
	if got := req("10.0.0.1"); got != http.StatusOK {
		t.Fatalf("headers from the proxy = %d, want 200", got)
	}
}

// The page must render with both truths unreachable — that is exactly when
// somebody is most likely to be looking at it.
func TestPanelRendersWithEverythingDown(t *testing.T) {
	h := testServer(t)
	r := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/", nil)
	q.RemoteAddr = "10.0.0.1:4000"
	q.Header.Set("Remote-User", "tom")
	q.Header.Set("Remote-Groups", "admins")
	h.ServeHTTP(r, q)
	if r.Code != http.StatusOK {
		t.Fatalf("page = %d", r.Code)
	}
	body := r.Body.String()
	for _, want := range []string{"CAN&#39;T TELL", "Try to wake anyway", "dejarik.css", "logo-animated.svg"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
	if strings.Contains(body, "READY") {
		t.Fatal("the page claimed READY with Sunshine unreachable")
	}
}

func TestUnknownProjectIs404(t *testing.T) {
	h := testServer(t)
	r := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/api/projects/nope", nil)
	q.RemoteAddr = "10.0.0.1:4000"
	q.Header.Set("Remote-User", "tom")
	q.Header.Set("Remote-Groups", "admins")
	h.ServeHTTP(r, q)
	if r.Code != http.StatusNotFound {
		t.Fatalf("= %d, want 404", r.Code)
	}
}
