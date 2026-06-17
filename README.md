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
- `Restitution` (front / back): `0.08 / 0.35` (now `0.05 / 0.25` — less bouncy ball off a player)

## Physics & player variables

A reference to every physics and per-player variable and what it does. Defaults are the
baseline values in `DefaultStats` / `DefaultTuning`; a role is just a `PlayerStats` preset,
so any of these can differ per player.

### Rigid body (`physics.Body` — players, ball, walls, cones)

The raw motion state, integrated each tick in the order: apply acceleration → soft-cap
speed → apply friction → move position.

- **Position / Velocity / Acceleration** — world-space state; acceleration is set from
  input each tick and then consumed.
- **Friction** — linear drag, a *negative* number applied as `v += v·Friction·dt` (per
  tick `v *= 1 + Friction·dt`); more negative stops faster.
- **InvMass** — `1/mass`; `0` means immovable/infinite mass (walls, cones) — never
  accelerates, never displaced.
- **MaxSpeed** — *soft* cap: your own acceleration can't exceed it, but a knock can, and
  friction bleeds the excess off (you're never hard-snapped down).
- **Shape / Radius** — collision circle (or segment for walls).

### Per-tick input (`sim.Intent`)

- **Move** — desired heading. **Throttle** — 0–1 acceleration scale. **Aim** — point to
  face (sets `Facing`). **ShootHeld** — held charges, released fires on the edge.
  **CancelCharge** — right-click while charging aborts the shot. **Trap** — held builds
  trap charge.

### Player runtime state (`sim.Player`)

- **Facing** — instant aim direction (cursor/AI target). **moveHeading** — actual steering
  direction, which *rotates toward* `Move` at `TurnRate` (this is what makes turning
  non-instant). **possession** (0–1), **control** (0–1), **touchCoef** (−1..1, this tick),
  **shootCharge** (sec), **trapCharge** (0–1) — see below. Charge timing: full shot at `shootChargeMax = 1.0s`; full trap at
  `trapChargeTime = 1.0s`; trap decays at `trapChargeDecay = 4.0/s`.

### `PlayerStats` — body / motion

- **Radius** `18` — body size. **Mass** `20` — heavier player shoves the ball more.
  **Friction** `-1.5` — high drag, so players stop quickly. **MaxSpeed** `140`.
  **Acceleration** `300`. **TurnRate** `14 rad/s` — max turn rate of the movement heading
  (a 180° reverse takes ~0.22s; `0` = instant).

### `PlayerStats` — ball-control geometry (surface gaps, units)

- **TouchRange** `2` — gap under which the ball is "touching" (hold/control/shoot).
- **PullRange** `6` — gap under which centre-pull and carry reach the ball.

### `PlayerStats` — angle curves (`CurveSpec{Curve, Front(0°), Back(180°)}`)

Each is evaluated from the ball dead-in-front (0°) to directly-behind (180°). Curve shapes
(`curves.go`): Linear, Quadratic (eases in), InverseQuadratic (eases out), Smoothstep,
Exponential.

- **Restitution** `0.05 / 0.25` (InvQuad) — bounciness on a *hard* contact; soft in front,
  springier behind.
- **CaptureSpeed** `320 / 70` (Linear) — impact speed *below which the ball sticks*
  (restitution 0) instead of bouncing.
- **CenterPull** `1000 / 0` (InvQuad) — spring drawing a near-but-not-touching ball in to
  make contact.
- **Stickiness** `420 / 0` (InvQuad) — capped adhesion holding a touching ball until a
  shot/bump overcomes it.
- **Control** `1500 / 300` (Linear) — tangential pull rolling a touching ball to the front.
- **Shoot** `shootForce / shootForce·0.3` (Linear, e.g. `500 / 150`) — shot power by angle.

### `PlayerStats` — scalar hold / damping

- **ControlDamping** `11` — bleeds sideways/orbital ball speed so it settles at the front.
- **OrbitStick** `8` — centripetal anti-fling: inward pull ∝ the ball's orbital speed, so a
  hard turn curves the ball around you instead of flinging it off.
- **SeatStrength** `14` — gently draws a touching ball flush to the surface (gap-proportional,
  so no jitter).

### `PlayerStats` — capture cone

- **CaptureConeDegrees** `15` — within ±15° of facing the ball reliably sticks.
- **CaptureConeSoft** `25` — over the next 25° capture decays to the back floor; beyond,
  side/back hits bounce.

### Possession mechanics

There are **two independent possession systems**. One is per-player (how firmly *you* hold
the ball); the other is per-team (how much *your team* has built up control). They affect
different things and never share state.

#### 1. Player possession (per-player — `Player.possession`, `updatePossession`)

*What it is:* the "ball at my feet" state — built toward 1 while the ball is *touching the
player anywhere* (any angle), decayed otherwise.

*Intent:* a player who has had the ball a moment holds it more firmly and carries it a touch
better, at a small speed cost — so a fresh poke is easy to steal but an established carry is
sticky.

*What it affects:*
- **Grip** = `GripFloor + (1-GripFloor)·possession` — scales **CenterPull** and
  **Stickiness** (the forces that keep the ball glued to you). A fresh touch barely holds;
  full possession clings through turns.
- **Roll-to-front control** — the **Control** force is multiplied by
  `(1 + PossessionControlBonus·possession)`, so a settled carrier rolls the ball to the
  front a touch more crisply.
- **Carry slowdown** — while the ball is at your feet, top speed and acceleration are scaled
  by **PossessionSpeedFactor** / **PossessionAccelFactor**.

*Variables:* **PossessionBuildSeconds** `1.5` / **PossessionReleaseSeconds** `0.4` (build /
decay time), **GripFloor** `0.3`, **PossessionControlBonus** `0.05` (up to +5% control at
full possession), **PossessionSpeedFactor** / **PossessionAccelFactor** `0.925` (~7.5%
slower). A parallel **control** state (gated by **PossessionArcRadians** `0.873`/50° — the
ball within the front arc) is *tracked but not yet wired to anything*.

#### 2. Team possession charge (per-team — `Match.advanceTeamPossession`)

*What it is:* a single 0..1 strength owned by whichever team is holding the ball — the
"we've worked the ball and built up control" meter.

*Intent:* sustained possession earns your **whole team** cleaner touches (passes are received
well, so you keep the ball), while the team that has **conceded** possession fumbles — a shot
they block flies off them harder. The charge survives a pass, so building it up isn't thrown
away the moment you move the ball on.

*How it works:*
- **Build** — while the owning team touches the ball, the charge builds to full over
  **teamBuildSeconds** `1.0` on an *accelerating* curve (`teamBuildCurve` = progress², weak
  early, steep toward the end).
- **Hold + decay** — after a release (nobody touching), it **holds** at full strength for
  **teamHoldSeconds** `1.5`, then fades on a smooth **convex** curve (`teamCoastEnvelope` =
  `1 − x²`) to 0 by **teamDecaySeconds** `3.5` — gentle at first, speeding up toward the end.
  The window is long, so a released charge lingers and decays slowly.
- **Inherited across a pass** — a receiving teammate inherits the charge *as it stands when
  they touch it*: receive within the hold and you keep the full built-up charge; receive late
  (deep in the decay, e.g. down to 30%) and you start at 30% and rebuild from there (the
  decayed strength is baked back into the build progress). Either way you continue, never restart.
- **Reset** — the **other team touching** the ball hands ownership over and restarts their
  build from zero; both teams touching at once (a scramble) clears it; so does a
  kickoff/shootout.

*What it affects:* each player's **touch coefficient** (`Player.touchCoef`, in `[-1,1]`),
which scales **CaptureSpeed** and **Restitution** in the ball contact (`TouchQuality`, in
`handleBallToPlayerInteraction`):
- **Owning team** → `OwnTeamMax·strength` (up to **+1**): capture up, bounce down → clean,
  sticky touches that scale up as the charge builds.
- **Other team** → `OtherTeam·strength` (down to **−0.6**): capture down, bounce up → the
  ball springs off them, more so the more possession you've built (so a blocked shot flies).
- **Neither team** (a loose ball) → coefficient 0 = the baseline curves, unchanged.

*Variables:* **OwnTeamMax** `+1.0`, **OtherTeam** `−0.6`, and the multiplier endpoints
(anchored at 1.0 for coefficient 0) **CaptureWorst/Best** `0.7 / 1.35`,
**RestitutionWorst/Best** `1.5 / 0.45`.

The two on-screen **test bars** over each player show **player possession** (top, white) and
the **team charge** (bottom — green while that team is boosted, red while it is the conceding
side); toggle with `render.ShowPossessionBars`.

### `PlayerStats` — charged shot

- **MinShootFactor** `0.35` — a tap fires at 35% power, full charge at 100%.
- **ShootSpeedFactor** `0.35` / **ShootAccelFactor** `0.4` — speed/accel at full charge
  (you're slow while winding up).

### `PlayerStats` — aim assist (so shots go where you aim despite the radial kick)

- **ShootAimAssist** `1.0` — blend from pure-radial (0) toward firing along `Facing` (1).
- **ShootAimAssistConeDegrees** `15` — full assist within ±15° of facing.
- **ShootAimAssistSoftDegrees** `0` — decay band past the cone (0 = hard cutoff; side/back
  shots fire purely radial).

### `PlayerStats` — trap ("good touch"), scaled by `trapCharge` 0→1

- **TrapPullBonus** `1.5` — up to ×2.5 stronger centre-pull (trap/steal a loose ball).
- **TrapRangeBonus** `10` — extends pull range by up to +10.
- **TrapControlBonus** `1.2` — stronger roll-to-front (snaps the ball to the front).
- **TrapCaptureBonus** `220` — raises capture speed by up to +220 (a damped first touch /
  save).
- **TrapRestitutionFactor** `1.3` — how strongly trap *deadens the bounce*: restitution is
  scaled by `1 - min(1, trapCharge·TrapRestitutionFactor)`, so a held trap stops the ball
  bouncing entirely by ~0.77 charge (on top of the higher capture speed). `0` = trap never
  affects bounce.
- **TrapSpeedFactor** `0.5` / **TrapAccelFactor** `0.55` — speed/accel at full trap (trapping
  is slow). **TrapRadiusBonus** `0` — grow while trapping (off).

### World physics (`config.Tuning`) + collision restitution

- **BallRadius** `7.5`, **BallFriction** `-0.3` (light drag → the ball rolls far),
  **BallMass** `1.5`.
- **BallWallRestitution** `0.90` — ball stays lively off walls/frame.
- **PlayerWallRestitution** `0.50` — players damped harder off walls.
- **ObstacleRestitution** `0.5` — bounce off cones. **NetRestitution** `0.2` — net catches
  the ball rather than springing it.
- player ↔ player: inelastic (positional separation, no bounce). ball ↔ player: a custom
  capture-vs-bounce path (below), never the generic resolver.

### How it fits together each tick

- **Movement**: `Move` sets a desired heading; `moveHeading` rotates toward it at
  `TurnRate`; acceleration = `moveHeading · Acceleration · throttle`. Effective top
  speed/accel are multiplied by trap, shoot-charge and (when the ball is at your feet)
  possession factors.
- **Dribble** (only ever moves the ball): centre-pull draws a near ball in; carry paces it
  with your movement; while touching, sticky hold + roll-to-front control + orbit-stick
  (anti-fling) + seat keep it glued to the front — all scaled by grip (possession) and trap.
- **Contact**: approach speed below the (cone- and trap-adjusted) CaptureSpeed → absorbed
  (sticks first touch); above → bounces with the angle's Restitution, deadened by trap and
  limited by the mass ratio off-centre.
- **Shooting**: power = `Shoot.Eval(angle) · (MinShootFactor + (1-MinShootFactor)·charge)`,
  added to the ball's current velocity along the radial (player→ball) direction, nudged
  toward `Facing` by the aim assist when the ball is in the front cone.
