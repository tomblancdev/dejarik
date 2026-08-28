// Package wolf is a client of one appliance's seat engine.
//
// Two doors. The PROBE is the plain-http `/serverinfo` an unpaired Moonlight
// client reads — the honest answer to "would I get a reply right now?", and
// it needs nothing. The API is the engine's own, and it authenticates
// NOBODY: whoever reaches it can pair a client, point it at any drawer, stop
// any session. On the appliance it is a unix socket; it reaches this program
// through a bridge on one port, and the firewall row to that port is the
// whole lock. So this client exposes the handful of NAMED operations the
// panel needs and never a general proxy — a compromised panel must not
// become an engine shell.
package wolf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one engine.
type Client struct {
	probe string
	api   string
	hc    *http.Client
}

// New builds a client.
func New(probeURL, apiURL string, timeout time.Duration) *Client {
	return &Client{
		probe: strings.TrimRight(probeURL, "/"),
		api:   strings.TrimRight(apiURL, "/"),
		hc:    &http.Client{Timeout: timeout},
	}
}

// Answering reports whether Moonlight would get a reply right now: 200 with
// `hostname` in the body, the same check a console's Sunshine gets.
func (c *Client) Answering(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.probe, nil)
	if err != nil {
		return false
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(b), []byte("hostname"))
}

// call is one API request. Every answer the engine gives carries `success`;
// a false one carries `error`, and so does a non-200 status.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("the engine's API: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	var env struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if res.StatusCode != http.StatusOK || (env.Success != nil && !*env.Success) {
		if env.Error != "" {
			return fmt.Errorf("the engine said: %s", env.Error)
		}
		return fmt.Errorf("the engine's API: %s", res.Status)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Pending is a Moonlight client that has asked to pair and is showing a PIN.
// The engine knows its address and nothing else about it.
type Pending struct {
	Secret string `json:"pair_secret"`
	IP     string `json:"client_ip"`
}

// Pending lists the clients waiting for their PIN to be typed.
func (c *Client) Pending(ctx context.Context) ([]Pending, error) {
	var out struct {
		Requests []Pending `json:"requests"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/pair/pending", nil, &out); err != nil {
		return nil, err
	}
	return out.Requests, nil
}

// Pair answers one pending request with the PIN its Moonlight showed.
func (c *Client) Pair(ctx context.Context, secret, pin string) error {
	return c.call(ctx, http.MethodPost, "/api/v1/pair/client",
		map[string]string{"pair_secret": secret, "pin": pin}, nil)
}

// Device is one paired client as the engine keeps it: an id derived from its
// certificate, the folder its seats open in, and the uid they run as. The
// engine gives a fresh pairing a folder named by a hash and the default uid
// — a device belongs to nobody until it is pointed at a drawer.
type Device struct {
	ID     string
	Folder string
	UID    int
	GID    int
}

type rawDevice struct {
	ID       string `json:"client_id"`
	Folder   string `json:"app_state_folder"`
	Settings struct {
		UID int `json:"run_uid"`
		GID int `json:"run_gid"`
	} `json:"settings"`
}

// Devices lists the paired clients, one per id. The engine's file can hold
// the same certificate twice (a device paired twice lands twice; a settings
// update and an unpair hit both), so the list is deduplicated here.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var out struct {
		Clients []rawDevice `json:"clients"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/clients", nil, &out); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	devs := make([]Device, 0, len(out.Clients))
	for _, r := range out.Clients {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		devs = append(devs, Device{ID: r.ID, Folder: r.Folder, UID: r.Settings.UID, GID: r.Settings.GID})
	}
	return devs, nil
}

// Point sends a client to a drawer: its seats open in that folder and run as
// that uid from its next connection on. This is the whole of identity on an
// appliance — a person is the folder their devices are pointed at.
func (c *Client) Point(ctx context.Context, id, folder string, uid, gid int) error {
	return c.call(ctx, http.MethodPost, "/api/v1/clients/settings", map[string]any{
		"client_id":        id,
		"app_state_folder": folder,
		"settings":         map[string]int{"run_uid": uid, "run_gid": gid},
	}, nil)
}

// Unpair forgets a client. Every entry with its certificate goes.
func (c *Client) Unpair(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, "/api/v1/unpair/client", map[string]string{"client_id": id}, nil)
}

// Session is one open seat. The engine's session carries no folder and no
// client id — only the address it streams to, the app, and the uid it runs
// as; the uid is how a seat is joined to its drawer.
type Session struct {
	ID     string
	AppID  string
	IP     string
	UID    int
	GID    int
	Width  int
	Height int
	FPS    int
}

type rawSession struct {
	ID       string `json:"client_id"` // the engine prints the SESSION id under this name
	AppID    string `json:"app_id"`
	IP       string `json:"client_ip"`
	Width    int    `json:"video_width"`
	Height   int    `json:"video_height"`
	FPS      int    `json:"video_refresh_rate"`
	Settings *struct {
		UID int `json:"run_uid"`
		GID int `json:"run_gid"`
	} `json:"client_settings"`
}

// Sessions lists the open seats.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []rawSession `json:"sessions"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/sessions", nil, &out); err != nil {
		return nil, err
	}
	ss := make([]Session, 0, len(out.Sessions))
	for _, r := range out.Sessions {
		s := Session{ID: r.ID, AppID: r.AppID, IP: r.IP, Width: r.Width, Height: r.Height, FPS: r.FPS}
		if r.Settings != nil {
			s.UID, s.GID = r.Settings.UID, r.Settings.GID
		}
		ss = append(ss, s)
	}
	return ss, nil
}

// Stop closes one seat: the engine ends the stream and removes the container.
func (c *Client) Stop(ctx context.Context, sessionID string) error {
	return c.call(ctx, http.MethodPost, "/api/v1/sessions/stop", map[string]string{"session_id": sessionID}, nil)
}

// App is one tile, as the engine names it.
type App struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Apps lists the tiles, so a seat can be named by its app rather than a number.
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	var out struct {
		Apps []App `json:"apps"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/apps", nil, &out); err != nil {
		return nil, err
	}
	return out.Apps, nil
}
