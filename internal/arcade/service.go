package arcade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	"github.com/tomblancdev/dejarik/internal/wolf"
)

// Service is the whole domain. Every caller goes through it — the JSON API,
// the panel, and (later) the port-holder and the relay. Nothing that acts
// lives inside an HTTP handler, so a listener can raise a project exactly
// the way a person does.
type Service struct {
	cfg  *config.Config
	vc   *veilleur.Client
	sun  map[string]*sunshine.Client
	wolf map[string]*wolf.Client
	st   *store.Store
	log  *slog.Logger
	now  func() time.Time

	// how long a pairing waits for the engine to list the new client
	pairWait time.Duration

	mu      sync.Mutex
	views   map[string]View
	fresh   time.Time
	ttl     time.Duration
	asked   map[string]time.Time
	lastWk  map[string]float64
	wakes   map[string]int
	pairs   int
	unpairs int

	// the seats of every appliance, as last read and joined to their
	// drawers; the engine's sessions whole, by id (a room is opened from
	// one); the apps whole, by id (a room is opened on one); the guards'
	// memory (guard.go); what the guard did
	seats    map[string][]Seat
	live     map[string]map[string]wolf.Session
	apps     map[string]map[string]wolf.App
	guards   map[string]*guard
	refusals map[string]int
	stops    map[string]int
	lastNo   map[string]Refusal

	// the rooms of every appliance (foyer.go): as last read, when each was
	// first seen, the PINs of the ones this program opened, the counters
	rooms     map[string][]Room
	roomFirst map[string]map[string]time.Time
	pins      map[string][]int
	opened    map[string]int
	joins     map[string]int
	roomNo    map[string]int
	roomStops map[string]int
	foyer     map[string][]*net.IPNet
	icons     map[string]icon
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
		cfg:      cfg,
		vc:       veilleur.New(strings.TrimRight(cfg.Veilleur.URL, "/"), tok, cfg.Veilleur.Timeout.D()),
		sun:      map[string]*sunshine.Client{},
		wolf:     map[string]*wolf.Client{},
		st:       st,
		log:      log,
		now:      time.Now,
		pairWait: 25 * time.Second,
		views:    map[string]View{},
		ttl:      2 * time.Second,
		asked:    map[string]time.Time{},
		lastWk:   map[string]float64{},
		wakes:    map[string]int{},
		seats:    map[string][]Seat{},
		live:     map[string]map[string]wolf.Session{},
		apps:     map[string]map[string]wolf.App{},
		guards:   map[string]*guard{},
		refusals: map[string]int{},
		stops:    map[string]int{},
		lastNo:   map[string]Refusal{},
		rooms:     map[string][]Room{},
		roomFirst: map[string]map[string]time.Time{},
		pins:      map[string][]int{},
		opened:    map[string]int{},
		joins:     map[string]int{},
		roomNo:    map[string]int{},
		roomStops: map[string]int{},
		foyer:     map[string][]*net.IPNet{},
	}
	for _, n := range cfg.Names() {
		p := cfg.Projects[n]
		if p.Engine() == "wolf" {
			s.wolf[n] = wolf.New(p.Wolf.ProbeURL, p.Wolf.APIURL, p.Wolf.Timeout.D())
			s.guards[n] = newGuard()
			s.apps[n] = map[string]wolf.App{}
			s.live[n] = map[string]wolf.Session{}
			s.roomFirst[n] = map[string]time.Time{}
			if p.HasFoyer() {
				nets, err := parseSources(p.Foyer.Sources)
				if err != nil {
					return nil, fmt.Errorf("project %s: foyer sources: %w", n, err)
				}
				s.foyer[n] = nets
			}
			continue
		}
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

// SetPairWait is for tests.
func (s *Service) SetPairWait(d time.Duration) { s.pairWait = d }

// Run keeps the truths fresh while nobody has the page open — the guard has
// to watch the seats whether or not somebody is looking. Returns when ctx
// ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.ttl)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refresh(ctx)
		}
	}
}

func (s *Service) answering(ctx context.Context, n string) bool {
	if c, ok := s.wolf[n]; ok {
		return c.Answering(ctx)
	}
	return s.sun[n].Answering(ctx)
}

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
	seats := map[string][]Seat{}
	rooms := map[string][]Room{}
	for _, n := range s.cfg.Names() {
		p := s.cfg.Projects[n]
		answering := s.answering(ctx, n)
		out[n] = resolve(n, inputs{project: p, board: b, boardErr: err, answering: answering})
		if _, isWolf := s.wolf[n]; isWolf {
			if answering {
				seats[n] = s.readSeats(ctx, n, p)
				rooms[n] = s.readRooms(ctx, n, p)
			} else {
				s.guards[n].observe(nil, s.now())
				seats[n] = nil
				rooms[n] = nil
				s.mu.Lock()
				s.live[n] = map[string]wolf.Session{}
				s.roomFirst[n] = map[string]time.Time{}
				s.mu.Unlock()
			}
		}
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
	for n, ss := range seats {
		s.seats[n] = ss
	}
	for n, rr := range rooms {
		s.rooms[n] = rr
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
	if p.HandStarted() {
		return v, fmt.Errorf("nothing here can wake %s — it is started by hand for now", or(p.Label, name))
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

// Device is one paired client, joined with what Dejarik remembers about it.
//
// On a console the owner is Dejarik's own record (Sunshine has no idea). On
// an appliance the owner is the ENGINE's fact — the drawer the client is
// pointed at (For) — and Dejarik only adds the name a person gave the device
// and who did the pairing.
type Device struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	By    string `json:"by,omitempty"`
	For   string `json:"for,omitempty"`
	Label string `json:"label,omitempty"`
	// Shared marks a device pointed at a shared drawer (everybody's).
	Shared bool `json:"shared,omitempty"`
	// Pointed is false for a device the engine paired that nobody has yet
	// sent to a drawer: it works, in a drawer of its own that belongs to
	// nobody, until an admin points it.
	Pointed bool       `json:"pointed"`
	Mine    bool       `json:"mine"`
	Since   *time.Time `json:"since,omitempty"`
}

// Devices lists what is paired. A player sees their own; an admin sees all,
// including the ones nobody claimed — devices paired straight at Sunshine's
// web UI, or at the engine before this program existed.
func (s *Service) Devices(ctx context.Context, name string, who auth.Identity) ([]Device, error) {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return nil, ErrNoProject
	}
	var out []Device
	if wc, isWolf := s.wolf[name]; isWolf {
		list, err := wc.Devices(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			dev := Device{UUID: d.ID, Name: "device " + short(d.ID)}
			if o, ok := s.st.Of(d.ID); ok {
				at := o.At
				dev.Name, dev.By, dev.Since = or(o.Device, dev.Name), o.By, &at
			}
			if person, known := p.Drawer(d.Folder); known {
				dev.For, dev.Label, dev.Shared, dev.Pointed = d.Folder, person.Label, person.Shared, true
				dev.Mine = strings.EqualFold(d.Folder, who.User)
			}
			if !who.IsAdmin() && !dev.Mine {
				continue
			}
			out = append(out, dev)
		}
	} else {
		list, err := s.sun[name].Devices(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			dev := Device{UUID: d.UUID, Name: d.Name, Pointed: true}
			if o, ok := s.st.Of(d.UUID); ok {
				at := o.At
				dev.By, dev.Since = o.By, &at
				dev.Mine = strings.EqualFold(o.By, who.User)
			}
			if !who.IsAdmin() && !dev.Mine {
				continue
			}
			out = append(out, dev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// short is what a device is called when nobody named it.
func short(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// Pair puts a device on a project and records who did it.
//
// On a console it relays the PIN to Sunshine. On an appliance it answers the
// pending request with the PIN, then POINTS the new client at a drawer —
// the caller's own, or (an admin only) somebody else's or a shared one —
// which is the whole of identity there: from its next connection that device
// opens that person's home and runs as their uid. The engine gives every
// fresh pairing a folder of its own and the default uid, and a device that
// pairs again lands there again, so pointing is not optional.
func (s *Service) Pair(ctx context.Context, name, pin, device, forWhom string, who auth.Identity) error {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return ErrNoProject
	}
	pin, device, forWhom = strings.TrimSpace(pin), strings.TrimSpace(device), strings.TrimSpace(forWhom)
	if len(pin) != 4 || strings.Trim(pin, "0123456789") != "" {
		return fmt.Errorf("a PIN is the four digits Moonlight showed you")
	}
	if device == "" {
		return fmt.Errorf("name the device, so you can tell it apart later")
	}
	v, _ := s.View(ctx, name)
	if v.State != Ready {
		if p.HandStarted() {
			return fmt.Errorf("pairing needs %s on — it is started by hand, ask an admin", or(p.Label, name))
		}
		return fmt.Errorf("pairing needs the console awake — press play first, then ask Moonlight for a PIN")
	}

	wc, isWolf := s.wolf[name]
	if !isWolf {
		if forWhom != "" && !strings.EqualFold(forWhom, who.User) {
			return fmt.Errorf("a console's devices are paired by the person who uses them")
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
		s.paired(name, device, "", who)
		return nil
	}

	drawer, err := s.drawerFor(p, forWhom, who)
	if err != nil {
		return err
	}
	person := p.People[drawer]

	pending, err := wc.Pending(ctx)
	if err != nil {
		return err
	}
	switch len(pending) {
	case 0:
		return fmt.Errorf("nothing is asking to pair — add %s in Moonlight first; it shows a PIN, then come back here", or(p.Label, name))
	case 1:
	default:
		return fmt.Errorf("%d devices are asking to pair at once and the engine cannot tell them apart — try again in a minute, one at a time", len(pending))
	}
	before, err := wc.Devices(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, d := range before {
		known[d.ID] = true
	}
	if err := wc.Pair(ctx, pending[0].Secret, pin); err != nil {
		s.log.Warn("pair failed", "project", name, "by", who.User, "err", err)
		return fmt.Errorf("%s did not accept that PIN — it expires after a minute, so ask Moonlight for a new one (%v)", or(p.Label, name), err)
	}
	// Answering the PIN resolves the engine's side; the CLIENT is added to
	// its list only when Moonlight finishes the handshake, a few round-trips
	// later (read live: ~10 s on a TV). So wait for it, rather than read the
	// list once and find nothing — which is what the first version did.
	fresh, err := s.awaitFresh(ctx, wc, p, known)
	if err != nil {
		s.log.Warn("paired, but not pointed", "project", name, "by", who.User, "for", drawer, "err", err)
		return fmt.Errorf("paired, but %v — an admin can point the new device from the list", err)
	}
	if err := wc.Point(ctx, fresh.ID, drawer, person.UID, person.GID); err != nil {
		s.log.Warn("paired, but not pointed", "project", name, "by", who.User, "for", drawer, "uuid", fresh.ID, "err", err)
		return fmt.Errorf("paired, but pointing the device at %s's drawer failed — an admin can point it from the list: %w", drawer, err)
	}
	_ = s.st.Claim(store.Owner{UUID: fresh.ID, Project: name, Device: device, By: who.User, For: drawer})
	s.paired(name, device, drawer, who)
	return nil
}

// awaitFresh waits for the client a pairing just created: the id that was
// not there before; failing that (a device pairing AGAIN keeps its id), the
// one the engine left in a folder that is nobody's drawer.
func (s *Service) awaitFresh(ctx context.Context, wc *wolf.Client, p config.Project, known map[string]bool) (*wolf.Device, error) {
	deadline := time.Now().Add(s.pairWait)
	for {
		after, err := wc.Devices(ctx)
		if err != nil {
			return nil, fmt.Errorf("the device list would not read back: %w", err)
		}
		for i := range after {
			if !known[after[i].ID] {
				return &after[i], nil
			}
		}
		for i := range after {
			if _, isDrawer := p.Drawer(after[i].Folder); !isDrawer {
				return &after[i], nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no new device showed up in the engine's list within %s", s.pairWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) paired(name, device, drawer string, who auth.Identity) {
	s.mu.Lock()
	s.pairs++
	s.mu.Unlock()
	s.log.Info("paired", "project", name, "device", device, "for", drawer, "by", who.User, "via", who.Via)
	s.st.Event("pair", who.User, name+"/"+device+drawerSuffix(drawer))
}

func drawerSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " -> " + d
}

// drawerFor decides which drawer a pairing (or a pointing) lands in, and who
// may say so. Your own: anybody with one. Somebody else's, or a shared one:
// an admin.
func (s *Service) drawerFor(p config.Project, forWhom string, who auth.Identity) (string, error) {
	drawer := forWhom
	if drawer == "" {
		drawer = who.User
	}
	person, ok := p.Drawer(drawer)
	if !ok {
		if forWhom == "" {
			return "", fmt.Errorf("you have no drawer on %s yet — an admin adds one line for you", or(p.Label, "the appliance"))
		}
		return "", fmt.Errorf("there is no drawer called %q", forWhom)
	}
	if !who.IsAdmin() {
		if person.Shared {
			return "", fmt.Errorf("%s is a shared drawer — only an admin may point a device at it", drawer)
		}
		if !strings.EqualFold(drawer, who.User) {
			return "", fmt.Errorf("only an admin may point a device at somebody else's drawer")
		}
	}
	return drawer, nil
}

// Point sends a paired device to a drawer. An admin's verb: it is how the TV
// becomes the house's, or a device paired before this program existed
// becomes somebody's.
func (s *Service) Point(ctx context.Context, name, uuid, drawer string, who auth.Identity) error {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return ErrNoProject
	}
	wc, isWolf := s.wolf[name]
	if !isWolf {
		return fmt.Errorf("a console has no drawers to point at")
	}
	if !who.IsAdmin() {
		return fmt.Errorf("pointing a device is an admin's verb")
	}
	drawer = strings.TrimSpace(drawer)
	person, ok := p.Drawer(drawer)
	if !ok {
		return fmt.Errorf("there is no drawer called %q", drawer)
	}
	if err := wc.Point(ctx, uuid, drawer, person.UID, person.GID); err != nil {
		return err
	}
	o, _ := s.st.Of(uuid)
	_ = s.st.Claim(store.Owner{UUID: uuid, Project: name, Device: or(o.Device, "device "+short(uuid)), By: who.User, For: drawer})
	s.log.Info("pointed", "project", name, "uuid", uuid, "for", drawer, "by", who.User)
	s.st.Event("point", who.User, name+"/"+uuid+" -> "+drawer)
	return nil
}

// Unpair removes a device. A player may only remove their own — on an
// appliance, one pointed at their drawer.
func (s *Service) Unpair(ctx context.Context, name, uuid string, who auth.Identity) error {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return ErrNoProject
	}
	if wc, isWolf := s.wolf[name]; isWolf {
		if !who.IsAdmin() {
			list, err := wc.Devices(ctx)
			if err != nil {
				return err
			}
			mine := false
			for _, d := range list {
				if d.ID == uuid && strings.EqualFold(d.Folder, who.User) {
					if person, known := p.Drawer(d.Folder); known && !person.Shared {
						mine = true
					}
				}
			}
			if !mine {
				return fmt.Errorf("that device is not yours")
			}
		}
		if err := wc.Unpair(ctx, uuid); err != nil {
			return err
		}
	} else {
		if !who.IsAdmin() {
			o, ok := s.st.Of(uuid)
			if !ok || !strings.EqualFold(o.By, who.User) {
				return fmt.Errorf("that device is not yours")
			}
		}
		if err := s.sun[name].Unpair(ctx, uuid); err != nil {
			return err
		}
	}
	_ = s.st.Forget(uuid)
	s.mu.Lock()
	s.unpairs++
	s.mu.Unlock()
	s.log.Info("unpaired", "project", name, "uuid", uuid, "by", who.User)
	s.st.Event("unpair", who.User, name+"/"+uuid)
	return nil
}

// Seat is one open session on an appliance, joined to its drawer by the uid
// it runs as (the engine's session carries no folder and no client id).
type Seat struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	AppID  string `json:"app_id"`
	Person string `json:"person,omitempty"`
	Label  string `json:"label,omitempty"`
	Shared bool   `json:"shared,omitempty"`
	// Device is the address the seat streams to — all the engine says of it.
	Device string    `json:"device"`
	Mode   string    `json:"mode,omitempty"`
	Since  time.Time `json:"since"`
	Mine   bool      `json:"mine"`
	// Hub marks a seat on the Foyer tile: the page itself, stateless, so
	// the one-drawer-one-seat guard leaves it alone (a person's phone and
	// the TV both sitting in the Foyer are not two games on one save).
	Hub bool `json:"hub,omitempty"`
}

// Refusal is the last thing the guard did on a project, in words.
type Refusal struct {
	At     time.Time `json:"at"`
	Person string    `json:"person"`
	Words  string    `json:"words"`
}

func personByUID(p config.Project, uid int) (string, bool) {
	for n, d := range p.People {
		if d.UID == uid {
			return n, true
		}
	}
	return "", false
}

// readSeats reads the engine's sessions, joins them to their drawers, and
// runs the guard. Called from refresh, without the lock.
func (s *Service) readSeats(ctx context.Context, n string, p config.Project) []Seat {
	wc := s.wolf[n]
	ss, err := wc.Sessions(ctx)
	if err != nil {
		s.log.Warn("sessions unreadable", "project", n, "err", err)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seats[n]
	}
	titles := s.titlesFor(ctx, n, ss)
	now := s.now()
	first := s.guards[n].observe(ss, now)

	live := make(map[string]wolf.Session, len(ss))
	seats := make([]Seat, 0, len(ss))
	for _, x := range ss {
		live[x.ID] = x
		st := Seat{ID: x.ID, AppID: x.AppID, App: or(titles[x.AppID], "app "+x.AppID), Device: x.IP, Since: first[x.ID]}
		if x.Width > 0 {
			st.Mode = fmt.Sprintf("%d×%d @ %d", x.Width, x.Height, x.FPS)
		}
		if who, ok := personByUID(p, x.UID); ok {
			st.Person, st.Label, st.Shared = who, p.People[who].Label, p.People[who].Shared
		}
		st.Hub = p.HasFoyer() && st.App == p.Foyer.Title
		seats = append(seats, st)
	}
	s.mu.Lock()
	s.live[n] = live
	s.mu.Unlock()

	// one drawer, one open seat (guard.go)
	closed := map[string]bool{}
	for _, c := range duplicates(seats) {
		for _, u := range c.Undecided {
			s.log.Warn("two seats on one drawer, and no way to tell which came first — stopping neither",
				"project", n, "person", c.Keep.Person, "app", c.Keep.App, "devices", []string{c.Keep.Device, u.Device})
		}
		for _, victim := range c.Stop {
			words := fmt.Sprintf("%s is already open for %s on %s since %s — quit it there first.",
				c.Keep.App, c.Keep.Label, c.Keep.Device, c.Keep.Since.Format("15:04"))
			if err := wc.Stop(ctx, victim.ID); err != nil {
				s.log.Error("refusal failed", "project", n, "session", victim.ID, "err", err)
				continue
			}
			closed[victim.ID] = true
			s.log.Info("refused", "project", n, "person", c.Keep.Person, "app", c.Keep.App,
				"kept", c.Keep.Device, "closed", victim.Device, "words", words)
			s.st.Event("refuse", c.Keep.Person, n+"/"+c.Keep.App+" on "+victim.Device)
			s.mu.Lock()
			s.refusals[n]++
			s.lastNo[n] = Refusal{At: now, Person: c.Keep.Person, Words: words}
			s.mu.Unlock()
		}
	}
	if len(closed) > 0 {
		kept := seats[:0]
		for _, st := range seats {
			if !closed[st.ID] {
				kept = append(kept, st)
			}
		}
		seats = kept
	}
	return seats
}

// titlesFor names the apps of the sessions given, reading the engine's list
// once per unknown id. The apps are kept whole: a room is opened on one.
func (s *Service) titlesFor(ctx context.Context, n string, ss []wolf.Session) map[string]string {
	s.mu.Lock()
	cache := s.apps[n]
	missing := false
	for _, x := range ss {
		if _, ok := cache[x.AppID]; !ok {
			missing = true
		}
	}
	s.mu.Unlock()
	if missing {
		s.readApps(ctx, n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.apps[n]))
	for k, v := range s.apps[n] {
		out[k] = v.Title
	}
	return out
}

// readApps refreshes the engine's app list — what a room may be opened on.
func (s *Service) readApps(ctx context.Context, n string) {
	apps, err := s.wolf[n].Apps(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cache := make(map[string]wolf.App, len(apps))
	for _, a := range apps {
		cache[a.ID] = a
	}
	s.apps[n] = cache
}

// Seats lists the open seats of a project. A player sees their own and the
// shared ones (a living-room seat is everybody's to see); an admin sees all.
func (s *Service) Seats(ctx context.Context, name string, who auth.Identity) ([]Seat, Refusal, error) {
	if _, ok := s.cfg.Projects[name]; !ok {
		return nil, Refusal{}, ErrNoProject
	}
	if _, isWolf := s.wolf[name]; !isWolf {
		return nil, Refusal{}, nil
	}
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Seat
	for _, st := range s.seats[name] {
		st.Mine = st.Person != "" && strings.EqualFold(st.Person, who.User)
		if !who.IsAdmin() && !st.Mine && !st.Shared {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	no := s.lastNo[name]
	if !who.IsAdmin() && no.Person != "" && !strings.EqualFold(no.Person, who.User) {
		no = Refusal{}
	}
	return out, no, nil
}

// Stop closes one seat. An admin, or the person whose drawer it is.
func (s *Service) Stop(ctx context.Context, name, id string, who auth.Identity) error {
	if _, ok := s.cfg.Projects[name]; !ok {
		return ErrNoProject
	}
	wc, isWolf := s.wolf[name]
	if !isWolf {
		return fmt.Errorf("a console has no seats to close")
	}
	s.mu.Lock()
	var target *Seat
	for i := range s.seats[name] {
		if s.seats[name][i].ID == id {
			target = &s.seats[name][i]
		}
	}
	s.mu.Unlock()
	if target == nil {
		return fmt.Errorf("no open seat with that id")
	}
	if !who.IsAdmin() && !strings.EqualFold(target.Person, who.User) {
		return fmt.Errorf("that seat is not yours")
	}
	if err := wc.Stop(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	s.stops[name]++
	s.fresh = time.Time{}
	s.mu.Unlock()
	s.log.Info("seat closed", "project", name, "session", id, "person", target.Person, "app", target.App, "by", who.User)
	s.st.Event("stop", who.User, name+"/"+target.App+" ("+or(target.Person, "nobody")+")")
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
	b.WriteString("# HELP dejarik_project_playable Whether the engine would answer a Moonlight client right now.\n# TYPE dejarik_project_playable gauge\n")
	for _, v := range views {
		n := 0
		if v.Play.OK {
			n = 1
		}
		fmt.Fprintf(&b, "dejarik_project_playable{project=%q} %d\n", v.Name, n)
	}
	// Two different questions, two series. "Reachable" is about the watchman;
	// "known" is about this target — and a guest on a sleeping node is
	// legitimately unknown every night, so an alert must never watch it.
	reach := 0
	for _, v := range views {
		if v.Reachable {
			reach = 1
		}
	}
	fmt.Fprintf(&b, "# HELP dejarik_watchman_reachable Whether Le Veilleur answered the last board read.\n# TYPE dejarik_watchman_reachable gauge\ndejarik_watchman_reachable %d\n", reach)
	b.WriteString("# HELP dejarik_watchman_known Whether Le Veilleur can currently SEE this target (a guest on a sleeping node cannot be seen - that is normal; a hand-started project is never seen).\n# TYPE dejarik_watchman_known gauge\n")
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
	b.WriteString("# HELP dejarik_wake_seconds Seconds from the press to the engine answering, last wake.\n# TYPE dejarik_wake_seconds gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "dejarik_wake_seconds{project=%q} %.0f\n", v.Name, s.lastWk[v.Name])
	}
	b.WriteString("# HELP dejarik_seats_open Open seats on an appliance, as last read from its engine.\n# TYPE dejarik_seats_open gauge\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_seats_open{project=%q} %d\n", v.Name, len(s.seats[v.Name]))
		}
	}
	b.WriteString("# HELP dejarik_seat_refusals_total Second seats on one drawer the guard closed (one drawer, one open seat).\n# TYPE dejarik_seat_refusals_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_seat_refusals_total{project=%q} %d\n", v.Name, s.refusals[v.Name])
		}
	}
	b.WriteString("# HELP dejarik_seat_stops_total Seats closed on purpose from the panel.\n# TYPE dejarik_seat_stops_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_seat_stops_total{project=%q} %d\n", v.Name, s.stops[v.Name])
		}
	}
	b.WriteString("# HELP dejarik_rooms_open Rooms (the engine's lobbies: games other devices may join) open on an appliance, as last read.\n# TYPE dejarik_rooms_open gauge\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_rooms_open{project=%q} %d\n", v.Name, len(s.rooms[v.Name]))
		}
	}
	b.WriteString("# HELP dejarik_rooms_opened_total Rooms opened from the Foyer.\n# TYPE dejarik_rooms_opened_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_rooms_opened_total{project=%q} %d\n", v.Name, s.opened[v.Name])
		}
	}
	b.WriteString("# HELP dejarik_room_joins_total Sessions switched into a room from the Foyer (the opener's own join included).\n# TYPE dejarik_room_joins_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_room_joins_total{project=%q} %d\n", v.Name, s.joins[v.Name])
		}
	}
	b.WriteString("# HELP dejarik_room_refusals_total Opens and joins the Foyer refused, in words (a tile already open on that home, a full room, a wrong PIN).\n# TYPE dejarik_room_refusals_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_room_refusals_total{project=%q} %d\n", v.Name, s.roomNo[v.Name])
		}
	}
	b.WriteString("# HELP dejarik_room_stops_total Rooms closed on purpose, from the Foyer or the panel.\n# TYPE dejarik_room_stops_total counter\n")
	for _, v := range views {
		if _, isWolf := s.wolf[v.Name]; isWolf {
			fmt.Fprintf(&b, "dejarik_room_stops_total{project=%q} %d\n", v.Name, s.roomStops[v.Name])
		}
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
