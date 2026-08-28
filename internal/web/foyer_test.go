package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const hub = "900000001" // the Foyer tile's app id at the fake engine
const retro = "261696729"

// fromSeat is a request the way a seat's kiosk sends it: from the appliance.
func fromSeat(method, path string, form url.Values) *http.Request {
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, path+"?"+form.Encode(), nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.RemoteAddr = "203.0.113.23:40000"
	return r
}

func seat(session string) url.Values {
	return url.Values{"session": {session}, "caps": {base64.RawURLEncoding.EncodeToString([]byte("video/x-raw(memory:DMABuf), format=(string)RGBA"))}}
}

func TestFoyerIsReadFromASeatOnly(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001)}
	f.mu.Unlock()

	// through the proxy, with the strongest identity there is: still not a seat
	r := httptest.NewRequest(http.MethodGet, "/foyer/wolf?session=111", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(r, "boss", "admins"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("the proxy reached the foyer: %d %s", w.Code, w.Body.String())
	}
	// from the appliance, with a session the engine does not have: a page that says so
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("999")))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "NOT HERE") {
		t.Fatalf("a made-up session got: %d %s", w.Code, w.Body.String()[:200])
	}
	// no foyer on a project that has none
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/console", seat("111")))
	if w.Code != http.StatusNotFound {
		t.Fatalf("a project without a foyer answered %d", w.Code)
	}
}

func TestFoyerKnowsWhoYouAreFromThePairing(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3999)}
	f.mu.Unlock()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("111")))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "someone") || !strings.Contains(body, "OPEN A ROOM") {
		t.Fatalf("someone's page: %d %s", w.Code, body)
	}
	if strings.Contains(body, "Le Foyer</span>") && strings.Contains(body, `name="app" value="`+hub+`"`) {
		t.Fatal("the hub offers a room on itself")
	}
	// a device nobody pointed: told so, offered nothing
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("222")))
	body = w.Body.String()
	if !strings.Contains(body, "nobody's drawer") || strings.Contains(body, "OPEN A ROOM") {
		t.Fatalf("nobody's page: %s", body)
	}
}

func TestOpenARoomJoinItStopIt(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3002)}
	f.mu.Unlock()

	// someone opens a room on RetroDECK
	form := seat("111")
	form.Set("app", retro)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/open", form))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("open: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	if len(f.created) != 1 {
		t.Fatalf("created %d lobbies", len(f.created))
	}
	c := f.created[0]
	if c["runner_state_folder"] != "someone/RetroDECK" || c["profile_id"] != "someone" || c["name"] != "RetroDECK" || c["multi_user"] != true || c["stop_when_everyone_leaves"] != false || c["pin"] != nil {
		t.Fatalf("the room asked for: %v", c)
	}
	vs := c["video_settings"].(map[string]any)
	if vs["width"] != 1920.0 || vs["refresh_rate"] != 60.0 || vs["runner_render_node"] != "/dev/dri/renderD128" || vs["video_producer_buffer_caps"] != "video/x-raw(memory:DMABuf), format=(string)RGBA" {
		t.Fatalf("the room's picture: %v", vs)
	}
	if c["runner"].(map[string]any)["name"] != "borne" || c["audio_settings"].(map[string]any)["channel_count"] != 2.0 || c["client_settings"].(map[string]any)["run_uid"] != 3001.0 {
		t.Fatalf("runner/audio/settings: %v %v %v", c["runner"], c["audio_settings"], c["client_settings"])
	}
	if len(f.joined) != 1 || f.joined[0]["moonlight_session_id"] != "111" || f.joined[0]["lobby_id"] != "lobby-1" {
		t.Fatalf("the opener was not put in the room: %v", f.joined)
	}
	f.mu.Unlock()

	// the other sees it, and joins
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("222")))
	body := w.Body.String()
	if !strings.Contains(body, "opened by someone") || !strings.Contains(body, "1 in") || !strings.Contains(body, `name="room" value="lobby-1"`) || strings.Contains(body, "STOP") {
		t.Fatalf("the other's page: %s", body)
	}
	form = seat("222")
	form.Set("room", "lobby-1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/join", form))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	in := f.lobbies[0]["connected_sessions"].([]string)
	f.mu.Unlock()
	if len(in) != 2 || in[1] != "222" {
		t.Fatalf("in the room: %v", in)
	}
	// joining twice is refused in words, not sent to the engine
	w = httptest.NewRecorder()
	r := fromSeat(http.MethodPost, "/foyer/wolf/join", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "in a room already") {
		t.Fatalf("a second join: %s", w.Body.String())
	}

	// the opener's page shows STOP; the other may not stop it; the opener may
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("111")))
	if !strings.Contains(w.Body.String(), "STOP") || !strings.Contains(w.Body.String(), "JOIN YOUR ROOM") {
		t.Fatalf("the opener's page: %s", w.Body.String())
	}
	form = seat("222")
	form.Set("room", "lobby-1")
	w = httptest.NewRecorder()
	r = fromSeat(http.MethodPost, "/foyer/wolf/stop", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "who opened it") {
		t.Fatalf("the other stopped it: %s", w.Body.String())
	}
	form = seat("111")
	form.Set("room", "lobby-1")
	w = httptest.NewRecorder()
	r = fromSeat(http.MethodPost, "/foyer/wolf/stop", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "Room closed") {
		t.Fatalf("the opener could not stop it: %s", w.Body.String())
	}
	f.mu.Lock()
	n := len(f.lobbies)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d rooms left", n)
	}
	// the metrics counted it all
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	m := w.Body.String()
	for _, want := range []string{`dejarik_rooms_opened_total{project="wolf"} 1`, `dejarik_room_joins_total{project="wolf"} 2`, `dejarik_room_refusals_total{project="wolf"} 1`, `dejarik_room_stops_total{project="wolf"} 1`, `dejarik_rooms_open{project="wolf"} 0`} {
		if !strings.Contains(m, want) {
			t.Fatalf("metrics lack %q:\n%s", want, m)
		}
	}
}

func TestARoomNeedsTheSeatsCapsAndAFreeHome(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("333", retro, "203.0.113.35", 3001)}
	f.mu.Unlock()

	// RetroDECK is open for someone as a tile on another device: no room on it
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("111")))
	if !strings.Contains(w.Body.String(), "already open for someone on 203.0.113.35 as a tile") {
		t.Fatalf("the busy home is not said: %s", w.Body.String())
	}
	form := seat("111")
	form.Set("app", retro)
	w = httptest.NewRecorder()
	r := fromSeat(http.MethodPost, "/foyer/wolf/open", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "quit it there") {
		t.Fatalf("the open was not refused: %s", w.Body.String())
	}
	f.mu.Lock()
	created := len(f.created)
	f.mu.Unlock()
	if created != 0 {
		t.Fatal("a room was opened on a home in use")
	}
	// Steam is free — but a seat with no caps would make a room that shows nothing
	form = url.Values{"session": {"111"}, "app": {"178625061"}}
	w = httptest.NewRecorder()
	r = fromSeat(http.MethodPost, "/foyer/wolf/open", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "video caps") {
		t.Fatalf("no caps, no refusal: %s", w.Body.String())
	}
}

func TestTheHubIsExemptFromTheGuard(t *testing.T) {
	h, f, _, tick := wolfServer(t)
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	read := func() {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/seats", nil), "someone", "players"))
	}
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001)}
	f.mu.Unlock()
	read()
	// the same person's second device lands in the Foyer too — the normal
	// way into a room, not a clash
	f.mu.Lock()
	f.sessions = append(f.sessions, session("112", hub, "203.0.113.31", 3001))
	f.mu.Unlock()
	tick(now.Add(10 * time.Second))
	read()
	f.mu.Lock()
	stops := len(f.stops)
	f.mu.Unlock()
	if stops != 0 {
		t.Fatalf("the guard closed a seat on the hub: %v", f.stops)
	}
	// but two RetroDECKs on one drawer are still a clash
	f.mu.Lock()
	f.sessions = append(f.sessions, session("113", retro, "203.0.113.32", 3001), session("114", retro, "203.0.113.33", 3001))
	f.mu.Unlock()
	tick(now.Add(20 * time.Second))
	read()
	tick(now.Add(30 * time.Second))
	f.mu.Lock()
	f.sessions = append(f.sessions[:3], session("115", retro, "203.0.113.34", 3001))
	f.mu.Unlock()
	read()
	f.mu.Lock()
	stops = len(f.stops)
	f.mu.Unlock()
	if stops != 1 {
		t.Fatalf("the guard forgot the tiles: %v", f.stops)
	}
}

func TestALockedRoomAsksItsPIN(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3002)}
	f.mu.Unlock()

	form := seat("111")
	form.Set("app", retro)
	form.Set("pin", "12")
	w := httptest.NewRecorder()
	r := fromSeat(http.MethodPost, "/foyer/wolf/open", form)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "four digits") {
		t.Fatalf("a two-digit PIN: %s", w.Body.String())
	}
	form.Set("pin", "4321")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/open", form))
	f.mu.Lock()
	if len(f.created) != 1 || f.created[0]["pin"] == nil {
		t.Fatalf("the room is not locked: %v", f.created)
	}
	f.mu.Unlock()

	// the other: no PIN, wrong PIN, right PIN
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("222")))
	if !strings.Contains(w.Body.String(), "locked") || !strings.Contains(w.Body.String(), "data-wheel") {
		t.Fatalf("the other's page has no wheels: %s", w.Body.String())
	}
	join := seat("222")
	join.Set("room", "lobby-1")
	w = httptest.NewRecorder()
	r = fromSeat(http.MethodPost, "/foyer/wolf/join", join)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "four digits") {
		t.Fatalf("no PIN: %s", w.Body.String())
	}
	join.Set("pin", "0000")
	w = httptest.NewRecorder()
	r = fromSeat(http.MethodPost, "/foyer/wolf/join", join)
	r.Header.Set("HX-Request", "true")
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "wrong PIN") {
		t.Fatalf("a wrong PIN: %s", w.Body.String())
	}
	join.Set("pin", "4321")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/join", join))
	f.mu.Lock()
	in := f.lobbies[0]["connected_sessions"].([]string)
	f.mu.Unlock()
	if len(in) != 2 {
		t.Fatalf("the right PIN did not open the door: %v", in)
	}

	// an admin on the panel closes it without the PIN: this program remembers it
	w = post(h, "/api/projects/wolf/rooms/lobby-1/stop", "", "boss", "admins")
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin stop: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	if len(f.stopped) != 1 || f.stopped[0]["pin"] == nil {
		t.Fatalf("the remembered PIN was not sent: %v", f.stopped)
	}
	f.mu.Unlock()
}

func TestRoomsOnTheAPIAndThePanel(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.lobbies = []map[string]any{{"id": "L1", "name": "RetroDECK", "multi_user": true, "started_by_profile_id": "other", "pin_required": false, "stop_when_everyone_leaves": false, "connected_sessions": []string{"a", "b"}}}
	f.mu.Unlock()

	r := httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/api/projects/wolf/rooms", nil), "someone", "players"))
	var out struct {
		Rooms []struct {
			ID, App, Person string
			In              int
			Mine            bool
		}
	}
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if len(out.Rooms) != 1 || out.Rooms[0].Person != "other" || out.Rooms[0].In != 2 || out.Rooms[0].Mine {
		t.Fatalf("rooms: %s", r.Body.String())
	}
	// not yours to stop; yours as the opener; anyone's as an admin
	w := post(h, "/api/projects/wolf/rooms/L1/stop", "", "someone", "players")
	if w.Code != http.StatusForbidden {
		t.Fatalf("someone stopped other's room: %d", w.Code)
	}
	w = post(h, "/api/projects/wolf/rooms/L1/stop", "", "other", "players")
	if w.Code != http.StatusNoContent {
		t.Fatalf("the opener could not stop it: %d %s", w.Code, w.Body.String())
	}
	// the panel lists rooms
	f.mu.Lock()
	f.lobbies = []map[string]any{{"id": "L2", "name": "Steam", "multi_user": true, "started_by_profile_id": "salon", "pin_required": true, "stop_when_everyone_leaves": false, "connected_sessions": []string{"a"}}}
	f.mu.Unlock()
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/panel/wolf", nil), "boss", "admins"))
	if !strings.Contains(r.Body.String(), "opened by le salon") || !strings.Contains(r.Body.String(), "locked") || !strings.Contains(r.Body.String(), "/room-stop/wolf") {
		t.Fatalf("the panel: %s", r.Body.String())
	}
}
