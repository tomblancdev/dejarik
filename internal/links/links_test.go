package links

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type mem struct{ m map[string]Report }

func (m *mem) SetLinked(project, sidecar string, drawers []string, at time.Time) error {
	m.m[project+"/"+sidecar] = Report{Drawers: drawers, At: at}
	return nil
}
func (m *mem) Linked() map[string]Report { return m.m }

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPKCEIsTheStandardShape(t *testing.T) {
	h := New(nil, quiet())
	state, verifier := h.Begin("wolf", "spotify", "someone", "someone")
	if len(state) < 20 || len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("state %q verifier %q", state, verifier)
	}
	u, err := AuthorizeURL(KindSpotify, "abc", "https://panel.example.com/links/callback", state, Challenge(verifier), []string{"streaming"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://accounts.spotify.com/authorize?", "client_id=abc", "code_challenge_method=S256", "response_type=code", "scope=streaming", "state=" + state} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize url lacks %s: %s", want, u)
		}
	}
	if _, err := AuthorizeURL("deezer", "abc", "x", state, "c", nil); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	// the state is consumed once
	st, ok := h.Finish(state)
	if !ok || st.Drawer != "someone" || st.Verifier != verifier {
		t.Fatalf("finish: %+v %v", st, ok)
	}
	if _, ok := h.Finish(state); ok {
		t.Fatal("a state was finished twice")
	}
}

func TestADanceExpiresInTenMinutes(t *testing.T) {
	h := New(nil, quiet())
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })
	state, _ := h.Begin("wolf", "spotify", "someone", "someone")
	now = now.Add(11 * time.Minute)
	if _, ok := h.Finish(state); ok {
		t.Fatal("a stale state was accepted")
	}
}

func TestExchangeTradesTheCodeWithPKCE(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = map[string]string{}
		for k := range r.PostForm {
			got[k] = r.PostForm.Get(k)
		}
		if got["grant_type"] != "authorization_code" || got["code"] != "c0de" || got["code_verifier"] != "v3rif13r" || got["client_id"] != "abc" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "bad"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t0k3n", "expires_in": 3600})
	}))
	defer srv.Close()
	h := New(nil, quiet())
	h.SetTokenURL(KindSpotify, srv.URL)
	tok, ttl, err := h.Exchange(context.Background(), KindSpotify, "abc", "https://panel.example.com/links/callback", "c0de", "v3rif13r")
	if err != nil || tok != "t0k3n" || ttl != time.Hour {
		t.Fatalf("exchange: %q %v %v", tok, ttl, err)
	}
	if got["client_secret"] != "" {
		t.Fatal("a secret was sent — PKCE needs none")
	}
	if _, _, err := h.Exchange(context.Background(), KindSpotify, "abc", "x", "wrong", "v3rif13r"); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("a refused code did not fail: %v", err)
	}
}

func TestPendingIsHandedOnceAndReportsAreRemembered(t *testing.T) {
	m := &mem{m: map[string]Report{}}
	h := New(m, quiet())
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })

	if h.Status("wolf", "spotify", "someone").Word() != "unknown" {
		t.Fatal("before any report the word must be unknown")
	}
	h.Add(Pending{Project: "wolf", Sidecar: "spotify", Drawer: "someone", Token: "t", Expires: now.Add(time.Hour)})
	if s := h.Status("wolf", "spotify", "someone"); !s.Pending || s.Word() != "linking" {
		t.Fatalf("pending: %+v", s)
	}
	if !h.Waiting("wolf") || h.Waiting("console") {
		t.Fatal("waiting is per project")
	}
	p, u := h.Take("wolf")
	if len(p) != 1 || p[0].Token != "t" || len(u) != 0 {
		t.Fatalf("take: %+v %+v", p, u)
	}
	if p, _ := h.Take("wolf"); len(p) != 0 {
		t.Fatal("a token was handed twice")
	}
	// the appliance reports: someone is linked
	h.Report("wolf", map[string][]string{"spotify": {"someone"}})
	if s := h.Status("wolf", "spotify", "someone"); !s.Linked || !s.Reported || s.Word() != "linked" {
		t.Fatalf("after the report: %+v", s)
	}
	if s := h.Status("wolf", "spotify", "other"); s.Linked || s.Word() != "not linked" {
		t.Fatalf("other: %+v", s)
	}
	// remembered across a restart, through the memory
	h2 := New(m, quiet())
	if s := h2.Status("wolf", "spotify", "someone"); !s.Linked || s.ReportedAt != now {
		t.Fatalf("not remembered: %+v", s)
	}
	// an unlink is queued, handed once, and the report then says so
	h2.QueueUnlink("wolf", "spotify", "someone")
	if s := h2.Status("wolf", "spotify", "someone"); !s.Unlinking || s.Word() != "unlinking" {
		t.Fatalf("unlinking: %+v", s)
	}
	_, u = h2.Take("wolf")
	if len(u) != 1 || u[0].Drawer != "someone" {
		t.Fatalf("unlink handed: %+v", u)
	}
	h2.Report("wolf", map[string][]string{"spotify": {}})
	if s := h2.Status("wolf", "spotify", "someone"); s.Linked || s.Unlinking {
		t.Fatalf("after the unlink: %+v", s)
	}
}

func TestAnExpiredTokenIsNeverHanded(t *testing.T) {
	h := New(nil, quiet())
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })
	h.Add(Pending{Project: "wolf", Sidecar: "spotify", Drawer: "someone", Token: "t", Expires: now.Add(time.Hour)})
	now = now.Add(2 * time.Hour)
	if p, _ := h.Take("wolf"); len(p) != 0 {
		t.Fatal("an expired token was handed")
	}
}
