package sunshine

import (
	"encoding/json"
	"testing"
)

// Sunshine's `status` is a JSON boolean on some endpoints and the string
// "true" on others, and has swapped between versions. Read live from the
// lab's console on 2026-08-25, /api/clients/list answered `"status":true` —
// decoding that into a string fails, and a successful pairing would have
// told the person it had failed.
func TestStatusReadsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"status":true}`, true},
		{`{"status":false}`, false},
		{`{"status":"true"}`, true},
		{`{"status":"TRUE"}`, true},
		{`{"status":"false"}`, false},
	} {
		var out struct {
			Status flexBool `json:"status"`
		}
		if err := json.Unmarshal([]byte(tc.body), &out); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if bool(out.Status) != tc.want {
			t.Fatalf("%s = %v, want %v", tc.body, bool(out.Status), tc.want)
		}
	}
}

// The device list is the shape the live console returned.
func TestDeviceListShape(t *testing.T) {
	const live = `{"named_certs":[{"enabled":true,"name":"TCL-couch","uuid":"ADE8B583-966F-2208-2303-FF8125B26A9B"}],"status":true}`
	var raw struct {
		NamedCerts []Device `json:"named_certs"`
		Clients    []Device `json:"clients"`
	}
	if err := json.Unmarshal([]byte(live), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.NamedCerts) != 1 || raw.NamedCerts[0].Name != "TCL-couch" || raw.NamedCerts[0].UUID == "" {
		t.Fatalf("parsed %+v", raw.NamedCerts)
	}
}
