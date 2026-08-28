<p align="center"><img src="ui/static/logo-animated.svg" alt="dejarik — let the wookiee win" width="640"></p>

# Dejarik

**The arcade's panel.** A person opens one page and gets three things: can I
play, wake it if not, and which of my devices are paired — and, on an
appliance with a seat per person, *who is in which seat*. It looks like a
starship control panel because that is what it is — instruments behind glass,
and one lit button that actually does something.

Named after the holochess table on the Millennium Falcon. Let the wookiee win.

## What it owns: almost nothing

Dejarik is a **face**, not machinery. Everything it shows belongs to
something else, and it says so:

| What | Who actually owns it |
|---|---|
| power — waking the tower and the console | [Le Veilleur](https://github.com/tomblancdev/veilleur) |
| pairing, streaming | the engine: Sunshine on a console, [Wolf](https://github.com/games-on-whales/wolf) on an appliance |
| who you are | the gateway (Authelia), through the reverse proxy |
| saves, the catalogue | the lab's storage |
| temporary firewall openings | [Le Videur](https://github.com/tomblancdev/videur) |

The one fact it keeps for itself: **who paired which device**, and the name
they gave it. Sunshine's pairing list is global — it knows a device called
`tom-phone` exists, not whose it is. That file is declared *replaceable*:
lose it and nobody loses a pairing, the devices just stop having an owner.

## Two engines, one page

A project runs on one of two engines, and the page is the same for both.

**A console** (Sunshine): one machine, one desktop. Pairing relays the PIN
to Sunshine's web UI with the one account it has; Dejarik remembers whose
the device is.

**An appliance** (Wolf): a seat per person, each in its own **drawer** — a
folder on the appliance, a uid its seats run as. Here **identity is the
pairing**: when a person types the PIN, Dejarik answers the engine's
pending request and then *points* the new device at their drawer, so from
its next connection it opens *their* home — their account, their library,
their saves — on every device pointed at them. The engine gives a fresh
pairing a folder of its own and the default uid, and a device that pairs
again lands there again, so pointing is not optional; it is the whole of
identity. Drawers are data the operator hands the panel (`people:` in the
config): one per account, plus **shared** drawers for the living-room
devices everybody uses — the TV pointed at `salon` plays as the house, and
only an admin may point a device there.

**One drawer, one open seat.** Every device a person paired opens the same
drawer — that is the point — and the engine will happily start a second seat
on that home while the first is in: two Steam clients on one config folder,
two emulators autosaving to one file. Nothing upstream prevents it and the
panel cannot intercept a launch (Moonlight talks to the engine directly). So
the panel *watches*: it reads the open seats every two seconds, joins each to
its drawer by the uid it runs as, and when two are open on the same drawer
in the same app it closes the **newer** one — the game somebody is playing
is never touched — and says why, on the page and in the log: *"Steam is
already open for tom on 203.0.113.10 since 20:14 — quit it there first."*
Two seats it saw at the same instant (it just started) cannot be told apart:
it stops neither and says so.

**Hand-started.** A project the watchman does not know names no `target`:
the panel shows ON or OFF from the engine alone, offers no button, and says
who to ask.

## Two truths, never collapsed

The whole design is one idea. Dejarik reads **two** sources and never lets
either speak for the other:

- **the watchman** — *should this be on, and is it coming up?*
- **Sunshine** — *would Moonlight get a reply right now?*

They disagree in both directions, and both disagreements matter:

- A VM that is **running but whose Sunshine has not started** is not ready,
  however green the board looks. Showing READY there puts a person in front
  of a connection that fails.
- A **watchman that has fallen over** does not stop anybody playing on a
  console that is answering perfectly well. So the panel says *"can't tell —
  but Sunshine answers, go ahead"* instead of pretending it knows nothing.

That is the `unknown` state, and it is the reason there are two probes
instead of one.

## The five states

| State | Means | The button |
|---|---|---|
| `ready` | Sunshine answered. Play. | lit, still live — `/play` is idempotent |
| `starting` | the chain is coming up, or it is up and Sunshine is not yet | sunk, amber |
| `asleep` | nothing running | lit |
| `blocked` | somebody put a **hands-off hold** on it at the watchman | dead |
| `unknown` | we cannot tell, and say so | plain — "try anyway" |

One more distinction the panel keeps: **"the watchman answered" and "the
watchman can see this target" are not the same question.** A guest on a
powered-off node is invisible to the watchman *every night* — that is
`asleep` (its node is known down, so it cannot be running), never
`unknown`. `dejarik_watchman_reachable` and `dejarik_watchman_known{project}`
are separate series for exactly this reason: an alert that watched the
second would page at bedtime, every day.

`blocked` deliberately does **not** read Le Veilleur's `blocked` field: that
one records why a machine may not be *stopped* (`min_uptime`, `grace`), which
would be exactly backwards as a reason a person cannot play. A hands-off hold
is the only thing that refuses a wake outright.

A failed wake is **not** a state — it is a `fault` carried alongside whatever
the machine is doing, so the button stays live and retrying is possible.

## The API is the product

The panel is its first client; your automations are the second. Every card
renders from a path a script can call, and the spec is published:

```
GET    /api/projects                    every project and its state
GET    /api/projects/{name}             one project — both truths
POST   /api/projects/{name}/play        idempotent; returns without waiting
GET    /api/projects/{name}/clients     paired devices (yours; all, for admins)
POST   /api/projects/{name}/clients     pair — relays the PIN Moonlight showed; on an
                                        appliance, then points the device at a drawer
DELETE /api/projects/{name}/clients/{uuid}
POST   /api/projects/{name}/clients/{uuid}/point   an admin sends a device to a drawer
GET    /api/projects/{name}/seats       open seats (yours + shared; all, for admins)
POST   /api/projects/{name}/seats/{id}/stop        close a seat (yours; any, for admins)
GET    /api/me · /healthz · /metrics · /openapi.json
```

People authenticate at the proxy (`Remote-User` / `Remote-Groups`, trusted
**from the proxy's address only**). Machines carry a bearer token each, so
the log names which one asked and one can be revoked alone.

## The page is one polled block

The console panel and the pairing form are rendered **together, from one
view**, inside a single element that re-reads itself. An earlier version
polled only the console half, so a completed wake left the pairing form
greyed out until the page was reloaded by hand — two fragments, one state,
and nothing keeping them honest.

The poll slows from 2 s to 10 s once nothing is moving, and the two inputs
carry `hx-preserve` so a half-typed PIN survives the swap. Nothing preserved
may carry `disabled`: a preserved node keeps its old attributes, which is the
same staleness bug wearing a different hat — there is a test for it.

## Built like its siblings

One static Go binary, no JavaScript toolchain, no `package.json`. The
stylesheet, the family mark, three font subsets and htmx are embedded, so
the page fetches nothing at render time — it works from a lane with no
egress and tells nobody who opened it.

```sh
podman run --rm -p 8080:8080 \
  -v ./config.yaml:/etc/dejarik/config.yaml:ro \
  ghcr.io/tomblancdev/dejarik:0.1.0
```

Config: [`config.example.yaml`](config.example.yaml). Secrets arrive by
environment (`DEJARIK_VEILLEUR_TOKEN`, the Sunshine credential), never from
the config file — the file is templated by a converge and readable, the env
file is not.

## Status

| | |
|---|---|
| `v0.1` | the panel: play, status, my clients. One project. Verified against a live Sunshine 2026-08-25 — `named_certs` is the client-list shape, and `status` comes back as a JSON **boolean**, not a string. |
| next | the library from the console's own catalogue · who is playing what · a lease card once Le Videur lands |

## This repo carries no environment

Addresses, hostnames, domains, VLAN and group names and the house word belong
to whoever runs it; the only thing crossing between a deployment and this repo
is a pinned image tag. Examples and tests use the documentation reserves —
`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` (RFC 5737),
`00:00:5E:00:53:xx` (RFC 7042), `example.com` (RFC 2606) — so nothing here
describes a real network. CI enforces it with a grep that fails the build, and
the mark ships carrying the product's name: set `house:` in the config and
the wordmark is redrawn with it at request time, so the same binary shows
`DEJARIK` to a stranger and somebody's house to the people who run it.

## Family

Dejarik talks to [Le Veilleur](https://github.com/tomblancdev/veilleur) (the
watchman that owns power) and sits beside
[Le Videur](https://github.com/tomblancdev/videur) (the bouncer that owns
temporary openings) and [La Loge](https://github.com/tomblancdev/la-loge) (the
Wi-Fi front desk).

## License

MIT — Tom Blanc. The mark and the faces belong to the
[La Loge](https://github.com/tomblancdev/la-loge) family (Big Shoulders
Stencil + IBM Plex Mono, OFL, embedded).
