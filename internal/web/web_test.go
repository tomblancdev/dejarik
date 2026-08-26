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
  trusted_proxies: ["192.0.2.10/32"]
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
      host: "203.0.113.21"
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
	if got := req("192.0.2.99"); got != http.StatusUnauthorized {
		t.Fatalf("forged headers from an untrusted address = %d, want 401", got)
	}
	if got := req("192.0.2.10"); got != http.StatusOK {
		t.Fatalf("headers from the proxy = %d, want 200", got)
	}
}

// The page must render with both truths unreachable — that is exactly when
// somebody is most likely to be looking at it.
func TestPanelRendersWithEverythingDown(t *testing.T) {
	h := testServer(t)
	r := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/", nil)
	q.RemoteAddr = "192.0.2.10:4000"
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
	q.RemoteAddr = "192.0.2.10:4000"
	q.Header.Set("Remote-User", "tom")
	q.Header.Set("Remote-Groups", "admins")
	h.ServeHTTP(r, q)
	if r.Code != http.StatusNotFound {
		t.Fatalf("= %d, want 404", r.Code)
	}
}

// The regression that sent me here: the console panel and the pairing form
// were separate fragments and only the console half was polled, so a wake
// left the form greyed out until the page was reloaded by hand. They must
// come back TOGETHER, from one render, or they can drift apart again.
func TestPolledFragmentCarriesBothPanels(t *testing.T) {
	h := testServer(t)
	r := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/panel/console", nil)
	q.RemoteAddr = "192.0.2.10:4000"
	q.Header.Set("Remote-User", "tom")
	q.Header.Set("Remote-Groups", "admins")
	h.ServeHTTP(r, q)
	if r.Code != http.StatusOK {
		t.Fatalf("= %d", r.Code)
	}
	body := r.Body.String()
	for _, want := range []string{
		`id="project-console"`, // the one polled element
		"hx-trigger=\"every ",  // …and it polls itself
		`class="crt`,           // the console half — its screen, whatever the state
		"pair a device",        // the clients half, in the SAME response
		`hx-preserve="true"`,   // typing survives the swap that follows
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the polled fragment is missing %q — the two halves can drift apart again", want)
		}
	}
	// A preserved node keeps its OLD attributes, so `disabled` must never sit
	// on one: it would freeze at whatever it was when the page first loaded.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `hx-preserve="true"`) && strings.Contains(line, "disabled") {
			t.Fatalf("a preserved input carries `disabled` — it would stay stale forever:\n%s", line)
		}
	}
}
