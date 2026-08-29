package web

import "net/http"

// The spec is hand-written and lives beside the handlers, the way Le
// Veilleur's does: no codegen, and it is reviewed in the same diff as the
// thing it describes. The paths under "reserved" are declared but not
// implemented — they say where the product is going without pretending to
// be there (games.md: library, sessions, saves, external accounts).
func (s *Server) openapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{
  "openapi": "3.1.0",
  "info": {
    "title": "Dejarik",
    "summary": "The arcade's panel: what can I play, wake it, and my paired devices.",
    "description": "Identity comes from the proxy (Remote-User / Remote-Groups) for people and from a bearer token for machines. Every card on the panel renders from one of these paths, so an automation can do anything a person can.",
    "version": "` + s.version + `",
    "license": {"name": "MIT"}
  },
  "servers": [{"url": "` + s.cfg.BaseURL + `"}],
  "components": {
    "securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}},
    "schemas": {
      "Truth": {
        "type": "object",
        "description": "One source's answer, and whether it answered at all.",
        "properties": {"known": {"type": "boolean"}, "ok": {"type": "boolean"}, "says": {"type": "string"}}
      },
      "Link": {
        "type": "object",
        "description": "One machine in the wake chain, parents first.",
        "properties": {"name": {"type": "string"}, "label": {"type": "string"}, "up": {"type": "boolean"}, "known": {"type": "boolean"}, "busy": {"type": "string"}}
      },
      "Project": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "label": {"type": "string"},
          "engine": {"type": "string", "enum": ["sunshine", "wolf"], "description": "What answers Moonlight here: a console's Sunshine, or an appliance's seat engine (a seat per person, each in its own drawer)."},
          "hand_started": {"type": "boolean", "description": "The watchman does not know this project: it is on when somebody started it, and nothing here can wake it."},
          "state": {
            "type": "string",
            "enum": ["ready", "starting", "asleep", "blocked", "unknown"],
            "description": "ready: the engine answered, play now. starting: the chain is coming up, or it is up and the engine is not yet. asleep: nothing running (on a hand-started project: off). blocked: a person put a hands-off hold on it at the watchman. unknown: we cannot tell, and say so."
          },
          "reason": {"type": "string", "description": "The state in words, for a person."},
          "detail": {"type": "string"},
          "up_for": {"type": "string"},
          "chain": {"type": "array", "items": {"$ref": "#/components/schemas/Link"}},
          "connect": {"type": "object", "properties": {"host": {"type": "string"}, "tcp": {"type": "array", "items": {"type": "integer"}}, "udp": {"type": "array", "items": {"type": "string"}}}},
          "wait_minutes": {"type": "integer", "description": "How long the machine waits for you after a wake before it may stop."},
          "fault": {"type": "string", "description": "The last wake attempt failed. Independent of state: the machine may be asleep and retryable."},
          "watchman": {"$ref": "#/components/schemas/Truth", "description": "Le Veilleur: should this be on?"},
          "play": {"$ref": "#/components/schemas/Truth", "description": "The engine: would Moonlight get a reply right now?"}
        }
      },
      "Client": {
        "type": "object",
        "properties": {"uuid": {"type": "string"}, "name": {"type": "string"}, "by": {"type": "string", "description": "Who paired it. Empty when nobody claimed it."}, "for": {"type": "string", "description": "An appliance: the drawer this device is pointed at — the person whose home, account and saves it opens. Empty until an admin points it."}, "label": {"type": "string"}, "shared": {"type": "boolean", "description": "Pointed at a shared drawer (a living-room device: everybody's)."}, "pointed": {"type": "boolean"}, "mine": {"type": "boolean"}, "since": {"type": "string", "format": "date-time"}}
      },
      "Seat": {
        "type": "object",
        "description": "One open session on an appliance, joined to its drawer by the uid it runs as.",
        "properties": {"id": {"type": "string"}, "app": {"type": "string"}, "app_id": {"type": "string"}, "person": {"type": "string", "description": "The drawer. Empty for a device nobody has pointed yet."}, "label": {"type": "string"}, "shared": {"type": "boolean"}, "device": {"type": "string", "description": "The address the seat streams to — all the engine says of it."}, "mode": {"type": "string"}, "since": {"type": "string", "format": "date-time"}, "mine": {"type": "boolean"}}
      },
      "Room": {
        "type": "object",
        "description": "One open room on an appliance: a game other devices may join (the engine's lobby), opened from Le Foyer by a drawer.",
        "properties": {"id": {"type": "string"}, "app": {"type": "string"}, "app_id": {"type": "string"}, "person": {"type": "string", "description": "The drawer that opened it."}, "label": {"type": "string"}, "shared": {"type": "boolean"}, "locked": {"type": "boolean", "description": "The engine asks its PIN of whoever joins or closes it."}, "in": {"type": "integer", "description": "Sessions in it right now."}, "since": {"type": "string", "format": "date-time"}, "mine": {"type": "boolean"}}
      },
      "Refusal": {
        "type": "object",
        "description": "The last time the guard closed a second seat on a drawer already in use — one drawer, one open seat.",
        "properties": {"at": {"type": "string", "format": "date-time"}, "person": {"type": "string"}, "words": {"type": "string"}}
      },
      "LinkStatus": {
        "type": "object",
        "description": "One drawer's link to an external account (a companion beside its seats needs it — Le Juke's Spotify). The appliance's last word, plus what waits for it.",
        "properties": {"reported": {"type": "boolean", "description": "The appliance has spoken about this link at least once."}, "linked": {"type": "boolean"}, "reported_at": {"type": "string", "format": "date-time"}, "pending": {"type": "boolean", "description": "A token waits for the appliance (it is woken if asleep)."}, "unlinking": {"type": "boolean"}}
      },
      "Link": {
        "type": "object",
        "properties": {"link": {"type": "string", "description": "The companion's name on the appliance (spotify)."}, "label": {"type": "string"}, "kind": {"type": "string"}, "drawer": {"type": "string"}, "drawer_label": {"type": "string"}, "shared": {"type": "boolean"}, "mine": {"type": "boolean"}, "status": {"$ref": "#/components/schemas/LinkStatus"}, "word": {"type": "string", "enum": ["linked", "not linked", "linking", "unlinking", "unknown"]}, "at": {"type": "string"}}
      },
      "Error": {"type": "object", "properties": {"error": {"type": "string"}}}
    }
  },
  "security": [{"bearer": []}],
  "paths": {
    "/api/me": {"get": {"summary": "Who the caller is and what they may do.", "responses": {"200": {"description": "the caller"}}}},
    "/api/projects": {"get": {"summary": "Every project and its state.", "responses": {"200": {"description": "the projects", "content": {"application/json": {"schema": {"type": "object", "properties": {"projects": {"type": "array", "items": {"$ref": "#/components/schemas/Project"}}}}}}}}}},
    "/api/projects/{name}": {"get": {"summary": "One project.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "the project", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Project"}}}}, "404": {"description": "no such project"}}}},
    "/api/projects/{name}/play": {"post": {"summary": "I want to play.", "description": "Idempotent. Raises the project and everything it needs through Le Veilleur, and returns without waiting: the wake carries on if the caller goes away. It holds nothing up — what keeps the machine on afterwards is a live Moonlight connection.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "already ready", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Project"}}}}, "202": {"description": "the chain is being raised", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Project"}}}}, "404": {"description": "no such project"}, "409": {"description": "the watchman refused — a hands-off hold", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}}}},
    "/api/projects/{name}/clients": {
      "get": {"summary": "Paired devices. A player sees their own; an admin sees all.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "the devices", "content": {"application/json": {"schema": {"type": "object", "properties": {"clients": {"type": "array", "items": {"$ref": "#/components/schemas/Client"}}}}}}}, "502": {"description": "Sunshine did not answer"}}},
      "post": {"summary": "Pair a device by relaying the PIN Moonlight showed.", "description": "On an appliance the new device is then POINTED at a drawer: the caller's own, or — an admin only — somebody else's or a shared one (the for field). From its next connection it opens that person's home and runs as their uid.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["pin", "device"], "properties": {"pin": {"type": "string", "pattern": "^[0-9]{4}$"}, "device": {"type": "string"}, "for": {"type": "string", "description": "The drawer (appliance). Empty = the caller's own."}}}}}}, "responses": {"201": {"description": "paired"}, "400": {"description": "bad PIN, the engine is off, no such drawer, or not yours to point at"}}}
    },
    "/api/projects/{name}/clients/{uuid}": {"delete": {"summary": "Unpair a device.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "uuid", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"204": {"description": "unpaired"}, "403": {"description": "not yours"}}}},
    "/api/projects/{name}/clients/{uuid}/point": {"post": {"summary": "Point a paired device at a drawer (appliance; admins).", "description": "How the TV becomes the house's, or a device paired before this program existed becomes somebody's.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "uuid", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["for"], "properties": {"for": {"type": "string"}}}}}}, "responses": {"200": {"description": "pointed"}, "403": {"description": "not an admin, no such drawer, or not an appliance"}}}},
    "/api/projects/{name}/seats": {"get": {"summary": "Open seats on an appliance: yours and the shared ones; all for an admin.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "the seats, and the last refusal if any", "content": {"application/json": {"schema": {"type": "object", "properties": {"seats": {"type": "array", "items": {"$ref": "#/components/schemas/Seat"}}, "refusal": {"$ref": "#/components/schemas/Refusal"}}}}}}}}},
    "/api/projects/{name}/seats/{id}/stop": {"post": {"summary": "Close a seat: yours, or any for an admin.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"204": {"description": "closed"}, "403": {"description": "not yours"}}}},
    "/api/projects/{name}/rooms": {"get": {"summary": "Open rooms on an appliance — a public thing in the house, everybody sees every one.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "the rooms", "content": {"application/json": {"schema": {"type": "object", "properties": {"rooms": {"type": "array", "items": {"$ref": "#/components/schemas/Room"}}}}}}}}}},
    "/api/projects/{name}/rooms/{id}/stop": {"post": {"summary": "Close a room: the drawer that opened it, or an admin.", "description": "Everybody in it is switched back to their own seat and the game is ended. A locked room whose PIN this program does not remember (it opened before a restart) needs the PIN in the body.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": false, "content": {"application/json": {"schema": {"type": "object", "properties": {"pin": {"type": "string", "pattern": "^[0-9]{4}$"}}}}}}, "responses": {"204": {"description": "closed"}, "403": {"description": "not yours, or the PIN"}}}},
    "/api/projects/{name}/links": {"get": {"summary": "The caller's links: an external account tied to their drawer (every drawer's, for an admin).", "description": "A link is the file a COMPANION beside the drawer's seats needs — Le Juke's Spotify receiver, music under whatever that drawer plays. The word is the appliance's last report.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "the links", "content": {"application/json": {"schema": {"type": "object", "properties": {"links": {"type": "array", "items": {"$ref": "#/components/schemas/Link"}}}}}}}, "404": {"description": "no such project"}}}},
    "/api/projects/{name}/links/sync": {"post": {"summary": "The appliance's turn: report who is linked, take what is pending. From the appliance's addresses only.", "description": "Every few seconds the appliance says which drawers hold each companion's file, and receives the tokens people obtained on their phones (each handed once, never logged) and the drawers to unlink. The Foyer's sources are the lock; the vhost never reaches this.", "security": [], "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"linked": {"type": "object", "additionalProperties": {"type": "array", "items": {"type": "string"}}, "description": "companion name -> the drawers that hold its file"}}}}}}, "responses": {"200": {"description": "what is pending", "content": {"application/json": {"schema": {"type": "object", "properties": {"pending": {"type": "array", "items": {"type": "object", "properties": {"sidecar": {"type": "string"}, "drawer": {"type": "string"}, "token": {"type": "string"}}}}, "unlink": {"type": "array", "items": {"type": "object", "properties": {"sidecar": {"type": "string"}, "drawer": {"type": "string"}}}}}}}}}, "403": {"description": "not the appliance"}, "404": {"description": "no such project, or nothing to link on it"}}}},
    "/links/{name}/{sidecar}/start": {"get": {"summary": "Link an account to a drawer — a person's tap, not for automations.", "description": "Sends the person to the provider (Authorization Code with PKCE, the house's own app, no secret). 'for' names the drawer: your own; an admin, anybody's or a shared one. The provider sends the person back to /links/callback; the token then waits in memory for the appliance, which is woken if asleep. The panel's page shows the result.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "sidecar", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "for", "in": "query", "required": false, "schema": {"type": "string"}}], "responses": {"302": {"description": "to the provider"}, "403": {"description": "not your drawer"}, "404": {"description": "nothing of that name to link"}}}},
    "/foyer/{name}": {"get": {"summary": "Le Foyer: the page in the stream — not for automations.", "description": "Read by the kiosk browser of the hub tile's seat. It authenticates nobody the usual way: the caller is the seat, identified by the session id the engine handed it (checked against the engine's live sessions) and by where it comes from (the appliance's addresses). The page is a shell drawn by its script from /foyer/{name}/state (the same session; polled), with a house game's artwork at /foyer/{name}/icon/{app}; its verbs (/open, /join, /stop under this path) take the same session and answer JSON when asked for it. The vhost never reaches any of it (wrong source).", "security": [], "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "session", "in": "query", "required": true, "schema": {"type": "string"}}, {"name": "caps", "in": "query", "required": false, "schema": {"type": "string"}, "description": "The seat's video buffer caps, base64url — what a room's compositor is made with."}], "responses": {"200": {"description": "the page"}, "403": {"description": "not from a seat"}, "404": {"description": "no such project, or no foyer on it"}}}},
    "/healthz": {"get": {"summary": "Liveness. A watchman outage is a degraded page, not a dead one.", "security": [], "responses": {"200": {"description": "ok"}}}},
    "/metrics": {"get": {"summary": "Prometheus text exposition, labelled by project.", "security": [], "responses": {"200": {"description": "metrics"}}}}
  },
  "x-reserved": {
    "description": "Declared, not implemented. Where the panel is going: the library from the storage's gamelists, saves. (Live sessions landed as /seats; external accounts as /links.)",
    "paths": ["/api/projects/{name}/library", "/api/projects/{name}/saves"]
  }
}
`))
}
