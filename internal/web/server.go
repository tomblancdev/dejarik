// Package web is Dejarik's surface: a JSON API, and a panel that is its
// first client. Both render from the same domain views, so the page can
// never say something the API would not.
package web

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/ui"
)

// Server wires the handlers.
type Server struct {
	cfg     *config.Config
	svc     *arcade.Service
	au      *auth.Auth
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
	return &Server{cfg: cfg, svc: svc, au: au, log: log, version: version, tpl: tpl}, nil
}

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

	// the panel
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /panel/{name}", s.panel)
	mux.HandleFunc("POST /play/{name}", s.play)
	mux.HandleFunc("POST /pair/{name}", s.pair)
	mux.HandleFunc("POST /unpair/{name}", s.unpair)
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
	_, _ = w.Write([]byte("# HELP dejarik_build_info Build information.\n# TYPE dejarik_build_info gauge\n" +
		"dejarik_build_info{version=\"" + s.version + "\"} 1\n" + s.svc.Metrics(r.Context())))
}

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
	if err := s.svc.Pair(r.Context(), name, r.FormValue("pin"), r.FormValue("device"), id); err != nil {
		errMsg = err.Error()
	} else {
		notice = "Paired. " + strings.TrimSpace(r.FormValue("device")) + " can stream now."
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
		Project: v.Name,
		Label:   v.Label,
		Admin:   id.IsAdmin(),
		Ready:   v.CanPlay(),
		Err:     errMsg,
		Notice:  notice,
	}
	// The device list needs Sunshine's admin API, which needs Sunshine to be
	// up. Asleep is not an error, so say nothing rather than shout.
	if v.CanPlay() {
		devs, err := s.svc.Devices(ctx, v.Name, id)
		if err != nil {
			if c.Err == "" {
				c.Err = "Sunshine did not hand over the device list: " + err.Error()
			}
		} else {
			c.Devices = devs
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
