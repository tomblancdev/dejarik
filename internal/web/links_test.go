package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tomblancdev/dejarik/internal/links"
)

// fromAppliance is the watcher's report: from the appliance's address, JSON.
func fromAppliance(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/projects/wolf/links/sync", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.23:40001"
	return r
}

type syncJSON struct {
	Pending []struct{ Sidecar, Drawer, Token string } `json:"pending"`
	Unlink  []struct{ Sidecar, Drawer string }        `json:"unlink"`
}

func TestLinkStartIsYourOwnDrawerOrAnAdmins(t *testing.T) {
	h, _, _, _ := wolfServer(t)
	// someone, for their own drawer: sent to Spotify with PKCE and the state
	r := httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "someone", "players"))
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "accounts.spotify.com" {
		t.Fatalf("not sent to spotify: %v %s", err, w.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("client_id") != "abc123" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://panel.example.com/links/callback" || q.Get("scope") != "streaming" || q.Get("state") == "" {
		t.Fatalf("the authorize url: %s", loc)
	}
	// someone, for somebody else's drawer: refused
	r = httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=other", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "someone", "players"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("somebody else's drawer: %d", w.Code)
	}
	// the shared drawer: an admin only
	r = httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=salon", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "someone", "players"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("the shared drawer by a player: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=salon", nil), "boss", "admins"))
	if w.Code != http.StatusFound {
		t.Fatalf("the shared drawer by an admin: %d %s", w.Code, w.Body.String())
	}
	// nothing of that name
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/deezer/start", nil), "someone", "players"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("an unknown link: %d", w.Code)
	}
}

func TestTheApplianceReportsAndTakesWhatIsPending(t *testing.T) {
	h, _, _, _ := wolfServer(t)
	// the lock: anybody else is refused, the strongest identity included
	r := httptest.NewRequest(http.MethodPost, "/api/projects/wolf/links/sync", strings.NewReader(`{"linked":{}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "boss", "admins"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("the proxy reached the sync: %d %s", w.Code, w.Body.String())
	}
	// the appliance reports: nobody linked, nothing pending
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(`{"linked":{"spotify":[]}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	var got syncJSON
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Pending) != 0 || len(got.Unlink) != 0 {
		t.Fatalf("something pending out of nowhere: %s", w.Body.String())
	}
	// the panel says "not linked" now that the appliance has spoken
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/links", nil), "someone", "players"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"word":"not linked"`) || strings.Contains(w.Body.String(), `"drawer":"other"`) {
		t.Fatalf("my links: %d %s", w.Code, w.Body.String())
	}
}

func TestATokenIsHandedOnceAndNeverShownToPeople(t *testing.T) {
	s, _, _, _ := wolfServerS(t)
	// a token parked for someone (the callback's doing)
	s.hub.Add(links.Pending{Project: "wolf", Sidecar: "spotify", Drawer: "someone", Token: "t0k3n", By: "someone", Expires: time.Now().Add(time.Hour)})
	h := s.Handler()
	// people see "linking", never the token
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/links", nil), "someone", "players"))
	if !strings.Contains(w.Body.String(), `"word":"linking"`) || strings.Contains(w.Body.String(), "t0k3n") {
		t.Fatalf("my links while pending: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/", nil), "someone", "players"))
	if strings.Contains(w.Body.String(), "t0k3n") {
		t.Fatal("the token leaked onto the page")
	}
	// the appliance takes it, once
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(`{"linked":{"spotify":[]}}`))
	var got syncJSON
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Pending) != 1 || got.Pending[0].Token != "t0k3n" || got.Pending[0].Drawer != "someone" || got.Pending[0].Sidecar != "spotify" {
		t.Fatalf("handed: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(`{"linked":{"spotify":["someone"]}}`))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Pending) != 0 {
		t.Fatalf("handed twice: %s", w.Body.String())
	}
	// and now the report says linked; an unlink from the panel is queued and handed
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/links", nil), "someone", "players"))
	if !strings.Contains(w.Body.String(), `"word":"linked"`) {
		t.Fatalf("after the report: %s", w.Body.String())
	}
	form := url.Values{"link": {"spotify"}, "for": {"someone"}}
	r := httptest.NewRequest(http.MethodPost, "/unlink/wolf", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "someone", "players"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("unlink: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(`{"linked":{"spotify":["someone"]}}`))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Unlink) != 1 || got.Unlink[0].Drawer != "someone" {
		t.Fatalf("the unlink was not handed: %s", w.Body.String())
	}
}

func TestTheFoyerShowsTheLockerToAKnownGuestOnly(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3999)}
	f.mu.Unlock()
	// a known guest: a card with the code's path
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/state", seat("111")))
	if !strings.Contains(w.Body.String(), `"sidecar":"spotify"`) || !strings.Contains(w.Body.String(), `"qr":"/foyer/wolf/links/spotify/qr"`) {
		t.Fatalf("the locker for a known guest: %s", w.Body.String())
	}
	// its code is a PNG that points the phone at the panel, for THIS drawer
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/links/spotify/qr", seat("111")))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("the code: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	// nobody's device: no card, no code
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/state", seat("222")))
	if strings.Contains(w.Body.String(), `"sidecar":"spotify"`) {
		t.Fatalf("a locker for nobody: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/links/spotify/qr", seat("222")))
	if w.Code != http.StatusForbidden {
		t.Fatalf("a code for nobody: %d", w.Code)
	}
	// unlink from the stream, for the seat's own drawer
	code, st := verb(h, "111", "links/spotify/unlink")
	if code != http.StatusOK || !strings.Contains(st.Notice, "Unlinking") {
		t.Fatalf("unlink from the foyer: %d %+v", code, st)
	}
}
