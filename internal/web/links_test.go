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

// spotify fakes the provider's token endpoint: a code trades for tokens, a
// refresh rotates the refresh token.
func spotify(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		switch {
		case f.Get("grant_type") == "authorization_code" && f.Get("code") == "c0de" && f.Get("code_verifier") != "" && f.Get("client_id") == "abc123" && f.Get("client_secret") == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-1", "refresh_token": "ref-1", "expires_in": 3600})
		case f.Get("grant_type") == "refresh_token" && f.Get("refresh_token") != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-2", "refresh_token": "ref-2", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fromAppliance is the watcher asking: from the appliance's address.
func fromAppliance(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.23:40001"
	return r
}

func TestLinkStartIsYourOwnDrawerOrAnAdmins(t *testing.T) {
	h, _, _, _ := wolfServer(t)
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
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=other", nil), "someone", "players"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("somebody else's drawer: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=salon", nil), "someone", "players"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("the shared drawer by a player: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start?for=salon", nil), "boss", "admins"))
	if w.Code != http.StatusFound {
		t.Fatalf("the shared drawer by an admin: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/deezer/start", nil), "someone", "players"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("an unknown link: %d", w.Code)
	}
}

func TestTheDanceStoresTheGrantAndTheApplianceGetsTokens(t *testing.T) {
	s, _, _, _ := wolfServerS(t)
	s.hub.SetTokenURL(links.KindSpotify, spotify(t).URL)
	h := s.Handler()

	// the appliance's two verbs are locked to its address
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/projects/wolf/links/sync", strings.NewReader("{}"))
	h.ServeHTTP(w, as(r, "boss", "admins"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("the proxy reached the sync: %d %s", w.Code, w.Body.String())
	}
	// nobody linked yet: the sync says so, a token is refused
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(http.MethodPost, "/api/projects/wolf/links/sync"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"spotify":[]`) {
		t.Fatalf("sync before: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(http.MethodGet, "/api/projects/wolf/links/spotify/token?drawer=someone"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("a token for an unlinked drawer: %d %s", w.Code, w.Body.String())
	}
	// the dance: start, then the provider sends the person back with the code
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/wolf/spotify/start", nil), "someone", "players"))
	loc, _ := url.Parse(w.Header().Get("Location"))
	state := loc.Query().Get("state")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/callback?code=c0de&state="+state, nil), "someone", "players"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	// a stale or reused state is refused
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/links/callback?code=c0de&state="+state, nil), "someone", "players"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a reused state: %d", w.Code)
	}
	// linked: the card says so, the sync names the drawer, the appliance gets a token — never the grant
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/links", nil), "someone", "players"))
	if !strings.Contains(w.Body.String(), `"word":"linked"`) || strings.Contains(w.Body.String(), "ref-1") || strings.Contains(w.Body.String(), `"drawer":"other"`) {
		t.Fatalf("my links: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(http.MethodPost, "/api/projects/wolf/links/sync"))
	if !strings.Contains(w.Body.String(), `"spotify":["someone"]`) {
		t.Fatalf("sync after: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(http.MethodGet, "/api/projects/wolf/links/spotify/token?drawer=someone"))
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if w.Code != http.StatusOK || tok.AccessToken != "acc-1" || tok.ExpiresIn < 3000 {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	// the page never shows the grant nor the token
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/", nil), "someone", "players"))
	if strings.Contains(w.Body.String(), "ref-1") || strings.Contains(w.Body.String(), "acc-1") {
		t.Fatal("a secret leaked onto the page")
	}
	// unlink from the panel: forgotten at once
	form := url.Values{"link": {"spotify"}, "for": {"someone"}}
	r = httptest.NewRequest(http.MethodPost, "/unlink/wolf", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "someone", "players"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("unlink: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromAppliance(http.MethodGet, "/api/projects/wolf/links/spotify/token?drawer=someone"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("a token after the unlink: %d", w.Code)
	}
	_ = time.Now
}

func TestTheFoyerShowsTheLockerToAKnownGuestOnly(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3999)}
	f.mu.Unlock()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/state", seat("111")))
	if !strings.Contains(w.Body.String(), `"sidecar":"spotify"`) || !strings.Contains(w.Body.String(), `"qr":"/foyer/wolf/links/spotify/qr"`) {
		t.Fatalf("the locker for a known guest: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/links/spotify/qr", seat("111")))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("the code: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
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
	code, st := verb(h, "111", "links/spotify/unlink")
	if code != http.StatusOK || !strings.Contains(st.Notice, "Unlinked") {
		t.Fatalf("unlink from the foyer: %d %+v", code, st)
	}
}
