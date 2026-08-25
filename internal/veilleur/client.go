// Package veilleur is a client of the watchman. Dejarik reads its board and
// asks it to raise things; it can do nothing else, by design — a client may
// ask for a wake, never for a stop (power.md decision 14).
package veilleur

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Hold is a person's decision to keep something up — or, when HandsOff, to
// have it left alone entirely. HandsOff is the ONLY thing that refuses a
// wake outright, so it is the only honest source for a "you can't" state.
type Hold struct {
	ID       string    `json:"id"`
	Target   string    `json:"target"`
	By       string    `json:"by"`
	Reason   string    `json:"reason"`
	Since    time.Time `json:"since"`
	HandsOff bool      `json:"hands_off"`
}

// Target is one row of the watchman's board.
//
// Deliberately absent: `blocked`. It reads like it means "you may not have
// this", but every place it is set is inside the STOP path — it records why
// a thing may not be powered off (min_uptime, grace, held-by). Showing it to
// a player as a reason they cannot play would be exactly backwards.
type Target struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Node      string   `json:"node"`
	Needs     []string `json:"needs"`
	Up        bool     `json:"up"`
	Known     bool     `json:"known"`
	Pending   string   `json:"pending"`
	LastError string   `json:"last_error"`
	UpFor     string   `json:"up_for"`
	Holds     []Hold   `json:"holds"`
}

// Board is the whole picture, one poll.
type Board struct {
	At         time.Time `json:"at"`
	ObserveErr string    `json:"observe_error"`
	Targets    []Target  `json:"targets"`
}

// Find returns a target by name.
func (b *Board) Find(name string) (Target, bool) {
	for _, t := range b.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// Chain is the wake chain for a target, parents first — the same order the
// watchman raises them in, walked from `needs`.
func (b *Board) Chain(name string) []Target {
	seen := map[string]bool{}
	var out []Target
	var walk func(string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		t, ok := b.Find(n)
		if !ok {
			return
		}
		for _, p := range t.Needs {
			walk(p)
		}
		out = append(out, t)
	}
	walk(name)
	return out
}

// Client talks to one Le Veilleur.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New builds a client. An empty token is allowed — the board is readable
// without one — but a wake would then be refused.
func New(base, tok string, timeout time.Duration) *Client {
	return &Client{base: base, token: tok, hc: &http.Client{Timeout: timeout}}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.hc.Do(req)
}

// Board reads the whole board in one request.
func (c *Client) Board(ctx context.Context) (*Board, error) {
	res, err := c.do(ctx, http.MethodGet, "/api/targets", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("veilleur board: %s", res.Status)
	}
	var b Board
	if err := json.NewDecoder(res.Body).Decode(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

// Wake asks for a target and everything it needs. It does not wait: the
// watchman carries on without us, which is what lets a person close the
// page mid-wake.
func (c *Client) Wake(ctx context.Context, target, reason string) error {
	res, err := c.do(ctx, http.MethodPost, "/api/targets/"+target+"/wake",
		map[string]any{"reason": reason, "wait": false})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusAccepted || res.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("wake %s: %s", target, res.Status)
}
