// Package arcade is the domain: projects, and the one question a person
// actually asks — can I play, and if not, why not?
//
// The whole design rests on reading TWO truths and never collapsing them:
//
//   - the watchman's:  should this be on, and is it coming up?
//   - Sunshine's:      would Moonlight get a reply right now?
//
// They disagree in both directions and both disagreements matter. A VM that
// is running but whose Sunshine has not started yet is NOT ready, however
// green the board looks. And a watchman that has fallen over does not stop
// anybody playing on a console that is answering perfectly well.
package arcade

import (
	"fmt"
	"strings"

	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/veilleur"
)

// State is what the panel shows and what an automation branches on.
type State string

const (
	// Ready: Sunshine answered. Go and play.
	Ready State = "ready"
	// Starting: the chain is coming up, or it is up and Sunshine is not yet.
	Starting State = "starting"
	// Asleep: nothing is running. Pressing the button does something.
	Asleep State = "asleep"
	// Blocked: a person took it out of play (a hands-off hold). Nothing to
	// retry — this is the ONLY thing that refuses a wake outright.
	Blocked State = "blocked"
	// Unknown: we cannot tell. Said out loud rather than guessed.
	Unknown State = "unknown"
)

// Truth is one source's answer, and whether it answered at all.
type Truth struct {
	Known bool   `json:"known"`
	OK    bool   `json:"ok"`
	Says  string `json:"says"`
}

// Link is one machine in the wake chain, parents first.
type Link struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	Up    bool   `json:"up"`
	Known bool   `json:"known"`
	Busy  string `json:"busy,omitempty"`
}

// View is one project, as a person and an automation both see it.
type View struct {
	Name        string         `json:"name"`
	Label       string         `json:"label"`
	State       State          `json:"state"`
	Reason      string         `json:"reason"`
	Detail      string         `json:"detail,omitempty"`
	UpFor       string         `json:"up_for,omitempty"`
	Chain       []Link         `json:"chain,omitempty"`
	Connect     config.Connect `json:"connect"`
	WaitMinutes int            `json:"wait_minutes,omitempty"`
	// Fault is independent of State: the last wake attempt failed, whatever
	// the machine is doing now. It lights the panel's fault lamp without
	// pretending to be a state of its own.
	Fault    string `json:"fault,omitempty"`
	Watchman Truth  `json:"watchman"`
	Play     Truth  `json:"play"`
}

// CanPlay is the one-line answer.
func (v View) CanPlay() bool { return v.State == Ready }

// Actionable reports whether pressing the button would do anything.
func (v View) Actionable() bool { return v.State == Asleep || v.State == Unknown || v.State == Ready }

type inputs struct {
	project   config.Project
	board     *veilleur.Board
	boardErr  error
	answering bool
}

// resolve is deliberately pure: two truths in, one view out. It is the piece
// most worth testing, because getting its precedence wrong is how a page
// starts lying to people.
func resolve(name string, in inputs) View {
	p := in.project
	v := View{
		Name:        name,
		Label:       or(p.Label, name),
		Connect:     p.Connect,
		WaitMinutes: p.WaitMinutes,
	}

	v.Play = Truth{Known: true, OK: in.answering}
	if in.answering {
		v.Play.Says = "Sunshine is answering"
	} else {
		v.Play.Says = "Sunshine is silent"
	}

	var self veilleur.Target
	var found bool
	if in.boardErr == nil && in.board != nil {
		for _, t := range in.board.Chain(p.Target) {
			v.Chain = append(v.Chain, Link{Name: t.Name, Label: t.Label, Up: t.Up, Known: t.Known, Busy: t.Pending})
			if t.LastError != "" && v.Fault == "" {
				v.Fault = t.LastError
			}
			if t.Name == p.Target {
				self, found = t, true
			}
		}
		v.UpFor = self.UpFor
		switch {
		case in.board.ObserveErr != "":
			v.Watchman = Truth{Known: false, Says: "the watchman cannot see the fleet"}
		case !found:
			v.Watchman = Truth{Known: false, Says: "the watchman has no target called " + p.Target}
		default:
			v.Watchman = Truth{Known: self.Known, OK: self.Up, Says: says(self)}
		}
	} else {
		v.Watchman = Truth{Known: false, Says: "the watchman is not answering"}
	}

	// 1. The truth that matters wins, whatever the watchman thinks. This is
	//    the fail-as-is rule (power.md decision 6) applied to its client:
	//    an outage of the watchman must never stop somebody playing.
	if in.answering {
		v.State = Ready
		v.Reason = "The console is warm and Sunshine is answering."
		if !v.Watchman.Known {
			v.Detail = "The watchman is not answering, so nobody can say how long this stays up."
		}
		return v
	}

	// 2. A person deliberately took it out of play. Nothing to retry.
	if hold, target, ok := handsOff(in.board, in.boardErr, p.Target); ok {
		v.State = Blocked
		who := or(hold.By, "somebody")
		v.Reason = fmt.Sprintf("%s asked for %s to be left alone.", who, target)
		if hold.Reason != "" {
			v.Reason += " " + strings.TrimSuffix(hold.Reason, ".") + "."
		}
		v.Detail = "a hands-off hold at the watchman — it has to be lifted there"
		return v
	}

	// 3. Nobody can tell us anything.
	if !v.Watchman.Known {
		v.State = Unknown
		v.Reason = "Nobody can say whether this should be on — and Sunshine is silent too."
		v.Detail = v.Watchman.Says
		return v
	}

	// 4. Something is happening.
	if busy := busyIn(v.Chain); busy != "" {
		v.State = Starting
		v.Reason = "Waking " + busy + ". You can close this page — the wake carries on."
		return v
	}

	// 5. Up, but not answering yet: the gap this whole design exists for.
	if self.Up {
		v.State = Starting
		v.Reason = "The console is up — Sunshine is still starting."
		v.Detail = "a minute at most; it is the last thing to come up"
		return v
	}

	v.State = Asleep
	v.Reason = "Nothing is running. Waking it takes about a minute."
	return v
}

// handsOff walks the whole chain, because Le Veilleur refuses a wake if ANY
// step of it is hands-off — not only the target itself.
func handsOff(b *veilleur.Board, err error, target string) (veilleur.Hold, string, bool) {
	if err != nil || b == nil {
		return veilleur.Hold{}, "", false
	}
	for _, t := range b.Chain(target) {
		for _, h := range t.Holds {
			if h.HandsOff {
				return h, or(t.Label, t.Name), true
			}
		}
	}
	return veilleur.Hold{}, "", false
}

func busyIn(chain []Link) string {
	for _, l := range chain {
		if l.Busy != "" {
			return or(l.Label, l.Name)
		}
	}
	return ""
}

func says(t veilleur.Target) string {
	switch {
	case !t.Known:
		return "the watchman cannot see it"
	case t.Up && t.UpFor != "":
		return "up for " + t.UpFor
	case t.Up:
		return "up"
	default:
		return "not running"
	}
}

func or(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
