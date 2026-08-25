package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tomblancdev/dejarik/internal/arcade"
)

// The JSON API. Two nouns: projects and their clients; one verb: play.
// Every card on the panel renders from one of these, so anything a person
// can do here, a script can do too — which is the point (a Home Assistant
// button is a token and one POST).

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": id.User, "groups": id.Groups, "role": id.Role, "via": id.Via, "admin": id.IsAdmin(),
	})
}

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.who(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.svc.Views(r.Context())})
}

func (s *Server) apiProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.who(w, r); !ok {
		return
	}
	v, found := s.svc.View(r.Context(), r.PathValue("name"))
	if !found {
		fail(w, http.StatusNotFound, "no such project")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// apiPlay is idempotent: asking to play something already up is a no-op that
// still answers with the view, so a client never has to check first.
func (s *Server) apiPlay(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	v, err := s.svc.Play(r.Context(), r.PathValue("name"), id)
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "project": v})
	default:
		code := http.StatusAccepted
		if v.State == arcade.Ready {
			code = http.StatusOK
		}
		writeJSON(w, code, v)
	}
}

func (s *Server) apiClients(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	devs, err := s.svc.Devices(r.Context(), r.PathValue("name"), id)
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		fail(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"clients": devs})
	}
}

func (s *Server) apiPair(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	var body struct {
		PIN    string `json:"pin"`
		Device string `json:"device"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.PIN == "" {
		body.PIN, body.Device = r.FormValue("pin"), r.FormValue("device")
	}
	err := s.svc.Pair(r.Context(), r.PathValue("name"), body.PIN, body.Device, id)
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		fail(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"device": body.Device, "by": id.User})
	}
}

func (s *Server) apiUnpair(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	err := s.svc.Unpair(r.Context(), r.PathValue("name"), r.PathValue("uuid"), id)
	switch {
	case errors.Is(err, arcade.ErrNoProject):
		fail(w, http.StatusNotFound, "no such project")
	case err != nil:
		fail(w, http.StatusForbidden, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
