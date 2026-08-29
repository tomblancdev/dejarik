package links

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Grant is what a person gave with a tap: the refresh token of the house's
// app at the provider, and what it was minted with. It lives WITH THE
// PERSON — in the identity gateway's record of them — so that any program
// of the house may later play their music, read their stats, whatever they
// agreed to, without a second dance. The panel is its broker: it refreshes
// (Spotify rotates the refresh token on use; the new one is written back)
// and hands out hour-long access tokens.
type Grant struct {
	RefreshToken string    `json:"refresh_token"`
	ClientID     string    `json:"client_id"`
	Scopes       []string  `json:"scopes"`
	Since        time.Time `json:"since"`
	By           string    `json:"by"`
}

// Vault is where a person's grants live, keyed by their account name and
// the link's name. The identity gateway in production; memory in tests.
type Vault interface {
	GetGrant(ctx context.Context, username, link string) (Grant, bool, error)
	SetGrant(ctx context.Context, username, link string, g Grant) error
	DeleteGrant(ctx context.Context, username, link string) error
}

// --- authentik: the person's attributes -----------------------------------

// Authentik keeps a grant under the user's attributes: attributes.links.<link>.
// It needs an API token of a service account allowed to view and change
// users — nothing wider.
type Authentik struct {
	base  string
	token string
	http  *http.Client
}

// NewAuthentik makes the client.
func NewAuthentik(base, token string, timeout time.Duration) *Authentik {
	return &Authentik{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: timeout}}
}

func (a *Authentik) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.http.Do(req)
}

type akUser struct {
	PK         int            `json:"pk"`
	Username   string         `json:"username"`
	Attributes map[string]any `json:"attributes"`
}

func (a *Authentik) user(ctx context.Context, username string) (akUser, error) {
	res, err := a.do(ctx, http.MethodGet, "/api/v3/core/users/?username="+url.QueryEscape(username), nil)
	if err != nil {
		return akUser{}, fmt.Errorf("the gateway did not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return akUser{}, fmt.Errorf("the gateway answered %d reading %s", res.StatusCode, username)
	}
	var page struct {
		Results []akUser `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return akUser{}, err
	}
	for _, u := range page.Results {
		if u.Username == username {
			return u, nil
		}
	}
	return akUser{}, fmt.Errorf("no account called %q at the gateway", username)
}

func grantOf(attrs map[string]any, link string) (Grant, bool) {
	links, _ := attrs["links"].(map[string]any)
	raw, ok := links[link]
	if !ok {
		return Grant{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Grant{}, false
	}
	var g Grant
	if err := json.Unmarshal(b, &g); err != nil || g.RefreshToken == "" {
		return Grant{}, false
	}
	return g, true
}

// GetGrant reads attributes.links.<link>.
func (a *Authentik) GetGrant(ctx context.Context, username, link string) (Grant, bool, error) {
	u, err := a.user(ctx, username)
	if err != nil {
		return Grant{}, false, err
	}
	g, ok := grantOf(u.Attributes, link)
	return g, ok, nil
}

func (a *Authentik) patch(ctx context.Context, u akUser, links map[string]any) error {
	attrs := map[string]any{}
	for k, v := range u.Attributes {
		attrs[k] = v
	}
	if len(links) == 0 {
		delete(attrs, "links")
	} else {
		attrs["links"] = links
	}
	res, err := a.do(ctx, http.MethodPatch, fmt.Sprintf("/api/v3/core/users/%d/", u.PK), map[string]any{"attributes": attrs})
	if err != nil {
		return fmt.Errorf("the gateway did not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<12))
		return fmt.Errorf("the gateway answered %d writing %s's attributes: %s", res.StatusCode, u.Username, strings.TrimSpace(string(b)))
	}
	return nil
}

// SetGrant writes attributes.links.<link>, keeping the rest of the
// person's attributes as they are.
func (a *Authentik) SetGrant(ctx context.Context, username, link string, g Grant) error {
	u, err := a.user(ctx, username)
	if err != nil {
		return err
	}
	links, _ := u.Attributes["links"].(map[string]any)
	if links == nil {
		links = map[string]any{}
	}
	links[link] = g
	return a.patch(ctx, u, links)
}

// DeleteGrant removes attributes.links.<link>.
func (a *Authentik) DeleteGrant(ctx context.Context, username, link string) error {
	u, err := a.user(ctx, username)
	if err != nil {
		return err
	}
	links, _ := u.Attributes["links"].(map[string]any)
	if links == nil {
		return nil
	}
	delete(links, link)
	return a.patch(ctx, u, links)
}

// --- memory: tests, and a panel with no gateway configured -----------------

// MemoryVault keeps grants in memory — tests, and the honest fallback when
// no gateway is configured (a restart forgets every link, and the config
// says so).
type MemoryVault struct {
	mu sync.Mutex
	m  map[string]Grant
}

// NewMemoryVault makes one.
func NewMemoryVault() *MemoryVault { return &MemoryVault{m: map[string]Grant{}} }

func (v *MemoryVault) GetGrant(_ context.Context, username, link string) (Grant, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	g, ok := v.m[username+"/"+link]
	return g, ok, nil
}

func (v *MemoryVault) SetGrant(_ context.Context, username, link string, g Grant) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[username+"/"+link] = g
	return nil
}

func (v *MemoryVault) DeleteGrant(_ context.Context, username, link string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, username+"/"+link)
	return nil
}
