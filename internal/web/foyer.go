package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/tomblancdev/dejarik/internal/arcade"
)

// Le Foyer — the page in the stream. No proxy, no headers, no login: the
// caller is a kiosk browser inside a seat on the appliance, and its
// identity is the session id the engine handed that seat (checked against
// the engine's live sessions) plus where the request comes from (the
// appliance's own addresses). See arcade/foyer.go.
//
//	GET  /foyer/{name}?session=&caps=   the page
//	GET  /foyer/{name}/panel            the polled block
//	POST /foyer/{name}/open   app, pin? the room is opened and the seat put in it
//	POST /foyer/{name}/join   room, pin?
//	POST /foyer/{name}/stop   room, pin?
//
// `caps` is the seat's WOLF_VIDEO_BUFFER_CAPS, base64url from the seat's
// start script (it holds spaces and parentheses), carried through every URL
// the page emits and decoded only when a room is opened.

type foyerVM struct {
	Project, Label, Title, House string
	Session, Caps                string
	Guest                        arcade.Guest
	Rooms                        []arcade.Room
	Shelf                        []arcade.Shelf
	Err, Notice, Words           string
	Version                      string
	PollSeconds                  int
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// guest resolves who is on the page, or writes the refusal. A session the
// engine does not have is answered with a page (the kiosk shows something
// readable), the other refusals with a status.
func (s *Server) guest(w http.ResponseWriter, r *http.Request) (string, arcade.Guest, bool) {
	name := r.PathValue("name")
	g, err := s.svc.Guest(r.Context(), name, r.FormValue("session"), remoteIP(r))
	switch {
	case errors.Is(err, arcade.ErrNoProject), errors.Is(err, arcade.ErrNoFoyer):
		http.NotFound(w, r)
		return name, g, false
	case errors.Is(err, arcade.ErrNotFromASeat):
		s.log.Warn("foyer refused", "project", name, "from", r.RemoteAddr, "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return name, g, false
	case err != nil:
		s.log.Warn("foyer refused", "project", name, "from", r.RemoteAddr, "session", r.FormValue("session"), "err", err)
		s.render(w, "foyer-gone", foyerVM{Project: name, Title: s.foyerTitle(name), House: s.cfg.House, Words: err.Error(), Version: s.version})
		return name, g, false
	}
	return name, g, true
}

func (s *Server) foyerTitle(name string) string {
	if p, ok := s.cfg.Projects[name]; ok && p.HasFoyer() {
		return p.Foyer.Title
	}
	return "Le Foyer"
}

func (s *Server) foyerData(ctx context.Context, name string, g arcade.Guest, r *http.Request, errMsg, notice string) foyerVM {
	p := s.cfg.Projects[name]
	vm := foyerVM{
		Project: name, Label: p.Label, Title: p.Foyer.Title, House: s.cfg.House,
		Session: g.Session, Caps: r.FormValue("caps"), Guest: g,
		Err: errMsg, Notice: notice, Version: s.version, PollSeconds: 3,
	}
	v, err := s.svc.Foyer(ctx, name, g)
	if err != nil && vm.Err == "" {
		vm.Err = err.Error()
	}
	vm.Rooms, vm.Shelf = v.Rooms, v.House
	return vm
}

// caps decodes what the seat's start script encoded; a value that is not
// base64url is taken as it is (a hand-typed URL).
func caps(v string) string {
	v = strings.TrimSpace(v)
	if b, err := base64.RawURLEncoding.DecodeString(v); err == nil {
		return string(b)
	}
	return v
}

// parsePIN turns "" into no PIN and four digits into one.
func parsePIN(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if len(v) != 4 || strings.Trim(v, "0123456789") != "" {
		return nil, fmt.Errorf("a PIN is four digits")
	}
	out := make([]int, 4)
	for i, c := range v {
		out[i] = int(c - '0')
	}
	return out, nil
}

func (s *Server) foyerPage(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	s.render(w, "foyer", s.foyerData(r.Context(), name, g, r, "", ""))
}

func (s *Server) foyerPanel(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	s.render(w, "foyer-panel", s.foyerData(r.Context(), name, g, r, "", ""))
}

// after answers a verb: the polled block for htmx, the page again otherwise.
func (s *Server) foyerAfter(w http.ResponseWriter, r *http.Request, name string, g arcade.Guest, errMsg, notice string) {
	if !htmx(r) {
		q := url.Values{"session": {g.Session}, "caps": {r.FormValue("caps")}}
		http.Redirect(w, r, "/foyer/"+name+"?"+q.Encode(), http.StatusSeeOther)
		return
	}
	s.render(w, "foyer-panel", s.foyerData(r.Context(), name, g, r, errMsg, notice))
}

func (s *Server) foyerOpen(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	pin, err := parsePIN(r.FormValue("pin"))
	if err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	room, err := s.svc.OpenRoom(r.Context(), name, g, r.FormValue("app"), caps(r.FormValue("caps")), pin)
	if err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	s.foyerAfter(w, r, name, g, "", "Room open on "+room.App+" — your picture switches to it now. Others find it under \"rooms open in the house\".")
}

func (s *Server) foyerJoin(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	pin, err := parsePIN(r.FormValue("pin"))
	if err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	room, err := s.svc.JoinRoom(r.Context(), name, g, r.FormValue("room"), pin)
	if err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	s.foyerAfter(w, r, name, g, "", "Joining "+room.App+" — your picture switches to the room now.")
}

func (s *Server) foyerStop(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	pin, err := parsePIN(r.FormValue("pin"))
	if err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	if err := s.svc.StopRoom(r.Context(), name, g, r.FormValue("room"), pin); err != nil {
		s.foyerAfter(w, r, name, g, err.Error(), "")
		return
	}
	s.foyerAfter(w, r, name, g, "", "Room closed. Everybody in it is back where they were.")
}

// --- the panel and the API: rooms seen from outside the stream ------------

func (s *Server) roomStop(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	pin, err := parsePIN(r.FormValue("pin"))
	if err == nil {
		err = s.svc.StopRoomBy(r.Context(), name, r.FormValue("room"), pin, id)
	}
	if err != nil {
		s.log.Warn("room stop refused", "project", name, "by", id.User, "room", r.FormValue("room"), "err", err)
		errMsg = err.Error()
	} else {
		notice = "Room closed."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

func (s *Server) apiRooms(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	rooms, err := s.svc.Rooms(r.Context(), r.PathValue("name"), id)
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		fail(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
	}
}

func (s *Server) apiRoomStop(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if r.Body != nil {
		_ = decodeJSON(r, &body)
	}
	if body.PIN == "" {
		body.PIN = r.FormValue("pin")
	}
	pin, err := parsePIN(body.PIN)
	if err == nil {
		err = s.svc.StopRoomBy(r.Context(), r.PathValue("name"), r.PathValue("id"), pin, id)
	}
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		s.log.Warn("room stop refused", "project", r.PathValue("name"), "by", id.User, "room", r.PathValue("id"), "err", err)
		fail(w, http.StatusForbidden, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
