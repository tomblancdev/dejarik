package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

// --- an appliance: drawers, pointing, seats, the guard ---------------------

// fakeWolf is the engine's API as the panel uses it, with a little state:
// the paired clients, the pending request, the sessions, and every settings
// or stop call it received.
type fakeWolf struct {
	mu       sync.Mutex
	clients  []map[string]any
	sessions []map[string]any
	pending  bool
	points   []map[string]any
	stops    []string
	next     int
}

func (f *fakeWolf) handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /serverinfo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<root><hostname>wolf</hostname></root>`))
	})
	mux.HandleFunc("GET /api/v1/pair/pending", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reqs := []map[string]string{}
		if f.pending {
			reqs = append(reqs, map[string]string{"pair_secret": "s", "client_ip": "203.0.113.7"})
		}
		ok(w, map[string]any{"success": true, "requests": reqs})
	})
	mux.HandleFunc("POST /api/v1/pair/client", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.pending = false
		f.next++
		f.clients = append(f.clients, map[string]any{"client_id": fmt.Sprint(f.next), "app_state_folder": "hash" + fmt.Sprint(f.next), "settings": map[string]any{"run_uid": 3999, "run_gid": 3000}})
		ok(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /api/v1/clients", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok(w, map[string]any{"success": true, "clients": f.clients})
	})
	mux.HandleFunc("POST /api/v1/clients/settings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.points = append(f.points, body)
		for _, c := range f.clients {
			if c["client_id"] == body["client_id"] {
				c["app_state_folder"] = body["app_state_folder"]
				c["settings"] = body["settings"]
			}
		}
		ok(w, map[string]any{"success": true})
	})
	mux.HandleFunc("POST /api/v1/unpair/client", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		kept := f.clients[:0]
		for _, c := range f.clients {
			if c["client_id"] != body["client_id"] {
				kept = append(kept, c)
			}
		}
		f.clients = kept
		ok(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok(w, map[string]any{"success": true, "sessions": f.sessions})
	})
	mux.HandleFunc("POST /api/v1/sessions/stop", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stops = append(f.stops, body["session_id"])
		kept := f.sessions[:0]
		for _, s := range f.sessions {
			if s["client_id"] != body["session_id"] {
				kept = append(kept, s)
			}
		}
		f.sessions = kept
		ok(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, map[string]any{"success": true, "apps": []map[string]string{{"id": "178625061", "title": "Steam"}, {"id": "261696729", "title": "RetroDECK"}}})
	})
	return mux
}

func session(id, app, ip string, uid int) map[string]any {
	return map[string]any{"client_id": id, "app_id": app, "client_ip": ip, "video_width": 1920, "video_height": 1080, "video_refresh_rate": 60,
		"client_settings": map[string]any{"run_uid": uid, "run_gid": 3000}}
}

// wolfServer wires a panel to a fake engine, with one person (someone,
// 3001), one more (other, 3002) and the shared drawer of the living room.
func wolfServer(t *testing.T) (http.Handler, *fakeWolf, *arcade.Service, func(time.Time)) {
	t.Helper()
	f := &fakeWolf{pending: true}
	eng := httptest.NewServer(f.handler())
	t.Cleanup(eng.Close)
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
  wolf:
    label: "the appliance"
    wolf:
      probe_url: "`+eng.URL+`/serverinfo"
      api_url: "`+eng.URL+`"
      timeout: 1s
    people:
      someone: { uid: 3001, gid: 3000 }
      other: { uid: 3002, gid: 3000 }
      salon: { label: "le salon", uid: 3100, gid: 3000, shared: true }
    connect:
      host: "203.0.113.23"
      tcp: [47984, 47989, 48010]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
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
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	svc.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	tick := func(t time.Time) { mu.Lock(); now = t; mu.Unlock() }
	s, err := New(c, svc, au, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), f, svc, tick
}

func as(r *http.Request, user, groups string) *http.Request {
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("Remote-User", user)
	r.Header.Set("Remote-Groups", groups)
	return r
}

func post(h http.Handler, path, body string, user, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(r, user, groups))
	return w
}

func TestPairingPointsTheDeviceAtADrawer(t *testing.T) {
	h, f, _, _ := wolfServer(t)

	// a person pairs their own phone: it lands in their drawer, as their uid
	w := post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"my phone"}`, "someone", "players")
	if w.Code != http.StatusCreated {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	if len(f.points) != 1 || f.points[0]["app_state_folder"] != "someone" || f.points[0]["settings"].(map[string]any)["run_uid"].(float64) != 3001 {
		t.Fatalf("the new device must be pointed at someone's drawer: %+v", f.points)
	}

	// a player may not point at the shared drawer, nor at somebody else's
	f.pending = true
	w = post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"tv","for":"salon"}`, "someone", "players")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "shared") {
		t.Fatalf("a player pointing at the shared drawer: %d %s", w.Code, w.Body)
	}
	w = post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"tv","for":"other"}`, "someone", "players")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "admin") {
		t.Fatalf("a player pointing at another's drawer: %d %s", w.Code, w.Body)
	}
	// an admin pairs the TV for the living room
	w = post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"tv","for":"salon"}`, "boss", "admins")
	if w.Code != http.StatusCreated {
		t.Fatalf("admin pair for salon: %d %s", w.Code, w.Body)
	}
	if len(f.points) != 2 || f.points[1]["app_state_folder"] != "salon" || f.points[1]["settings"].(map[string]any)["run_uid"].(float64) != 3100 {
		t.Fatalf("the tv must be pointed at the shared drawer: %+v", f.points)
	}
	// somebody with no drawer is told so in words
	f.pending = true
	w = post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"x"}`, "stranger", "players")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no drawer") {
		t.Fatalf("no drawer: %d %s", w.Code, w.Body)
	}
	// nothing pending is said plainly (the stranger was refused before the
	// engine was touched, so their request is still there: drop it)
	f.pending = false
	w = post(h, "/api/projects/wolf/clients", `{"pin":"1234","device":"x"}`, "someone", "players")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "nothing is asking") {
		t.Fatalf("nothing pending: %d %s", w.Code, w.Body)
	}

	// the lists: a player sees their drawer, an admin everything with owners
	r := httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/clients", nil), "someone", "players"))
	var got struct {
		Clients []arcade.Device `json:"clients"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(got.Clients) != 1 || got.Clients[0].Name != "my phone" || !got.Clients[0].Mine || got.Clients[0].For != "someone" {
		t.Fatalf("someone's list = %+v", got.Clients)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/clients", nil), "boss", "admins"))
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(got.Clients) != 2 {
		t.Fatalf("admin's list = %+v", got.Clients)
	}
	for _, d := range got.Clients {
		if d.Name == "tv" && (!d.Shared || d.For != "salon" || d.By != "boss") {
			t.Fatalf("tv = %+v", d)
		}
	}

	// an admin re-points the tv at a person; a player cannot
	w = post(h, "/api/projects/wolf/clients/2/point", `{"for":"other"}`, "someone", "players")
	if w.Code != http.StatusForbidden {
		t.Fatalf("a player pointing: %d", w.Code)
	}
	w = post(h, "/api/projects/wolf/clients/2/point", `{"for":"other"}`, "boss", "admins")
	if w.Code != http.StatusOK || f.points[len(f.points)-1]["app_state_folder"] != "other" {
		t.Fatalf("admin point: %d %+v", w.Code, f.points)
	}

	// unpair: not yours is refused, yours goes
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodDelete, "/api/projects/wolf/clients/2", nil), "someone", "players"))
	if r.Code != http.StatusForbidden {
		t.Fatalf("unpair another's: %d", r.Code)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodDelete, "/api/projects/wolf/clients/1", nil), "someone", "players"))
	if r.Code != http.StatusNoContent || len(f.clients) != 1 {
		t.Fatalf("unpair own: %d, clients = %+v", r.Code, f.clients)
	}
}

func TestOneDrawerOneOpenSeatOnTheWire(t *testing.T) {
	h, f, svc, tick := wolfServer(t)
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)

	// the TV is in Steam as someone; the panel sees it
	f.sessions = []map[string]any{session("s-tv", "178625061", "203.0.113.10", 3001)}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/seats", nil), "someone", "players"))
	var got struct {
		Seats   []arcade.Seat  `json:"seats"`
		Refusal arcade.Refusal `json:"refusal"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(got.Seats) != 1 || got.Seats[0].App != "Steam" || got.Seats[0].Person != "someone" || !got.Seats[0].Mine {
		t.Fatalf("seats = %+v", got.Seats)
	}

	// the phone opens Steam on the same drawer a while later: closed, in words
	f.mu.Lock()
	f.sessions = append(f.sessions, session("s-phone", "178625061", "203.0.113.11", 3001))
	f.mu.Unlock()
	tick(now.Add(10 * time.Second))
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/seats", nil), "someone", "players"))
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(f.stops) != 1 || f.stops[0] != "s-phone" {
		t.Fatalf("the newer seat must be closed: stops = %v", f.stops)
	}
	if len(got.Seats) != 1 || got.Seats[0].ID != "s-tv" {
		t.Fatalf("the older seat stays: %+v", got.Seats)
	}
	if !strings.Contains(got.Refusal.Words, "Steam is already open") || !strings.Contains(got.Refusal.Words, "203.0.113.10") {
		t.Fatalf("the refusal in words: %+v", got.Refusal)
	}

	// a different app on the same drawer, and another person in the same
	// app, are both fine
	f.mu.Lock()
	f.sessions = append(f.sessions, session("s-retro", "261696729", "203.0.113.11", 3001), session("s-other", "178625061", "203.0.113.12", 3002))
	f.mu.Unlock()
	tick(now.Add(20 * time.Second))
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/seats", nil), "boss", "admins"))
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(f.stops) != 1 || len(got.Seats) != 3 {
		t.Fatalf("stops = %v, seats = %+v", f.stops, got.Seats)
	}
	// a player does not see another person's seat — nor their refusal
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/seats", nil), "other", "players"))
	got.Seats, got.Refusal = nil, arcade.Refusal{}
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if len(got.Seats) != 1 || got.Seats[0].Person != "other" || got.Refusal.Words != "" {
		t.Fatalf("other's view = %+v / %+v", got.Seats, got.Refusal)
	}

	// closing a seat: not yours is refused; an admin may; the metric counts
	w := post(h, "/api/projects/wolf/seats/s-other/stop", "", "someone", "players")
	if w.Code != http.StatusForbidden {
		t.Fatalf("stop another's: %d", w.Code)
	}
	w = post(h, "/api/projects/wolf/seats/s-other/stop", "", "boss", "admins")
	if w.Code != http.StatusNoContent || f.stops[len(f.stops)-1] != "s-other" {
		t.Fatalf("admin stop: %d %v", w.Code, f.stops)
	}
	m := svc.Metrics(context.Background())
	if !strings.Contains(m, `dejarik_seat_refusals_total{project="wolf"} 1`) || !strings.Contains(m, `dejarik_seat_stops_total{project="wolf"} 1`) {
		t.Fatalf("metrics:\n%s", m)
	}
}

func TestHandStartedHasNoButton(t *testing.T) {
	h, _, _, _ := wolfServer(t)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf", nil), "someone", "players"))
	var v arcade.View
	_ = json.Unmarshal(r.Body.Bytes(), &v)
	if !v.HandStarted || v.State != arcade.Ready || v.Engine != "wolf" {
		t.Fatalf("view = %+v", v)
	}
	// on: play is a no-op that answers
	w := post(h, "/api/projects/wolf/play", "", "someone", "players")
	if w.Code != http.StatusOK {
		t.Fatalf("play while on: %d %s", w.Code, w.Body)
	}
	// the page renders the appliance's words, not a console's
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/", nil), "boss", "admins"))
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "open seats") || !strings.Contains(r.Body.String(), "le salon (shared)") {
		t.Fatalf("page: %d\n%s", r.Code, r.Body.String())
	}
}
