// Package web is Dejarik's surface: a JSON API, and a panel that is its
// first client. Both render from the same domain views, so the page can
// never say something the API would not.
package web

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/links"
	"github.com/tomblancdev/dejarik/ui"
)

// Server wires the handlers.
type Server struct {
	cfg     *config.Config
	svc     *arcade.Service
	au      *auth.Auth
	hub     *links.Hub
	log     *slog.Logger
	version string
	tpl     *template.Template
}

// New parses the templates at start: a page that cannot render should fail
// the container, not the first person who opens it.
func New(cfg *config.Config, svc *arcade.Service, au *auth.Auth, version string, log *slog.Logger) (*Server, error) {
	tpl, err := template.ParseFS(ui.Templates, "*.html")
	if err != nil {
		return nil, err
	}
	// the links' vault: the identity gateway when the config names it (a
	// person's grant lives with the person), memory otherwise — and the
	// panel says so, because memory forgets every link at a restart
	var vault links.Vault = links.NewMemoryVault()
	if cfg.Authentik.URL != "" {
		tok := os.Getenv(cfg.Authentik.TokenEnv)
		if tok == "" {
			log.Warn("no authentik token: links cannot be kept with people", "env", cfg.Authentik.TokenEnv)
		}
		vault = links.NewAuthentik(cfg.Authentik.URL, tok, cfg.Authentik.Timeout.D())
	} else {
		for _, n := range cfg.Names() {
			if cfg.Projects[n].HasLinks() {
				log.Warn("no authentik url: the links are kept in memory and forgotten at every restart", "project", n)
			}
		}
	}
	return &Server{cfg: cfg, svc: svc, au: au, hub: links.New(vault, shelf{svc.Store()}, log), log: log, version: version, tpl: tpl}, nil
}

// Hub is the links hub, for tests.
func (s *Server) Hub() *links.Hub { return s.hub }

// Handler routes everything.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /openapi.json", s.openapi)
	// the mark is rendered, not served: the operator's house word goes in it
	mux.HandleFunc("GET /static/logo-animated.svg", s.mark)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(ui.Static)))

	// the API is the product; the panel below is a client of these
	mux.HandleFunc("GET /api/me", s.apiMe)
	mux.HandleFunc("GET /api/projects", s.apiProjects)
	mux.HandleFunc("GET /api/projects/{name}", s.apiProject)
	mux.HandleFunc("POST /api/projects/{name}/play", s.apiPlay)
	mux.HandleFunc("GET /api/projects/{name}/clients", s.apiClients)
	mux.HandleFunc("POST /api/projects/{name}/clients", s.apiPair)
	mux.HandleFunc("DELETE /api/projects/{name}/clients/{uuid}", s.apiUnpair)
	// an appliance: drawers and seats
	mux.HandleFunc("POST /api/projects/{name}/clients/{uuid}/point", s.apiPoint)
	mux.HandleFunc("GET /api/projects/{name}/seats", s.apiSeats)
	mux.HandleFunc("POST /api/projects/{name}/seats/{id}/stop", s.apiStop)
	mux.HandleFunc("GET /api/projects/{name}/rooms", s.apiRooms)
	mux.HandleFunc("POST /api/projects/{name}/rooms/{id}/stop", s.apiRoomStop)
	// the links (links.go): an external account, linked to a drawer with a
	// tap; the sync is the APPLIANCE's, from the Foyer's sources only
	mux.HandleFunc("GET /api/projects/{name}/links", s.apiLinks)
	mux.HandleFunc("POST /api/projects/{name}/links/sync", s.apiLinksSync)
	mux.HandleFunc("GET /api/projects/{name}/links/{sidecar}/token", s.apiLinkToken)
	mux.HandleFunc("GET /links/{name}/{sidecar}/start", s.linkStart)
	mux.HandleFunc("GET /links/callback", s.linkCallback)

	// the panel
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /panel/{name}", s.panel)
	mux.HandleFunc("POST /play/{name}", s.play)
	mux.HandleFunc("POST /pair/{name}", s.pair)
	mux.HandleFunc("POST /unpair/{name}", s.unpair)
	mux.HandleFunc("POST /point/{name}", s.point)
	mux.HandleFunc("POST /stop/{name}", s.stop)
	mux.HandleFunc("POST /room-stop/{name}", s.roomStop)
	mux.HandleFunc("POST /unlink/{name}", s.unlink)

	// Le Foyer: the page in the stream (foyer.go) — its own identity, no
	// proxy in front
	mux.HandleFunc("GET /foyer/{name}", s.foyerPage)
	mux.HandleFunc("GET /foyer/{name}/state", s.foyerStateHandler)
	mux.HandleFunc("GET /foyer/{name}/icon/{app}", s.foyerIcon)
	mux.HandleFunc("POST /foyer/{name}/open", s.foyerOpen)
	mux.HandleFunc("POST /foyer/{name}/join", s.foyerJoin)
	mux.HandleFunc("POST /foyer/{name}/stop", s.foyerStop)
	// the Foyer's own shelf: a link's QR for the seat's drawer, and its unlink
	mux.HandleFunc("GET /foyer/{name}/links/{sidecar}/qr", s.foyerLinkQR)
	mux.HandleFunc("POST /foyer/{name}/links/{sidecar}/unlink", s.foyerUnlink)
	return mux
}

func (s *Server) mark(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(ui.Lockup(s.cfg.House))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	ok, why := s.svc.Healthy()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(why + "\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	started, linked, unlinked, refreshed, handed := s.hub.Counters()
	_, _ = w.Write([]byte("# HELP dejarik_build_info Build information.\n# TYPE dejarik_build_info gauge\n" +
		"dejarik_build_info{version=\"" + s.version + "\"} 1\n" + s.svc.Metrics(r.Context()) +
		"# HELP dejarik_links_started_total Link dances started (a person sent to the provider).\n# TYPE dejarik_links_started_total counter\n" +
		"dejarik_links_started_total " + itoa(started) + "\n" +
		"# HELP dejarik_links_linked_total Grants stored (a dance that came back and was kept).\n# TYPE dejarik_links_linked_total counter\n" +
		"dejarik_links_linked_total " + itoa(linked) + "\n" +
		"# HELP dejarik_links_unlinked_total Grants forgotten.\n# TYPE dejarik_links_unlinked_total counter\n" +
		"dejarik_links_unlinked_total " + itoa(unlinked) + "\n" +
		"# HELP dejarik_links_refreshed_total Access tokens minted from a grant (the provider asked).\n# TYPE dejarik_links_refreshed_total counter\n" +
		"dejarik_links_refreshed_total " + itoa(refreshed) + "\n" +
		"# HELP dejarik_links_handed_total Access tokens handed to the appliance (cache or fresh).\n# TYPE dejarik_links_handed_total counter\n" +
		"dejarik_links_handed_total " + itoa(handed) + "\n"))
}

func itoa(n int) string { return strconv.Itoa(n) }

// who resolves the caller, or writes the refusal.
func (s *Server) who(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id := s.au.Identify(r)
	if id.Role == auth.None {
		http.Error(w, "who are you?", http.StatusUnauthorized)
		return id, false
	}
	return id, true
}

// --- the panel ------------------------------------------------------------

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	page := pageVM{Me: id, Version: s.version}
	for _, v := range s.svc.Views(r.Context()) {
		page.Blocks = append(page.Blocks, block(present(v), s.clientsData(r.Context(), v, id, "", "")))
	}
	s.render(w, "page", page)
}

func (s *Server) panel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	v, found := s.svc.View(r.Context(), r.PathValue("name"))
	if !found {
		http.NotFound(w, r)
		return
	}
	s.render(w, "project", block(present(v), s.clientsData(r.Context(), v, id, "", "")))
}

func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	v, err := s.svc.Play(r.Context(), name, id)
	if err != nil {
		s.log.Warn("play refused", "project", name, "by", id.User, "err", err)
	}
	if !htmx(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := present(v)
	if err != nil {
		p.Fault = err.Error()
		p.Lamps[2].On = true
	}
	s.render(w, "project", block(p, s.clientsData(r.Context(), v, id, "", "")))
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	if err := s.svc.Pair(r.Context(), name, r.FormValue("pin"), r.FormValue("device"), r.FormValue("for"), id); err != nil {
		s.log.Warn("pair refused", "project", name, "by", id.User, "for", r.FormValue("for"), "err", err)
		errMsg = err.Error()
	} else {
		notice = "Paired. " + strings.TrimSpace(r.FormValue("device")) + " can stream now."
		if f := strings.TrimSpace(r.FormValue("for")); f != "" {
			notice = "Paired, and pointed at " + f + "'s drawer. " + strings.TrimSpace(r.FormValue("device")) + " can stream now."
		}
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

func (s *Server) point(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	if err := s.svc.Point(r.Context(), name, r.FormValue("uuid"), r.FormValue("for"), id); err != nil {
		s.log.Warn("point refused", "project", name, "by", id.User, "uuid", r.FormValue("uuid"), "for", r.FormValue("for"), "err", err)
		errMsg = err.Error()
	} else {
		notice = "Pointed at " + strings.TrimSpace(r.FormValue("for")) + "'s drawer. It opens there from its next connection."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	if err := s.svc.Stop(r.Context(), name, r.FormValue("id"), id); err != nil {
		s.log.Warn("stop refused", "project", name, "by", id.User, "seat", r.FormValue("id"), "err", err)
		errMsg = err.Error()
	} else {
		notice = "Seat closed."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

func (s *Server) unpair(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	if err := s.svc.Unpair(r.Context(), name, r.FormValue("uuid"), id); err != nil {
		s.log.Warn("unpair refused", "project", name, "by", id.User, "uuid", r.FormValue("uuid"), "err", err)
		errMsg = err.Error()
	} else {
		notice = "Unpaired. That device has to pair again to stream."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

func (s *Server) afterClients(w http.ResponseWriter, r *http.Request, name string, id auth.Identity, errMsg, notice string) {
	if !htmx(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	v, found := s.svc.View(r.Context(), name)
	if !found {
		http.NotFound(w, r)
		return
	}
	s.render(w, "project", block(present(v), s.clientsData(r.Context(), v, id, errMsg, notice)))
}

func (s *Server) clientsData(ctx context.Context, v arcade.View, id auth.Identity, errMsg, notice string) clientsVM {
	c := clientsVM{
		Project:     v.Name,
		Label:       v.Label,
		Engine:      v.Engine,
		EngineWord:  "Sunshine",
		HandStarted: v.HandStarted,
		Admin:       id.IsAdmin(),
		Ready:       v.CanPlay(),
		Me:          id.User,
		Err:         errMsg,
		Notice:      notice,
	}
	if v.Engine == "wolf" {
		c.EngineWord = "the appliance"
		p := s.cfg.Projects[v.Name]
		_, c.HasDrawer = p.Drawer(id.User)
		for _, n := range p.Drawers() {
			d := p.People[n]
			c.Drawers = append(c.Drawers, drawerVM{Name: n, Label: d.Label, Shared: d.Shared})
		}
		// the links: what a person may tie to their drawer (an admin: every
		// drawer's), and what the appliance last said about each
		c.HasLinks = p.HasLinks()
		c.Links = s.linkStates(ctx, v.Name, id)
	}
	// The device list needs the engine's API, which needs the engine to be
	// up. Asleep is not an error, so say nothing rather than shout.
	if v.CanPlay() {
		devs, err := s.svc.Devices(ctx, v.Name, id)
		if err != nil {
			if c.Err == "" {
				c.Err = c.EngineWord + " did not hand over the device list: " + err.Error()
			}
		} else {
			c.Devices = devs
		}
		if v.Engine == "wolf" {
			seats, no, err := s.svc.Seats(ctx, v.Name, id)
			if err == nil {
				c.Seats, c.Refusal = seats, no
			}
			if rooms, err := s.svc.Rooms(ctx, v.Name, id); err == nil {
				c.Rooms = rooms
			}
		}
	}
	return c
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
	}
}

func htmx(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }
