package web

import (
	"context"
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

// The links — an external account, tied to a drawer with a tap (links.go in
// the domain says why). The panel is the broker: the grant lives with the
// person at the identity gateway, and the appliance asks here for a token.
//
//	GET  /links/{name}/{sidecar}/start?for=   a person (own drawer; an admin any) → the provider
//	GET  /links/callback?code&state           the provider sends the person back: the grant is stored
//	POST /unlink/{name}   link, for           the grant is forgotten
//	POST /api/projects/{name}/links/sync      THE APPLIANCE: which drawers are linked, per link
//	GET  /api/projects/{name}/links/{sidecar}/token?drawer=   THE APPLIANCE: an hour's access token
//	GET  /api/projects/{name}/links           the caller's links (every drawer's, for an admin)
//	GET  /foyer/{name}/links/{sidecar}/qr     in the stream: the QR the phone scans (→ /start for the seat's drawer)
//	POST /foyer/{name}/links/{sidecar}/unlink in the stream: the seat's own drawer
//
// The appliance's two verbs answer only from its addresses (the Foyer's
// sources), like the Foyer itself: the row is the lock. A token is never
// logged.

// shelf adapts the store to the hub's shelf for shared drawers.
type shelf struct{ st *store.Store }

func (s shelf) SetGrant(k string, g links.Grant) error {
	return s.st.SetGrant(k, store.Grant{RefreshToken: g.RefreshToken, ClientID: g.ClientID, Scopes: g.Scopes, Since: g.Since, By: g.By})
}

func (s shelf) GetGrant(k string) (links.Grant, bool) {
	g, ok := s.st.GetGrant(k)
	if !ok {
		return links.Grant{}, false
	}
	return links.Grant{RefreshToken: g.RefreshToken, ClientID: g.ClientID, Scopes: g.Scopes, Since: g.Since, By: g.By}, true
}

func (s shelf) DeleteGrant(k string) error { return s.st.DeleteGrant(k) }

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
	name, sidecar, p, l, ok := s.linkOf(w, r)
	if !ok {
		return
	}
	drawer, err := s.svc.DrawerOf(name, r.FormValue("for"), id)
	if err != nil {
		s.log.Warn("link refused", "project", name, "link", sidecar, "by", id.User, "for", r.FormValue("for"), "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	state, verifier := s.hub.Begin(name, sidecar, drawer, id.User, p.People[drawer].Shared)
	u, err := links.AuthorizeURL(l.Kind, l.ClientID, s.redirectURI(), state, links.Challenge(verifier), l.Scopes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("link started", "project", name, "link", sidecar, "drawer", drawer, "by", id.User)
	http.Redirect(w, r, u, http.StatusFound)
}

// linkCallback is where the provider sends the person back. The code is
// traded for tokens; the refresh token — the grant — goes to the person's
// record at the gateway (a shared drawer's to the panel's own store).
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
	back := strings.TrimRight(s.cfg.BaseURL, "/") + "/"
	if e := r.FormValue("error"); e != "" {
		s.log.Warn("link refused by the provider", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "err", e)
		http.Error(w, l.Label+" said: "+e+" — nothing was linked. Back to the panel: "+back, http.StatusBadRequest)
		return
	}
	tok, refresh, err := s.hub.Exchange(r.Context(), l.Kind, l.ClientID, s.redirectURI(), r.FormValue("code"), st.Verifier)
	if err != nil {
		s.log.Warn("link failed at the provider", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "err", err)
		http.Error(w, "the code could not be traded for a token: "+err.Error(), http.StatusBadGateway)
		return
	}
	g := links.Grant{RefreshToken: refresh, ClientID: l.ClientID, Scopes: l.Scopes, Since: time.Now(), By: id.User}
	if err := s.hub.Link(r.Context(), st.Project, st.Sidecar, st.Drawer, st.Shared, g, tok); err != nil {
		s.log.Error("the grant could not be stored", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "err", err)
		http.Error(w, "linked at "+l.Label+", but the grant could not be kept: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("linked", "project", st.Project, "link", st.Sidecar, "drawer", st.Drawer, "by", id.User, "shared", st.Shared)
	s.svc.Store().Event("link", id.User, st.Project+"/"+st.Sidecar+" -> "+st.Drawer)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// unlink forgets a drawer's grant, from the panel.
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
	if err == nil {
		err = s.hub.Unlink(r.Context(), name, sidecar, drawer, p.People[drawer].Shared)
	}
	if err != nil {
		s.log.Warn("unlink refused", "project", name, "link", sidecar, "by", id.User, "for", r.FormValue("for"), "err", err)
		errMsg = err.Error()
	} else {
		s.log.Info("unlinked", "project", name, "link", sidecar, "drawer", drawer, "by", id.User)
		s.svc.Store().Event("unlink", id.User, name+"/"+sidecar+" -> "+drawer)
		notice = "Unlinked " + p.Links[sidecar].Label + ". A seat already open keeps its music until it closes."
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

// fromAppliance is the lock on the appliance's two verbs.
func (s *Server) fromAppliance(w http.ResponseWriter, r *http.Request, name string) (config.Project, bool) {
	p, ok := s.cfg.Projects[name]
	if !ok || !p.HasLinks() {
		fail(w, http.StatusNotFound, "no such project, or nothing to link on it")
		return p, false
	}
	if !s.svc.FromAppliance(name, remoteIP(r)) {
		s.log.Debug("links refused", "project", name, "from", r.RemoteAddr)
		fail(w, http.StatusForbidden, "the appliance asks here, nobody else")
		return p, false
	}
	return p, true
}

// apiLinksSync tells the appliance which drawers are linked, per link.
func (s *Server) apiLinksSync(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.fromAppliance(w, r, name)
	if !ok {
		return
	}
	linked := map[string][]string{}
	for _, sidecar := range p.LinkNames() {
		linked[sidecar] = []string{}
		for _, d := range p.Drawers() {
			if s.hub.Status(r.Context(), name, sidecar, d, p.People[d].Shared).Linked {
				linked[sidecar] = append(linked[sidecar], d)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": linked})
}

// apiLinkToken hands the appliance an access token for a drawer's link —
// from the broker's cache, or a refresh of the grant. Never logged.
func (s *Server) apiLinkToken(w http.ResponseWriter, r *http.Request) {
	name, sidecar := r.PathValue("name"), r.PathValue("sidecar")
	p, ok := s.fromAppliance(w, r, name)
	if !ok {
		return
	}
	if _, has := p.Links[sidecar]; !has {
		fail(w, http.StatusNotFound, "nothing called "+sidecar+" to link here")
		return
	}
	drawer := strings.TrimSpace(r.FormValue("drawer"))
	person, isDrawer := p.Drawer(drawer)
	if !isDrawer {
		fail(w, http.StatusNotFound, "no drawer called "+drawer)
		return
	}
	tok, err := s.hub.AccessToken(r.Context(), name, sidecar, drawer, person.Shared)
	switch {
	case errors.Is(err, links.ErrNotLinked):
		fail(w, http.StatusNotFound, drawer+" is not linked to "+sidecar)
	case err != nil:
		s.log.Warn("no token for the appliance", "project", name, "link", sidecar, "drawer", drawer, "err", err)
		fail(w, http.StatusBadGateway, err.Error())
	default:
		s.log.Info("token handed to the appliance", "project", name, "link", sidecar, "drawer", drawer, "good_for", time.Until(tok.Expires).Round(time.Second).String())
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok.Access, "expires_in": int(time.Until(tok.Expires).Seconds())})
	}
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
	Since       string       `json:"since,omitempty"`
}

// linkStates lists the links the caller may see: their own drawer's; every
// drawer's for an admin.
func (s *Server) linkStates(ctx context.Context, name string, id auth.Identity) []linkVM {
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
			st := s.hub.Status(ctx, name, sidecar, d, person.Shared)
			vm := linkVM{Sidecar: sidecar, Label: l.Label, Kind: l.Kind, Drawer: d, DrawerLabel: person.Label, Shared: person.Shared, Mine: mine, Status: st, Word: st.Word()}
			if st.Linked && !st.Since.IsZero() {
				vm.Since = st.Since.Local().Format("2006-01-02 15:04")
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
	out := s.linkStates(r.Context(), name, id)
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

func (s *Server) foyerLinks(ctx context.Context, name string, g arcade.Guest) []linkCard {
	p := s.cfg.Projects[name]
	if !g.Known() || !p.HasLinks() {
		return nil
	}
	var out []linkCard
	for _, sidecar := range p.LinkNames() {
		st := s.hub.Status(ctx, name, sidecar, g.Person, g.Shared)
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
	if err := s.hub.Unlink(r.Context(), name, sidecar, g.Person, g.Shared); err != nil {
		s.foyerAfter(w, r, name, g, "the grant could not be forgotten: "+err.Error(), "")
		return
	}
	s.log.Info("unlinked", "project", name, "link", sidecar, "drawer", g.Person, "by", "foyer:"+g.Device)
	s.svc.Store().Event("unlink", g.Person, name+"/"+sidecar+" (from the foyer)")
	s.foyerAfter(w, r, name, g, "", fmt.Sprintf("Unlinked %s. A seat already open keeps its music until it closes.", l.Label))
}
