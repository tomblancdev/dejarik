// Package auth turns a request into an identity. Dejarik authenticates
// nobody itself: a reverse proxy doing forward-auth (Caddy + Authelia) sets
// the identity headers, and only a trusted proxy may do so. Machines — a
// Home Assistant button, a script somebody wrote — carry bearer tokens, so
// the log names which one asked and one can be revoked alone.
//
// This is the third repetition of the La Loge contract, deliberately.
package auth

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/tomblancdev/dejarik/internal/config"
)

// Role is what an identity may do.
type Role string

const (
	// None is nobody: the request is refused.
	None Role = ""
	// Player may see the projects, ask to play, and manage their own devices.
	Player Role = "player"
	// Admin may do all of that, plus see and unpair everyone's devices.
	Admin Role = "admin"
)

// Identity is the caller.
type Identity struct {
	User   string
	Groups []string
	Role   Role
	Via    string // header | token
}

// IsAdmin is the only privilege question this program asks.
func (i Identity) IsAdmin() bool { return i.Role == Admin }

// Auth resolves identities.
type Auth struct {
	cfg     config.Auth
	proxies []*net.IPNet
	tokens  map[string]token
}

type token struct {
	name string
	role Role
}

// New parses the trusted proxies and the tokens file.
func New(cfg config.Auth) (*Auth, error) {
	a := &Auth{cfg: cfg, tokens: map[string]token{}}
	for _, c := range cfg.TrustedProxies {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies %q: %w", c, err)
		}
		a.proxies = append(a.proxies, n)
	}
	if cfg.TokensFile != "" {
		if err := a.loadTokens(cfg.TokensFile); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// loadTokens reads `name:role:token` lines. The file is a secret: it is
// mounted, never baked, and never logged.
func (a *Auth) loadTokens(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no machine clients is a valid answer
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for ln := 1; sc.Scan(); ln++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return fmt.Errorf("tokens file line %d: want name:role:token", ln)
		}
		r := Role(strings.TrimSpace(parts[1]))
		if r != Player && r != Admin {
			return fmt.Errorf("tokens file line %d: role must be player or admin", ln)
		}
		a.tokens[strings.TrimSpace(parts[2])] = token{name: strings.TrimSpace(parts[0]), role: r}
	}
	return sc.Err()
}

// Identify reads the request. A bearer token wins if present; otherwise the
// proxy's headers are trusted only when the proxy itself is.
func (a *Auth) Identify(r *http.Request) Identity {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if t, ok := a.tokens[strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))]; ok {
			return Identity{User: t.name, Role: t.role, Via: "token"}
		}
		return Identity{}
	}
	if !a.fromTrustedProxy(r) {
		return Identity{}
	}
	user := strings.TrimSpace(r.Header.Get("Remote-User"))
	if user == "" {
		return Identity{}
	}
	var groups []string
	for _, g := range strings.Split(r.Header.Get("Remote-Groups"), ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	id := Identity{User: user, Groups: groups, Via: "header"}
	switch {
	case anyOf(groups, a.cfg.AdminGroups):
		id.Role = Admin
	case anyOf(groups, a.cfg.PlayerGroups):
		id.Role = Player
	}
	return id
}

func (a *Auth) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range a.proxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func anyOf(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}
