package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushReq builds a multipart push: files by name -> content, and a shelf.
func pushReq(path, system string, files map[string]string) *http.Request {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if system != "" {
		_ = w.WriteField("system", system)
	}
	for name, content := range files {
		fw, _ := w.CreateFormFile("file", name)
		_, _ = io.WriteString(fw, content)
	}
	_ = w.Close()
	r := httptest.NewRequest(http.MethodPost, path, &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

func TestLibraryPushIsAnAdminsAndLandsOnTheShelf(t *testing.T) {
	s, _, _, _ := wolfServerS(t)
	h := s.Handler()
	lib := s.libs["wolf"].Path()

	// a player is refused
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "", map[string]string{"Game.sfc": "ROM"}), "someone", "players"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("player push: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(lib, "snes", "Game.sfc")); err == nil {
		t.Fatal("the player's file landed")
	}

	// an admin's lands, the shelf from the name
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "", map[string]string{"Game.sfc": "ROM"}), "admin", "admins"))
	if w.Code != http.StatusCreated {
		t.Fatalf("admin push: %d %s", w.Code, w.Body.String())
	}
	var res pushResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Added) != 1 || res.Added[0].System != "snes" || res.Added[0].File != "Game.sfc" || res.Added[0].Bytes != 3 || len(res.Refused) != 0 {
		t.Fatalf("result: %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(lib, "snes", "Game.sfc")); err != nil || string(b) != "ROM" {
		t.Fatalf("on the shelf: %q %v", b, err)
	}

	// again: refused, never replaced
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "", map[string]string{"Game.sfc": "NEW"}), "admin", "admins"))
	if w.Code != http.StatusConflict {
		t.Fatalf("second push: %d %s", w.Code, w.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(lib, "snes", "Game.sfc")); string(b) != "ROM" {
		t.Fatalf("replaced: %q", b)
	}

	// a disc image needs its shelf named; with one, a cue and its bin land together
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "", map[string]string{"Disc.cue": "cue"}), "admin", "admins"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "choose the shelf") {
		t.Fatalf("a cue with no shelf: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "psx", map[string]string{"Disc.cue": "cue", "Disc (Track 1).bin": "bin"}), "admin", "admins"))
	if w.Code != http.StatusCreated {
		t.Fatalf("psx push: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Added) != 2 {
		t.Fatalf("cue+bin: %+v", res)
	}
	// the wrong kind of file for a shelf is refused, the right one beside it lands
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "nes", map[string]string{"Other.nes": "x", "Wrong.sfc": "y"}), "admin", "admins"))
	if w.Code != http.StatusCreated {
		t.Fatalf("mixed push: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Added) != 1 || res.Added[0].File != "Other.nes" || len(res.Refused) != 1 || res.Refused[0].File != "Wrong.sfc" {
		t.Fatalf("mixed: %+v", res)
	}

	// the shelves, and one shelf's titles
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/library", nil), "someone", "players"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"system":"snes"`) || !strings.Contains(w.Body.String(), `"ready":true`) {
		t.Fatalf("shelves: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/library?system=psx", nil), "someone", "players"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Disc.cue"`) {
		t.Fatalf("titles: %d %s", w.Code, w.Body.String())
	}

	// metrics count it
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{`dejarik_library_store_ready{project="wolf"} 1`, `dejarik_library_added_total{project="wolf"} 4`, `dejarik_library_titles{project="wolf"} 4`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func TestLibraryDetectAndThePanel(t *testing.T) {
	s, _, _, _ := wolfServerS(t)
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/library/detect?file=Mario.sfc", nil), "someone", "players"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"system":"snes"`) {
		t.Fatalf("detect sfc: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/library/detect?file=FF7.iso", nil), "someone", "players"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"candidates"`) || strings.Contains(w.Body.String(), `"system"`) {
		t.Fatalf("detect iso: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/library/detect?file=notes.txt", nil), "someone", "players"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("detect txt: %d", w.Code)
	}

	// the panel: everybody sees the shelves, only an admin gets the push
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/", nil), "someone", "players"))
	body := w.Body.String()
	if !strings.Contains(body, "library // the house store") || strings.Contains(body, "push a rom onto its shelf") {
		t.Fatalf("player's page: has library=%v has push=%v", strings.Contains(body, "library // the house store"), strings.Contains(body, "push a rom"))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/", nil), "admin", "admins"))
	body = w.Body.String()
	if !strings.Contains(body, "push a rom onto its shelf") || !strings.Contains(body, `hx-encoding="multipart/form-data"`) || !strings.Contains(body, `<option value="snes">`) {
		t.Fatalf("admin's page lacks the push form")
	}

	// the panel's form: the same push, then the block with the words
	r := pushReq("/library/wolf", "", map[string]string{"Kart.z64": "n64"})
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "admin", "admins"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "On the shelf: n64/Kart.z64") {
		t.Fatalf("panel push: %d %s", w.Code, w.Body.String()[:min(400, len(w.Body.String()))])
	}
	if _, err := os.Stat(filepath.Join(s.libs["wolf"].Path(), "n64", "Kart.z64")); err != nil {
		t.Fatal("the panel's push did not land")
	}
}

func TestLibraryAwayIsRefusedNotEmpty(t *testing.T) {
	s, _, _, _ := wolfServerS(t)
	h := s.Handler()
	// take the store away
	if err := os.RemoveAll(s.libs["wolf"].Path()); err != nil {
		t.Fatal(err)
	}
	s.libs["wolf"].Refresh()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(pushReq("/api/projects/wolf/library", "", map[string]string{"Game.sfc": "ROM"}), "admin", "admins"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("push on an away store: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, as(httptest.NewRequest(http.MethodGet, "/", nil), "admin", "admins"))
	if !strings.Contains(w.Body.String(), "The store is away") {
		t.Fatal("the page does not say the store is away")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), `dejarik_library_store_ready{project="wolf"} 0`) {
		t.Fatal("metrics do not say the store is away")
	}
}
