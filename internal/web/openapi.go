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
          "state": {
            "type": "string",
            "enum": ["ready", "starting", "asleep", "blocked", "unknown"],
            "description": "ready: Sunshine answered, play now. starting: the chain is coming up, or it is up and Sunshine is not yet. asleep: nothing running. blocked: a person put a hands-off hold on it at the watchman. unknown: we cannot tell, and say so."
          },
          "reason": {"type": "string", "description": "The state in words, for a person."},
          "detail": {"type": "string"},
          "up_for": {"type": "string"},
          "chain": {"type": "array", "items": {"$ref": "#/components/schemas/Link"}},
          "connect": {"type": "object", "properties": {"host": {"type": "string"}, "tcp": {"type": "array", "items": {"type": "integer"}}, "udp": {"type": "array", "items": {"type": "string"}}}},
          "wait_minutes": {"type": "integer", "description": "How long the machine waits for you after a wake before it may stop."},
          "fault": {"type": "string", "description": "The last wake attempt failed. Independent of state: the machine may be asleep and retryable."},
          "watchman": {"$ref": "#/components/schemas/Truth", "description": "Le Veilleur: should this be on?"},
          "play": {"$ref": "#/components/schemas/Truth", "description": "Sunshine: would Moonlight get a reply right now?"}
        }
      },
      "Client": {
        "type": "object",
        "properties": {"uuid": {"type": "string"}, "name": {"type": "string"}, "by": {"type": "string", "description": "Who paired it. Empty when nobody claimed it."}, "mine": {"type": "boolean"}, "since": {"type": "string", "format": "date-time"}}
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
      "post": {"summary": "Pair a device by relaying the PIN Moonlight showed.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["pin", "device"], "properties": {"pin": {"type": "string", "pattern": "^[0-9]{4}$"}, "device": {"type": "string"}}}}}}, "responses": {"201": {"description": "paired"}, "400": {"description": "bad PIN, or the console is asleep"}}}
    },
    "/api/projects/{name}/clients/{uuid}": {"delete": {"summary": "Unpair a device.", "parameters": [{"name": "name", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "uuid", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"204": {"description": "unpaired"}, "403": {"description": "not yours"}}}},
    "/healthz": {"get": {"summary": "Liveness. A watchman outage is a degraded page, not a dead one.", "security": [], "responses": {"200": {"description": "ok"}}}},
    "/metrics": {"get": {"summary": "Prometheus text exposition, labelled by project.", "security": [], "responses": {"200": {"description": "metrics"}}}}
  },
  "x-reserved": {
    "description": "Declared, not implemented. Where the panel is going (docs/games.md): the library from the tank's ES-DE gamelists, live sessions, saves, linked external accounts.",
    "paths": ["/api/projects/{name}/library", "/api/projects/{name}/sessions", "/api/projects/{name}/saves", "/api/accounts"]
  }
}
`))
}
