package arcade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/store"
	"github.com/tomblancdev/dejarik/internal/sunshine"
	"github.com/tomblancdev/dejarik/internal/veilleur"
)

// Service is the whole domain. Every caller goes through it — the JSON API,
// the panel, and (later) the port-holder and the relay. Nothing that acts
// lives inside an HTTP handler, so a listener can raise a project exactly
// the way a person does.
type Service struct {
	cfg *config.Config
	vc  *veilleur.Client
	sun map[string]*sunshine.Client
	st  *store.Store
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	views   map[string]View
	fresh   time.Time
	ttl     time.Duration
	asked   map[string]time.Time
	lastWk  map[string]float64
	wakes   map[string]int
	pairs   int
	unpairs int
}

// New wires the clients. Secrets arrive by environment, never from the
// config file: the file is templated by a converge and readable, the env
// file is sops-backed and no_log.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) (*Service, error) {
	tok := os.Getenv(or(cfg.Veilleur.TokenEnv, "DEJARIK_VEILLEUR_TOKEN"))
	if tok == "" {
		log.Warn("no veilleur token: the board is readable but nothing could be woken")
	}
	s := &Service{
		cfg:    cfg,
		vc:     veilleur.New(strings.TrimRight(cfg.Veilleur.URL, "/"), tok, cfg.Veilleur.Timeout.D()),
		sun:    map[string]*sunshine.Client{},
		st:     st,
		log:    log,
		now:    time.Now,
		views:  map[string]View{},
		ttl:    2 * time.Second,
		asked:  map[string]time.Time{},
		lastWk: map[string]float64{},
		wakes:  map[string]int{},
	}
	for _, n := range cfg.Names() {
		p := cfg.Projects[n]
		basic := ""
		if p.Sunshine.BasicAuthEnv != "" {
			basic = os.Getenv(p.Sunshine.BasicAuthEnv)
			if basic == "" {
				log.Warn("no sunshine credential: pairing and the device list are off",
					"project", n, "env", p.Sunshine.BasicAuthEnv)
			}
		}
		s.sun[n] = sunshine.New(p.Sunshine.ProbeURL, p.Sunshine.AdminURL, basic, p.Sunshine.Timeout.D())
	}
	return s, nil
}

// SetClock is for tests.
func (s *Service) SetClock(f func() time.Time) { s.now = f }

// refresh reads both truths once. The TTL is what keeps a room full of
// pollers from turning into a room full of ssh sessions on the watchman.
func (s *Service) refresh(ctx context.Context) {
	s.mu.Lock()
	if s.now().Sub(s.fresh) < s.ttl {
		s.mu.Unlock()
		return
	}
	s.fresh = s.now()
	s.mu.Unlock()

	b, err := s.vc.Board(ctx)
	if err != nil {
		s.log.Warn("watchman unreachable", "err", err)
	}

	out := make(map[string]View, len(s.cfg.Projects))
	for _, n := range s.cfg.Names() {
		p := s.cfg.Projects[n]
		answering := s.sun[n].Answering(ctx)
		out[n] = resolve(n, inputs{project: p, board: b, boardErr: err, answering: answering})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for n, v := range out {
		// wake latency, measured the only honest way: from the press to the
		// moment Moonlight would actually get a reply (power.md open point 7
		// asked for this number instead of the guess on the page).
		if v.State == Ready {
			if t, ok := s.asked[n]; ok {
				s.lastWk[n] = s.now().Sub(t).Seconds()
				delete(s.asked, n)
				s.log.Info("ready", "project", n, "after_seconds", s.lastWk[n])
			}
		}
		s.views[n] = v
	}
}

// Views returns every project, in file order.
func (s *Service) Views(ctx context.Context) []View {
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(s.cfg.Projects))
	for _, n := range s.cfg.Names() {
		if v, ok := s.views[n]; ok {
			out = append(out, v)
		}
	}
	return out
}

// View returns one project.
func (s *Service) View(ctx context.Context, name string) (View, bool) {
	if _, ok := s.cfg.Projects[name]; !ok {
		return View{}, false
	}
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[name]
	return v, ok
}

// ErrNoProject is returned for a name that is not in the config.
var ErrNoProject = errors.New("no such project")

// Play is the one verb. It is idempotent on purpose: pressing it while the
// console is already up is a no-op that still answers, which is why the
// button never has to grey out.
//
// It does NOT hold anything up. Under the watchman's v0.3 model a wake is a
// request that is forgotten; what keeps the console up afterwards is a live
// Moonlight connection. So a person gets the target's min_uptime to reach
// the couch, and nothing here can pin the tower on.
func (s *Service) Play(ctx context.Context, name string, who auth.Identity) (View, error) {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return View{}, ErrNoProject
	}
	v, _ := s.View(ctx, name)
	if v.State == Ready {
		return v, nil
	}
	reason := fmt.Sprintf("%s wants to play %s", or(who.User, "somebody"), or(p.Label, name))
	if err := s.vc.Wake(ctx, p.Target, reason); err != nil {
		s.log.Error("wake refused", "project", name, "by", who.User, "err", err)
		return v, err
	}
	s.log.Info("play", "project", name, "by", who.User, "via", who.Via, "target", p.Target)
	s.st.Event("play", who.User, name)

	s.mu.Lock()
	if _, running := s.asked[name]; !running {
		s.asked[name] = s.now()
	}
	s.wakes[name]++
	s.fresh = time.Time{} // the next read tells the truth, not the cache
	s.mu.Unlock()

	v, _ = s.View(ctx, name)
	return v, nil
}

// Device is one paired client, joined with the owner Dejarik remembers.
type Device struct {
	UUID  string    `json:"uuid"`
	Name  string    `json:"name"`
	By    string    `json:"by,omitempty"`
	Mine  bool      `json:"mine"`
	Since time.Time `json:"since,omitempty"`
}

// Devices lists what is paired. A player sees their own; an admin sees all,
// including the ones nobody claimed — devices paired straight at Sunshine's
// web UI, before Dejarik existed or around it.
func (s *Service) Devices(ctx context.Context, name string, who auth.Identity) ([]Device, error) {
	if _, ok := s.cfg.Projects[name]; !ok {
		return nil, ErrNoProject
	}
	list, err := s.sun[name].Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(list))
	for _, d := range list {
		dev := Device{UUID: d.UUID, Name: d.Name}
		if o, ok := s.st.Of(d.UUID); ok {
			dev.By, dev.Since = o.By, o.At
			dev.Mine = strings.EqualFold(o.By, who.User)
		}
		if !who.IsAdmin() && !dev.Mine {
			continue
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Pair relays a PIN to Sunshine and records who did it.
func (s *Service) Pair(ctx context.Context, name, pin, device string, who auth.Identity) error {
	if _, ok := s.cfg.Projects[name]; !ok {
		return ErrNoProject
	}
	pin, device = strings.TrimSpace(pin), strings.TrimSpace(device)
	if len(pin) != 4 || strings.Trim(pin, "0123456789") != "" {
		return fmt.Errorf("a PIN is the four digits Moonlight showed you")
	}
	if device == "" {
		return fmt.Errorf("name the device, so you can tell it apart later")
	}
	v, _ := s.View(ctx, name)
	if v.State != Ready {
		return fmt.Errorf("pairing needs the console awake — press play first, then ask Moonlight for a PIN")
	}
	if err := s.sun[name].Pair(ctx, pin, device); err != nil {
		s.log.Warn("pair failed", "project", name, "by", who.User, "err", err)
		return err
	}
	// Sunshine's pin call does not hand back the certificate's uuid, so the
	// owner is attached by looking the device up straight afterwards. Worst
	// case the device simply has no owner and an admin can say who it is.
	if list, err := s.sun[name].Devices(ctx); err == nil {
		for _, d := range list {
			if strings.EqualFold(d.Name, device) {
				_ = s.st.Claim(store.Owner{UUID: d.UUID, Project: name, Device: d.Name, By: who.User})
				break
			}
		}
	}
	s.mu.Lock()
	s.pairs++
	s.mu.Unlock()
	s.log.Info("paired", "project", name, "device", device, "by", who.User, "via", who.Via)
	s.st.Event("pair", who.User, name+"/"+device)
	return nil
}

// Unpair removes a device. A player may only remove their own.
func (s *Service) Unpair(ctx context.Context, name, uuid string, who auth.Identity) error {
	if _, ok := s.cfg.Projects[name]; !ok {
		return ErrNoProject
	}
	if !who.IsAdmin() {
		o, ok := s.st.Of(uuid)
		if !ok || !strings.EqualFold(o.By, who.User) {
			return fmt.Errorf("that device is not yours")
		}
	}
	if err := s.sun[name].Unpair(ctx, uuid); err != nil {
		return err
	}
	_ = s.st.Forget(uuid)
	s.mu.Lock()
	s.unpairs++
	s.mu.Unlock()
	s.log.Info("unpaired", "project", name, "uuid", uuid, "by", who.User)
	s.st.Event("unpair", who.User, name+"/"+uuid)
	return nil
}

// Metrics is the Prometheus text exposition, hand-written like the
// siblings'. Every series is labelled by project from day one, so a second
// project does not repaint the dashboards.
func (s *Service) Metrics(ctx context.Context) string {
	views := s.Views(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	states := []State{Ready, Starting, Asleep, Blocked, Unknown}

	b.WriteString("# HELP dejarik_project_state Which state a project is in (1 = this one).\n# TYPE dejarik_project_state gauge\n")
	for _, v := range views {
		for _, st := range states {
			n := 0
			if v.State == st {
				n = 1
			}
			fmt.Fprintf(&b, "dejarik_project_state{project=%q,state=%q} %d\n", v.Name, st, n)
		}
	}
	b.WriteString("# HELP dejarik_project_playable Whether Sunshine would answer a Moonlight client right now.\n# TYPE dejarik_project_playable gauge\n")
	for _, v := range views {
		n := 0
		if v.Play.OK {
			n = 1
		}
		fmt.Fprintf(&b, "dejarik_project_playable{project=%q} %d\n", v.Name, n)
	}
	b.WriteString("# HELP dejarik_watchman_known Whether Le Veilleur answered on the last pass.\n# TYPE dejarik_watchman_known gauge\n")
	for _, v := range views {
		n := 0
		if v.Watchman.Known {
			n = 1
		}
		fmt.Fprintf(&b, "dejarik_watchman_known{project=%q} %d\n", v.Name, n)
	}
	b.WriteString("# HELP dejarik_wakes_total Times somebody asked to play and a wake was sent.\n# TYPE dejarik_wakes_total counter\n")
	for _, v := range views {
		fmt.Fprintf(&b, "dejarik_wakes_total{project=%q} %d\n", v.Name, s.wakes[v.Name])
	}
	b.WriteString("# HELP dejarik_wake_seconds Seconds from the press to Sunshine answering, last wake.\n# TYPE dejarik_wake_seconds gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "dejarik_wake_seconds{project=%q} %.0f\n", v.Name, s.lastWk[v.Name])
	}
	fmt.Fprintf(&b, "# HELP dejarik_pairings_total Devices paired through the panel.\n# TYPE dejarik_pairings_total counter\ndejarik_pairings_total %d\n", s.pairs)
	fmt.Fprintf(&b, "# HELP dejarik_unpairings_total Devices removed through the panel.\n# TYPE dejarik_unpairings_total counter\ndejarik_unpairings_total %d\n", s.unpairs)
	return b.String()
}

// Healthy is what gatus asks. Dejarik is healthy when it can serve the page;
// a watchman outage is a DEGRADED page, not a dead one — saying otherwise
// would page somebody for a button that is temporarily grey.
func (s *Service) Healthy() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cfg.Projects) == 0 {
		return false, "no projects"
	}
	return true, ""
}
