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

## Le Foyer — rooms, in the stream

A tile is **private**: the engine renders it for one device, and no other
device can be put into it, however the two are paired. So two devices could
never share a game — until the engine's **lobby**, a game other sessions may
be switched into. Dejarik drives it as a **room**, from a page *inside the
stream*: **Le Foyer**, the hub tile.

The tile is a seat that runs nothing but a kiosk browser on
`/foyer/<project>`. **Identity there is the pairing, again:** the engine
hands that seat its own session id; the page reads the session back from
the engine and joins it to its drawer by the uid it runs as — the guard's
own join — and trusts nothing else. Two locks and no login: the page answers
only from the appliance's addresses (`foyer.sources`; the seats sit behind
it) and only for a session the engine has open right now. A made-up id
maps to nothing; the vhost never reaches it (wrong source).

On the page: **rooms open in the house** (JOIN — and STOP for the one who
opened it) and **the house** (every tile but the hub: OPEN A ROOM, or LOCK
WITH A PIN first — four wheels a pad turns; the engine asks the PIN of
whoever joins or closes it). A room runs on the opener's home for that game
— the same the tile uses, so the saves are theirs — with the opener's
picture, and **stays open when everybody has left**: a stream that drops
comes back to a game still running; STOP is for when it should be gone; a
room nobody is in lets the appliance sleep, and dies with it. The engine's
own combo **START + UP + RB** brings a pad back to the page.

Two rules keep the homes honest. A room is refused on a home a **tile** has
open (*"RetroDECK is already open for salon on the TV as a tile — quit it
there"*): two boxes on one save folder is what the guard exists to prevent.
And seats on the hub tile are **exempt from the guard**: the page holds no
save, and a person's phone and TV both sitting in the Foyer is the normal
way into a room, not a clash.

The page is a **hub of cards**: each room and each house game is one card
carrying its own actions with the pad's glyphs — **A** the first (open a
room, join), **X** the second (lock with a PIN), **Y** the third (stop, on a
room that is yours), **B** backs out; ◀ ▶ walk a shelf, ▲ ▼ change shelves;
in PIN mode ◀ ▶ move between the wheels and ▲ ▼ turn one. Arrow keys,
Enter/x/y/Escape, the digits, a mouse and a finger do the same. The page
keeps its *own* state (the ring, a PIN in progress) and renders the cards
from `/foyer/<project>/state`, polled every few seconds: a poll replaces
data, never the page. Artwork is the tile's own icon, fetched once by the
panel (a seat has no way out of the house) and tinted phosphor. Rooms also
show on the panel, where an admin may close any.

## Mon vestiaire — an account under your game

A drawer may carry a **companion**: a container the appliance starts beside
every seat of that drawer, for what the seat's image does not have. The
first is a Spotify Connect receiver ([Le Juke](https://github.com/tomblancdev/juke)):
link a Spotify account to your drawer once and *"L'Arcade – RetroDECK"*,
*"– Steam"*, whatever you open, appears in **that** account's Spotify app
whenever the seat does — the music mixed under the game, in the stream, the
phone's slider its fader. The companion needs one file in the drawer: the
account's stored credentials. Making it was a terminal's job; here it is a
tap.

**On the panel** (your phone): *my accounts* — **LINK** sends you to the
provider's own page with the house's app (Authorization Code with **PKCE**:
no secret anywhere, so the client id sits in the readable config), you
agree, you land back here, and the short-lived token **waits in memory**
for the appliance — woken if asleep, since only it can take it. The word on
the card — *linked · not linked · linking · unlinking · unknown* — is the
**appliance's last report**, remembered with its time; never a guess. Flip
the switch to unlink. An admin sees every drawer (the TV's shared one
included).

**In the stream** (Le Foyer's third shelf): the same card shows a **QR
code** — scan it with your phone and it opens the panel, signed in as you,
straight at the provider's page for *this* drawer; the card flips by itself
when the link has landed. A stream is no place to type a password: the
phone is.

**The appliance's half** is one endpoint: `POST /api/projects/{name}/links/sync`,
from the Foyer's sources only — it reports which drawers hold each
companion's file and takes what is pending (a token, handed once, never
logged; or a drawer to unlink). The watcher on the appliance
(`wolf-sidecars`, in the `tomblancdev.arcade` collection) calls it every
few seconds and runs the companion's `link` mode with the token, as the
drawer's uid, on the drawer's own folder — outside every app home, so one
link serves every tile and no golden template ever carries it.

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
GET    /api/projects/{name}/rooms       open rooms; POST …/rooms/{id}/stop closes one
GET    /api/projects/{name}/links       your links (every drawer's, for admins)
POST   /api/projects/{name}/links/sync  THE APPLIANCE: report who is linked, take what is pending
GET    /links/{name}/{sidecar}/start    a person's tap: to the provider (then /links/callback)
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
