package library

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestTableIsESDEs(t *testing.T) {
	if len(systems) < 150 {
		t.Fatalf("the table is thin: %d systems", len(systems))
	}
	for _, n := range []string{"snes", "nes", "gb", "gbc", "gba", "n64", "gc", "psx", "ps2", "megadrive", "mastersystem", "dreamcast", "pcengine"} {
		if _, ok := SystemOf(n); !ok {
			t.Errorf("no %s", n)
		}
	}
	sy, _ := SystemOf("snes")
	if !sy.Takes(".sfc") || !sy.Takes(".SFC") || sy.Takes(".iso") {
		t.Fatalf("snes takes: %v", sy.Extensions)
	}
}

func TestDetectClassicsThenShelvesThenAmbiguity(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, quiet())
	// a classic extension with many takers in the table: the canonical one
	for f, want := range map[string]string{"Mario.sfc": "snes", "zelda.NES": "nes", "tetris.gb": "gb", "gold.gbc": "gbc", "ruby.gba": "gba", "kart.z64": "n64", "sonic.md": "megadrive", "melee.rvz": "gc"} {
		sy, _, err := s.Detect(f)
		if err != nil || sy.Name != want {
			t.Errorf("%s: %v %v (want %s)", f, sy.Name, err, want)
		}
	}
	// the store's own shelf wins over the classic: a gbc shelf takes .gb
	if err := os.Mkdir(filepath.Join(dir, "gbc"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Refresh()
	if sy, _, err := s.Detect("tetris.gb"); err != nil || sy.Name != "gbc" {
		t.Fatalf("with a gbc shelf, .gb: %v %v", sy.Name, err)
	}
	// two shelves take it: back to the classic
	if err := os.Mkdir(filepath.Join(dir, "gb"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Refresh()
	if sy, _, err := s.Detect("tetris.gb"); err != nil || sy.Name != "gb" {
		t.Fatalf("with gb and gbc shelves, .gb: %v %v", sy.Name, err)
	}
	// a disc image cannot tell
	_, cands, err := s.Detect("Final Fantasy VII.iso")
	if !errors.Is(err, ErrAmbiguous) || len(cands) < 5 {
		t.Fatalf(".iso: %v, %d candidates", err, len(cands))
	}
	if _, _, err := s.Detect("readme.txt"); !errors.Is(err, ErrNoSystem) {
		t.Fatalf(".txt: %v", err)
	}
	if _, _, err := s.Detect("noext"); !errors.Is(err, ErrNoSystem) {
		t.Fatalf("no ext: %v", err)
	}
}

func TestAddLandsWholeAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, quiet())
	a, err := s.Add("snes", "Super Game (USA).sfc", strings.NewReader("ROM"))
	if err != nil || a.Bytes != 3 || a.System != "snes" || a.File != "Super Game (USA).sfc" {
		t.Fatalf("add: %+v %v", a, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "snes", "Super Game (USA).sfc"))
	if err != nil || string(b) != "ROM" {
		t.Fatalf("on disk: %q %v", b, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "snes")); len(ents) != 1 {
		t.Fatalf("a .part left behind: %d entries", len(ents))
	}
	if _, err := s.Add("snes", "Super Game (USA).sfc", strings.NewReader("OTHER")); !errors.Is(err, ErrExists) {
		t.Fatalf("second add: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "snes", "Super Game (USA).sfc")); string(b) != "ROM" {
		t.Fatalf("overwritten: %q", b)
	}
	shelves, ready, _ := s.Shelves()
	if !ready || len(shelves) != 1 || shelves[0].System != "snes" || shelves[0].Titles != 1 || shelves[0].Bytes != 3 || !shelves[0].Known {
		t.Fatalf("shelves: %+v ready=%v", shelves, ready)
	}
	added, bytes := s.Counters()
	if added != 1 || bytes != 3 {
		t.Fatalf("counters: %d %d", added, bytes)
	}
	titles, err := s.Titles("snes")
	if err != nil || len(titles) != 1 || titles[0].Name != "Super Game (USA).sfc" {
		t.Fatalf("titles: %+v %v", titles, err)
	}
}

func TestAddRefusals(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, quiet())
	cases := []struct {
		system, file string
		want         error
	}{
		{"atari", "x.a26", ErrNoSystem},
		{"snes", "x.iso", ErrWrongShelf},
		{"snes", ".hidden.sfc", ErrBadName},
		{"snes", "", ErrBadName},
		{"snes", "noext", ErrBadName},
		{"snes", "bad\x00name.sfc", ErrBadName},
		{"snes", strings.Repeat("a", 201) + ".sfc", ErrBadName},
		{"snes", "empty.sfc", ErrEmpty},
	}
	for _, c := range cases {
		_, err := s.Add(c.system, c.file, strings.NewReader(""))
		if !errors.Is(err, c.want) {
			t.Errorf("%s/%q: %v (want %v)", c.system, c.file, err, c.want)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "snes")); len(ents) != 0 {
		t.Fatalf("a refusal left something: %v", ents)
	}
	// a path is not a name: only its base survives, and dots do not climb
	if n, err := CleanName("../../etc/Game.sfc"); err != nil || n != "Game.sfc" {
		t.Fatalf("clean: %q %v", n, err)
	}
	if n, err := CleanName(`C:\Users\x\Game.sfc`); err != nil || n != "Game.sfc" {
		t.Fatalf("clean windows: %q %v", n, err)
	}
}

func TestAwayStoreSaysSo(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "absent"), quiet())
	if _, ready, why := s.Shelves(); ready || why == "" {
		t.Fatalf("absent store: ready=%v why=%q", ready, why)
	}
	if _, err := s.Add("snes", "x.sfc", strings.NewReader("x")); !errors.Is(err, ErrAway) {
		t.Fatalf("add on an absent store: %v", err)
	}
	if Human(12*1024) != "12 K" || Human(5*1024*1024+512*1024) != "5.5 M" || Human(1<<30) != "1.0 G" || Human(3) != "3 B" {
		t.Fatalf("human: %s %s %s", Human(12*1024), Human(5*1024*1024+512*1024), Human(1<<30))
	}
}
