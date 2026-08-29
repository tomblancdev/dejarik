package links

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type shelf struct {
	mu sync.Mutex
	m  map[string]Grant
}

func newShelf() *shelf { return &shelf{m: map[string]Grant{}} }
func (s *shelf) SetGrant(k string, g Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = g
	return nil
}
func (s *shelf) GetGrant(k string) (Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.m[k]
	return g, ok
}
func (s *shelf) DeleteGrant(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPKCEIsTheStandardShape(t *testing.T) {
	h := New(NewMemoryVault(), newShelf(), quiet())
	state, verifier := h.Begin("wolf", "spotify", "someone", "someone", false)
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
	st, ok := h.Finish(state)
	if !ok || st.Drawer != "someone" || st.Verifier != verifier {
		t.Fatalf("finish: %+v %v", st, ok)
	}
	if _, ok := h.Finish(state); ok {
		t.Fatal("a state was finished twice")
	}
}

func TestADanceExpiresInTenMinutes(t *testing.T) {
	h := New(NewMemoryVault(), newShelf(), quiet())
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })
	state, _ := h.Begin("wolf", "spotify", "someone", "someone", false)
	now = now.Add(11 * time.Minute)
	if _, ok := h.Finish(state); ok {
		t.Fatal("a stale state was accepted")
	}
}

// provider fakes Spotify's token endpoint: a code trades for tokens, a
// refresh rotates the refresh token, anything else is refused.
func provider(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	refreshes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		switch {
		case f.Get("grant_type") == "authorization_code" && f.Get("code") == "c0de" && f.Get("code_verifier") != "" && f.Get("client_id") == "abc" && f.Get("client_secret") == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-1", "refresh_token": "ref-1", "expires_in": 3600})
		case f.Get("grant_type") == "refresh_token" && strings.HasPrefix(f.Get("refresh_token"), "ref-") && f.Get("client_id") == "abc":
			refreshes++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-" + f.Get("refresh_token"), "refresh_token": "ref-rotated", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "bad"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &refreshes
}

func TestTheGrantLivesWithThePersonAndTheBrokerRefreshesIt(t *testing.T) {
	srv, refreshes := provider(t)
	vault, sh := NewMemoryVault(), newShelf()
	h := New(vault, sh, quiet())
	h.SetTokenURL(KindSpotify, srv.URL)
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })
	ctx := context.Background()

	if s := h.Status(ctx, "wolf", "spotify", "someone", false); s.Linked || s.Word() != "not linked" {
		t.Fatalf("before: %+v", s)
	}
	if _, err := h.AccessToken(ctx, "wolf", "spotify", "someone", false); err != ErrNotLinked {
		t.Fatalf("a token for an unlinked drawer: %v", err)
	}
	// the dance: the code trades for tokens, the grant lands with the person
	tok, refresh, err := h.Exchange(ctx, KindSpotify, "abc", "https://panel.example.com/links/callback", "c0de", "v3rif13r")
	if err != nil || tok.Access != "acc-1" || refresh != "ref-1" {
		t.Fatalf("exchange: %+v %q %v", tok, refresh, err)
	}
	if err := h.Link(ctx, "wolf", "spotify", "someone", false, Grant{RefreshToken: refresh, ClientID: "abc", Scopes: []string{"streaming"}, Since: now, By: "someone"}, tok); err != nil {
		t.Fatal(err)
	}
	if g, ok, _ := vault.GetGrant(ctx, "someone", "spotify"); !ok || g.RefreshToken != "ref-1" {
		t.Fatalf("the vault: %+v %v", g, ok)
	}
	if s := h.Status(ctx, "wolf", "spotify", "someone", false); !s.Linked || s.By != "someone" {
		t.Fatalf("after: %+v", s)
	}
	// the first token is served from the cache, no refresh
	if got, err := h.AccessToken(ctx, "wolf", "spotify", "someone", false); err != nil || got.Access != "acc-1" || *refreshes != 0 {
		t.Fatalf("cached: %+v %v refreshes=%d", got, err, *refreshes)
	}
	// near expiry: a refresh, and the rotated grant written back
	now = now.Add(58 * time.Minute)
	got, err := h.AccessToken(ctx, "wolf", "spotify", "someone", false)
	if err != nil || got.Access != "acc-ref-1" || *refreshes != 1 {
		t.Fatalf("refreshed: %+v %v refreshes=%d", got, err, *refreshes)
	}
	if g, _, _ := vault.GetGrant(ctx, "someone", "spotify"); g.RefreshToken != "ref-rotated" {
		t.Fatalf("the rotated grant was not written back: %+v", g)
	}
	// a shared drawer keeps its grant with the panel, not the gateway
	if err := h.Link(ctx, "wolf", "spotify", "salon", true, Grant{RefreshToken: "ref-salon", ClientID: "abc"}, Token{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sh.GetGrant("wolf/spotify/salon"); !ok {
		t.Fatal("the shared drawer's grant is not on the shelf")
	}
	if _, ok, _ := vault.GetGrant(ctx, "salon", "spotify"); ok {
		t.Fatal("a shared drawer's grant reached the vault")
	}
	// unlink forgets the grant and the cached token
	if err := h.Unlink(ctx, "wolf", "spotify", "someone", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AccessToken(ctx, "wolf", "spotify", "someone", false); err != ErrNotLinked {
		t.Fatalf("after unlink: %v", err)
	}
	if _, _, err := h.Exchange(ctx, KindSpotify, "abc", "x", "wrong", "v"); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("a refused code did not fail: %v", err)
	}
}
