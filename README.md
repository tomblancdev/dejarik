<p align="center"><img src="ui/static/logo-animated.svg" alt="Le Squat — dejarik // let the wookiee win" width="640"></p>

# Dejarik

**The arcade's panel.** A person opens one page and gets three things: can I
play, wake it if not, and which of my devices are paired. It looks like a
starship control panel because that is what it is — instruments behind glass,
and one lit button that actually does something.

Named after the holochess table on the Millennium Falcon. Let the wookiee win.

## What it owns: almost nothing

Dejarik is a **face**, not machinery. Everything it shows belongs to
something else, and it says so:

| What | Who actually owns it |
|---|---|
| power — waking the tower and the console | [Le Veilleur](https://github.com/tomblancdev/veilleur) |
| pairing, streaming | Sunshine, on the console |
| who you are | the gateway (Authelia), through the reverse proxy |
| saves, the catalogue | the lab's storage |
| temporary firewall openings | [Le Videur](https://github.com/tomblancdev/videur) |

The one fact it keeps for itself: **who paired which device**. Sunshine's
pairing list is global — it knows a device called `tom-phone` exists, not
whose it is. That file is declared *replaceable*: lose it and nobody loses a
pairing, the devices just stop having an owner.

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
POST   /api/projects/{name}/clients     pair — relays the PIN Moonlight showed
DELETE /api/projects/{name}/clients/{uuid}
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

Part of [Le Squat](https://github.com/tomblancdev) — with
[La Loge](https://github.com/tomblancdev/la-loge) (the Wi-Fi front desk),
[Le Veilleur](https://github.com/tomblancdev/veilleur) (the watchman) and
[Le Videur](https://github.com/tomblancdev/videur) (the bouncer).

MIT.
