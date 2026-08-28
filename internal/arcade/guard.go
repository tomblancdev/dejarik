package arcade

import (
	"sort"
	"time"

	"github.com/tomblancdev/dejarik/internal/wolf"
)

// One drawer, one open seat.
//
// Every device a person paired is pointed at the same drawer, so the same
// home opens on the phone and on the TV — that is the point. The engine,
// though, will happily start a SECOND seat on that home while the first is
// in: two Steam clients on one config folder, two emulators autosaving to
// one file every ten seconds. Nothing upstream prevents it, and the panel
// cannot intercept a launch (Moonlight talks to the engine directly). What it
// can do is watch, and close the newer seat within a poll — the older game is
// never touched — and say why, here and in the log.
//
// The only thing that tells the newer from the older is when this program
// first saw each session, so a guard remembers that. Two seats it saw at the
// same instant (it just started) cannot be told apart: it stops neither and
// says so.

type guard struct {
	first map[string]time.Time
}

func newGuard() *guard { return &guard{first: map[string]time.Time{}} }

// observe records the sessions present now and returns when each was first
// seen. Sessions that are gone are forgotten, so a session id the engine
// reuses later starts fresh.
func (g *guard) observe(ss []wolf.Session, now time.Time) map[string]time.Time {
	present := make(map[string]time.Time, len(ss))
	for _, s := range ss {
		t, ok := g.first[s.ID]
		if !ok {
			t = now
		}
		present[s.ID] = t
	}
	g.first = present
	return present
}

// clash is one drawer with more than one seat open in the same app.
type clash struct {
	Keep      Seat   // the oldest — the game somebody is actually playing
	Stop      []Seat // the newer ones, to be closed
	Undecided []Seat // as old as Keep: nobody can say which came first
}

// duplicates finds every clash. Seats that belong to nobody's drawer (a
// device not yet pointed at a person) are left alone: their homes are
// distinct, there is nothing to protect. So are seats on the hub tile: the
// Foyer page holds no save, and a person's phone and TV both sitting in it
// is the normal way into a room, not a clash.
func duplicates(seats []Seat) []clash {
	byKey := map[string][]Seat{}
	var order []string
	for _, s := range seats {
		if s.Person == "" || s.Hub {
			continue
		}
		k := s.Person + "\x00" + s.AppID
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], s)
	}
	var out []clash
	for _, k := range order {
		group := byKey[k]
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].Since.Before(group[j].Since) })
		c := clash{Keep: group[0]}
		for _, s := range group[1:] {
			if s.Since.After(c.Keep.Since) {
				c.Stop = append(c.Stop, s)
			} else {
				c.Undecided = append(c.Undecided, s)
			}
		}
		out = append(out, c)
	}
	return out
}
