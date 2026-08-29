// Package library is the house store: the ROMs every seat reads, laid out
// the way ES-DE (RetroDECK's front end) reads a store — a folder per
// system, named as ES-DE names it — and the one place a title pushed on the
// panel lands.
//
// The panel is a FACE and owns no data; the store is the tank's, mounted
// into this program by whoever runs it. What this package adds is the
// door: an admin drops a file on the panel, it streams straight into the
// right shelf as the store's owner, and every seat sees it at its next
// scan. Nothing per person — a person's own titles are a different shelf,
// not yet built.
//
// Three honesties. A file is written under a hidden name and renamed into
// place only once it is whole, so a seat never scans half a ROM. A title
// that is already on the shelf is refused, never replaced. And a store
// that does not answer (the mount is away) reads as AWAY on the page and
// refuses a push — never as an empty store.
package library

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// System is one shelf ES-DE knows: its folder, its name on the menu, the
// extensions it takes.
type System struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Extensions []string `json:"extensions"`
}

// Takes reports whether the system takes a file of this extension.
func (s System) Takes(ext string) bool {
	ext = strings.ToLower(ext)
	for _, e := range s.Extensions {
		if e == ext {
			return true
		}
	}
	return false
}

// Shelf is one system's folder in the store, as last read.
type Shelf struct {
	System string `json:"system"`
	Label  string `json:"label"`
	Titles int    `json:"titles"`
	Bytes  int64  `json:"bytes"`
	// Known: the folder is a system ES-DE names (an unknown folder is
	// shown, but nothing can be pushed into it)
	Known bool `json:"known"`
}

// Title is one file on a shelf.
type Title struct {
	Name  string    `json:"name"`
	Bytes int64     `json:"bytes"`
	Since time.Time `json:"since"`
}

// Added is one file that landed.
type Added struct {
	System string `json:"system"`
	File   string `json:"file"`
	Bytes  int64  `json:"bytes"`
}

var (
	// ErrAway: the store does not answer (the mount is absent or refused).
	ErrAway = errors.New("the store is away")
	// ErrNoSystem: not a system ES-DE names.
	ErrNoSystem = errors.New("not a system this store knows")
	// ErrBadName: a file name the store will not take.
	ErrBadName = errors.New("not a file name for a shelf")
	// ErrExists: the title is already on the shelf.
	ErrExists = errors.New("already on the shelf")
	// ErrWrongShelf: the system does not take this extension.
	ErrWrongShelf = errors.New("this shelf does not take that kind of file")
	// ErrAmbiguous: more than one system takes this extension — choose.
	ErrAmbiguous = errors.New("more than one system takes this kind of file — choose the shelf")
	// ErrEmpty: nothing arrived.
	ErrEmpty = errors.New("the file was empty")
)

// Store is a ROM store on disk.
type Store struct {
	path string
	log  *slog.Logger
	now  func() time.Time

	mu      sync.RWMutex
	shelves []Shelf
	ready   bool
	why     string
	read    time.Time
	added   int
	bytes   int64
}

// Open points at a store. It never fails: an absent store is a store that
// is away, and the page says so.
func Open(path string, log *slog.Logger) *Store {
	s := &Store{path: filepath.Clean(path), log: log, now: time.Now}
	s.Refresh()
	return s
}

// Path is where the store is.
func (s *Store) Path() string { return s.path }

// Systems is ES-DE's table, by name.
func Systems() []System { return systems }

// SystemOf finds a system by its folder name.
func SystemOf(name string) (System, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, sy := range systems {
		if sy.Name == name {
			return sy, true
		}
	}
	return System{}, false
}

// canonical is the shelf a classic extension means when ES-DE's table lists
// several (it lists every regional and add-on variant: .sfc is snes, but
// also snesna, satellaview, sufami and the two Game Boys through a core
// that takes it). The store's own shelves win first; this table answers
// when the store holds nothing yet. A disc image (.iso .cue .bin .chd) or
// an archive is never here: those are chosen.
var canonical = map[string]string{
	".sfc": "snes", ".smc": "snes", ".swc": "snes", ".fig": "snes",
	".nes": "nes", ".unf": "nes", ".unif": "nes", ".fds": "fds",
	".gb": "gb", ".gbc": "gbc", ".gba": "gba", ".nds": "nds", ".3ds": "n3ds", ".cia": "n3ds",
	".n64": "n64", ".z64": "n64", ".v64": "n64", ".ndd": "n64dd",
	".gcm": "gc", ".gcz": "gc", ".rvz": "gc", ".wbfs": "wii", ".wad": "wii",
	".md": "megadrive", ".smd": "megadrive", ".gen": "megadrive", ".32x": "sega32x",
	".sms": "mastersystem", ".gg": "gamegear", ".sg": "sg-1000",
	".pce": "pcengine", ".sgx": "supergrafx",
	".vb": "virtualboy", ".ws": "wonderswan", ".wsc": "wonderswancolor",
	".ngp": "ngp", ".ngc": "ngpc",
	".a26": "atari2600", ".a52": "atari5200", ".a78": "atari7800", ".lnx": "atarilynx", ".j64": "atarijaguar", ".jag": "atarijaguar",
	".col": "colecovision", ".int": "intellivision", ".vec": "vectrex",
	".d64": "c64", ".t64": "c64", ".prg": "c64", ".adf": "amiga",
	".pbp": "psx", ".cso": "psp",
}

// Detect names the shelf a file belongs on from its extension: the store's
// own shelf that takes it if there is exactly one, else the canonical
// system for a classic extension, else the one system in ES-DE's table
// that takes it. Everything else is ErrAmbiguous (with the candidates) or
// ErrNoSystem (nothing takes it).
func (s *Store) Detect(filename string) (System, []System, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return System{}, nil, ErrNoSystem
	}
	var cands []System
	for _, sy := range systems {
		if sy.Takes(ext) {
			cands = append(cands, sy)
		}
	}
	if len(cands) == 0 {
		return System{}, nil, ErrNoSystem
	}
	if len(cands) == 1 {
		return cands[0], cands, nil
	}
	// the store's own shelves first: the house has already decided
	s.mu.RLock()
	var mine []System
	for _, sh := range s.shelves {
		for _, c := range cands {
			if c.Name == sh.System {
				mine = append(mine, c)
			}
		}
	}
	s.mu.RUnlock()
	if len(mine) == 1 {
		return mine[0], cands, nil
	}
	if c, ok := canonical[ext]; ok {
		if sy, ok := SystemOf(c); ok && sy.Takes(ext) {
			return sy, cands, nil
		}
	}
	return System{}, cands, ErrAmbiguous
}

// Refresh re-reads the shelves. The page renders from what this read, so a
// store that stopped answering costs one slow refresh in the background,
// never a slow page.
func (s *Store) Refresh() {
	shelves, err := s.readShelves()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = s.now()
	if err != nil {
		if s.ready || s.why == "" {
			s.log.Warn("the store is away", "path", s.path, "err", err)
		}
		s.ready, s.why, s.shelves = false, err.Error(), nil
		return
	}
	if !s.ready && s.why != "" {
		s.log.Info("the store is back", "path", s.path, "shelves", len(shelves))
	}
	s.ready, s.why, s.shelves = true, "", shelves
}

// Run refreshes every `every` until ctx ends.
func (s *Store) Run(ctx interface{ Done() <-chan struct{} }, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Refresh()
		}
	}
}

func (s *Store) readShelves() ([]Shelf, error) {
	ents, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var out []Shelf
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sh := Shelf{System: e.Name()}
		if sy, ok := SystemOf(e.Name()); ok {
			sh.Label, sh.Known = sy.Label, true
		} else {
			sh.Label = e.Name()
		}
		files, err := os.ReadDir(filepath.Join(s.path, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !counts(f.Name()) || f.IsDir() {
				continue
			}
			sh.Titles++
			if fi, err := f.Info(); err == nil {
				sh.Bytes += fi.Size()
			}
		}
		out = append(out, sh)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].System < out[j].System })
	return out, nil
}

// counts: what a shelf counts as a title — not a dotfile (a .part in
// flight, ES-DE's own markers) and not the systeminfo ES-DE drops.
func counts(name string) bool {
	return !strings.HasPrefix(name, ".") && name != "systeminfo.txt"
}

// Shelves is the last read: the shelves, whether the store answered, and
// why not when it did not.
func (s *Store) Shelves() ([]Shelf, bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Shelf(nil), s.shelves...), s.ready, s.why
}

// Ready reports whether the store answered at the last read.
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// Titles reads one shelf, live.
func (s *Store) Titles(system string) ([]Title, error) {
	sy, ok := SystemOf(system)
	if !ok {
		return nil, ErrNoSystem
	}
	files, err := os.ReadDir(filepath.Join(s.path, sy.Name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrAway, err)
	}
	var out []Title
	for _, f := range files {
		if f.IsDir() || !counts(f.Name()) {
			continue
		}
		t := Title{Name: f.Name()}
		if fi, err := f.Info(); err == nil {
			t.Bytes, t.Since = fi.Size(), fi.ModTime()
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Add streams one file onto a shelf. The shelf is created if ES-DE names
// it; the file lands under a hidden name and is renamed into place once it
// is whole; a title already there is refused. Never overwrites.
func (s *Store) Add(system, filename string, r io.Reader) (Added, error) {
	sy, ok := SystemOf(system)
	if !ok {
		return Added{}, ErrNoSystem
	}
	name, err := CleanName(filename)
	if err != nil {
		return Added{}, err
	}
	if !sy.Takes(filepath.Ext(name)) {
		return Added{}, fmt.Errorf("%w: %s takes %s", ErrWrongShelf, sy.Name, strings.Join(sy.Extensions, " "))
	}
	if !s.Ready() {
		s.Refresh()
		if !s.Ready() {
			return Added{}, ErrAway
		}
	}
	dir := filepath.Join(s.path, sy.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Added{}, fmt.Errorf("%w: %v", ErrAway, err)
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		return Added{}, ErrExists
	}
	tmp := filepath.Join(dir, "."+name+".part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return Added{}, fmt.Errorf("%w: it is being pushed right now", ErrExists)
		}
		return Added{}, fmt.Errorf("%w: %v", ErrAway, err)
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = ErrEmpty
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, ErrEmpty) {
			return Added{}, err
		}
		return Added{}, fmt.Errorf("the push did not land: %w", err)
	}
	s.mu.Lock()
	s.added++
	s.bytes += n
	s.mu.Unlock()
	s.Refresh()
	return Added{System: sy.Name, File: name, Bytes: n}, nil
}

// Counters: files landed and bytes written since start.
func (s *Store) Counters() (int, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.added, s.bytes
}

// CleanName takes the file's own name and refuses anything that is not a
// plain name for a shelf: no path, no dotfile, nothing unprintable, and
// short enough for any filesystem.
func CleanName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(strings.TrimSpace(name))
	switch {
	case name == "", name == ".", name == "/", name == "..":
		return "", ErrBadName
	case strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%w: a hidden file", ErrBadName)
	case len(name) > 200:
		return "", fmt.Errorf("%w: longer than 200 bytes", ErrBadName)
	case filepath.Ext(name) == "":
		return "", fmt.Errorf("%w: no extension, so no shelf", ErrBadName)
	}
	for _, r := range name {
		if r == '/' || r == 0 || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: unprintable characters", ErrBadName)
		}
	}
	return name, nil
}

// Human prints a size the way a shelf would: 12 K, 5.4 M, 1.2 G.
func Human(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1f G", float64(b)/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1f M", float64(b)/(k*k))
	case b >= k:
		return fmt.Sprintf("%d K", b/k)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
