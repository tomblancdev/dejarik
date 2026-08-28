package wolf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A fake engine: the handful of endpoints the client uses, with the shapes
// read from the real one (its OpenAPI schema and its source).
func fakeEngine(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /serverinfo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><root status_code="200"><hostname>wolf</hostname></root>`))
	})
	mux.HandleFunc("GET /api/v1/pair/pending", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "requests": []map[string]string{{"pair_secret": "s3cr3t", "client_ip": "203.0.113.7"}}})
	})
	mux.HandleFunc("GET /api/v1/clients", func(w http.ResponseWriter, _ *http.Request) {
		// the same certificate twice, the way the engine's file can hold it
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "clients": []map[string]any{
			{"client_id": "111", "app_state_folder": "someone", "settings": map[string]any{"run_uid": 3001, "run_gid": 3000}},
			{"client_id": "111", "app_state_folder": "someone", "settings": map[string]any{"run_uid": 3001, "run_gid": 3000}},
			{"client_id": "222", "app_state_folder": "9876543210", "settings": map[string]any{"run_uid": 3999, "run_gid": 3000}},
		}})
	})
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "sessions": []map[string]any{
			{"client_ip": "203.0.113.7", "video_width": 2560, "video_height": 1440, "video_refresh_rate": 60, "app_id": "178625061", "client_id": "5551", "client_settings": map[string]any{"run_uid": 3001, "run_gid": 3000}},
		}})
	})
	mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "apps": []map[string]any{{"title": "Steam", "id": "178625061"}}})
	})
	record := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_path"] = r.URL.Path
		calls = append(calls, body)
		if body["pin"] == "0000" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "Invalid pair secret"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}
	for _, p := range []string{"/api/v1/pair/client", "/api/v1/clients/settings", "/api/v1/unpair/client", "/api/v1/sessions/stop"} {
		mux.HandleFunc("POST "+p, record)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestReadsTheEngine(t *testing.T) {
	srv, calls := fakeEngine(t)
	c := New(srv.URL+"/serverinfo", srv.URL, time.Second)
	ctx := context.Background()

	if !c.Answering(ctx) {
		t.Fatal("the probe answered with a hostname and was read as silent")
	}
	pend, err := c.Pending(ctx)
	if err != nil || len(pend) != 1 || pend[0].Secret != "s3cr3t" {
		t.Fatalf("pending = %+v, %v", pend, err)
	}
	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("a certificate held twice must list once: %+v", devs)
	}
	if devs[0].Folder != "someone" || devs[0].UID != 3001 || devs[1].UID != 3999 {
		t.Fatalf("devices = %+v", devs)
	}
	ss, err := c.Sessions(ctx)
	if err != nil || len(ss) != 1 || ss[0].ID != "5551" || ss[0].UID != 3001 || ss[0].AppID != "178625061" || ss[0].Width != 2560 {
		t.Fatalf("sessions = %+v, %v", ss, err)
	}
	apps, err := c.Apps(ctx)
	if err != nil || len(apps) != 1 || apps[0].Title != "Steam" {
		t.Fatalf("apps = %+v, %v", apps, err)
	}

	if err := c.Pair(ctx, "s3cr3t", "1234"); err != nil {
		t.Fatal(err)
	}
	if err := c.Point(ctx, "222", "someone", 3001, 3000); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "5551"); err != nil {
		t.Fatal(err)
	}
	if err := c.Unpair(ctx, "222"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls = %+v", *calls)
	}
	pt := (*calls)[1]
	if pt["_path"] != "/api/v1/clients/settings" || pt["client_id"] != "222" || pt["app_state_folder"] != "someone" {
		t.Fatalf("point = %+v", pt)
	}
	if st := pt["settings"].(map[string]any); st["run_uid"].(float64) != 3001 || st["run_gid"].(float64) != 3000 {
		t.Fatalf("point settings = %+v", st)
	}
	if (*calls)[2]["session_id"] != "5551" {
		t.Fatalf("stop = %+v", (*calls)[2])
	}
}

func TestTheEnginesOwnWordsComeBack(t *testing.T) {
	srv, _ := fakeEngine(t)
	c := New(srv.URL+"/serverinfo", srv.URL, time.Second)
	err := c.Pair(context.Background(), "s3cr3t", "0000")
	if err == nil || err.Error() != "the engine said: Invalid pair secret" {
		t.Fatalf("err = %v", err)
	}
}

func TestSilentWhenDown(t *testing.T) {
	c := New("http://127.0.0.1:9/serverinfo", "http://127.0.0.1:9", 200*time.Millisecond)
	if c.Answering(context.Background()) {
		t.Fatal("nothing listens there")
	}
	if _, err := c.Devices(context.Background()); err == nil {
		t.Fatal("an unreachable engine must be an error, not an empty list")
	}
}
