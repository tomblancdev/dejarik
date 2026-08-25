// Package sunshine is a client of one console's Sunshine.
//
// Two doors, two trust levels. The PROBE is plain http `/serverinfo` — what
// an unpaired Moonlight client reads — and needs nothing. The ADMIN calls
// (list clients, pair, unpair) carry Sunshine's one basic-auth credential,
// so this client exposes exactly three named operations and never a general
// proxy: a compromised Dejarik must not become a Sunshine shell.
package sunshine

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one Sunshine.
type Client struct {
	probe string
	admin string
	basic string // base64 user:pass
	hc    *http.Client
	phc   *http.Client
}

// New builds a client. The admin hop skips certificate verification on
// purpose: Sunshine serves its own self-signed cert on the LAN and Caddy
// already does exactly this for the `sunshine.` vhost (games.md 27).
func New(probeURL, adminURL, basic string, timeout time.Duration) *Client {
	return &Client{
		probe: strings.TrimRight(probeURL, "/"),
		admin: strings.TrimRight(adminURL, "/"),
		basic: basic,
		phc:   &http.Client{Timeout: timeout},
		hc: &http.Client{Timeout: timeout, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed on a named LAN address
		}},
	}
}

// Answering reports whether Moonlight would get a reply right now.
//
// The check is the one the ansible role proved: 200 with `hostname` in the
// body. A VM that is running but whose Sunshine has not started yet answers
// nothing here — which is the entire reason this second truth exists.
func (c *Client) Answering(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.probe, nil)
	if err != nil {
		return false
	}
	res, err := c.phc.Do(req)
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

// flexBool reads Sunshine's `status`, which is a JSON boolean on some
// endpoints and the STRING "true" on others — and has swapped between
// versions. Read live from the console 2026-08-25: /api/clients/list answers
// `"status":true`. Decoding that into a string fails, which would have made
// a SUCCESSFUL pairing report an error to the person who just did it.
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	var asBool bool
	if err := json.Unmarshal(raw, &asBool); err == nil {
		*b = flexBool(asBool)
		return nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return err
	}
	*b = flexBool(strings.EqualFold(strings.TrimSpace(asString), "true"))
	return nil
}

// Device is one paired client, as Sunshine knows it. Sunshine does not know
// WHOSE it is — that is the one fact Dejarik keeps for itself.
type Device struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

func (c *Client) adminReq(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if c.basic == "" {
		return nil, fmt.Errorf("no sunshine credential configured")
	}
	var rdr io.Reader = bytes.NewReader(nil)
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.admin+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+c.basic)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

// Devices lists what Sunshine has paired.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	res, err := c.adminReq(ctx, http.MethodGet, "/api/clients/list", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sunshine clients: %s", res.Status)
	}
	// Sunshine's payload has moved between versions; read it tolerantly and
	// keep the two fields that matter.
	var raw struct {
		NamedCerts []Device `json:"named_certs"`
		Clients    []Device `json:"clients"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw.NamedCerts) > 0 {
		return raw.NamedCerts, nil
	}
	return raw.Clients, nil
}

// Pair relays one PIN. This is the whole reason Dejarik holds the
// credential: it is the only way a person without an admin login can put
// their own device on the console.
func (c *Client) Pair(ctx context.Context, pin, name string) error {
	res, err := c.adminReq(ctx, http.MethodPost, "/api/pin",
		map[string]string{"pin": pin, "name": name})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sunshine pin: %s", res.Status)
	}
	var out struct {
		Status flexBool `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return err
	}
	if !bool(out.Status) {
		return fmt.Errorf("the console did not accept that PIN — it expires after a minute, so ask Moonlight for a new one")
	}
	return nil
}

// Unpair removes one device.
func (c *Client) Unpair(ctx context.Context, uuid string) error {
	res, err := c.adminReq(ctx, http.MethodPost, "/api/clients/unpair",
		map[string]string{"uuid": uuid})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sunshine unpair: %s", res.Status)
	}
	return nil
}
