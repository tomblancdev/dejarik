package arcade

// Le Foyer — the hub in the stream, and the rooms.
//
// A tile session is PRIVATE: the engine renders it for one client and no
// other client can be put into it, which is why two devices could never
// share a game however they were paired. A ROOM is the engine's lobby: a
// game started on the engine's own compositor that any number of sessions
// may be switched into — their stream, their pads, their mouse follow them
// in, and back out (the engine's START+UP+RB combo, or the stream ending).
//
// The Foyer is the page a person lands on when they tap the hub tile: a
// seat that is nothing but a kiosk browser on this program. Identity there
// is the pairing, as everywhere on an appliance: the engine hands that seat
// its own session id, the page reads that session back from the engine and
// joins it to its drawer by the uid it runs as — the same join the guard
// does — and trusts nothing else. Two locks and no login: the page answers
// only from the appliance's own addresses (the seats sit behind it), and
// only for a session the engine has open right now. A made-up id maps to
// nothing; the vhost never reaches this (wrong source).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/wolf"
)

// ErrNoFoyer: the project has no hub tile.
var ErrNoFoyer = errors.New("this project has no foyer")

// ErrNotFromASeat: the request did not come from the appliance.
var ErrNotFromASeat = errors.New("the foyer is read from inside a seat only")

// ErrNoSession: the engine has no open session with that id.
var ErrNoSession = errors.New("no open seat carries that session — start Le Foyer again from Moonlight")

// Guest is who is on the Foyer page: a live session, joined to its drawer.
type Guest struct {
	Session string `json:"session"`
	Person  string `json:"person,omitempty"`
	Label   string `json:"label,omitempty"`
	Shared  bool   `json:"shared,omitempty"`
	// Device is the address the seat streams to; Mode its picture.
	Device string `json:"device"`
	Mode   string `json:"mode,omitempty"`
	raw    wolf.Session
}

// Known reports whether the guest's device is pointed at a drawer. A device
// nobody pointed yet plays in nobody's home, and may open nothing here.
func (g Guest) Known() bool { return g.Person != "" }

func parseSources(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", c, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func inAny(nets []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Service) foyerOf(name string) (config.Project, *wolf.Client, error) {
	p, ok := s.cfg.Projects[name]
	if !ok {
		return config.Project{}, nil, ErrNoProject
	}
	wc, isWolf := s.wolf[name]
	if !isWolf || !p.HasFoyer() {
		return config.Project{}, nil, ErrNoFoyer
	}
	return p, wc, nil
}

func (s *Service) liveSession(name, id string) (wolf.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.live[name][id]
	return x, ok
}

// Guest identifies who is on the page. The request must come from the
// appliance, and the session must be one the engine has open now. A seat
// that opened within the last poll is read again before it is refused.
func (s *Service) Guest(ctx context.Context, name, sessionID string, from net.IP) (Guest, error) {
	p, _, err := s.foyerOf(name)
	if err != nil {
		return Guest{}, err
	}
	if !inAny(s.foyer[name], from) {
		return Guest{}, ErrNotFromASeat
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Guest{}, ErrNoSession
	}
	s.refresh(ctx)
	x, ok := s.liveSession(name, sessionID)
	if !ok {
		s.mu.Lock()
		s.fresh = time.Time{}
		s.mu.Unlock()
		s.refresh(ctx)
		if x, ok = s.liveSession(name, sessionID); !ok {
			return Guest{}, ErrNoSession
		}
	}
	g := Guest{Session: x.ID, Device: x.IP, raw: x}
	if x.Width > 0 {
		g.Mode = fmt.Sprintf("%d×%d @ %d", x.Width, x.Height, x.FPS)
	}
	if who, known := personByUID(p, x.UID); known {
		g.Person, g.Label, g.Shared = who, p.People[who].Label, p.People[who].Shared
	}
	return g, nil
}

// Room is one open lobby, joined to the drawer that opened it (the engine
// keeps that as the lobby's profile id, a free string this program fills
// with the drawer's name).
type Room struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	AppID  string `json:"app_id,omitempty"`
	Person string `json:"person,omitempty"`
	Label  string `json:"label,omitempty"`
	Shared bool   `json:"shared,omitempty"`
	// Locked: the engine asks its PIN of whoever joins or closes it.
	Locked bool `json:"locked"`
	// In is how many sessions are in it right now.
	In       int       `json:"in"`
	Sessions []string  `json:"-"`
	Since    time.Time `json:"since"`
	Mine     bool      `json:"mine"`
}

// readRooms reads the engine's lobbies and joins them to their drawers.
// Called from refresh, without the lock.
func (s *Service) readRooms(ctx context.Context, n string, p config.Project) []Room {
	wc := s.wolf[n]
	lobbies, err := wc.Lobbies(ctx)
	if err != nil {
		s.log.Warn("rooms unreadable", "project", n, "err", err)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.rooms[n]
	}
	s.mu.Lock()
	known := len(s.apps[n]) > 0
	s.mu.Unlock()
	if !known && len(lobbies) > 0 {
		s.readApps(ctx, n)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	first := make(map[string]time.Time, len(lobbies))
	out := make([]Room, 0, len(lobbies))
	for _, l := range lobbies {
		t, seen := s.roomFirst[n][l.ID]
		if !seen {
			t = now
		}
		first[l.ID] = t
		r := Room{ID: l.ID, App: l.Name, Locked: l.PinRequired, In: len(l.Sessions), Sessions: l.Sessions, Since: t}
		if _, ok := p.Drawer(l.StartedBy); ok {
			r.Person, r.Label, r.Shared = l.StartedBy, p.People[l.StartedBy].Label, p.People[l.StartedBy].Shared
		} else {
			r.Label = l.StartedBy
		}
		for id, a := range s.apps[n] {
			if a.Title == l.Name {
				r.AppID = id
			}
		}
		out = append(out, r)
	}
	s.roomFirst[n] = first
	for id := range s.pins {
		if _, still := first[id]; !still {
			delete(s.pins, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// Rooms lists the rooms of a project for the panel and the API. A room is
// a public thing in the house: everybody sees every one.
func (s *Service) Rooms(ctx context.Context, name string, who auth.Identity) ([]Room, error) {
	if _, ok := s.cfg.Projects[name]; !ok {
		return nil, ErrNoProject
	}
	if _, isWolf := s.wolf[name]; !isWolf {
		return nil, nil
	}
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Room, 0, len(s.rooms[name]))
	for _, r := range s.rooms[name] {
		r.Mine = r.Person != "" && strings.EqualFold(r.Person, who.User)
		out = append(out, r)
	}
	return out, nil
}

// Shelf is one house game on the Foyer: what a room may be opened on.
type Shelf struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	// Room is the id of the guest's own room on this game, if one is open
	// (the page offers JOIN in place of OPEN).
	Room string `json:"room,omitempty"`
	// Busy says, in words, why a room cannot be opened on it right now.
	Busy string `json:"busy,omitempty"`
}

// FoyerView is the page: who you are, the rooms, the house.
type FoyerView struct {
	Guest Guest   `json:"guest"`
	Rooms []Room  `json:"rooms"`
	House []Shelf `json:"house"`
}

// Foyer renders the page for a guest.
func (s *Service) Foyer(ctx context.Context, name string, g Guest) (FoyerView, error) {
	p, _, err := s.foyerOf(name)
	if err != nil {
		return FoyerView{}, err
	}
	s.refresh(ctx)
	s.mu.Lock()
	if len(s.apps[name]) == 0 {
		s.mu.Unlock()
		s.readApps(ctx, name)
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	v := FoyerView{Guest: g}
	for _, r := range s.rooms[name] {
		r.Mine = g.Known() && r.Person == g.Person
		v.Rooms = append(v.Rooms, r)
	}
	for id, a := range s.apps[name] {
		if a.Title == p.Foyer.Title {
			continue
		}
		sh := Shelf{ID: id, Title: a.Title, Icon: a.Icon}
		if g.Known() {
			sh.Busy = s.openBlocked(name, g, id, a.Title)
			for _, r := range s.rooms[name] {
				if r.Person == g.Person && r.App == a.Title {
					sh.Room, sh.Busy = r.ID, ""
				}
			}
		}
		v.House = append(v.House, sh)
	}
	sort.Slice(v.House, func(i, j int) bool { return v.House[i].Title < v.House[j].Title })
	return v, nil
}

// openBlocked says why a room cannot be opened on a game for a guest:
// their home is in use by a tile (private, one home one game — the guard's
// rule seen from here) or a room of theirs is already open on it. With the
// lock held.
func (s *Service) openBlocked(name string, g Guest, appID, title string) string {
	for _, st := range s.seats[name] {
		if st.Person == g.Person && st.AppID == appID {
			return fmt.Sprintf("%s is already open for %s on %s as a tile — a tile is private; quit it there, then open the room here.", title, g.Label, st.Device)
		}
	}
	for _, r := range s.rooms[name] {
		if r.Person == g.Person && r.App == title {
			return fmt.Sprintf("a room on %s is already open for %s — join it.", title, g.Label)
		}
	}
	return ""
}

func iconPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// OpenRoom opens a room on a house game for a guest and puts the guest in
// it. The room runs on the guest's home for that game — the same the tile
// uses, so the saves are theirs — with the guest's picture, and stays open
// when everybody has left (a stream that drops comes back to a game still
// running; STOP is for when it should be gone; a room nobody is in lets
// the appliance sleep, and dies with it). Caps is the seat's own video
// buffer caps: the one value a room's compositor needs that the engine
// never prints, and without which the room would stream nothing.
func (s *Service) OpenRoom(ctx context.Context, name string, g Guest, appID, caps string, pin []int) (Room, error) {
	p, wc, err := s.foyerOf(name)
	if err != nil {
		return Room{}, err
	}
	if !g.Known() {
		return Room{}, fmt.Errorf("this device is nobody's yet — an admin points it at a drawer on the panel first")
	}
	if len(pin) != 0 && len(pin) != 4 {
		return Room{}, fmt.Errorf("a PIN is four digits")
	}
	caps = strings.TrimSpace(caps)
	if caps == "" {
		return Room{}, fmt.Errorf("the seat did not hand over its video caps, so the room would show nothing — start Le Foyer again from Moonlight")
	}
	s.mu.Lock()
	app, ok := s.apps[name][appID]
	s.mu.Unlock()
	if !ok {
		s.readApps(ctx, name)
		s.mu.Lock()
		app, ok = s.apps[name][appID]
		s.mu.Unlock()
		if !ok {
			return Room{}, fmt.Errorf("no such game")
		}
	}
	if app.Title == p.Foyer.Title {
		return Room{}, fmt.Errorf("you are in %s already", p.Foyer.Title)
	}
	s.refresh(ctx)
	s.mu.Lock()
	busy := s.openBlocked(name, g, appID, app.Title)
	s.mu.Unlock()
	if busy != "" {
		s.refused(name, g, "open", app.Title, busy)
		return Room{}, errors.New(busy)
	}
	node := or(app.RenderNode, p.Foyer.RenderNode)
	audio := g.raw.AudioChannels
	if audio <= 0 {
		audio = 2
	}
	settings := g.raw.Settings
	if len(settings) == 0 {
		person := p.People[g.Person]
		settings = []byte(fmt.Sprintf(`{"run_uid":%d,"run_gid":%d}`, person.UID, person.GID))
	}
	req := wolf.LobbyRequest{
		ProfileID:     g.Person,
		Name:          app.Title,
		Icon:          iconPtr(app.Icon),
		MultiUser:     true,
		Pin:           pin,
		StopWhenEmpty: false,
		Video:         wolf.VideoSettings{Width: g.raw.Width, Height: g.raw.Height, Refresh: g.raw.FPS, WaylandGPU: node, RunnerGPU: node, Caps: caps},
		Audio:         wolf.AudioSettings{ChannelCount: audio},
		Settings:      settings,
		StateFolder:   g.Person + "/" + app.Title,
		Runner:        app.Runner,
	}
	id, err := wc.CreateLobby(ctx, req)
	if err != nil {
		s.log.Error("room refused by the engine", "project", name, "app", app.Title, "person", g.Person, "err", err)
		return Room{}, fmt.Errorf("the engine could not open the room: %w", err)
	}
	s.mu.Lock()
	s.opened[name]++
	if len(pin) == 4 {
		s.pins[id] = pin
	}
	s.fresh = time.Time{}
	s.mu.Unlock()
	s.log.Info("room opened", "project", name, "room", id, "app", app.Title, "person", g.Person, "device", g.Device, "locked", len(pin) == 4)
	s.st.Event("room", g.Person, name+"/"+app.Title)
	room := Room{ID: id, App: app.Title, AppID: appID, Person: g.Person, Label: g.Label, Shared: g.Shared, Locked: len(pin) == 4, Since: s.now(), Mine: true}
	if err := wc.JoinLobby(ctx, id, g.Session, pin); err != nil {
		s.log.Warn("room opened, but the opener could not be put in it", "project", name, "room", id, "person", g.Person, "err", err)
		return room, fmt.Errorf("the room is open, but your seat could not be put in it (%v) — press JOIN", err)
	}
	s.mu.Lock()
	s.joins[name]++
	s.mu.Unlock()
	s.log.Info("room joined", "project", name, "room", id, "app", app.Title, "person", g.Person, "device", g.Device)
	room.In = 1
	return room, nil
}

func (s *Service) roomByID(ctx context.Context, name, id string) (Room, bool) {
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rooms[name] {
		if r.ID == id {
			return r, true
		}
	}
	return Room{}, false
}

func (s *Service) refused(name string, g Guest, verb, app, words string) {
	s.mu.Lock()
	s.roomNo[name]++
	s.mu.Unlock()
	s.log.Info("room "+verb+" refused", "project", name, "app", app, "person", g.Person, "device", g.Device, "words", words)
}

// JoinRoom switches the guest's session into a room. The engine refuses a
// full room and a wrong PIN; this refuses a guest already in one (the page
// is not even visible from inside a room, but a stale one could press).
func (s *Service) JoinRoom(ctx context.Context, name string, g Guest, roomID string, pin []int) (Room, error) {
	_, wc, err := s.foyerOf(name)
	if err != nil {
		return Room{}, err
	}
	if !g.Known() {
		return Room{}, fmt.Errorf("this device is nobody's yet — an admin points it at a drawer on the panel first")
	}
	room, ok := s.roomByID(ctx, name, roomID)
	if !ok {
		return Room{}, fmt.Errorf("that room is gone")
	}
	s.mu.Lock()
	for _, r := range s.rooms[name] {
		for _, sid := range r.Sessions {
			if sid == g.Session {
				s.mu.Unlock()
				words := fmt.Sprintf("you are in a room already (%s) — hold START + UP + RB in it to come back here first.", r.App)
				s.refused(name, g, "join", room.App, words)
				return Room{}, errors.New(words)
			}
		}
	}
	s.mu.Unlock()
	if room.Locked && len(pin) != 4 {
		s.mu.Lock()
		remembered := s.pins[roomID]
		s.mu.Unlock()
		if remembered != nil && room.Person == g.Person {
			pin = remembered // the opener, back in their own room: no wheels
		} else {
			return Room{}, fmt.Errorf("this room is locked — its four digits, please")
		}
	}
	if !room.Locked {
		pin = nil
	}
	if err := wc.JoinLobby(ctx, roomID, g.Session, pin); err != nil {
		words := err.Error()
		switch {
		case strings.Contains(strings.ToLower(words), "full"):
			words = "this room is full."
		case strings.Contains(strings.ToLower(words), "pin"):
			words = "wrong PIN."
		}
		s.refused(name, g, "join", room.App, words)
		return Room{}, errors.New(words)
	}
	s.mu.Lock()
	s.joins[name]++
	s.fresh = time.Time{}
	s.mu.Unlock()
	s.log.Info("room joined", "project", name, "room", roomID, "app", room.App, "person", g.Person, "device", g.Device)
	s.st.Event("join", g.Person, name+"/"+room.App+" ("+or(room.Person, room.Label)+"'s room)")
	room.Mine = room.Person == g.Person
	return room, nil
}

// StopRoom closes a room from the Foyer: the drawer that opened it, only.
// On a shared drawer that is whoever holds the pad, by decision 75.
func (s *Service) StopRoom(ctx context.Context, name string, g Guest, roomID string, pin []int) error {
	_, _, err := s.foyerOf(name)
	if err != nil {
		return err
	}
	room, ok := s.roomByID(ctx, name, roomID)
	if !ok {
		return fmt.Errorf("that room is gone")
	}
	if !g.Known() || room.Person != g.Person {
		return fmt.Errorf("only %s, who opened it, may close this room", or(room.Label, "the one"))
	}
	return s.stopRoom(ctx, name, room, pin, g.Person)
}

// StopRoomBy closes a room from the panel or the API: an admin, or the
// person whose drawer opened it.
func (s *Service) StopRoomBy(ctx context.Context, name, roomID string, pin []int, who auth.Identity) error {
	if _, ok := s.cfg.Projects[name]; !ok {
		return ErrNoProject
	}
	if _, isWolf := s.wolf[name]; !isWolf {
		return fmt.Errorf("a console has no rooms")
	}
	room, ok := s.roomByID(ctx, name, roomID)
	if !ok {
		return fmt.Errorf("no open room with that id")
	}
	if !who.IsAdmin() && !strings.EqualFold(room.Person, who.User) {
		return fmt.Errorf("that room is not yours")
	}
	return s.stopRoom(ctx, name, room, pin, who.User)
}

// stopRoom closes a room with the PIN this program remembers for it, or the
// one given (after a restart the memory is gone: the four digits, please).
func (s *Service) stopRoom(ctx context.Context, name string, room Room, pin []int, by string) error {
	wc := s.wolf[name]
	s.mu.Lock()
	remembered := s.pins[room.ID]
	s.mu.Unlock()
	switch {
	case !room.Locked:
		pin = nil
	case remembered != nil:
		pin = remembered
	case len(pin) != 4:
		return fmt.Errorf("this room is locked and its PIN is not remembered here — the four digits, please")
	}
	if err := wc.StopLobby(ctx, room.ID, pin); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "pin") {
			return fmt.Errorf("wrong PIN")
		}
		return err
	}
	s.mu.Lock()
	s.roomStops[name]++
	delete(s.pins, room.ID)
	s.fresh = time.Time{}
	s.mu.Unlock()
	s.log.Info("room closed", "project", name, "room", room.ID, "app", room.App, "person", room.Person, "in", room.In, "by", by)
	s.st.Event("room-stop", by, name+"/"+room.App+" ("+or(room.Person, room.Label)+")")
	return nil
}

// --- artwork ---------------------------------------------------------------

type icon struct {
	body  []byte
	ctype string
	at    time.Time
	miss  bool
}

// Icon returns a house game's artwork, fetched from the URL the engine names
// for it (a seat has no way out of the house; this program has) and kept
// for a day. A miss is remembered for an hour, so an unreachable URL costs
// one fetch, not one per card per poll.
func (s *Service) Icon(ctx context.Context, name, appID string) ([]byte, string, bool) {
	s.mu.Lock()
	app, ok := s.apps[name][appID]
	if s.icons == nil {
		s.icons = map[string]icon{}
	}
	c, cached := s.icons[appID]
	s.mu.Unlock()
	if !ok || app.Icon == "" {
		return nil, "", false
	}
	now := s.now()
	if cached && ((c.miss && now.Sub(c.at) < time.Hour) || (!c.miss && now.Sub(c.at) < 24*time.Hour)) {
		return c.body, c.ctype, !c.miss
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.Icon, nil)
	if err != nil {
		return nil, "", false
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		if res != nil {
			res.Body.Close()
		}
		s.log.Warn("artwork unreachable", "project", name, "app", app.Title, "url", app.Icon, "err", err)
		s.mu.Lock()
		s.icons[appID] = icon{at: now, miss: true}
		s.mu.Unlock()
		return nil, "", false
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, "", false
	}
	ctype := res.Header.Get("Content-Type")
	if ctype == "" {
		ctype = "image/png"
	}
	s.mu.Lock()
	s.icons[appID] = icon{body: body, ctype: ctype, at: now}
	s.mu.Unlock()
	return body, ctype, true
}
