package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/tomblancdev/dejarik/internal/auth"
	"github.com/tomblancdev/dejarik/internal/library"
)

// The library — the house store, written from the panel (library.go in the
// domain says how). An admin drops a file, it streams straight onto its
// shelf as the store's owner, and every seat sees it at its next scan.
//
//	GET  /api/projects/{name}/library            the shelves (everybody); ?system= lists one shelf's titles
//	GET  /api/projects/{name}/library/detect     ?file= → the shelf a name belongs on, or the candidates
//	POST /api/projects/{name}/library            ADMINS: multipart — `system` (a shelf, or empty = from the name), `file` (one or many)
//	POST /library/{name}                         the panel's form (the same push, then the block)
//
// A push is multipart streamed part by part: nothing is buffered, a 1 G
// disc image costs a 1 G disk write and nothing else. Every file answers
// for itself — landed, or refused with the reason — so a cue and its bins
// travel together and a wrong one does not sink the rest.

type shelfVM struct {
	System string
	Label  string
	Titles int
	Size   string
	Known  bool
}

type systemOpt struct {
	Name  string
	Label string
}

type libraryVM struct {
	Ready   bool
	Why     string
	Path    string
	Shelves []shelfVM
	Titles  int
	Size    string
	// Systems: every shelf ES-DE names, for the push form's selector
	Systems []systemOpt
	Project string
}

func libraryData(l *library.Store, project string) libraryVM {
	shelves, ready, why := l.Shelves()
	vm := libraryVM{Ready: ready, Why: why, Path: l.Path(), Project: project}
	var bytes int64
	for _, sh := range shelves {
		vm.Shelves = append(vm.Shelves, shelfVM{System: sh.System, Label: sh.Label, Titles: sh.Titles, Size: library.Human(sh.Bytes), Known: sh.Known})
		vm.Titles += sh.Titles
		bytes += sh.Bytes
	}
	vm.Size = library.Human(bytes)
	for _, sy := range library.Systems() {
		vm.Systems = append(vm.Systems, systemOpt{Name: sy.Name, Label: sy.Label})
	}
	return vm
}

// libraryOf resolves a project's store, or writes the refusal.
func (s *Server) libraryOf(w http.ResponseWriter, r *http.Request) (string, *library.Store, bool) {
	name := r.PathValue("name")
	if _, ok := s.cfg.Projects[name]; !ok {
		fail(w, http.StatusNotFound, "no such project")
		return name, nil, false
	}
	l, ok := s.libs[name]
	if !ok {
		fail(w, http.StatusNotFound, "this project has no library")
		return name, nil, false
	}
	return name, l, true
}

func (s *Server) apiLibrary(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.who(w, r); !ok {
		return
	}
	_, l, ok := s.libraryOf(w, r)
	if !ok {
		return
	}
	if sys := strings.TrimSpace(r.FormValue("system")); sys != "" {
		titles, err := l.Titles(sys)
		switch {
		case errors.Is(err, library.ErrNoSystem):
			fail(w, http.StatusNotFound, err.Error())
		case err != nil:
			fail(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeJSON(w, http.StatusOK, map[string]any{"system": strings.ToLower(strings.TrimSpace(sys)), "titles": titles})
		}
		return
	}
	shelves, ready, why := l.Shelves()
	out := map[string]any{"ready": ready, "path": l.Path(), "shelves": shelves, "systems": library.Systems()}
	if why != "" {
		out["why"] = why
	}
	writeJSON(w, http.StatusOK, out)
}

// apiLibraryDetect names the shelf a file belongs on, from its name alone —
// what the form asks before it uploads anything.
func (s *Server) apiLibraryDetect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.who(w, r); !ok {
		return
	}
	_, l, ok := s.libraryOf(w, r)
	if !ok {
		return
	}
	sy, cands, err := l.Detect(r.FormValue("file"))
	out := map[string]any{"file": r.FormValue("file")}
	if len(cands) > 0 {
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.Name)
		}
		out["candidates"] = names
	}
	switch {
	case err == nil:
		out["system"] = sy.Name
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, library.ErrAmbiguous):
		out["error"] = err.Error()
		writeJSON(w, http.StatusOK, out)
	default:
		out["error"] = err.Error()
		writeJSON(w, http.StatusNotFound, out)
	}
}

// pushResult is what a push answers: every file for itself.
type pushResult struct {
	Added   []library.Added `json:"added"`
	Refused []refusedFile   `json:"refused,omitempty"`
}

type refusedFile struct {
	File string `json:"file"`
	Why  string `json:"why"`
}

// push streams the request's parts onto the shelves. Admins only: the
// store is the house's, and a title on it is on every seat.
func (s *Server) push(r *http.Request, name string, l *library.Store, id auth.Identity) (pushResult, error) {
	var res pushResult
	if !id.IsAdmin() {
		return res, errors.New("only an admin adds to the house store")
	}
	if !l.Ready() {
		l.Refresh()
		if !l.Ready() {
			return res, library.ErrAway
		}
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		return res, errors.New("a push is multipart/form-data: a `file` per title, `system` for the shelf")
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	system := ""
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("the push broke off: %w", err)
		}
		switch part.FormName() {
		case "system":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			system = strings.ToLower(strings.TrimSpace(string(b)))
			if system == "auto" {
				system = ""
			}
		case "file":
			fname := part.FileName()
			if strings.TrimSpace(fname) == "" {
				_, _ = io.Copy(io.Discard, part)
				continue
			}
			shelf := system
			if shelf == "" {
				sy, _, err := l.Detect(fname)
				if err != nil {
					res.Refused = append(res.Refused, refusedFile{File: fname, Why: err.Error()})
					_, _ = io.Copy(io.Discard, part)
					continue
				}
				shelf = sy.Name
			}
			added, err := l.Add(shelf, fname, part)
			if err != nil {
				s.log.Warn("rom refused", "project", name, "system", shelf, "file", fname, "by", id.User, "err", err)
				res.Refused = append(res.Refused, refusedFile{File: fname, Why: err.Error()})
				_, _ = io.Copy(io.Discard, part)
				continue
			}
			s.log.Info("rom added", "project", name, "system", added.System, "file", added.File, "bytes", added.Bytes, "by", id.User)
			s.svc.Store().Event("rom", id.User, name+"/"+added.System+"/"+added.File)
			res.Added = append(res.Added, added)
		default:
			_, _ = io.Copy(io.Discard, part)
		}
	}
	if len(res.Added) == 0 && len(res.Refused) == 0 {
		return res, errors.New("nothing to push: no `file` part")
	}
	return res, nil
}

func (s *Server) apiLibraryPush(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name, l, ok := s.libraryOf(w, r)
	if !ok {
		return
	}
	res, err := s.push(r, name, l, id)
	switch {
	case err != nil && !id.IsAdmin():
		s.log.Warn("push refused", "project", name, "by", id.User, "err", err)
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, library.ErrAway):
		fail(w, http.StatusServiceUnavailable, err.Error())
	case err != nil:
		fail(w, http.StatusBadRequest, err.Error())
	case len(res.Added) == 0:
		writeJSON(w, http.StatusConflict, res)
	default:
		writeJSON(w, http.StatusCreated, res)
	}
}

// libraryPush is the panel's form: the same push, then the block with the
// words.
func (s *Server) libraryPush(w http.ResponseWriter, r *http.Request) {
	id, ok := s.who(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	l, has := s.libs[name]
	var errMsg, notice string
	if !has {
		errMsg = "this project has no library"
	} else {
		res, err := s.push(r, name, l, id)
		if err != nil {
			s.log.Warn("push refused", "project", name, "by", id.User, "err", err)
			errMsg = err.Error()
		} else {
			errMsg, notice = pushWords(res)
		}
	}
	s.afterClients(w, r, name, id, errMsg, notice)
}

// pushWords tells a push's result in a line or two.
func pushWords(res pushResult) (errMsg, notice string) {
	if len(res.Added) > 0 {
		var parts []string
		for _, a := range res.Added {
			parts = append(parts, a.System+"/"+a.File+" ("+library.Human(a.Bytes)+")")
		}
		notice = "On the shelf: " + strings.Join(parts, ", ") + ". Every seat sees it at its next scan — in RetroDECK, Utilities → Rescan ROM directory, or restart it."
	}
	if len(res.Refused) > 0 {
		var parts []string
		for _, f := range res.Refused {
			parts = append(parts, f.File+": "+f.Why)
		}
		errMsg = "Refused — " + strings.Join(parts, "; ")
	}
	return errMsg, notice
}

// libraryMetrics: the store's counters, per project.
func (s *Server) libraryMetrics() string {
	if len(s.libs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# HELP dejarik_library_store_ready Whether the library's store answered at the last read (the mount is there).\n# TYPE dejarik_library_store_ready gauge\n")
	for n, l := range s.libs {
		v := 0
		if l.Ready() {
			v = 1
		}
		fmt.Fprintf(&b, "dejarik_library_store_ready{project=%q} %d\n", n, v)
	}
	b.WriteString("# HELP dejarik_library_titles Titles on the shelves, as last read.\n# TYPE dejarik_library_titles gauge\n")
	for n, l := range s.libs {
		shelves, _, _ := l.Shelves()
		t := 0
		for _, sh := range shelves {
			t += sh.Titles
		}
		fmt.Fprintf(&b, "dejarik_library_titles{project=%q} %d\n", n, t)
	}
	b.WriteString("# HELP dejarik_library_added_total Titles pushed onto a shelf since start.\n# TYPE dejarik_library_added_total counter\n")
	for n, l := range s.libs {
		a, _ := l.Counters()
		fmt.Fprintf(&b, "dejarik_library_added_total{project=%q} %d\n", n, a)
	}
	b.WriteString("# HELP dejarik_library_bytes_total Bytes written onto the shelves since start.\n# TYPE dejarik_library_bytes_total counter\n")
	for n, l := range s.libs {
		_, by := l.Counters()
		fmt.Fprintf(&b, "dejarik_library_bytes_total{project=%q} %d\n", n, by)
	}
	return b.String()
}

// unused guard: keep json imported for the API shapes above
var _ = json.Marshal
