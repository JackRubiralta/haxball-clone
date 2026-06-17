# phootball

A top-down, physics-based soccer game (a Haxball-style clone) written in Go with
[Ebiten](https://ebitengine.org/). The tuned ball-dribbling physics is the heart of the
game: the ball sticks to a player's front and can be dribbled, trapped, stolen, and
shot. On top of that sits a full match: a clock, win conditions, extra time, golden
goal, playable penalties, scoring attribution, optional positional rules, a menu, a
zooming camera, and sound.

## Running

```sh
# Local game. With no flags it opens a menu (mode select + settings).
go run ./cmd/game

# Fast-path flags jump straight into a match:
go run ./cmd/game -solo              # one player + ball, no opponents (dribble practice)
go run ./cmd/game -ai-both           # spectate AI vs AI
go run ./cmd/game -duo               # two players, switch control with 1 / 2 (testing)

# Shape the match:
go run ./cmd/game -field large -camera ball -zoom 1.8
go run ./cmd/game -mode quick -win-score 3
go run ./cmd/game -mode timed -minutes 3
go run ./cmd/game -mode cup -minutes 5         # timed, then extra time, then penalties
go run ./cmd/game -offside-frac 0.667 -gk-box-max 1

# LAN play (server-authoritative): the server runs all the physics, headless.
go run ./cmd/server -addr :4000 -mode cup -minutes 5 -seed 7
go run ./cmd/client -addr HOST:4000
```

Controls: **WASD** move, **mouse** aim, **hold left-click** to charge a shot (release to
fire), **right-click** to trap, **Esc**/**P** to pause, **mouse wheel** to zoom (with
`-camera ball`), **C** to toggle follow/fit.

## Command-line flags

Common to every binary: `-log-level` (debug|info|warn|error), `-log-format` (text|json),
`-version`.

| Area | Flags | Binaries |
| --- | --- | --- |
| Pitch | `-field` (standard\|small\|large), `-play-width/-play-height`, `-goal-width/-goal-depth`, `-penalty-width/-penalty-depth`, `-goalarea-width/-goalarea-depth` | game, server |
| Match | `-mode` (friendly\|quick\|timed\|cup\|golden), `-minutes`, `-win-score`, `-extra-time`, `-golden-goal`, `-penalties`, `-direct-pens` | game, server |
| Rules | `-offside-frac` (0..1, 0=off), `-gk-box-max` (0=off), `-zone-enforce` (clamp\|evict) | game, server |
| Determinism | `-seed` | game, server |
| Teams/Net | `-team-size`, `-addr`, `-tick-rate` (server) | game, server, client |
| Presentation | `-camera` (fit\|ball), `-zoom`, `-mute`, `-volume` | game, client |

Invalid flags, unknown presets, and out-of-range values exit with code **2** and print
usage; `-version` and `-h` exit **0**. The server and client shut down cleanly on
SIGINT/SIGTERM.

## Architecture

The code is split into layers so the same deterministic simulation runs in the local
game and on the authoritative server. Ebiten (and audio) are confined to the client-side
packages; everything the server links is headless.

```
cmd/game      local single-process game: menu + simulation + render + audio
cmd/server    authoritative headless host (runs all collisions)        -- no Ebiten/audio
cmd/client    thin client: sends intents, renders + sounds snapshots
internal/geom     Vec and 2D vector math                                [leaf]
internal/config   Geometry + Ruleset + Tuning + Seed; presets; flags    -> geom only
internal/logging  slog handler builder                                  -> stdlib slog only
internal/physics  Body + Shape (Circle/Segment), collision engine       -> geom
internal/sim      entities, Match.Step, rules/clock/scoring/zones,
                  sound-event emission                                  -> config, physics, geom
internal/control  Controller interface + role-aware AI                  -> sim
internal/input    human keyboard/mouse controller (Ebiten)             -> sim, render
internal/render   drawing + camera transform + UI primitives           -> sim, geom, config
internal/menu     client state machine (menu/settings/pause/result)     -> render, sim, control, input, config, audio   (client-only)
internal/audio    sound-event playback (Ebiten audio)                   -> sim                                          (client-only)
internal/netcode  TCP + gob server/client, snapshots                    -> sim, config
```

`internal/config` is the **single source of truth** for pitch geometry: `sim` builds the
field from it, `render` draws the boxes/markings from it, and `netcode` ships it to
clients in every snapshot. There is no second, disagreeing copy of the dimensions.

### The headless invariant

The server links no graphics or audio. After any change, verify:

```sh
go list -deps ./cmd/server | grep -E 'ebiten|x/image|oto'    # must print nothing
```

`internal/sim`, `internal/config`, `internal/physics`, `internal/control`, and
`internal/geom` never import Ebiten, audio, or `inpututil`.

### The determinism contract

`Match.Step(inputs, dt)` is deterministic and headless: given the same seed and the same
inputs it produces identical state on the server and every client (so a snapshot is just
a projection of authoritative state). This rests on:

- **Fixed `dt`** and timers accumulated as `+= dt` (no wall-clock).
- **Stable iteration** over the `Players` slice and the fixed `Teams[2]` array (no
  map-order dependence in the simulation).
- **One seeded RNG** (`Match.rng`, from `config.Seed`), used only for coin tosses
  (kickoff side, penalty order) — never a package global.
- **Sound is data, not behaviour**: the simulation only *emits* `sim.SoundEvent` values;
  the client turns them into audio, so the deterministic core never depends on a sound
  device.

The server also rejects any intent containing non-finite floats (one NaN would desync
every client) and clamps throttle to `[0,1]`.

## Gameplay systems

- **Dribble physics** — front-cone capture, centre-pull, sticky hold, roll-to-front, an
  anti-fling centripetal stick, charged shots, and a trap ("good touch"). Per-player
  stats and role presets ride swappable angle-response curves.
- **Match rules** — friendly / first-to-N / timed; a drawn match runs the configured
  chain of extra time → golden goal → a **playable** penalty shootout. A clock,
  pause, and a scoreboard with the phase label round it out.
- **Scoring attribution** — last-touch tracking credits the scorer and an assist,
  flags own goals, and (football-style) keeps a shooter's credit when a defender merely
  deflects a shot that was already going in.
- **Positional rules** (off by default) — an anti-camp offside line at a configurable
  fraction of the pitch, and a keeper-box occupancy limit, both enforced as a soft
  clamp that never touches the ball.
- **Camera** — fit-the-pitch by default; a ball-follow mode with zoom for larger maps.
  Aim stays correct at any zoom because `ScreenToWorld` inverts the same transform.

## Production-hardening backlog

Beyond what ships here, the natural next steps are: an in-menu LAN host/join flow
(today LAN runs through the dedicated `cmd/server`/`cmd/client` binaries); sending the
settings once on join instead of in every snapshot; on-disk settings persistence (v1 is
in-memory, CLI flags are the persistent surface); client-side snapshot interpolation;
and replacing the procedurally-generated placeholder sound effects in
`internal/audio/assets` with real recordings.

## Saved tuning values (in case)

Previous values for knobs that were changed, kept here so they can be restored:

- `PullRange`: `8` (now `6`)
- `TrapRangeBonus`: `14` (now `10`)
```
