package arcade

import (
	"testing"
	"time"

	"github.com/tomblancdev/dejarik/internal/wolf"
)

func at(min int) time.Time { return time.Date(2026, 1, 1, 20, min, 0, 0, time.UTC) }

func TestGuardRemembersWhenItFirstSawASession(t *testing.T) {
	g := newGuard()
	first := g.observe([]wolf.Session{{ID: "a"}}, at(0))
	if !first["a"].Equal(at(0)) {
		t.Fatal("a new session is first seen now")
	}
	first = g.observe([]wolf.Session{{ID: "a"}, {ID: "b"}}, at(5))
	if !first["a"].Equal(at(0)) || !first["b"].Equal(at(5)) {
		t.Fatalf("first = %v", first)
	}
	// a gone, then back under the same id: it is a new session
	g.observe([]wolf.Session{{ID: "b"}}, at(6))
	first = g.observe([]wolf.Session{{ID: "a"}, {ID: "b"}}, at(7))
	if !first["a"].Equal(at(7)) {
		t.Fatal("a session that went away and came back is new")
	}
}

func TestOneDrawerOneOpenSeat(t *testing.T) {
	tv := Seat{ID: "1", Person: "someone", AppID: "steam", Device: "203.0.113.10", Since: at(0)}
	phone := Seat{ID: "2", Person: "someone", AppID: "steam", Device: "203.0.113.11", Since: at(3)}
	other := Seat{ID: "3", Person: "other", AppID: "steam", Since: at(4)}
	retro := Seat{ID: "4", Person: "someone", AppID: "retro", Since: at(4)}
	nobody := Seat{ID: "5", AppID: "steam", Since: at(4)}
	nobody2 := Seat{ID: "6", AppID: "steam", Since: at(5)}

	cl := duplicates([]Seat{tv, phone, other, retro, nobody, nobody2})
	if len(cl) != 1 {
		t.Fatalf("clashes = %+v", cl)
	}
	if cl[0].Keep.ID != "1" || len(cl[0].Stop) != 1 || cl[0].Stop[0].ID != "2" || len(cl[0].Undecided) != 0 {
		t.Fatalf("the newer seat on the same drawer and app goes, the older stays: %+v", cl[0])
	}

	// two seen at the same instant: nobody can say which came first
	same := phone
	same.Since = at(0)
	cl = duplicates([]Seat{tv, same})
	if len(cl) != 1 || len(cl[0].Stop) != 0 || len(cl[0].Undecided) != 1 {
		t.Fatalf("same age must be undecided, not a kill: %+v", cl)
	}
}
