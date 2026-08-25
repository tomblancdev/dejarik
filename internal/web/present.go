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

	switch v.State {
	case arcade.Ready:
		p.StateWord, p.ScreenClass = "READY", "s-ready"
		// Still live, and deliberately: /play is idempotent, so the button
		// never has to grey out and never has to change what it means.
		p.Button = buttonVM{Class: "lit", Label: "I want to play", Sub: "the console is warm"}
		p.ShowConnect = true
	case arcade.Starting:
		p.StateWord, p.ScreenClass = "WAKING", "s-starting"
		p.Button = buttonVM{Class: "busy", Label: "Waking…", Sub: "the chain is coming up", Disabled: true}
	case arcade.Asleep:
		p.StateWord, p.ScreenClass = "ASLEEP", "s-asleep"
		p.Button = buttonVM{Class: "lit", Label: "I want to play", Sub: "press to raise the chain"}
	case arcade.Blocked:
		p.StateWord, p.ScreenClass = "HANDS OFF", "s-blocked"
		p.Button = buttonVM{Class: "", Label: "I want to play", Sub: "held at the watchman", Disabled: true}
	default:
		p.StateWord, p.ScreenClass = "CAN'T TELL", "s-unknown"
		p.Button = buttonVM{Class: "", Label: "Try to wake anyway", Sub: "the watchman may be back"}
		p.ShowConnect = v.Play.OK
	}

	p.Lamps = []lampVM{
		{Label: "sunshine", Class: "green", On: v.Play.OK},
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
	Admin   bool
	Ready   bool
	Devices []arcade.Device
	Err     string
	Notice  string
}

type blockVM struct {
	Panel   panelVM
	Clients clientsVM
}

type pageVM struct {
	Me      auth.Identity
	Version string
	Blocks  []blockVM
}
