// Package links — the external accounts a person links to their drawer.
//
// The first is Spotify: a receiver runs beside every seat of a drawer (Le
// Juke, a companion the appliance starts) and needs that drawer's Spotify
// credentials once. Making them was a terminal's job; this makes it a tap on
// the person's own phone. The panel runs the OAuth dance with the house's
// own app at the provider (Authorization Code with PKCE — no secret anywhere),
// the provider sends the person back here with a code, the code becomes a
// short-lived access token, and the token waits IN MEMORY for the appliance:
// the appliance reports every few seconds which drawers hold each
// companion's file and takes what is pending — a drawer and its token, or a
// drawer to unlink — over the row the hub tile already has. The token never
// touches a disk here; what the panel remembers is the appliance's last
// report (who is linked, and when it said so), which is the only truth about
// a link there is.
package links

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kinds are the providers this program knows how to talk to.
const KindSpotify = "spotify"

// endpoints of a provider: where the person is sent, where the code is traded.
type endpoints struct{ authorize, token string }

var providers = map[string]endpoints{
	KindSpotify: {authorize: "https://accounts.spotify.com/authorize", token: "https://accounts.spotify.com/api/token"},
}

// Key names one linkable thing on one project.
type Key struct{ Project, Sidecar string }

// Start is a dance in progress: who asked, for which drawer, and the PKCE
// verifier the callback needs. Kept ten minutes.
type Start struct {
	Project, Sidecar, Drawer, By, Verifier string
	At                                     time.Time
}

// Pending is a token waiting for the appliance to take it.
type Pending struct {
	Project string `json:"-"`
	Sidecar string `json:"sidecar"`
	Drawer  string `json:"drawer"`
	Token   string `json:"token"`
	By      string `json:"-"`
	Expires time.Time `json:"-"`
}

// Unlink is a drawer to unlink, waiting for the appliance the same way.
type Unlink struct {
	Project string `json:"-"`
	Sidecar string `json:"sidecar"`
	Drawer  string `json:"drawer"`
}

// Report is the appliance's last word on one Key: which drawers hold the
// companion's file, and when it said so.
type Report struct {
	Drawers []string  `json:"drawers"`
	At      time.Time `json:"at"`
}

// Status is one drawer's link, in the five words the card needs.
type Status struct {
	// Reported: the appliance has spoken at least once about this Key.
	Reported   bool      `json:"reported"`
	Linked     bool      `json:"linked"`
	ReportedAt time.Time `json:"reported_at,omitempty"`
	// Pending: a token waits for the appliance. Unlinking: an unlink does.
	Pending   bool `json:"pending"`
	Unlinking bool `json:"unlinking"`
}

// Word is the status in one word, for a card.
func (s Status) Word() string {
	switch {
	case s.Pending:
		return "linking"
	case s.Unlinking:
		return "unlinking"
	case !s.Reported:
		return "unknown"
	case s.Linked:
		return "linked"
	}
	return "not linked"
}

// Memory is where the panel keeps the last reports between restarts — the
// store, in practice. Replaceable: the appliance says it all again within
// seconds of being up.
type Memory interface {
	SetLinked(project, sidecar string, drawers []string, at time.Time) error
	Linked() map[string]Report
}

// Hub holds the dances, the pending tokens and the reports of every project.
type Hub struct {
	mu      sync.Mutex
	now     func() time.Time
	starts  map[string]Start
	pending []Pending
	unlinks []Unlink
	reports map[Key]Report
	mem     Memory
	http    *http.Client
	log     *slog.Logger
	// tokenURL overrides a provider's token endpoint (tests).
	tokenURL map[string]string

	// counters, for /metrics
	Started, Finished, Failed, Handed, Unlinked int
}

// New makes a Hub, remembering what the memory holds.
func New(mem Memory, log *slog.Logger) *Hub {
	h := &Hub{
		now:      time.Now,
		starts:   map[string]Start{},
		reports:  map[Key]Report{},
		mem:      mem,
		http:     &http.Client{Timeout: 15 * time.Second},
		log:      log,
		tokenURL: map[string]string{},
	}
	if mem != nil {
		for k, r := range mem.Linked() {
			if i := strings.LastIndex(k, "/"); i > 0 {
				h.reports[Key{Project: k[:i], Sidecar: k[i+1:]}] = r
			}
		}
	}
	return h
}

// SetClock is for tests.
func (h *Hub) SetClock(f func() time.Time) { h.now = f }

// SetTokenURL is for tests: where a provider's code is traded.
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

// expire drops what is stale. With the lock held.
func (h *Hub) expire() {
	now := h.now()
	for k, st := range h.starts {
		if now.Sub(st.At) > 10*time.Minute {
			delete(h.starts, k)
		}
	}
	kept := h.pending[:0]
	for _, p := range h.pending {
		if now.Before(p.Expires) {
			kept = append(kept, p)
		} else {
			h.log.Warn("link expired before the appliance took it", "project", p.Project, "sidecar", p.Sidecar, "drawer", p.Drawer)
		}
	}
	h.pending = kept
}

// Begin opens a dance: the state the provider echoes back, the verifier the
// callback trades with the code.
func (h *Hub) Begin(project, sidecar, drawer, by string) (state, verifier string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	state, verifier = random(24), random(48)
	h.starts[state] = Start{Project: project, Sidecar: sidecar, Drawer: drawer, By: by, Verifier: verifier, At: h.now()}
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

// Exchange trades the code for an access token at the provider (PKCE: the
// verifier, no secret). Returns the token and how long it lives.
func (h *Hub) Exchange(ctx context.Context, kind, clientID, redirect, code, verifier string) (string, time.Duration, error) {
	ep, ok := providers[kind]
	if !ok {
		return "", 0, fmt.Errorf("no provider called %q", kind)
	}
	u := ep.token
	if o := h.tokenURL[kind]; o != "" {
		u = o
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%s did not answer: %w", kind, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if res.StatusCode != http.StatusOK || tok.AccessToken == "" {
		return "", 0, fmt.Errorf("%s refused the code (%d %s %s)", kind, res.StatusCode, tok.Error, tok.Description)
	}
	ttl := time.Duration(tok.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return tok.AccessToken, ttl, nil
}

// Add parks a token for the appliance. A second token for the same drawer
// replaces the first; a queued unlink for it is dropped (the person changed
// their mind).
func (h *Hub) Add(p Pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	kept := h.pending[:0]
	for _, x := range h.pending {
		if !(x.Project == p.Project && x.Sidecar == p.Sidecar && x.Drawer == p.Drawer) {
			kept = append(kept, x)
		}
	}
	h.pending = append(kept, p)
	u := h.unlinks[:0]
	for _, x := range h.unlinks {
		if !(x.Project == p.Project && x.Sidecar == p.Sidecar && x.Drawer == p.Drawer) {
			u = append(u, x)
		}
	}
	h.unlinks = u
	h.Finished++
}

// QueueUnlink asks the appliance to remove a drawer's link. A pending token
// for it is dropped.
func (h *Hub) QueueUnlink(project, sidecar, drawer string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.pending[:0]
	for _, x := range h.pending {
		if !(x.Project == project && x.Sidecar == sidecar && x.Drawer == drawer) {
			kept = append(kept, x)
		}
	}
	h.pending = kept
	for _, x := range h.unlinks {
		if x.Project == project && x.Sidecar == sidecar && x.Drawer == drawer {
			return
		}
	}
	h.unlinks = append(h.unlinks, Unlink{Project: project, Sidecar: sidecar, Drawer: drawer})
	h.Unlinked++
}

// Take hands the appliance everything pending on a project, once.
func (h *Hub) Take(project string) (pending []Pending, unlinks []Unlink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	kept := h.pending[:0]
	for _, x := range h.pending {
		if x.Project == project {
			pending = append(pending, x)
		} else {
			kept = append(kept, x)
		}
	}
	h.pending = kept
	u := h.unlinks[:0]
	for _, x := range h.unlinks {
		if x.Project == project {
			unlinks = append(unlinks, x)
		} else {
			u = append(u, x)
		}
	}
	h.unlinks = u
	h.Handed += len(pending)
	return pending, unlinks
}

// Report records the appliance's word: per sidecar, the drawers that hold
// its file. Remembered across restarts through the memory.
func (h *Hub) Report(project string, linked map[string][]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for sidecar, drawers := range linked {
		d := append([]string(nil), drawers...)
		sort.Strings(d)
		h.reports[Key{Project: project, Sidecar: sidecar}] = Report{Drawers: d, At: now}
		if h.mem != nil {
			_ = h.mem.SetLinked(project, sidecar, d, now)
		}
	}
}

// Status is one drawer's link.
func (h *Hub) Status(project, sidecar, drawer string) Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	var s Status
	if r, ok := h.reports[Key{Project: project, Sidecar: sidecar}]; ok {
		s.Reported, s.ReportedAt = true, r.At
		for _, d := range r.Drawers {
			if d == drawer {
				s.Linked = true
			}
		}
	}
	for _, p := range h.pending {
		if p.Project == project && p.Sidecar == sidecar && p.Drawer == drawer {
			s.Pending = true
		}
	}
	for _, u := range h.unlinks {
		if u.Project == project && u.Sidecar == sidecar && u.Drawer == drawer {
			s.Unlinking = true
		}
	}
	return s
}

// Counters are the tallies for /metrics: dances started, tokens parked,
// tokens handed to an appliance, unlinks queued.
func (h *Hub) Counters() (started, parked, handed, unlinked int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Started, h.Finished, h.Handed, h.Unlinked
}

// Waiting reports whether anything is pending on a project — what decides
// whether the appliance should be woken for it.
func (h *Hub) Waiting(project string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()
	for _, p := range h.pending {
		if p.Project == project {
			return true
		}
	}
	for _, u := range h.unlinks {
		if u.Project == project {
			return true
		}
	}
	return false
}
