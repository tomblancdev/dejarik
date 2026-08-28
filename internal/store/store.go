// Package store keeps the one fact Dejarik owns.
//
// Sunshine's pairing list is global: it knows a device called `tom-phone` is
// paired, not whose it is. So "my clients" needs somebody to remember who
// paired what — and that somebody is this. Everything else on the page is
// rendered from a system that owns it (the tank, Le Veilleur, Sunshine,
// Authelia), and stays there.
//
// Declared REPLACEABLE: lose this file and nobody loses a save or a
// pairing — the devices still work, they just stop having an owner until
// somebody pairs them again. A JSON file plus an event log, the La Loge
// store, for a thing that holds tens of records.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Owner is who put a device on a project — and, on an appliance, which
// drawer they pointed it at (For). The drawer itself is the ENGINE's fact
// (the client's folder); this only keeps the name a person gave the device
// and who did the pairing, which the engine never knew.
type Owner struct {
	UUID    string    `json:"uuid"`
	Project string    `json:"project"`
	Device  string    `json:"device"`
	By      string    `json:"by"`
	For     string    `json:"for,omitempty"`
	At      time.Time `json:"at"`
}

type file struct {
	Owners []Owner `json:"owners"`
}

// Store is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	path   string
	events string
	f      file
	now    func() time.Time
}

// Open loads (or creates) the store under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	probe := filepath.Join(dir, ".writable")
	if err := os.WriteFile(probe, []byte("dejarik"), 0o600); err != nil {
		return nil, fmt.Errorf("data: %s is not writable by uid %d: %w", dir, os.Getuid(), err)
	}
	_ = os.Remove(probe)

	s := &Store{
		path:   filepath.Join(dir, "pairings.json"),
		events: filepath.Join(dir, "events.log"),
		now:    time.Now,
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &s.f); err != nil {
		return nil, fmt.Errorf("pairings.json: %w", err)
	}
	return s, nil
}

// SetClock is for tests.
func (s *Store) SetClock(f func() time.Time) { s.now = f }

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Event appends one audit line. It never fails the caller: an unwritable
// log must not stop somebody pairing their controller.
func (s *Store) Event(kind, actor, detail string) {
	line, err := json.Marshal(map[string]string{
		"at": s.now().Format(time.RFC3339), "type": kind, "actor": actor, "detail": detail,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Claim records who paired a device.
func (s *Store) Claim(o Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o.At = s.now()
	for i, e := range s.f.Owners {
		if e.UUID == o.UUID {
			s.f.Owners[i] = o
			return s.save()
		}
	}
	s.f.Owners = append(s.f.Owners, o)
	return s.save()
}

// Forget drops a device's owner.
func (s *Store) Forget(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.f.Owners[:0]
	for _, e := range s.f.Owners {
		if e.UUID != uuid {
			out = append(out, e)
		}
	}
	s.f.Owners = out
	return s.save()
}

// Of returns the owner of a device, if anybody claimed it. A device paired
// before Dejarik existed — or straight at Sunshine's own web UI — simply has
// no owner, and the page says so rather than guessing.
func (s *Store) Of(uuid string) (Owner, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.f.Owners {
		if e.UUID == uuid {
			return e, true
		}
	}
	return Owner{}, false
}

// All returns every claim, newest first.
func (s *Store) All() []Owner {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Owner(nil), s.f.Owners...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}
