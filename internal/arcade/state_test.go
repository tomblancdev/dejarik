package arcade

import (
	"errors"
	"testing"

	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/veilleur"
)

var project = config.Project{
	Label:       "The console",
	Target:      "console",
	WaitMinutes: 10,
	Connect:     config.Connect{Host: "10.10.50.21", TCP: []int{47989}},
}

// board builds a two-link fleet: muscle1 (the tower) <- console.
func board(tower, console veilleur.Target) *veilleur.Board {
	tower.Name, console.Name = "muscle1", "console"
	tower.Label, console.Label = "the tower", "the console"
	console.Needs = []string{"muscle1"}
	return &veilleur.Board{Targets: []veilleur.Target{tower, console}}
}

func up() veilleur.Target   { return veilleur.Target{Up: true, Known: true, UpFor: "12m8s"} }
func down() veilleur.Target { return veilleur.Target{Up: false, Known: true} }

func check(t *testing.T, got View, want State) {
	t.Helper()
	if got.State != want {
		t.Fatalf("state = %q (%s), want %q", got.State, got.Reason, want)
	}
	if got.Reason == "" {
		t.Fatalf("state %q came with no reason — a person is owed one", got.State)
	}
}

// The whole point of two truths: Sunshine wins. A watchman that is wrong, or
// dead, must never stop somebody playing on a console that answers.
func TestSunshineAnsweringBeatsEverything(t *testing.T) {
	t.Run("board says down", func(t *testing.T) {
		v := resolve("console", inputs{project: project, board: board(down(), down()), answering: true})
		check(t, v, Ready)
	})
	t.Run("board unreachable", func(t *testing.T) {
		v := resolve("console", inputs{project: project, boardErr: errors.New("connection refused"), answering: true})
		check(t, v, Ready)
		if v.Detail == "" {
			t.Fatal("ready with a dead watchman should say so in the small print")
		}
	})
}

// Up is not playable. This gap — VM running, Sunshine not started — is the
// reason the second truth exists at all; collapsing it would put READY on
// the panel a minute before Moonlight would connect.
func TestUpButSilentIsStarting(t *testing.T) {
	v := resolve("console", inputs{project: project, board: board(up(), up()), answering: false})
	check(t, v, Starting)
	if v.Watchman.OK != true {
		t.Fatal("the watchman's own answer should still be reported as up")
	}
	if v.Play.OK {
		t.Fatal("play truth must stay false while Sunshine is silent")
	}
}

// Blocked means a PERSON took it out of play. It is the only thing that
// refuses a wake outright (engine.Wake -> HandsOff).
func TestHandsOffBlocks(t *testing.T) {
	t.Run("on the target", func(t *testing.T) {
		c := down()
		c.Holds = []veilleur.Hold{{ID: "h1", By: "tom", Reason: "swapping the card", HandsOff: true}}
		v := resolve("console", inputs{project: project, board: board(up(), c), answering: false})
		check(t, v, Blocked)
	})
	// Le Veilleur refuses if ANY step of the chain is hands-off, so a hold on
	// the tower must block the console too.
	t.Run("on a parent", func(t *testing.T) {
		m := down()
		m.Holds = []veilleur.Hold{{ID: "h2", By: "tom", Reason: "the tower is open", HandsOff: true}}
		v := resolve("console", inputs{project: project, board: board(m, down()), answering: false})
		check(t, v, Blocked)
		if got := v.Reason; got == "" || !contains(got, "the tower") {
			t.Fatalf("reason should name the machine that is held: %q", got)
		}
	})
	// A plain hold keeps something UP. It has nothing to do with whether a
	// wake is allowed, and must not read as a refusal.
	t.Run("a plain hold does not block", func(t *testing.T) {
		c := down()
		c.Holds = []veilleur.Hold{{ID: "h3", By: "tom", Reason: "backup window"}}
		v := resolve("console", inputs{project: project, board: board(up(), c), answering: false})
		check(t, v, Asleep)
	})
}

func TestUnknownIsSaidOutLoud(t *testing.T) {
	t.Run("watchman unreachable", func(t *testing.T) {
		v := resolve("console", inputs{project: project, boardErr: errors.New("timeout"), answering: false})
		check(t, v, Unknown)
	})
	t.Run("watchman is blind", func(t *testing.T) {
		b := board(down(), down())
		b.ObserveErr = "ssh: no route to host"
		v := resolve("console", inputs{project: project, board: b, answering: false})
		check(t, v, Unknown)
	})
	t.Run("no such target", func(t *testing.T) {
		p := project
		p.Target = "nope"
		v := resolve("console", inputs{project: p, board: board(down(), down()), answering: false})
		check(t, v, Unknown)
	})
}

func TestWakingAndAsleep(t *testing.T) {
	t.Run("a parent is coming up", func(t *testing.T) {
		m := down()
		m.Pending = "raising"
		v := resolve("console", inputs{project: project, board: board(m, down()), answering: false})
		check(t, v, Starting)
	})
	t.Run("nothing running", func(t *testing.T) {
		v := resolve("console", inputs{project: project, board: board(down(), down()), answering: false})
		check(t, v, Asleep)
		if !v.Actionable() {
			t.Fatal("asleep must be actionable — the button is the whole page")
		}
	})
}

// A failed wake is not a state: the machine is still asleep, we just also
// know the last attempt did not take. Keeping it independent is what lets
// the button stay live so a person can try again.
func TestFaultIsIndependentOfState(t *testing.T) {
	m := down()
	m.LastError = "raising muscle1: magic packet sent, no answer in 3m"
	v := resolve("console", inputs{project: project, board: board(m, down()), answering: false})
	check(t, v, Asleep)
	if v.Fault == "" {
		t.Fatal("the last error should be carried")
	}
	if !v.Actionable() {
		t.Fatal("a fault must not disarm the button — retrying is the sane response")
	}
}

func TestChainIsParentsFirst(t *testing.T) {
	v := resolve("console", inputs{project: project, board: board(up(), up()), answering: true})
	if len(v.Chain) != 2 || v.Chain[0].Name != "muscle1" || v.Chain[1].Name != "console" {
		t.Fatalf("chain = %+v, want muscle1 then console", v.Chain)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
