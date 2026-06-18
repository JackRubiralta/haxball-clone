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
fire), **right-click** to trap, **middle-click** to push, **Esc**/**P** to pause, **mouse
wheel** to zoom (with `-camera ball`), **C** to toggle follow/fit.

## Abilities

Three buttons, three ways to touch the ball — each is best at one job:

- **Shoot — hold left-click, release to fire.** Winds up a kick whose power scales with how
  long you hold (a tap ≈ a third power, a full second ≈ full power), and **aims it where
  you're facing** rather than wherever the ball happens to sit, so a moving ball still goes
  where you point. It only fires into the **front 180°** (the whole hemisphere you're facing) —
  you can't shoot a ball that's behind you — and you move slower while winding up.
  *Main thing:* your aimed scoring/passing shot, with chargeable power.

- **Trap — hold right-click ("good touch").** Tightens your grip on the ball: it deadens an
  incoming ball so a hard pass or shot sticks to your feet instead of bouncing off, and it
  snaps the ball to your front and glues it there as you move. It also reaches a little
  further to drag in (or steal) a loose ball nearby. You move slower while trapping, and the
  grip is strongest a moment after you press — holding it too long eases off.
  *Main thing:* tight close control — **turning and pivoting with the ball quickly** without
  it squirting away (cleanly receiving a hard pass is the bonus).

- **Push — middle-click.** An instant poke that shoves the ball away at ~70% of a full shot,
  in **any direction** (including sideways or behind you) and on any ball within your pull
  reach — not just one at your feet. No wind-up and no aiming, so it fires faster than a shot.
  *Main thing:* a quick jab to clear, escape pressure, or knock a loose ball away when a
  charged front-shot is too slow or pointed the wrong way.

## Command-line flags

Common to every binary: `-log-level` (debug|info|warn|error), `-log-format` (text|json),
`-version`.

| Area | Flags | Binaries |
| --- | --- | --- |
| Pitch | `-field` (standard\|small\|large), `-play-width/-play-height`, `-goal-width/-goal-depth`, `-penalty-width/-penalty-depth`, `-goalarea-width/-goalarea-depth` | game, server |
| Match | `-mode` (friendly\|quick\|timed\|cup\|golden), `-minutes`, `-win-score`, `-extra-time`, `-golden-goal`, `-penalties`, `-direct-pens` | game, server |
| Rules | `-offside-frac` (0..1, 0=off), `-penalty-box-max`, `-penalty-box-max-opp`, `-goalarea-box-max`, `-goalarea-box-max-opp`, `-goalarea-keeper-only`, `-zone-enforce` (clamp\|evict). `-gk-box-max` is a **deprecated** alias for `-goalarea-box-max`. | game, server |
| AI | `-difficulty` (easy\|normal\|hard\|impossible) — validated against `control.SkillNames()` in the command layer | game, server |
| Determinism | `-seed` | game, server |
| Teams/Net | `-team-size`, `-addr`, `-tick-rate` (server) | game, server, client |
| Presentation | `-camera` (ball\|player\|fit; aliases `follow`/`active`), `-zoom`, `-mute`, `-volume` | game, client |

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

### The AI ≤ human boundary

A controller (the AI **or** the human) acts only through `Controller.Intent(view sim.View)`
over real ticks and observes only through `sim.View` — the simulation cannot tell a bot from
a person. This is enforced at the type level: `View.Me(id)` returns a `SelfView` (which exposes
the player's own velocity, steering heading, possession build and tuning), but every
other-player handle — `Carrier`, `Teammates`, `Squad`, `Opponents` — is an `ObservedView` that
exposes **only what a human sees on screen** (position, facing, radius, role/side, and the two
charge gauges; see `netcode.EntityState`). A non-self handle cannot be type-asserted up to
`SelfView`, so the AI literally cannot read an opponent's hidden state. The raw RNG seed is not
exposed either — variety comes from `View.NoiseSalt(id)`. The AI also charges shots/traps at the
same per-tick rate as a human and uses one ability at a time (Trap > Push > Shoot), like the
three mouse buttons. (Tests: `control/boundary_test.go`, `sim/chargerate_test.go`.)

## Match stats & play-by-play

An opt-in, write-only recorder (`internal/sim/record.go`) folds a chronological event log and
per-player / per-team aggregates out of the existing authoritative sinks (touches, kicks, the
collision resolver, goal resolution, the kickoff reset, the penalty taker) with **zero**
effect on the simulation — every hook is nil-safe and recording is off unless
`Match.EnableRecording()` is called, so a disabled match is byte-identical. It is deliberately
**not** reachable through `sim.View` (a human can't see aggregated stats, so neither can the
AI); the only way in is `Match.Recorder()` / `Match.Stats()`.

Recorded per player: touches, passes (forward/sideways/backward, completed/attempted), key
passes, assists, interceptions, tackles/possession-wins, saves, shots, shots-on-target, goals,
own-goals, clearances, possession seconds, distance covered, and time-in-thirds — plus the same
team totals and the full event log. Possession is decided by the possession **radius**: the ball
is a team's while exactly one team has a player within possession reach (neither = loose, both =
contested; loose/contested count for no one).

In the local game it is on by default: press **Tab** in a match to toggle the live stats panel,
and a JSON record is written to `phootball-matches/match-seed<seed>-<L>v<R>.json` when the match
finishes. Over the network the same numbers ride in each `Snapshot` (`Stats` + a per-tick
`Events` delta), so the client HUD shows identical figures. The JSON (`MatchRecord`) carries the
schema version, seed, geometry, ruleset, rosters, final score, winner, duration, the full event
log, and the aggregates; it round-trips through `encoding/json`.

## Building, testing & CI

```sh
make            # vet + build + test + race + headless guard + golden replay (the full gate)
make test-race  # go test -race ./...
make headless   # fails if cmd/server links Ebiten
make golden     # the feel-freeze characterization (regenerate: go test ./internal/sim -run TestGoldenReplay -update)
```

`internal/sim/replay_test.go` drives fixed-seed, AI-independent scripted matches and asserts the
ball trajectory + scores + goal attribution byte-match a golden file — the safety net that pins
the frozen "feel" across refactors. The GitHub Actions workflow (`.github/workflows/ci.yml`) runs
the same gate (vet, build, test, `-race`, the headless guard, and the golden replay) on every
push/PR. The module is named `phootball` (the working directory is `haxball-clone`); the import
path everywhere is `phootball/...`.

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

### Directional cones — where facing makes a touch more effective

Every ball interaction is measured by **the angle off your `Facing`** (`ballAngle`): `0°` = ball
dead ahead, `180°` = directly behind. The front is more effective — each cone below is **full
strength inside its half-angle, then fades** toward the back. Cones are written `±half-angle
(total)`; the listed modifiers widen/narrow that half-angle.

**Ball-control cones** (full strength inside the half-angle, then the named angle curve decays to
its "behind" value — see *angle curves* above for the endpoints):

- **Capture cone — ±30° (`CaptureConeRadians`) + a 55° soft band (`CaptureConeSoft`).** Inside ±30°
  the ball reliably sticks (the capture-speed threshold is at its front peak, **260**); across the
  next 55° (out to ~85°) that threshold decays to the side/back floor (**30**), so off-front touches
  bounce off instead of sticking. Off-front hits also **bounce livelier** (restitution
  ×`(1+(1-cone))`, up to 2×). Widened by a team **buff +3°** (`ConeBonusRadians`) and a held **trap
  +3°** (`CaptureConeTrapBonus`); **narrowed by a team debuff −12°** (`ConeDebuffRadians` —
  asymmetric, so a marked opponent catches far less). *(A cone-weighted impulse split also exists
  but is off by default — `uniformImpulseScale = true`.)*
- **Control cone — ±22° (`ControlConeRadians`, 44° total).** Full strength here for **two** forces
  that then taper to the back: the **sticky hold** (Stickiness `420→30`, resists the ball
  separating) and the **roll-to-front control** (Control `1500→300`, steers the ball onto the
  front). Widened by your **own possession +5°** (`ControlConePossessionBonus`) and a **trap +2°**
  (`ControlConeTrapBonus`); not team-buff scaled. The roll-to-front *magnitude* additionally gets a
  trap bonus (`TrapControlBonus`) and a possession bonus (`PossessionControlBonus`, +9% at full).
- **Centre-pull cone — ±5° (`CenterPullConeRadians`, 10° total).** Full-strength centre-pull spring
  (CenterPull `800→0`) that drags a near, not-yet-touching ball into contact. Widened by **own
  possession +1°/side** (`CenterPullConePossessionBonus`) and a **trap +2°/side**
  (`CenterPullConeTrapBonus`).
**Shot cones** (measured the instant you shoot — full detail under *the fire cone, aim assist & power*):

- **Fire cone — ±90° (180° total, `fireConeHalfAngle`) — the full front hemisphere.** Whether a shot
  fires at all, *and* where the aim assist applies: a shot only fires within ±90° of facing, and
  `ShootAimAssist 0.97` steers its launch toward your facing by the same amount everywhere in the
  cone. Aim and fire are one and the same region — there is no separate "aim cone".
- **Full-power cone — ±30° (60° total, `fullPowerHalfAngle`).** *How hard a shot fires.* Full power
  anywhere inside ±30°, then power tapers linearly (`shotFalloffExp 1.0`) to 0 at the ±90° fire-cone
  edge.

**AI recovery cone** (not a sim cone — it lives in the AI's own tuning, the `control` package):

- **Recovery cone — ±50° (`recoverConeRad`).** The front half-angle within which the AI keeps the
  ball; once it drifts past, the AI turns to scoop it back to the front (recovery + dribble turn
  cap). This used to be a `PlayerTuning` field (`PossessionArcRadians`) gating an unused "control"
  build-up state — both the state and the field were removed, and the value now lives only in the AI.

### Possession mechanics

There are **two independent possession systems**. One is per-player (how firmly *you* hold
the ball); the other is per-team (how much *your team* has built up control). They affect
different things and never share state.

**The reach — who is "in the contest" (`possessionReach`).** Both systems decide who counts using
one per-player reach: **`possessionReach()` = `PossessionRange` (default `5`, a surface gap), and it
is NOT trap-extended.** It is always the *acting* player's own reach — you build possession while
the ball is within *your* reach, and a marker drains a target while the target is within *the
marker's* reach. So it is **asymmetric**: give one player a larger `PossessionRange` and it contests
the ball, and **marks/drains opponents, from further away** — it doesn't have to get as close. (A
held **trap** pulls the *ball* in from further — `pullRadius()` — but it does **not** widen this
reach: trapping never lets you contest the ball or mark a player from further.)

#### 1. Player possession (per-player — `Player.possession`, `updatePossession`)

*What it is:* the "ball at my feet" state — built toward 1 while *you* are the player drawing
the ball, decayed otherwise. You build it while the ball is within your **possession reach**
(`possessionReach()`, above — *not* trap-extended) — you do **not** have to be touching it — but
only **one** player builds at a time (see *Who builds*).

*Intent:* a player who has had the ball a moment holds it more firmly and carries it a touch
better, at a small speed cost — so a fresh poke is easy to steal but an established carry is
sticky. Single-builder so a contest is *decisive*: when an opponent closes a carrier down, the
newcomer's bar rises while the carrier's falls, instead of both pinning at max.

*Who builds it (`advancePossessionBuilder` + `engaged`):* a player is **engaged** if the ball is
within its possession reach **or** it is body-bumping the current holder (so a physical challenge
counts even when the ball isn't quite in reach). Of the engaged players, the **latest** one to
become engaged is the *sole* builder — everyone else *decays* toward 0 (not instantly to zero).
Each player is stamped the tick it engages; the newest stamp wins. If the latest builder loses
the ball, the build falls back to whoever still has it in reach.

*Won/denied in a contest (`updateBallPossessor` + the per-tick possession update):* the sole
**builder** — the latest player with the ball in its possession reach (NOT trap-extended) — *gains* possession,
so you only build while you can actually reach the ball (Rule 3). A **holder marked by an opponent
that is NOT near the ball** is *denied*: its possession **drains** at **PossessionStealRate** while
the marker gains **nothing** (Rule 2 — marking denies, but you only take the ball *for yourself* if
you can reach it). Drain and gain are **decoupled** (no transfer): a steal happens when a ball-near
opponent out-builds the holder; pure marking just bleeds the holder toward 0. The ball changes
hands once a different builder out-holds the current holder. A **loose** ball — no holder still in
reach — is claimed only on an actual *touch*, so a ball merely in range or flying past doesn't flip
possession (protects passes). A player who *passed* has possession 0 (`shoot` resets it), so a
clean reception starts cold.

*Why each rule is shaped this way:*
- **Marking only DRAINS (it doesn't steal).** A player's possession is their **grip** on the
  ball, so eroding it is the *setup* for a steal, not the steal itself. Marking a carrier whose
  ball you can't yet reach drains their grip without handing you anything — because you haven't
  actually won the ball. The point is leverage: a loosened grip makes the ball **easier to knock
  free**, and (just as important) a drained carrier **can't instantly re-grip it** the moment it
  pops loose, so the same pressure that wins the ball also keeps you from immediately losing it
  back.
- **You only GAIN grip with the ball in reach.** Acquisition requires actually getting the ball
  into *your* possession reach — which is why drain and gain are **decoupled**: marking *denies*, only
  reaching the ball *acquires*. Without this, standing next to a carrier would magically transfer
  the ball to you; with it, tight marking is a real but honest tool — it pries the ball loose,
  then you still have to collect it.

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
**PossessionStealRate** `1.0` (possession/sec a marked holder is *denied/drained* at). The
build/steal/mark **reach** is the **possession reach** (`possessionReach()` = `PossessionRange` `5`, NOT trap-extended, per acting player — a larger `PossessionRange` reaches further; see *the reach*);
**PossessionSpeedFactor** / **PossessionAccelFactor** `0.925` (~7.5% slower). (A parallel
**control** build-up state once tracked whether the ball sat within a ~50° front cone, but it was
never wired to any mechanic and has been removed; the AI keeps its own front cone — see
*directional cones*.)

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
- **Handover (gradual — never an instant reset)** — an opponent contesting the ball does NOT
  clear/flip the charge outright. It **drains** toward zero (below); only once it bottoms out *and*
  an opponent actually has the ball in reach does ownership hand over — that team then builds its
  own charge from zero and the former owner becomes the conceding side ("drain to black on both,
  *then* the opponent gets the boost"). A kickoff/shootout still clears it.
- **Team-wide drain — marked carrier (Rule 1, has-ball)** — while a boosted player that HAS the
  ball is within an opponent's **possession reach** (the marker's own — NOT trap-extended; an opponent marking the carrier), OR an
  opponent has the ball itself in reach, the owning team's **receiving buff is suppressed**
  (`possBuffDrain` scales only the *owners'* published coefficient toward 0) and the charge drains
  toward the gradual handover above. Crucially this erodes **only the owning team's buff — the
  conceding team's debuff (`OtherTeam·strength`) is left untouched**: pressing the carrier takes
  away *their* clean touches without giving the pressing side (which has conceded possession) any
  cleaner ones of its own. Sustained pressure wears the boost down, then hands it over; a glancing
  approach only nicks it.
- **Per-player drain — marked off-ball player (Rule 1, no-ball)** — a boosted player WITHOUT the
  ball that has an opponent within **that opponent's possession reach** (marked off the ball) has only *its own* published
  `touchCoef` eroded: a per-player `boostDrain` (0..1) rises at `boostContactDrainPerSecond` (2.0/s)
  while marked and recovers at `boostContactRecoverPerSecond` (1.5/s) otherwise, scaling that
  player's coef by `(1 − boostDrain)`. The **team charge and team-mates are untouched** — only the
  marked player's clean-touch buff fades toward neutral. (A conceding player has no boost to lose.)

*Why each rule is shaped this way:*
- **A contest drains the BUFF, never the DEBUFF.** The team charge is a **receiving** boost — it
  makes the owning team's touches clean so passes stick. Pressing an opponent's carrier should
  take *that* away (they fumble under pressure), but it should **not** reward the pressing side,
  which has **conceded** possession and hasn't earned a clean touch. So a contest suppresses only
  the **owning team's buff** and leaves the conceding team's **debuff in place** — you deny their
  clean touches, you don't heal your own. (Otherwise the act of pressing would quietly cure your
  own fumbliness, which makes no sense: you haven't built any control, you've just got close.)
- **The boost only changes hands by WINNING the ball.** There is no instant "touch it and the
  buff is yours". A contest drains the charge to zero on **both** sides first, and only once an
  opponent actually has the ball in reach does the new owner start building it **up from zero**.
  This makes the boost feel earned (sustained possession), not stolen on contact, and a single
  poke can interrupt a buildup without flipping the whole advantage to a team that hasn't held
  the ball yet.
- **Marking off the ball is localized (per-player), not team-wide.** An opponent who marks a
  boosted player that *isn't* carrying only spoils **that one player's** clean touch (`boostDrain`),
  not the whole team's charge — pressure should cost the opponent exactly the player it commits a
  marker to, no more, and it lifts the instant the marker leaves.

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
- **Opponent gets the ball (touch or pull-range)** → the charge **drains gradually to zero, then
  hands over** to that team, which builds anew. *(current — Rule 4; a kickoff/shootout still clears
  it instantly.)* The old behaviour was an **instant reset/flip** on the opponent's touch *(no
  longer used — too abrupt).*
- **Opponent within possession reach of the boosted ball-carrier** → drains the **whole team** charge
  (`teamDrainPerSecond` 1.0). *(current — Rule 1, has-ball: marking the carrier wears the buff down
  without winning the ball.)*
- **Opponent within possession reach of a boosted player OFF the ball** → drains only **that player's**
  touch-boost (`boostDrain`), leaving the team charge and team-mates intact. *(current — Rule 1,
  no-ball: an opponent only spoils the player it is actually marking.)*
- *Severity* is always a gradual **drain** now, never an instant **reset** *(reset = option, not
  used — the gradual drain reads more clearly and rewards sustained pressure).*

**B. PLAYER possession — who builds it, and how it is taken:**
- **Build (gain)** → only the sole **builder**, the latest player with the **ball within its
  possession reach** (`inPullRange` → `possessionReach()`, NOT trap-extended). *(current —
  Rule 3: you gain only while you can actually reach the ball; ball-touching alone, the original
  model, is superseded.)*
- **Denial (drain)** → a holder **marked by an opponent that is NOT near the ball** is drained at
  `PossessionStealRate`, and that marker gains nothing. *(current — Rule 2: tight marking denies the
  carrier even when you can't take the ball for yourself.)*
- **Steal** → a **ball-near** opponent out-builds the holder and takes over; drain and gain are
  decoupled (no transfer). *(current.)*
- **Loose-ball takeover** (no holder still in reach): claim only on an **actual touch** *(current —
  protects passes)* vs on **pull-range** *(rejected: a ball flying past would flip possession,
  which tanks passing).*

The current design favours **gradual** triggers grounded in the ball or in real marking —
possession changes happen over time and reward positioning. The one proximity-only effect is
**denial** (marking erodes a carrier's possession/buff but can NOT hand it to you — you still need
the ball to gain), which keeps it from feeling like a magnetic, take-it-by-standing-nearby steal.

### `PlayerStats` — charged shot

- **MinShootFactor** `0.35` — a tap fires at 35% power, full charge at 100%.
- **ShootSpeedFactor** `0.35` / **ShootAccelFactor** `0.4` — speed/accel at full charge
  (you're slow while winding up).

### `PlayerStats` — the fire cone, aim assist & power

A left-click shot is governed by **one cone** (where it fires and aims) and **one cone inside it**
(how hard), each measured as the angle off your `Facing` (0° = dead ahead):

- **Fire cone — ±90° (180° total, `fireConeHalfAngle`) — the full front hemisphere.** *Whether a shot
  fires, and where it aims.* The left-click shot works across the whole front hemisphere; a ball at
  or behind ±90° can't be shot (poke it with the middle-click push instead). The **aim assist** lives
  on this same cone — there is no separate aim cone: a shot is kicked along the radial (player→ball),
  but anywhere in the cone the launch is blended toward your `Facing` by **ShootAimAssist** `0.97`
  (`0` = raw radial physics, `1` = exactly along the facing, `0.97` = 97% of the way there), applied
  **uniformly** with no angular falloff — so the shot goes where you aim equally well anywhere in
  front, centred or out at the side.
- **Full-power cone — ±30° (60° total, `fullPowerHalfAngle`).** *How hard it fires.* Power is
  **full** anywhere inside ±30°, then tapers — **linearly** (`shotFalloffExp` `1.0`) — to **0 at
  the ±90° fire-cone edge**. So a shot in front of you is at full strength, an angled one is steadily
  weaker, and one right at the ±90° side has almost nothing. Only the power tapers; the aim stays
  full across the whole cone.

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
  **BallMass** `1.5` — the ball:player mass ratio (`1.5 : 20`) drives the contact: 
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
  toward `Facing` by the aim assist when the ball is in the front 180° hemisphere (±90° of facing).
