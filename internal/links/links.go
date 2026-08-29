// Package links — the external accounts a person links to their drawer.
//
// The first is Spotify: a device runs beside every seat of a drawer (Le
// Juke, a companion the appliance starts — Spotify's Web Playback SDK in a
// browser) and needs an hour-long access token, over and over. Making that
// possible was a terminal's job; this makes it a tap on the person's own
// phone, from anywhere. The panel runs the OAuth dance with the house's own
// app at the provider (Authorization Code with PKCE — no secret anywhere),
// the provider sends the person back here with a code, the code becomes a
// refresh token — the GRANT — which is stored WITH THE PERSON, in the
// identity gateway's record of them (a shared drawer, which has no person,
// keeps its grant with the panel). From then on the panel is the broker: it
// refreshes the grant (the provider rotates it; the new one is written
// back) and hands out access tokens to the appliance, and later to any
// program of the house the person agreed to. The appliance keeps nothing.
package links

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Kinds are the providers this program knows how to talk to.
const KindSpotify = "spotify"

// endpoints of a provider: where the person is sent, where codes and
// refresh tokens are traded.
type endpoints struct{ authorize, token string }

var providers = map[string]endpoints{
	KindSpotify: {authorize: "https://accounts.spotify.com/authorize", token: "https://accounts.spotify.com/api/token"},
}

// ErrNotLinked: the drawer holds no grant for that link.
var ErrNotLinked = errors.New("not linked")

// Start is a dance in progress: who asked, for which drawer, and the PKCE
// verifier the callback needs. Kept ten minutes.
type Start struct {
	Project, Sidecar, Drawer, By, Verifier string
	Shared                                 bool
	At                                     time.Time
}

// Shelf is where a SHARED drawer's grant lives — it has no person behind
// it, so no record at the gateway; the panel's own store keeps it.
type Shelf interface {
	SetGrant(key string, g Grant) error
	GetGrant(key string) (Grant, bool)
	DeleteGrant(key string) error
}

// Status is one drawer's link, in the words the card needs.
type Status struct {
	Linked bool      `json:"linked"`
	Since  time.Time `json:"since,omitempty"`
	By     string    `json:"by,omitempty"`
}

// Word is the status in one word.
func (s Status) Word() string {
	if s.Linked {
		return "linked"
	}
	return "not linked"
}

// Token is an access token and when it stops being one.
type Token struct {
	Access  string
	Expires time.Time
}

// Hub holds the dances, the broker's cache of access tokens, and the two
// places grants live.
type Hub struct {
	mu     sync.Mutex
	now    func() time.Time
	starts map[string]Start
	tokens map[string]Token
	vault  Vault
	shelf  Shelf
	http   *http.Client
	log    *slog.Logger
	// tokenURL overrides a provider's token endpoint (tests).
	tokenURL map[string]string

	// counters, for /metrics
	Started, Linked, Unlinked, Refreshed, Handed int
}

// New makes a Hub over a vault (people) and a shelf (shared drawers).
func New(vault Vault, shelf Shelf, log *slog.Logger) *Hub {
	return &Hub{
		now:      time.Now,
		starts:   map[string]Start{},
		tokens:   map[string]Token{},
		vault:    vault,
		shelf:    shelf,
		http:     &http.Client{Timeout: 15 * time.Second},
		log:      log,
		tokenURL: map[string]string{},
	}
}

// SetClock is for tests.
func (h *Hub) SetClock(f func() time.Time) { h.now = f }

// SetTokenURL is for tests: where a provider's codes are traded.
func (h *Hub) SetTokenURL(kind, u string) { h.tokenURL[kind] = u }

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is PKCE's S256: the verifier hashed, base64url.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// expire drops stale dances. With the lock held.
func (h *Hub) expire() {
	now := h.now()
	for k, st := range h.starts {
		if now.Sub(st.At) > 10*time.Minute {
			delete(h.starts, k)
		}
	}
}

// Begin opens a dance: the state the provider echoes back, the verifier the
// callback trades with the code.
func (h *Hub) Begin(project, sidecar, drawer, by string, shared bool) (state, verifier string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	state, verifier = random(24), random(48)
	h.starts[state] = Start{Project: project, Sidecar: sidecar, Drawer: drawer, By: by, Shared: shared, Verifier: verifier, At: h.now()}
	h.Started++
	return state, verifier
}

// Finish closes a dance by its state, once.
func (h *Hub) Finish(state string) (Start, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	st, ok := h.starts[state]
	if ok {
		delete(h.starts, state)
	}
	return st, ok
}

// AuthorizeURL is where the person is sent.
func AuthorizeURL(kind, clientID, redirect, state, challenge string, scopes []string) (string, error) {
	ep, ok := providers[kind]
	if !ok {
		return "", fmt.Errorf("no provider called %q", kind)
	}
	q := url.Values{
		"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {redirect}, "state": {state},
		"scope": {strings.Join(scopes, " ")}, "code_challenge_method": {"S256"}, "code_challenge": {challenge},
	}
	return ep.authorize + "?" + q.Encode(), nil
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (h *Hub) trade(ctx context.Context, kind string, form url.Values) (tokenReply, error) {
	ep, ok := providers[kind]
	if !ok {
		return tokenReply{}, fmt.Errorf("no provider called %q", kind)
	}
	u := ep.token
	if o := h.tokenURL[kind]; o != "" {
		u = o
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.http.Do(req)
	if err != nil {
		return tokenReply{}, fmt.Errorf("%s did not answer: %w", kind, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var tok tokenReply
	_ = json.Unmarshal(body, &tok)
	if res.StatusCode != http.StatusOK || tok.AccessToken == "" {
		return tokenReply{}, fmt.Errorf("%s refused (%d %s %s)", kind, res.StatusCode, tok.Error, tok.Description)
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	return tok, nil
}

// Exchange trades the code for tokens at the provider (PKCE: the verifier,
// no secret). Returns the access token and its life, and the refresh token
// — the grant.
func (h *Hub) Exchange(ctx context.Context, kind, clientID, redirect, code, verifier string) (Token, string, error) {
	tok, err := h.trade(ctx, kind, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "code_verifier": {verifier},
	})
	if err != nil {
		return Token{}, "", err
	}
	return Token{Access: tok.AccessToken, Expires: h.now().Add(time.Duration(tok.ExpiresIn) * time.Second)}, tok.RefreshToken, nil
}

func key(project, sidecar, drawer string) string { return project + "/" + sidecar + "/" + drawer }

func (h *Hub) get(ctx context.Context, sidecar, drawer string, shared bool, project string) (Grant, bool, error) {
	if shared {
		g, ok := h.shelf.GetGrant(key(project, sidecar, drawer))
		return g, ok, nil
	}
	return h.vault.GetGrant(ctx, drawer, sidecar)
}

func (h *Hub) put(ctx context.Context, sidecar, drawer string, shared bool, project string, g Grant) error {
	if shared {
		return h.shelf.SetGrant(key(project, sidecar, drawer), g)
	}
	return h.vault.SetGrant(ctx, drawer, sidecar, g)
}

// Link stores a grant with its person (or, for a shared drawer, with the
// panel), and keeps the access token that came with it.
func (h *Hub) Link(ctx context.Context, project, sidecar, drawer string, shared bool, g Grant, first Token) error {
	if g.RefreshToken == "" {
		return errors.New("the provider handed no refresh token — nothing to keep")
	}
	var err error
	if shared {
		err = h.shelf.SetGrant(key(project, sidecar, drawer), g)
	} else {
		err = h.vault.SetGrant(ctx, drawer, sidecar, g)
	}
	if err != nil {
		return err
	}
	h.mu.Lock()
	if first.Access != "" {
		h.tokens[key(project, sidecar, drawer)] = first
	}
	h.Linked++
	h.mu.Unlock()
	return nil
}

// Unlink forgets a grant, and the token cached from it.
func (h *Hub) Unlink(ctx context.Context, project, sidecar, drawer string, shared bool) error {
	var err error
	if shared {
		err = h.shelf.DeleteGrant(key(project, sidecar, drawer))
	} else {
		err = h.vault.DeleteGrant(ctx, drawer, sidecar)
	}
	if err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.tokens, key(project, sidecar, drawer))
	h.Unlinked++
	h.mu.Unlock()
	return nil
}

// Status is one drawer's link.
func (h *Hub) Status(ctx context.Context, project, sidecar, drawer string, shared bool) Status {
	g, ok, err := h.get(ctx, sidecar, drawer, shared, project)
	if err != nil {
		h.log.Warn("the vault did not answer", "drawer", drawer, "link", sidecar, "err", err)
	}
	if !ok {
		return Status{}
	}
	return Status{Linked: true, Since: g.Since, By: g.By}
}

// AccessToken is the broker's verb: a token good for a while, from the
// cache or from a refresh. The provider rotates the refresh token on use;
// the new one is written back where the grant lives.
func (h *Hub) AccessToken(ctx context.Context, project, sidecar, drawer string, shared bool) (Token, error) {
	k := key(project, sidecar, drawer)
	h.mu.Lock()
	t, cached := h.tokens[k]
	now := h.now()
	h.mu.Unlock()
	if cached && t.Expires.After(now.Add(5*time.Minute)) {
		h.mu.Lock()
		h.Handed++
		h.mu.Unlock()
		return t, nil
	}
	g, ok, err := h.get(ctx, sidecar, drawer, shared, project)
	if err != nil {
		return Token{}, err
	}
	if !ok {
		return Token{}, ErrNotLinked
	}
	kind := KindSpotify
	tok, err := h.trade(ctx, kind, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.RefreshToken}, "client_id": {g.ClientID}})
	if err != nil {
		return Token{}, err
	}
	t = Token{Access: tok.AccessToken, Expires: now.Add(time.Duration(tok.ExpiresIn) * time.Second)}
	if tok.RefreshToken != "" && tok.RefreshToken != g.RefreshToken {
		g.RefreshToken = tok.RefreshToken
		if err := h.put(ctx, sidecar, drawer, shared, project, g); err != nil {
			h.log.Error("the rotated grant could not be written back — the next refresh may fail", "drawer", drawer, "link", sidecar, "err", err)
		}
	}
	h.mu.Lock()
	h.tokens[k] = t
	h.Refreshed++
	h.Handed++
	h.mu.Unlock()
	return t, nil
}

// Counters are the tallies for /metrics.
func (h *Hub) Counters() (started, linked, unlinked, refreshed, handed int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Started, h.Linked, h.Unlinked, h.Refreshed, h.Handed
}
