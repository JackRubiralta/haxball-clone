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

- `PullRange`: `8` (now `5`)
- `TrapRangeBonus`: `14` (now `6`)
- `Restitution` (front / back): `0.08 / 0.35` (now `0.10 / 0.20` — front raised so head-on shots deflect)

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

- **Facing** — aim direction. The AI's aim applies instantly (it is already smoothed in the
  control layer); a human's **cursor** aim *rotates toward* the cursor at `TurnRate` (via the
  `AimFromCursor` intent flag) so the disk can't instantly snap around. **moveHeading** —
  actual steering direction, which *rotates toward* `Move` at `TurnRate` (turning is
  non-instant). **possession** (0–1), **control** (0–1), **touchCoef** (−1..1, this tick),
  **shootCharge** (sec), **trapCharge** (0–1) — see below. Charge timing: full shot at `shootChargeMax = 1.0s`; full trap at
  `trapChargeTime = 1.0s`; trap decays at `trapChargeDecay = 4.0/s`.

### `PlayerStats` — body / motion

- **Radius** `18` — body size. **Mass** `20` — heavier player shoves the ball more, and is
  shoved back *less* by a hard hit (the ball:player mass ratio drives the contact — see Contact).
  **Friction** `-1.5` — high drag, so players stop quickly. **MaxSpeed** `140`.
  **Acceleration** `300`. **TurnRate** `14 rad/s` — max turn rate of the movement heading AND
  the human cursor aim (a 180° turn takes ~0.22s; snappy but non-instant; `0` = instant).

### `PlayerStats` — ball-control geometry (surface gaps, units)

- **TouchRange** `2` — gap under which the ball is "touching" (hold/control/shoot).
- **PullRange** `5` — gap under which centre-pull and carry reach the ball (reduced from 6).

### `PlayerStats` — angle curves (`CurveSpec{Curve, Front(0°), Back(180°)}`)

Each is evaluated from the ball dead-in-front (0°) to directly-behind (180°). Curve shapes
(`curves.go`): Linear, Quadratic (eases in), InverseQuadratic (eases out), Smoothstep,
Exponential.

- **Restitution** `0.10 / 0.20` (InvQuad) — bounciness on a *hard* contact; front raised to
  0.10 so a head-on hard shot deflects off a player rather than dying at their feet.
- **CaptureSpeed** `260 / 30` (Linear) — impact speed *below which the ball sticks*
  (restitution 0) instead of bouncing. Front lowered to 260 (the ball clears capture more
  easily); back 30, so off-front hits stick much less. A full-power shot (~500) easily clears
  it, so an opponent never captures it — it deflects off.
- **CenterPull** `800 / 0` (InvQuad) — spring drawing a near-but-not-touching ball in to
  make contact (power reduced from 950).
- **Stickiness** `420 / 30` (InvQuad) — capped adhesion holding a touching ball until a
  shot/bump overcomes it; a small baseline hold even at the back (`30`).
- **Control** `1500 / 300` (Linear) — tangential pull rolling a touching ball to the front.
- **Shoot** `shootForce / shootForce·0.3` (Linear, e.g. `500 / 150`) — shot power by angle.

### `PlayerStats` — scalar hold / damping

- **ControlDamping** `11` — bleeds sideways/orbital ball speed so it settles at the front.
- **OrbitStick** `8` — centripetal anti-fling: inward pull ∝ the ball's orbital speed, so a
  hard turn curves the ball around you instead of flinging it off.
- **SeatStrength** `14` — gently draws a touching ball flush to the surface (gap-proportional,
  so no jitter).

### `PlayerStats` — capture cone

- **CaptureConeRadians** `0.384` (≈22°) — within ±22° of facing the ball reliably sticks
  (a bigger cone). The trade-off: the in-cone capture peak is modest (CaptureSpeed front 260),
  so the cone covers more angle but holds less firmly.
- **CaptureConeSoft** `0.524` (≈30°) — over the next ~30° capture decays to the back floor;
  beyond ~52° total, side/back hits bounce.

### Possession mechanics

There are **two independent possession systems**. One is per-player (how firmly *you* hold
the ball); the other is per-team (how much *your team* has built up control). They affect
different things and never share state.

#### 1. Player possession (per-player — `Player.possession`, `updatePossession`)

*What it is:* the "ball at my feet" state — built toward 1 while *you* are the player drawing
the ball, decayed otherwise. You build it while the ball is within your **pull radius**
(`pullRadius()` = `PullRange + TrapRangeBonus·trapCharge`, so a held trap reaches further) — you
do **not** have to be touching it — but only **one** player builds at a time (see *Who builds*).

*Intent:* a player who has had the ball a moment holds it more firmly and carries it a touch
better, at a small speed cost — so a fresh poke is easy to steal but an established carry is
sticky. Single-builder so a contest is *decisive*: when an opponent closes a carrier down, the
newcomer's bar rises while the carrier's falls, instead of both pinning at max.

*Who builds it (`advancePossessionBuilder` + `engaged`):* a player is **engaged** if the ball is
within its pull radius **or** it is body-bumping the current holder (so a physical challenge
counts even when the ball isn't quite in reach). Of the engaged players, the **latest** one to
become engaged is the *sole* builder — everyone else *decays* toward 0 (not instantly to zero).
Each player is stamped the tick it engages; the newest stamp wins. If the latest builder loses
the ball, the build falls back to whoever still has it in reach.

*Won in a contest (`updateBallPossessor` / `contestPossession`):* while the holder is still
engaged and a *different* player is the latest engager, possession transfers GRADUALLY from the
holder **into that builder** — the holder loses exactly what the builder gains
(**PossessionStealRate**/sec, conserved), and the ball changes hands once the builder holds the
larger share. Possession always flows *to* the latest engager and never leaks back, so a
sustained challenge wins the ball cleanly (rather than both bars bleeding to zero). A **loose**
ball — no holder still engaged — is claimed only on an actual *touch*, so a ball merely in range
or flying past doesn't flip possession (this protects passes). A player who *passed* has
possession 0 (`shoot` resets it), so a clean reception starts cold; only a contested take
carries possession.

*What it affects (possession modulates these only MILDLY, and the two hold forces in
opposite directions):*
- **Centre-pull grip** = `CenterPullGripFloor + (1-CenterPullGripFloor)·possession` — scales
  **CenterPull**. With a high floor (`0.65`) possession changes it only a little (`0.65 → 1.0`),
  far less than before.
- **Stickiness grip** = `1 − StickinessPossessionDebuff·possession` — scales **Stickiness**,
  *slightly DOWN* with possession (a settled carrier is a hair less sticky, down to `0.97`).
- **Roll-to-front control** — the **Control** force is multiplied by
  `(1 + PossessionControlBonus·possession)`, so a settled carrier rolls the ball to the
  front a touch more crisply (up to **×1.09** at full possession).
- **Carry slowdown** — while the ball is at your feet, top speed and acceleration are scaled
  by **PossessionSpeedFactor** / **PossessionAccelFactor**.

*Variables:* **PossessionBuildSeconds** `1.5` / **PossessionReleaseSeconds** `0.4` (build /
decay time), **CenterPullGripFloor** `0.65`, **StickinessPossessionDebuff** `0.03`,
**PossessionControlBonus** `0.09` (up to +9% control at full possession),
**PossessionStealRate** `1.0` (possession/sec transferred while contesting). The build/steal
**reach** is the pull radius (`PullRange + TrapRangeBonus·trapCharge`, trap-extended);
**PossessionSpeedFactor** / **PossessionAccelFactor** `0.925` (~7.5% slower). A parallel
**control** state (gated by **PossessionArcRadians** `0.873`/50° — the ball within the front
arc) is *tracked but not yet wired to anything*.

#### 2. Team possession charge (per-team — `Match.advanceTeamPossession`)

*What it is:* a single 0..1 strength owned by whichever team is holding the ball — the
"we've worked the ball and built up control" meter.

*Intent:* sustained possession earns your **whole team** cleaner touches (passes are received
well, so you keep the ball), while the team that has **conceded** possession fumbles — a shot
they block flies off them harder. The charge survives a pass, so building it up isn't thrown
away the moment you move the ball on.

*How it works:*
- **Build** — while the owning team touches the ball, the charge builds to full over
  **teamBuildSeconds** `1.5` on a strongly accelerating **cubic** curve (`teamBuildCurve` =
  progress³) — it stays low for most of the build and spikes only near the end.
- **Hold + decay** — after a release (nobody touching), it **holds** at full strength for
  **teamHoldSeconds** `1.5`, then fades on a smooth **convex** curve (`teamCoastEnvelope` =
  `1 − x²`) to 0 by **teamDecaySeconds** `3.5` — gentle at first, speeding up toward the end.
  The window is long, so a released charge lingers and decays slowly.
- **Inherited across a pass** — a receiving teammate inherits the charge *as it stands when
  they touch it*: receive within the hold and you keep the full built-up charge; receive late
  (deep in the decay, e.g. down to 30%) and you start at 30% and rebuild from there (the
  decayed strength is baked back into the build progress). Either way you continue, never restart.
- **Reset** — the **other team touching** the ball hands ownership over and restarts their
  build from zero; both teams touching at once (a scramble) clears it; a kickoff/shootout
  clears it.
- **Drained by a challenge (team-wide)** — a challenge on the held ball does NOT reset the charge
  but **drains** it at `teamDrainPerSecond` (1.0/s of build progress), so sustained pressure wears
  the boost away while a glancing bump only nicks it. Two triggers count as a challenge: an
  **opposing-player collision involving the ball carrier** (a body challenge), or a **ranged
  pull-radius contest** — the owning team has the ball in a player's pull radius *and* an opponent
  also has it in *their* pull radius (`ballInTeamPullRange`), so closing down to arm's length drains
  it without contact.
- **Per-player contact drain (localized)** — separately, any boosted player **body-touched by an
  opponent** (even off the ball) has only *its own* published `touchCoef` eroded: a per-player
  `boostDrain` (0..1) rises at `boostContactDrainPerSecond` (2.0/s) while an opponent is on it and
  recovers at `boostContactRecoverPerSecond` (1.5/s) when they leave, scaling that player's coef by
  `(1 − boostDrain)`. The **team charge and team-mates are untouched** — only the marked player's
  clean-touch buff fades toward neutral. (A conceding player has no boost to lose.)

*What it affects:* each player's **touch coefficient** (`Player.touchCoef`, in `[-1,1]`),
which scales **CaptureSpeed** and **Restitution** in the ball contact (`TouchQuality`, in
`handleBallToPlayerInteraction`):
- **Owning team** → `OwnTeamMax·strength` (up to **+1**): capture up, bounce down → clean,
  sticky touches that scale up as the charge builds (full-charge capture ≈ front × `CaptureBest`,
  290 × 1.025 ≈ 297), so a fully-built possession receives firmly.
- **Other team** → `OtherTeam·strength` (down to **−1.0**): capture down, bounce up (up to
  ×3) → the ball springs off them, more so the more possession you've built (a blocked shot flies).
- **Neither team** (a loose ball) → coefficient 0 = the baseline curves, unchanged.
- **Capture cone** → scales ASYMMETRICALLY with the coefficient (see `captureConeRadians`):
  the buff WIDENS the owning team's reliable cone a little (`ConeBonusRadians` ≈3° at full
  charge — biggest cone), while the debuff NARROWS the conceding team's more (`ConeDebuffRadians`
  ≈12° at full enemy charge — cone shrinks to ~10°, still well under the ~22° baseline). So a
  debuffed opponent catches less off the dead-on line. Dead-on (angle 0) is always inside
  the cone, so straight-on shots/captures are unchanged — only off-axis catching shrinks.

*Variables:* **OwnTeamMax** `+1.0`, **OtherTeam** `−1.0`, and the multiplier endpoints
(anchored at 1.0 for coefficient 0) **CaptureWorst/Best** `0.628 / 1.025`,
**RestitutionWorst/Best** `3.0 / 0.675`. These are scaled against the lowered baselines
(CaptureSpeed front `290`, Restitution front `0.08`) so the buffed/debuffed *absolute* touches
are preserved (buffed capture ≈ 297, debuffed bounce ≈ 0.24).

The two on-screen **test bars** over each player show **player possession** (top, white) and
the **team charge** (bottom — green while that team is boosted, red while it is the conceding
side); toggle with `render.ShowPossessionBars`.

#### Design choices & open options

Both possession systems are *disrupted* by the opposing team, and exactly **what triggers that
disruption** is a deliberate, still-open design choice rather than a settled rule. The axes below
list the plausible triggers for each mechanic, mark the **current** choice, and note the
trade-off — so the feel can be retuned by swapping which trigger fires (and whether it is
gradual/instant and per-player/team-wide).

**A. What disrupts the TEAM possession charge (the squad-wide touch buff):**
- **Opponent touches the ball** → hands ownership over and restarts their build (also: both teams
  touching at once, kickoff, and shootout clear it). *(current — the primary, most intuitive
  handover: they won the ball, you lost the buff.)*
- **Opponent body-checks the player who has the ball** → drains the charge gradually
  (`teamDrainPerSecond` 1.0), not a reset. *(current — pressure wears a buildup down even without
  winning the ball; a glancing bump only nicks it.)* Open sub-choices on this one:
  - *Scope:* **both are now live** — the **whole team's** charge drains when the *carrier* is
    challenged, AND a separate **per-player localized** drain erodes only the *touch-boost* of any
    boosted player an opponent body-checks (even off the ball), leaving the team charge and
    team-mates intact (more intuitive — an opponent only spoils the player they're actually on).
    See "Per-player contact drain" in the mechanics above.
  - *Severity:* **drain** gradually *(current, both)* vs **reset** it instantly *(option, not used)*.
- **Ball in an opponent's pull radius** (no contact yet) → drains at arm's length. *(current — a
  ranged pull-radius contest: it drains when an opponent has the ball in reach AND the owning team
  still does too, so closing down erodes the buff without a body-check; it does NOT drain off a
  single far opponent or a ball flying away.)* Pros: pull-range pressure erodes the buff early; the
  owner-also-in-range guard stops it from firing on a loose/escaping ball.
- **A player merely inside an opponent's pull radius** (proximity, ball not involved) → drain.
  *(option, not used.)* Pure marking-pressure; cons: you'd lose control with nobody touching the
  ball *or* you, which reads as arbitrary.

**B. What counts as "engaging" the ball for PLAYER possession (who builds + who can steal):**
- **Ball touching the player** → build/steal. *(the original model; now superseded.)*
- **Ball within the player's pull radius** (trap-extended, `PullRange + TrapRangeBonus·trapCharge`)
  → build/steal from arm's length. *(current — `inPullRange`; rewards positioning and turns the
  trap into a defensive reach tool.)*
- **Body-contact with the current holder** → counts as engaged even if the ball isn't quite in
  reach, so a physical challenge still contests. *(current — `playersTouching`.)*
- **A player inside another player's pull radius** (player-proximity, ball elsewhere) → steal.
  *(option, not used.)* Steal purely by marking tightly; cons: you'd drain a carrier whose ball is
  on their far side just because you stood next to them — decoupled from the ball, unintuitive.
- **Loose-ball takeover** (no holder still engaged): claim only on an **actual touch**
  *(current — protects passes)* vs claim on **pull-range** *(rejected: a ball merely flying past
  would flip possession, which tanks passing).*

The current design deliberately favours **ball-grounded, gradual** triggers — disruption is tied
to the ball being touched/reachable or to a real body-check, and it happens over time — which
keeps possession changes readable and rewards positioning over magnetic, proximity-only effects.
Swapping any line above to an *option* shifts that balance (more aggressive pressing, faster
swings, or a more localized buff) at the cost of some intuitiveness.

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

- **TrapPullBonus** `1.0` — up to ×2 stronger centre-pull (trap/steal a loose ball); reduced from 1.5.
- **TrapRangeBonus** `6` — extends pull range by up to +6 (reduced from 10).
- **TrapControlBonus** `1.25` — stronger roll-to-front (snaps the ball to the front).
- **TrapStickinessBonus** `0.5` — stiffens the sticky hold while trapping (`Stickiness ×
  (1 + TrapStickinessBonus·trapCharge)`, up to +50% at full trap).
- **TrapCaptureBonus** `60` — small capture-speed bump (+60 at full trap); the trap now relies
  mainly on deadening the bounce rather than a big capture lift.
- **TrapRestitutionFactor** `0.4` — how strongly trap *deadens the bounce*: restitution is
  scaled by `1 - min(1, trapCharge·TrapRestitutionFactor)`. At `0.4`, even a full trap only
  damps the bounce to ~60%, so a hard shot clearly deflects off a trapping defender/keeper
  rather than dying at their feet. `0` = trap never affects bounce.
- **TrapSpeedFactor** `0.5` / **TrapAccelFactor** `0.55` — speed/accel at full trap (trapping
  is slow). **TrapRadiusBonus** `0` — grow while trapping (off).

### World physics (`config.Tuning`) + collision restitution

- **BallRadius** `7.5`, **BallFriction** `-0.3` (light drag → the ball rolls far),
  **BallMass** `1.5` — the ball:player mass ratio (`1.5 : 20`) drives the contact: it damps the
  bounce off a player and sets how hard a heavy hit shoves the player back (see Contact).
- **BallWallRestitution** `0.90` — ball stays lively off walls/frame.
- **PlayerWallRestitution** `0.50` — players damped harder off walls.
- **ObstacleRestitution** `0.5` — bounce off cones. **NetRestitution** `0.2` — net catches
  the ball rather than springing it.
- player ↔ player: inelastic (positional separation, no bounce). ball ↔ player: a custom
  capture-vs-bounce path (below), never the generic resolver — and a *really hard* hit is the
  one case the ball moves the player (it shoves them back).

### How it fits together each tick

- **Movement**: `Move` sets a desired heading; `moveHeading` rotates toward it at
  `TurnRate`; acceleration = `moveHeading · Acceleration · throttle`. Effective top
  speed/accel are multiplied by trap, shoot-charge and (when the ball is at your feet)
  possession factors.
- **Dribble** (only ever moves the ball): centre-pull draws a near ball in; carry paces it
  with your movement; while touching, sticky hold + roll-to-front control + orbit-stick
  (anti-fling) + seat keep it glued to the front — all scaled by grip (possession) and trap.
- **Contact**: approach speed below the (cone- and trap-adjusted) CaptureSpeed → absorbed
  (sticks first touch); above → bounces with the angle's Restitution (deadened by trap). Two
  mass effects, both driven by the ball:player mass ratio:
  - **Bounce damping** — the bounce is mass-ratio damped *at every angle* now (uniformly *less
    bouncing*). The old angle-split impulse scaling (full impulse in the front cone, damped only
    off-centre) is gone; `uniformImpulseScale` toggles it back if needed. A clean capture still
    absorbs fully.
  - **Hard-hit shove** — a really hard hit (approach speed above `ballPushThreshold`) is the one
    place the ball moves the player: the excess momentum shoves the player back along the ball's
    travel, scaled by the ball:player mass ratio and `ballPushFactor`, so a heavier or faster
    ball pushes harder. Dribble and soft contacts leave the player planted.
- **Shooting**: power = `Shoot.Eval(angle) · (MinShootFactor + (1-MinShootFactor)·charge)`,
  added to the ball's current velocity along the radial (player→ball) direction, nudged
  toward `Facing` by the aim assist when the ball is in the front cone.
