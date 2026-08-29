package web

import (
	"fmt"
	"strings"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
)

// present turns a domain View into the handful of words and class names the
// panel needs. Keeping it here means the template has no logic to get wrong
// and the domain has no opinion about metal.

type buttonVM struct {
	Class    string
	Label    string
	Sub      string
	Disabled bool
}

type lampVM struct {
	Label string
	Class string // green | amber | red
	On    bool
}

type panelVM struct {
	arcade.View
	StateWord   string
	ScreenClass string
	Button      buttonVM
	Lamps       []lampVM
	ShowConnect bool
	Ports       string
}

func present(v arcade.View) panelVM {
	p := panelVM{View: v}

	switch {
	case v.HandStarted && v.State == arcade.Ready:
		// nothing to press: it is on because somebody started it
		p.StateWord, p.ScreenClass = "ON", "s-ready"
		p.Button = buttonVM{Class: "lit", Label: "It is on", Sub: "connect with Moonlight", Disabled: true}
		p.ShowConnect = true
	case v.HandStarted:
		p.StateWord, p.ScreenClass = "OFF", "s-asleep"
		p.Button = buttonVM{Class: "", Label: "I want to play", Sub: "started by hand — ask an admin", Disabled: true}
	case v.State == arcade.Ready:
		p.StateWord, p.ScreenClass = "READY", "s-ready"
		// Still live, and deliberately: /play is idempotent, so the button
		// never has to grey out and never has to change what it means.
		p.Button = buttonVM{Class: "lit", Label: "I want to play", Sub: "the console is warm"}
		p.ShowConnect = true
	case v.State == arcade.Starting:
		p.StateWord, p.ScreenClass = "WAKING", "s-starting"
		p.Button = buttonVM{Class: "busy", Label: "Waking…", Sub: "the chain is coming up", Disabled: true}
	case v.State == arcade.Asleep:
		p.StateWord, p.ScreenClass = "ASLEEP", "s-asleep"
		p.Button = buttonVM{Class: "lit", Label: "I want to play", Sub: "press to raise the chain"}
	case v.State == arcade.Blocked:
		p.StateWord, p.ScreenClass = "HANDS OFF", "s-blocked"
		p.Button = buttonVM{Class: "", Label: "I want to play", Sub: "held at the watchman", Disabled: true}
	default:
		p.StateWord, p.ScreenClass = "CAN'T TELL", "s-unknown"
		p.Button = buttonVM{Class: "", Label: "Try to wake anyway", Sub: "the watchman may be back"}
		p.ShowConnect = v.Play.OK
	}

	p.Lamps = []lampVM{
		{Label: v.Engine, Class: "green", On: v.Play.OK},
		{Label: "watchman", Class: watchClass(v), On: v.Watchman.Known},
		{Label: "fault", Class: "red", On: v.Fault != ""},
	}
	p.Ports = ports(v)
	return p
}

func watchClass(v arcade.View) string {
	if v.Watchman.Known {
		return "green"
	}
	return "red"
}

func ports(v arcade.View) string {
	var b []string
	if len(v.Connect.TCP) > 0 {
		var n []string
		for _, t := range v.Connect.TCP {
			n = append(n, fmt.Sprint(t))
		}
		b = append(b, "TCP "+strings.Join(n, " "))
	}
	if len(v.Connect.UDP) > 0 {
		b = append(b, "UDP "+strings.Join(v.Connect.UDP, " "))
	}
	return strings.Join(b, "   ")
}

type clientsVM struct {
	Project string
	Label   string
	// Engine: sunshine | wolf; EngineWord is how the page names it
	Engine      string
	EngineWord  string
	HandStarted bool
	Admin       bool
	Ready       bool
	Me          string
	// the drawers of an appliance: what a device can be pointed at. Every
	// one for an admin; a player only needs to know whether they have one
	HasDrawer bool
	Drawers   []drawerVM
	// the links: an external account tied to a drawer (Le Juke's Spotify —
	// music under whatever that drawer plays). Yours; every drawer's for an
	// admin. HasLinks: the project offers any at all
	HasLinks bool
	Links    []linkVM
	Devices  []arcade.Device
	Seats     []arcade.Seat
	Refusal   arcade.Refusal
	Rooms     []arcade.Room
	Err       string
	Notice    string
}

type drawerVM struct {
	Name   string
	Label  string
	Shared bool
}

type blockVM struct {
	Panel   panelVM
	Clients clientsVM
	// PollSeconds is how often the block re-reads itself. Fast while
	// something is moving, lazy once it has settled: a page sitting at READY
	// while somebody types a PIN has no reason to be swapped every 3 s.
	PollSeconds int
}

// block pairs the two halves and picks the poll rate from the state. They are
// rendered together, from one view, so they cannot disagree — which is the
// bug this replaced: only the console half used to be polled, so a wake left
// the pairing form greyed out until the page was reloaded by hand.
func block(p panelVM, c clientsVM) blockVM {
	poll := 10
	if p.State == arcade.Starting {
		poll = 2
	}
	return blockVM{Panel: p, Clients: c, PollSeconds: poll}
}

type pageVM struct {
	Me      auth.Identity
	Version string
	Blocks  []blockVM
}
