package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/tomblancdev/dejarik/internal/arcade"
	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/config"
	"github.com/tomblancdev/dejarik/internal/links"
	"github.com/tomblancdev/dejarik/internal/store"
)

// The links — an external account, linked to a drawer with a tap (links.go
// in the domain says why). Three faces:
//
//	GET  /links/{name}/{sidecar}/start?for=   a person (own drawer; an admin any) → the provider
//	GET  /links/callback?code&state           the provider sends the person back; the token waits
//	POST /unlink/{name}   link, for           queue the drawer's unlink
//	POST /api/projects/{name}/links/sync      THE APPLIANCE: reports who is linked, takes what is pending
//	GET  /api/projects/{name}/links           the caller's links (every drawer's, for an admin)
//	GET  /foyer/{name}/links/{sidecar}/qr     in the stream: the QR the phone scans (→ /start for the seat's drawer)
//	POST /foyer/{name}/links/{sidecar}/unlink in the stream: the seat's own drawer
//
// The sync answers only from the appliance's addresses (the Foyer's
// sources), like the Foyer itself: the row is the lock. A token is handed
// once and never logged.

// memory adapts the store to the hub's memory of the appliance's reports.
type memory struct{ st *store.Store }

func (m memory) SetLinked(project, sidecar string, drawers []string, at time.Time) error {
	return m.st.SetLinked(project, sidecar, drawers, at)
}

func (m memory) Linked() map[string]links.Report {
	out := map[string]links.Report{}
	for k, v := range m.st.LinkedReports() {
		out[k] = links.Report{Drawers: v.Drawers, At: v.At}
	}
	return out
}

func (s *Server) redirectURI() string { return strings.TrimRight(s.cfg.BaseURL, "/") + "/links/callback" }

// linkOf resolves a project's link by the path, or writes the refusal.
func (s *Server) linkOf(w http.ResponseWriter, r *http.Request) (string, string, config.Project, config.Link, bool) {
	name, sidecar := r.PathValue("name"), r.PathValue("sidecar")
	p, ok := s.cfg.Projects[name]
	if !ok {
		http.NotFound(w, r)
		return name, sidecar, p, config.Link{}, false
	}
	l, ok := p.Links[sidecar]
	if !ok {
		label := p.Label
		if label == "" {
			label = name
		}
		http.Error(w, "nothing called "+sidecar+" to link on "+label, http.StatusNotFound)
		return name, sidecar, p, l, false
	}
	return name, sidecar, p, l, true
}

// linkStart sends the person to the provider, for a drawer they may act on.
func (s *Server) linkStart(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name, sidecar, _, l, ok := s.linkOf(w, r)
	if !ok {
		return
	}
	drawer, err := s.svc.DrawerOf(name, r.FormValue("for"), id)
	if err != nil {
		s.log.Warn("link refused", "project", name, "link", sidecar, "by", id.User, "for", r.FormValue("for"), "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	state, verifier := s.hub.Begin(name, sidecar, drawer, id.User)
	u, err := links.AuthorizeURL(l.Kind, l.ClientID, s.redirectURI(), state, links.Challenge(verifier), l.Scopes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("link started", "project", name, "link", sidecar, "drawer", drawer, "by", id.User)
	http.Redirect(w, r, u, http.StatusFound)
}

// linkCallback is where the provider sends the person back. The code is
// traded for a token, which then waits for the appliance — woken if asleep,
// since nothing else would ever take it.
func (s *Server) linkCallback(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	st, ok := s.hub.Finish(r.FormValue("state"))
	if !ok {
		http.Error(w, "this link request is unknown or older than ten minutes — start again from the panel", http.StatusBadRequest)
		return
	}
	p := s.cfg.Projects[st.Project]
	l := p.Links[st.Sidecar]
	if e := r.FormValue("error"); e != "" {
		s.log.Warn("link refused by the provider", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "err", e)
		http.Error(w, l.Label+" said: "+e+" — nothing was linked. Back to the panel: "+strings.TrimRight(s.cfg.BaseURL, "/")+"/", http.StatusBadRequest)
		return
	}
	token, ttl, err := s.hub.Exchange(r.Context(), l.Kind, l.ClientID, s.redirectURI(), r.FormValue("code"), st.Verifier)
	if err != nil {
		s.log.Warn("link failed at the provider", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "err", err)
		http.Error(w, "the code could not be traded for a token: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.hub.Add(links.Pending{Project: st.Project, Sidecar: st.Sidecar, Drawer: st.Drawer, Token: token, By: id.User, Expires: time.Now().Add(ttl - 30*time.Second)})
	s.log.Info("link pending — the appliance takes it at its next report", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "token_ttl", ttl.String())
	s.svc.Store().Event("link", id.User, st.Project+"/"+st.Sidecar+" -> "+st.Drawer)
	s.wakeForLinks(r, st.Project, id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// wakeForLinks raises the appliance when something waits for it: a token
// parked in memory expires within the hour, and only the appliance can
// take it. Idempotent, like /play.
func (s *Server) wakeForLinks(r *http.Request, name string, id auth.Identity) {
	p := s.cfg.Projects[name]
	if p.HandStarted() || !s.hub.Waiting(name) {
		return
	}
	if v, found := s.svc.View(r.Context(), name); found && v.State != arcade.Ready {
		if _, err := s.svc.Play(r.Context(), name, id); err != nil {
			s.log.Warn("the appliance could not be woken for a pending link", "project", name, "err", err)
		}
	}
}

// unlink queues a drawer's unlink from the panel.
func (s *Server) unlink(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var errMsg, notice string
	sidecar := strings.TrimSpace(r.FormValue("link"))
	p, found := s.cfg.Projects[name]
	if _, has := p.Links[sidecar]; !found || !has {
		http.NotFound(w, r)
		return
	}
	drawer, err := s.svc.DrawerOf(name, r.FormValue("for"), id)
	if err != nil {
		s.log.Warn("unlink refused", "project", name, "link", sidecar, "by", id.User, "for", r.FormValue("for"), "err", err)
		errMsg = err.Error()
	} else {
		s.hub.QueueUnlink(name, sidecar, drawer)
		s.log.Info("unlink queued", "project", name, "link", sidecar, "drawer", drawer, "by", id.User)
		s.svc.Store().Event("unlink", id.User, name+"/"+sidecar+" -> "+drawer)
		s.wakeForLinks(r, name, id)
		notice = "Unlinking " + p.Links[sidecar].Label + " — the appliance removes it at its next report (seconds when it is on)."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

// apiLinksSync is the appliance's turn: it says which drawers hold each
// companion's file and takes what is pending. From the Foyer's sources only.
func (s *Server) apiLinksSync(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.cfg.Projects[name]
	if !ok || !p.HasLinks() {
		fail(w, http.StatusNotFound, "no such project, or nothing to link on it")
		return
	}
	if !s.svc.FromAppliance(name, remoteIP(r)) {
		s.log.Debug("links sync refused", "project", name, "from", r.RemoteAddr)
		fail(w, http.StatusForbidden, "the appliance reports here, nobody else")
		return
	}
	var body struct {
		Linked map[string][]string `json:"linked"`
	}
	if err := decodeJSON(r, &body); err != nil {
		fail(w, http.StatusBadRequest, "a body like {\"linked\": {\"spotify\": [\"someone\"]}}")
		return
	}
	s.hub.Report(name, body.Linked)
	pending, unlinks := s.hub.Take(name)
	for _, x := range pending {
		s.log.Info("link handed to the appliance", "project", name, "link", x.Sidecar, "drawer", x.Drawer, "by", x.By)
	}
	for _, x := range unlinks {
		s.log.Info("unlink handed to the appliance", "project", name, "link", x.Sidecar, "drawer", x.Drawer)
	}
	if pending == nil {
		pending = []links.Pending{}
	}
	if unlinks == nil {
		unlinks = []links.Unlink{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "unlink": unlinks})
}

// linkVM is one drawer's link, for the panel and the API.
type linkVM struct {
	Sidecar     string       `json:"link"`
	Label       string       `json:"label"`
	Kind        string       `json:"kind"`
	Drawer      string       `json:"drawer"`
	DrawerLabel string       `json:"drawer_label"`
	Shared      bool         `json:"shared,omitempty"`
	Mine        bool         `json:"mine"`
	Status      links.Status `json:"status"`
	Word        string       `json:"word"`
	At          string       `json:"at,omitempty"`
}

// linkStates lists the links the caller may see: their own drawer's; every
// drawer's for an admin.
func (s *Server) linkStates(name string, id auth.Identity) []linkVM {
	p, ok := s.cfg.Projects[name]
	if !ok || !p.HasLinks() {
		return nil
	}
	var out []linkVM
	for _, sidecar := range p.LinkNames() {
		l := p.Links[sidecar]
		for _, d := range p.Drawers() {
			person := p.People[d]
			mine := strings.EqualFold(d, id.User)
			if !id.IsAdmin() && !mine {
				continue
			}
			st := s.hub.Status(name, sidecar, d)
			vm := linkVM{Sidecar: sidecar, Label: l.Label, Kind: l.Kind, Drawer: d, DrawerLabel: person.Label, Shared: person.Shared, Mine: mine, Status: st, Word: st.Word()}
			if st.Reported {
				vm.At = st.ReportedAt.Local().Format("15:04")
			}
			out = append(out, vm)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mine && !out[j].Mine })
	return out
}

func (s *Server) apiLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if _, ok := s.cfg.Projects[name]; !ok {
		fail(w, http.StatusNotFound, "no such project")
		return
	}
	out := s.linkStates(name, id)
	if out == nil {
		out = []linkVM{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// --- in the stream ----------------------------------------------------------

// linkCard is one link on the Foyer's own shelf, for the guest's drawer.
type linkCard struct {
	Sidecar string       `json:"sidecar"`
	Label   string       `json:"label"`
	Word    string       `json:"word"`
	Status  links.Status `json:"status"`
	// QR is the path of the code the phone scans (the page appends its
	// session); it opens /start for this very drawer, behind the gateway.
	QR string `json:"qr"`
}

func (s *Server) foyerLinks(name string, g arcade.Guest) []linkCard {
	p := s.cfg.Projects[name]
	if !g.Known() || !p.HasLinks() {
		return nil
	}
	var out []linkCard
	for _, sidecar := range p.LinkNames() {
		st := s.hub.Status(name, sidecar, g.Person)
		out = append(out, linkCard{Sidecar: sidecar, Label: p.Links[sidecar].Label, Word: st.Word(), Status: st,
			QR: "/foyer/" + name + "/links/" + url.PathEscape(sidecar) + "/qr"})
	}
	return out
}

// foyerLinkQR draws the code the phone scans: the link's /start for the
// seat's own drawer, at the panel's public address — the person's phone,
// signed in at the gateway, is where the provider's page belongs.
func (s *Server) foyerLinkQR(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	sidecar := r.PathValue("sidecar")
	p := s.cfg.Projects[name]
	if _, has := p.Links[sidecar]; !has {
		http.NotFound(w, r)
		return
	}
	if !g.Known() {
		http.Error(w, "this device is nobody's yet", http.StatusForbidden)
		return
	}
	target := strings.TrimRight(s.cfg.BaseURL, "/") + "/links/" + url.PathEscape(name) + "/" + url.PathEscape(sidecar) + "/start?for=" + url.QueryEscape(g.Person)
	png, err := qrcode.Encode(target, qrcode.Medium, 360)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) foyerUnlink(w http.ResponseWriter, r *http.Request) {
	name, g, ok := s.guest(w, r)
	if !ok {
		return
	}
	sidecar := r.PathValue("sidecar")
	p := s.cfg.Projects[name]
	l, has := p.Links[sidecar]
	if !has {
		s.foyerAfter(w, r, name, g, "nothing called "+sidecar+" to unlink", "")
		return
	}
	if !g.Known() {
		s.foyerAfter(w, r, name, g, "this device is nobody's yet — an admin points it at a drawer on the panel first", "")
		return
	}
	s.hub.QueueUnlink(name, sidecar, g.Person)
	s.log.Info("unlink queued", "project", name, "link", sidecar, "drawer", g.Person, "by", "foyer:"+g.Device)
	s.svc.Store().Event("unlink", g.Person, name+"/"+sidecar+" (from the foyer)")
	s.foyerAfter(w, r, name, g, "", fmt.Sprintf("Unlinking %s — gone within seconds.", l.Label))
}

// errNoLinks is what a project without links answers.
var errNoLinks = errors.New("nothing to link on this project")
