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

// fromSeat is a request the way a seat's page sends it: from the appliance,
// asking for JSON.
func fromSeat(method, path string, form url.Values) *http.Request {
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, path+"?"+form.Encode(), nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("Accept", "application/json")
	r.RemoteAddr = "203.0.113.23:40000"
	return r
}

func seat(session string) url.Values {
	return url.Values{"session": {session}, "caps": {base64.RawURLEncoding.EncodeToString([]byte("video/x-raw(memory:DMABuf), format=(string)RGBA"))}}
}

type stateJSON struct {
	Known bool `json:"known"`
	Guest struct {
		Person, Label string
		Shared        bool
	} `json:"guest"`
	Rooms []struct {
		ID, App, Person string
		Locked, Mine    bool
		In              int
	} `json:"rooms"`
	House []struct {
		ID, Title, Room, Busy string
	} `json:"house_shelf"`
	Notice string `json:"notice"`
	Error  string `json:"error"`
}

func state(t *testing.T, h http.Handler, session string) stateJSON {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/state", seat(session)))
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	var st stateJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// verb posts one of the Foyer's verbs and returns the status and the answer.
func verb(h http.Handler, session, v string, kv ...string) (int, stateJSON) {
	form := seat(session)
	for i := 0; i+1 < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/"+v, form))
	var st stateJSON
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	return w.Code, st
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
	// the page is a shell carrying the session for its script
	w = httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf", seat("111")))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-session="111"`) || !strings.Contains(w.Body.String(), "foyer.js") {
		t.Fatalf("the shell: %d %s", w.Code, w.Body.String()[:300])
	}
}

func TestFoyerKnowsWhoYouAreFromThePairing(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3999)}
	f.mu.Unlock()

	st := state(t, h, "111")
	if !st.Known || st.Guest.Person != "someone" || len(st.House) != 2 {
		t.Fatalf("someone's state: %+v", st)
	}
	for _, x := range st.House {
		if x.ID == hub {
			t.Fatal("the hub offers a room on itself")
		}
	}
	// a device nobody pointed: told so, offered nothing
	st = state(t, h, "222")
	if st.Known || st.Guest.Person != "" {
		t.Fatalf("nobody's state: %+v", st)
	}
	code, ans := verb(h, "222", "open", "app", retro)
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "nobody's") {
		t.Fatalf("nobody opened a room: %d %+v", code, ans)
	}
}

func TestOpenARoomJoinItStopIt(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3002)}
	f.mu.Unlock()

	// someone opens a room on RetroDECK
	code, ans := verb(h, "111", "open", "app", retro)
	if code != http.StatusOK || !strings.Contains(ans.Notice, "Room open on RetroDECK") {
		t.Fatalf("open: %d %+v", code, ans)
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
	st := state(t, h, "222")
	if len(st.Rooms) != 1 || st.Rooms[0].Person != "someone" || st.Rooms[0].In != 1 || st.Rooms[0].Mine {
		t.Fatalf("the other's rooms: %+v", st.Rooms)
	}
	code, ans = verb(h, "222", "join", "room", "lobby-1")
	if code != http.StatusOK || !strings.Contains(ans.Notice, "Joining RetroDECK") {
		t.Fatalf("join: %d %+v", code, ans)
	}
	f.mu.Lock()
	in := f.lobbies[0]["connected_sessions"].([]string)
	f.mu.Unlock()
	if len(in) != 2 || in[1] != "222" {
		t.Fatalf("in the room: %v", in)
	}
	// joining twice is refused in words, not sent to the engine
	code, ans = verb(h, "222", "join", "room", "lobby-1")
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "in a room already") {
		t.Fatalf("a second join: %d %+v", code, ans)
	}

	// the opener's state shows the room as theirs, on the house shelf too
	st = state(t, h, "111")
	if len(st.Rooms) != 1 || !st.Rooms[0].Mine {
		t.Fatalf("the opener's rooms: %+v", st.Rooms)
	}
	var retroShelf string
	for _, x := range st.House {
		if x.ID == retro {
			retroShelf = x.Room
		}
	}
	if retroShelf != "lobby-1" {
		t.Fatalf("the house shelf does not point at the opener's room: %+v", st.House)
	}
	// the other may not stop it; the opener may
	code, ans = verb(h, "222", "stop", "room", "lobby-1")
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "who opened it") {
		t.Fatalf("the other stopped it: %d %+v", code, ans)
	}
	code, ans = verb(h, "111", "stop", "room", "lobby-1")
	if code != http.StatusOK || !strings.Contains(ans.Notice, "Room closed") {
		t.Fatalf("the opener could not stop it: %d %+v", code, ans)
	}
	f.mu.Lock()
	n := len(f.lobbies)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d rooms left", n)
	}
	// the metrics counted it all
	w := httptest.NewRecorder()
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
	st := state(t, h, "111")
	var busy string
	for _, x := range st.House {
		if x.ID == retro {
			busy = x.Busy
		}
	}
	if !strings.Contains(busy, "already open for someone on 203.0.113.35 as a tile") {
		t.Fatalf("the busy home is not said: %+v", st.House)
	}
	code, ans := verb(h, "111", "open", "app", retro)
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "quit it there") {
		t.Fatalf("the open was not refused: %d %+v", code, ans)
	}
	f.mu.Lock()
	created := len(f.created)
	f.mu.Unlock()
	if created != 0 {
		t.Fatal("a room was opened on a home in use")
	}
	// Steam is free — but a seat with no caps would make a room that shows nothing
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodPost, "/foyer/wolf/open", url.Values{"session": {"111"}, "app": {"178625061"}}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "video caps") {
		t.Fatalf("no caps, no refusal: %d %s", w.Code, w.Body.String())
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
	f.sessions = append(f.sessions, session("113", retro, "203.0.113.32", 3001))
	f.mu.Unlock()
	tick(now.Add(20 * time.Second))
	read()
	f.mu.Lock()
	f.sessions = append(f.sessions, session("115", retro, "203.0.113.34", 3001))
	f.mu.Unlock()
	tick(now.Add(30 * time.Second))
	read()
	f.mu.Lock()
	stops = len(f.stops)
	f.mu.Unlock()
	if stops != 1 {
		t.Fatalf("the guard forgot the tiles: %v", f.stops)
	}
}

func TestALockedRoomAsksItsPIN(t *testing.T) {
	h, f, _, tick := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001), session("222", hub, "203.0.113.31", 3002)}
	f.mu.Unlock()

	code, ans := verb(h, "111", "open", "app", retro, "pin", "12")
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "four digits") {
		t.Fatalf("a two-digit PIN: %d %+v", code, ans)
	}
	code, _ = verb(h, "111", "open", "app", retro, "pin", "4321")
	if code != http.StatusOK {
		t.Fatalf("open locked: %d", code)
	}
	f.mu.Lock()
	if len(f.created) != 1 || f.created[0]["pin"] == nil {
		t.Fatalf("the room is not locked: %v", f.created)
	}
	f.mu.Unlock()

	// the other: no PIN, wrong PIN, right PIN
	st := state(t, h, "222")
	if len(st.Rooms) != 1 || !st.Rooms[0].Locked {
		t.Fatalf("the other's rooms: %+v", st.Rooms)
	}
	code, ans = verb(h, "222", "join", "room", "lobby-1")
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "four digits") {
		t.Fatalf("no PIN: %d %+v", code, ans)
	}
	code, ans = verb(h, "222", "join", "room", "lobby-1", "pin", "0000")
	if code != http.StatusBadRequest || !strings.Contains(ans.Error, "wrong PIN") {
		t.Fatalf("a wrong PIN: %d %+v", code, ans)
	}
	code, _ = verb(h, "222", "join", "room", "lobby-1", "pin", "4321")
	if code != http.StatusOK {
		t.Fatalf("the right PIN: %d", code)
	}
	f.mu.Lock()
	in := f.lobbies[0]["connected_sessions"].([]string)
	f.lobbies[0]["connected_sessions"] = []string{"222"} // the opener left
	f.mu.Unlock()
	if len(in) != 2 {
		t.Fatalf("the right PIN did not open the door: %v", in)
	}
	// the opener comes back to their own locked room: no wheels, the panel remembers
	tick(time.Date(2026, 1, 1, 20, 0, 5, 0, time.UTC)) // past the panel's 2 s cache: the engine's list is read again
	code, ans = verb(h, "111", "join", "room", "lobby-1")
	if code != http.StatusOK {
		t.Fatalf("the opener back in: %d %+v", code, ans)
	}

	// an admin on the panel closes it without the PIN: this program remembers it
	w := post(h, "/api/projects/wolf/rooms/lobby-1/stop", "", "boss", "admins")
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
	w := post(h, "/api/projects/wolf/rooms/L1/stop", "", "someone", "players")
	if w.Code != http.StatusForbidden {
		t.Fatalf("someone stopped other's room: %d", w.Code)
	}
	w = post(h, "/api/projects/wolf/rooms/L1/stop", "", "other", "players")
	if w.Code != http.StatusNoContent {
		t.Fatalf("the opener could not stop it: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	f.lobbies = []map[string]any{{"id": "L2", "name": "Steam", "multi_user": true, "started_by_profile_id": "salon", "pin_required": true, "stop_when_everyone_leaves": false, "connected_sessions": []string{"a"}}}
	f.mu.Unlock()
	r = httptest.NewRecorder()
	h.ServeHTTP(r, as(httptest.NewRequest(http.MethodGet, "/panel/wolf", nil), "boss", "admins"))
	if !strings.Contains(r.Body.String(), "opened by le salon") || !strings.Contains(r.Body.String(), "locked") || !strings.Contains(r.Body.String(), "/room-stop/wolf") {
		t.Fatalf("the panel: %s", r.Body.String())
	}
}

func TestArtworkIsFetchedOnceAndKept(t *testing.T) {
	h, f, _, _ := wolfServer(t)
	f.mu.Lock()
	f.sessions = []map[string]any{session("111", hub, "203.0.113.30", 3001)}
	f.mu.Unlock()
	hits := 0
	art := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNG!"))
	}))
	defer art.Close()
	f.mu.Lock()
	f.iconURL = art.URL + "/retrodeck.png"
	f.mu.Unlock()
	_ = state(t, h, "111") // reads the apps
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/icon/"+retro, seat("111")))
		if w.Code != http.StatusOK || w.Body.String() != "PNG!" || w.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("icon: %d %s", w.Code, w.Body.String())
		}
	}
	if hits != 1 {
		t.Fatalf("the artwork was fetched %d times", hits)
	}
	// a game with no artwork
	w := httptest.NewRecorder()
	h.ServeHTTP(w, fromSeat(http.MethodGet, "/foyer/wolf/icon/178625061", seat("111")))
	if w.Code != http.StatusNotFound {
		t.Fatalf("no artwork: %d", w.Code)
	}
}
