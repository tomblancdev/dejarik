// Package config is what the lab hands Dejarik: which projects exist, where
// the two truths are read from, and who may ask.
//
// Dejarik owns no machinery. Every value here points at something that does:
// a Le Veilleur target (power), a Sunshine endpoint (can I play), a proxy
// that has already decided who the caller is.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole file.
type Config struct {
	Listen string `yaml:"listen"`
	// House is the operator's own word, substituted into the mark. Empty
	// (the default) ships the panel's own name and names nobody else.
	House    string             `yaml:"house"`
	BaseURL  string             `yaml:"base_url"`
	Timezone string             `yaml:"timezone"`
	DataDir  string             `yaml:"data_dir"`
	Auth     Auth               `yaml:"auth"`
	Veilleur Veilleur           `yaml:"veilleur"`
	Projects map[string]Project `yaml:"projects"`

	names []string
}

// Auth: identity is the proxy's job (the La Loge contract), machines carry
// a bearer token each so the log names which one asked.
type Auth struct {
	TrustedProxies []string `yaml:"trusted_proxies"`
	AdminGroups    []string `yaml:"admin_groups"`
	PlayerGroups   []string `yaml:"player_groups"`
	TokensFile     string   `yaml:"tokens_file"`
}

// Veilleur is the watchman's API — the only thing that may power anything.
type Veilleur struct {
	URL      string   `yaml:"url"`
	TokenEnv string   `yaml:"token_env"`
	Timeout  Duration `yaml:"timeout"`
}

// Project is one thing a person can play. The page renders one card per
// entry, so a second project is data, not a release.
//
// A project runs on ONE of two engines: Sunshine (a console — one machine,
// one desktop, pairing at its web UI) or Wolf (an appliance — a seat per
// person, each in its own drawer, pairing through the engine's API). Exactly
// one of the two blocks is set.
type Project struct {
	Label string `yaml:"label"`
	// Target is the Le Veilleur target this project rides on. Naming it
	// (instead of assuming the project's own name) is what keeps the client
	// generic — see the vision: a project page, never a console page.
	// EMPTY means hand-started: nobody can wake it from here, and the panel
	// says so instead of offering a button that would do nothing.
	Target   string   `yaml:"target"`
	Sunshine Sunshine `yaml:"sunshine"`
	Wolf     Wolf     `yaml:"wolf"`
	// People are the drawers of a Wolf project: the folder a paired device is
	// pointed at, and the uid its seats then run as. Keyed by the person's
	// account name at the gateway — one account, one drawer — except a
	// SHARED drawer (a living-room device everybody uses), which has no
	// account behind it and only an admin may point a device at.
	People  map[string]Person `yaml:"people"`
	Connect Connect           `yaml:"connect"`
	// WaitMinutes is the target's min_uptime, copied here for one purpose:
	// telling a person how long the machine will wait for them. Le Veilleur
	// owns the number; this is the honest way to say it out loud.
	WaitMinutes int `yaml:"wait_minutes"`
}

// Engine names which of the two a project runs on.
func (p Project) Engine() string {
	if p.Wolf.ProbeURL != "" {
		return "wolf"
	}
	return "sunshine"
}

// HandStarted reports a project the watchman does not know: it is on when
// somebody started it, and nothing here can change that.
func (p Project) HandStarted() bool { return p.Target == "" }

// Drawer returns a person's drawer, if the project has one for that name.
func (p Project) Drawer(name string) (Person, bool) {
	d, ok := p.People[name]
	return d, ok
}

// Drawers lists the drawers, people first, shared last, each group by name.
func (p Project) Drawers() []string {
	var people, shared []string
	for n, d := range p.People {
		if d.Shared {
			shared = append(shared, n)
		} else {
			people = append(people, n)
		}
	}
	sort.Strings(people)
	sort.Strings(shared)
	return append(people, shared...)
}

// Wolf is the seat engine of an appliance. ProbeURL is its plain-http
// `/serverinfo` — what an unpaired Moonlight reads, no credential. APIURL is
// the engine's own API, which authenticates NOBODY: on the appliance it is a
// unix socket, and it reaches this program through a bridge on one port
// whose firewall row is the whole lock. So the client uses named operations
// only (pair, point a client at a drawer, list, sessions, stop) and never a
// general proxy — a compromised panel must not become an engine shell.
type Wolf struct {
	ProbeURL string   `yaml:"probe_url"`
	APIURL   string   `yaml:"api_url"`
	Timeout  Duration `yaml:"timeout"`
}

// Person is one drawer: the seat's uid/gid, a label, and whether it is a
// shared drawer with no account behind it.
type Person struct {
	Label  string `yaml:"label"`
	UID    int    `yaml:"uid"`
	GID    int    `yaml:"gid"`
	Shared bool   `yaml:"shared"`
}

// Sunshine has two doors and Dejarik uses both, on purpose.
//
// ProbeURL is the plain-http `/serverinfo` an UNPAIRED Moonlight client
// reads — the same door a player's app knocks on, and the honest answer to
// "can I play right now?". It needs no credential, so the second truth keeps
// working even if the admin credential is wrong or revoked.
//
// AdminURL is the web UI's API (pair, list clients, unpair). Sunshine has
// exactly one account, so this credential is powerful: the client relays
// NAMED endpoints only, never a general proxy (games.md decision 28).
type Sunshine struct {
	ProbeURL     string   `yaml:"probe_url"`
	AdminURL     string   `yaml:"admin_url"`
	BasicAuthEnv string   `yaml:"basic_auth_env"`
	Timeout      Duration `yaml:"timeout"`
}

// Connect is what a person types into Moonlight.
type Connect struct {
	Host string   `yaml:"host" json:"host"`
	TCP  []int    `yaml:"tcp" json:"tcp,omitempty"`
	UDP  []string `yaml:"udp" json:"udp,omitempty"`
}

// Duration is a YAML-friendly time.Duration ("5s", "2m").
type Duration time.Duration

// UnmarshalYAML parses "90s" and friends.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(strings.TrimSpace(n.Value))
	if err != nil {
		return fmt.Errorf("%q is not a duration: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

// D is the plain duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Names are the project names in file order, so the page is stable.
func (c *Config) Names() []string { return c.names }

// Load reads and validates. A config that cannot be trusted is refused at
// start rather than half-applied at request time.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	if c.Veilleur.Timeout == 0 {
		c.Veilleur.Timeout = Duration(5 * time.Second)
	}
	if len(c.Projects) == 0 {
		return nil, fmt.Errorf("no projects: Dejarik with nothing to play is a blank page")
	}
	// file order, so the panel does not reshuffle between requests
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err == nil {
		c.names = projectOrder(&doc, c.Projects)
	}
	for _, n := range c.names {
		p := c.Projects[n]
		switch {
		case p.Sunshine.ProbeURL != "" && p.Wolf.ProbeURL != "":
			return nil, fmt.Errorf("project %q: both a sunshine and a wolf block — a project runs on one engine", n)
		case p.Sunshine.ProbeURL == "" && p.Wolf.ProbeURL == "":
			return nil, fmt.Errorf("project %q: no sunshine or wolf probe_url — there would be only one truth", n)
		}
		if p.Sunshine.Timeout == 0 {
			p.Sunshine.Timeout = Duration(3 * time.Second)
		}
		if p.Wolf.Timeout == 0 {
			p.Wolf.Timeout = Duration(3 * time.Second)
		}
		if p.Engine() == "wolf" {
			if p.Wolf.APIURL == "" {
				return nil, fmt.Errorf("project %q: a wolf project needs api_url — pairing and the seats live there", n)
			}
			seen := map[int]string{}
			for name, d := range p.People {
				if d.UID <= 0 {
					return nil, fmt.Errorf("project %q: drawer %q has no uid — a seat has to run as somebody", n, name)
				}
				if other, dup := seen[d.UID]; dup {
					return nil, fmt.Errorf("project %q: drawers %q and %q share uid %d — a live seat is told apart by its uid", n, other, name, d.UID)
				}
				seen[d.UID] = name
				if d.GID == 0 {
					d.GID = d.UID
				}
				if d.Label == "" {
					d.Label = name
				}
				p.People[name] = d
			}
		}
		c.Projects[n] = p
	}
	if c.Veilleur.URL == "" {
		return nil, fmt.Errorf("no veilleur url: nothing could ever be woken")
	}
	return &c, nil
}

func projectOrder(doc *yaml.Node, have map[string]Project) []string {
	var out []string
	if len(doc.Content) == 0 {
		return keys(have)
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "projects" {
			continue
		}
		m := root.Content[i+1]
		for j := 0; j+1 < len(m.Content); j += 2 {
			if _, ok := have[m.Content[j].Value]; ok {
				out = append(out, m.Content[j].Value)
			}
		}
	}
	if len(out) == 0 {
		return keys(have)
	}
	return out
}

func keys(m map[string]Project) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
