ultracode

# Mission: Take the Go HaxBall-clone from solid prototype to production-grade, add rich match stats & play-by-play, and make the AI<=human boundary real and tested — all without changing the physics feel.

You are an autonomous senior Go engineer working in exhaustive multi-agent mode. The repository at `/home/jack/Desktop/hax/haxball-clone` is a HaxBall-style 2D physics football game in Go (module `phootball`, Ebiten for rendering only). Your job is a complete, behaviour-preserving production overhaul plus one new feature: rich per-player/per-team STATS and a chronological PLAY-BY-PLAY event log, persistable per game and surfaced live.

You have NO prior context beyond this prompt and the actual code. Read the real files before changing them. Every claim about line numbers below is approximate — verify against the live source.

---

## REPOSITORY ARCHITECTURE (real packages & files)

- `internal/sim` — authoritative, deterministic simulation. Key files:
  - `match.go` — `Match` struct, `Step(inputs map[int]Intent, deltaTime float64)` tick pipeline, `applyIntent`, `resolveInteractions`, `resetKickoff`, `BuildMatch`/`BuildSolo`/`BuildDuo`/`buildFormation`, `applyConfig`. Package consts `ballWallRestitution=0.90`, `playerWallRestitution=0.50`, `obstacleRestitution`, `netRestitution`.
  - `intent.go` — `Intent{Move, Throttle, Aim, ShootHeld, Trap, CancelCharge}` — the SOLE per-tick action channel.
  - `view.go` — read-only `View`/`PlayerView`/`BallView`/`FieldView` interfaces + live-match wrappers — the SOLE observation channel. Exposes `Me`, `Carrier`, `Teammates`, `Squad`, `Opponents`, `Field`, `Tick`, `Clock`, `Seed`, `BallFriction`, etc.
  - `interaction.go` — ball<->player dribble/attraction/capture/shoot physics. **FROZEN FEEL.** Contains `shoot(player, ball) bool`, `handleBallToPlayerInteraction(...) (touched bool, bounce float64)`, retention/anti-fling math.
  - `entity.go` — `Ball`, `Player`, move/turn-rate, charge constants (`shootChargeMax`, `trapChargeTime`), `NormShootCharge`, `NewBall(center, radius)` (friction -0.3, mass 1.5 hard-coded).
  - `scoring.go` — `Touch`, `TouchKind{TouchDribble,TouchKick}`, `ScoreEvent`, `recordTouch(p, kind)` (the central touch sink, lossy ring cap 8, collapses repeat touches, cleared at kickoff), `resolveGoal(side) ScoreEvent`, `findAssist`, `deflectionShooter`.
  - `stats.go` — `PlayerStats` = per-player **physics TUNING** block (NOT match stats) + aim-assist + `DefaultStats`.
  - `curves.go` — `AngleCurve` shapes. **FROZEN FEEL.**
  - `field.go` — `Field` geometry, goal/net build, `CheckGoal`, `goalMouthRange`, `ConfineBall`/`ConfinePlayer` (wall feel).
  - `rules.go`, `phase.go`, `penalties.go` (shootout state machine + `stepPenaltyPlay` which DUPLICATES the interaction order + `recordKick`), `zonerule.go` (offside/box keep-out + `ballCarrier()` firm-possession test), `roles.go` (`Role` enum + `StatsForRole`), `events.go` (`SoundEvent` + `DrainEvents`/`Sounds`), `rng.go` (seeded RNG + `coinToss`), `goal.go`/`score.go`/`team.go`.
- `internal/physics` — gameplay-agnostic rigid-body engine. **FROZEN FEEL.** `body.go` (`Body.Update(dt)`: integrate -> soft speed cap -> friction), `collision.go` (`Collide`/`Resolve`, `ReflectInside`/`ClampInside`), `shape.go` (`Circle`/`Segment`/AABB). **No Ebiten allowed here.**
- `internal/geom` — `vec.go` pure vector math. **FROZEN FEEL** for the primitives.
- `internal/control` — the AI. Files: `controller.go` (`Controller interface { Intent(view sim.View) sim.Intent }`), `ai.go` (decision loop, `reactTicks` latency, `LastAction()`), `perception.go`, `predict.go`, `teamplan.go`, `abilities.go` (`shootAt` charge controller — sets `CancelCharge=true` on overtime branch), `onball.go` (511-line god-file: `bestPass`, scoring weights), `offball.go`, `keeper.go`, `formation.go`, `steering.go`, `zones.go`, `tuning.go` (`aiTuning` + `Skill` tiers + `paramsForSkill`; dead fields `shootGap`/`cornerInset`/`turnTrapSettled`/`trapApproachFactor`), `noise.go` (hash-based, no RNG), `geomutil.go`.
- `internal/input` — `human.go` (`Human` controller, WASD/mouse -> Intent; ignores the View; depends on `render.ScreenToWorld`).
- `internal/netcode` — `protocol.go` (`Snapshot`, `EntityState`, `ClientMsg`, `SnapshotOf`), `server.go` (authoritative TCP+gob server, `sanitizeIntent`, `freeSlot`, `tickLoop`, `Bot` interface), `client.go` (`Dial`, `Send`, `Snapshot()`).
- `internal/render` — `render.go` (draw pipeline + HUD + `ScreenToWorld` via **package-global transform** + `BeginUI`), `camera.go` (client-only smoothing).
- `internal/menu` — `app.go` (AppState machine, owns match/controllers/camera/audio, `afterStep`), `settings.go` (`Settings` embeds `config.MatchSetup`, `BuildMatch`, `Config()` swallows errors), `ui.go` (immediate-mode widgets + palette).
- `internal/config` — `config.go` (`Config{Geometry,Ruleset,Tuning,Seed}`, `Default()`), `geometry.go`, `ruleset.go`, `matchsetup.go` (`MatchSetup` + `Validate`/`Build`), `flags.go` (`ParseGame`/`ParseServer`/`ParseClient`, `validDifficulty` — hand-copied skill enum), `tuning.go` (`Tuning` + `DefaultTuning`; **mostly unread by sim — a no-op trap**).
- `cmd/{client,server,game}/main.go` — entry points; each duplicates a `code(err)` exit-mapper + `signal.NotifyContext` scaffold.
- `go.mod` — module `phootball`, `go 1.21`, ebiten v2.7.3 (toolchain is go1.26).

There is uncommitted work already on disk (many `internal/control/*.go` and `internal/sim/view.go`, `intent.go`, etc. are modified/untracked). **Begin by reading the real current state of every file you touch** — do not trust the map's line numbers; re-derive them.

---

## HARD CONSTRAINTS (non-negotiable — read twice)

**(1) DO NOT change the physics behaviour or "feel".** The following are FROZEN — behaviour-preserving refactors (rename, extract, move constants into config *with byte-identical values*, add hooks guarded by `if rec != nil`) are allowed, but ANY change that alters the simulated trajectory, timing, or outcome is forbidden:
   - `internal/sim/interaction.go` (ball<->player dribble/attraction/capture/shoot, retention & anti-fling math).
   - `internal/sim/curves.go` (`AngleCurve` shapes), `internal/sim/stats.go` (`DefaultStats` tuning curves, aim-assist), `internal/sim/entity.go` charge constants and `NewBall` physical values.
   - `internal/physics/*` (body integration, soft speed cap, friction, collision impulse) and `internal/geom/vec.go` math.
   - The interaction ORDER inside `Step`/`resolveInteractions`/`stepPenaltyPlay` and `ConfineBall`/`ConfinePlayer` restitution behaviour.
   You MUST land a golden-replay characterization test FIRST (Workstream F.1) and treat ANY golden diff as a hard failure. Tuning the feel is NOT in scope; preserving it exactly is mandatory.

**(2) The AI is limited to EXACTLY what a human can do.** Both AI and human act ONLY through `Controller.Intent(view sim.View) sim.Intent` over real ticks, and observe ONLY through `sim.View`. The AI must not:
   - charge a shot/trap faster than a human (charge accumulates by `deltaTime`, driven only by `Intent.ShootHeld`/`Intent.Trap`);
   - read hidden state a human can't see (e.g. exact opponent `Velocity()`/internal `Heading()`/`Possession()`/full tuning `Stats()`, the raw RNG `Seed()`, or any aggregated match-stats/recorder).
   This boundary must be ENFORCED at the type level and AUDITED by tests.

**(3) Preserve determinism.** No wall-clock (`time.Now`) and no unseeded RNG anywhere in `internal/sim` or `internal/control`. All variety is seed-driven (sim `rng.go` `coinToss`, control `noise.go` hash). Stats recording must be deterministic too (key off `m.Clock`/`m.Tick` only). Replays must be byte-identical.

**(4) Keep the build green.** After every change: `go build ./...`, `go vet ./...`, `go test ./...`, and `go test -race ./...` must pass. The headless-server invariant must hold: `go list -deps ./cmd/server | grep ebiten` returns nothing. Existing tests (e.g. `TestCancelChargeSuppressesKick`, control `TestDeterminism`, the AI sweeps) stay green.

---

## EXECUTION PLAYBOOK (how to work)

- Work in PHASES, fanning out sub-agents per workstream where independent, but SERIALIZE anything that touches `internal/sim` physics-adjacent files behind the golden-replay test.
- **Phase order is mandatory:** F.1 (golden replay test) → D-P0 items (config-tuning threading, render-globals, netcode race) → the `PlayerStats`->`PlayerTuning` rename (B) → A (stats feature) in parallel with B (boundary) → C (AI quality) → D-rest + E (UI/HUD) → F (full test build-out) → docs/CI.
- After EACH change set: run `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`. If a golden trace diffs, STOP and revert — you altered the feel.
- Run AI-vs-AI simulations to validate behaviour and gather stats, INCLUDING the large 6-a-side map (use `config.LargeGeometry`, formation build). Reuse the existing AI harness (`internal/control/ai_test.go`, `passcompletion`/`measureKicks`-style helpers) across multiple seeds (>=6). Compare pass%, shots, on-target, goals before/after the boundary de-powering and record any regression.
- Make SCOPED commits per workstream/sub-item with clear messages. Branch off `main` (do not commit directly to `main`). Only commit/push when a workstream's acceptance criteria pass. End commit messages with the required `Co-Authored-By` trailer.
- NEVER alter physics feel. When in doubt, add a `if rec != nil` hook and a test, not a behaviour change.

---

## WORKSTREAM A — Match data, play-by-play recording & live stats

**Objective:** Add an opt-in, deterministic, WRITE-ONLY recorder in `internal/sim` that captures a chronological event log and folds it into per-player and per-team aggregates, hooked at existing authoritative sinks with ZERO physics-behaviour change, exposed via a clean read API, projected over netcode, and persisted as JSON per game. It must NOT be reachable through `sim.View`.

### A.1 Recorder subsystem (new file `internal/sim/record.go`)
Define (all with `json` struct tags):
- `type EventKind int` with consts: `EvTouch, EvPass, EvPassIncomplete, EvInterception, EvTackle, EvShot, EvShotOnTarget, EvSave, EvGoal, EvOwnGoal, EvClearance, EvOutOfPlay, EvKickoff, EvPenaltyKick`.
- `type PassDir int` with `PassForward, PassSideways, PassBackward`.
- `type Event struct { Tick uint64; Time float64; Kind EventKind; Player int; Team Side; Target int /* recipient/victim, -1 if none */; Pos geom.Vec; BallVel geom.Vec; Dir PassDir; Power float64; Flags uint8 }`.
- `type PlayerStat struct { PlayerID, Number int; Role Role; Side Side; Touches, PassesAttempted, PassesCompleted, PassesForward, PassesSideways, PassesBackward, KeyPasses, Assists, Interceptions, Tackles, PossessionWins, Saves, Shots, ShotsOnTarget, Goals, OwnGoals, Clearances int; PossessionSeconds, DistanceCovered float64; ThirdSeconds [3]float64 }` (thirds are per-team-relative: index 0 = own defensive third .. 2 = attacking third).
- `type TeamStat struct { Side Side; Name string; Goals, Shots, ShotsOnTarget, Passes, PassesCompleted, Interceptions, Saves int; PossessionSeconds float64; ThirdSeconds [3]float64 }` with method `PossessionPct(total float64) float64`.
- `type MatchStats struct { Players []PlayerStat; Teams []TeamStat; Events []Event }` (stable order keyed by `m.Players`/`m.Teams` order).
- `type Recorder struct { ... }` holding `Events []Event`, `Players map[int]*PlayerStat`, `Teams map[Side]*TeamStat`, and cross-event derivation state: `lastKickerID int`, `lastKickWasShot, lastKickOnTarget bool`, `lastKickTick uint64`, `lastShotTarget Side`, `prevPos map[int]geom.Vec`, plus a `drainIdx int` cursor for per-tick deltas.
- `func NewRecorder(m *Match) *Recorder` — pre-seeds `Players`/`Teams` from `m.Players`/`m.Teams` so every roster slot exists in deterministic order.
- All mutation helpers are methods on `*Recorder` and must be **nil-safe** (`func (r *Recorder) onTouch(...) { if r == nil { return } ... }`) so call sites need only `m.rec.onX(...)`.
- `func (r *Recorder) DrainNewEvents() []Event` — returns events appended since the last drain (mirrors `events.go` `DrainEvents`).

### A.2 Match wiring (`match.go`)
- Add unexported field `rec *Recorder` to `Match`.
- Add `func (m *Match) EnableRecording()` (sets `m.rec = NewRecorder(m)`), `func (m *Match) Recorder() *Recorder`, and `func (m *Match) Stats() MatchStats` (returns a deep COPY into stable-ordered slices — never leaks internal maps/pointers).
- Recording is OFF by default (nil recorder). Every hook is `if m.rec != nil { m.rec.onX(...) }` (or rely on nil-safe receivers). Disabled recording must be byte-identical to today.

### A.3 Hooks (at EXISTING authoritative sinks — verify exact lines in source)
1. **Touches / passes / interceptions / tackles** — in `recordTouch` (`scoring.go`). Mirror its existing distinct-toucher collapse: count one `Touches` per genuinely NEW distinct toucher. On a transition where the previous distinct touch was `TouchKick`: same team next toucher -> completed `Pass` (increment passer `PassesCompleted` + classify direction, increment receiver-side as needed); opponent next toucher -> previous kicker `PassesAttempted` (not Completed) + opponent `Interceptions`. When the previous touch was `TouchDribble` and an opponent takes it -> `Tackle`/`PossessionWin`. A key pass = a completed pass whose receiver's next event is a Shot.
2. **Shots / shots-on-target** — in the `Step` kick/shoot loop (`match.go`, the `if p.WantsKick { if shoot(p, m.Ball) {...} }` block) and its mirror in `stepPenaltyPlay`. On a successful `shoot()`, call `m.rec.onKick(p, m.Ball, m)`: always store kicker + from-position (for pass derivation); count a `Shot` only when goal-directed; test shot-on-target by ray-casting the post-kick `ball.Velocity` from `ball.Position` against the attacking goal mouth segment (`f.GoalOn(attacking.Opponent()).Mouth`, reuse `goalMouthRange`/`CheckGoal` geometry — READ-ONLY ray, no mutation). Bound the ray by a max distance derived from ball friction so a straight ray doesn't over-count long shots that would decelerate and drop short. Set the latch fields for save derivation.
3. **Saves / woodwork** — in `resolveInteractions` (`match.go`) and the penalty mirror. When `handleBallToPlayerInteraction` reports a touch by a defending player while `lastKickWasShot && lastKickOnTarget` and the ball has NOT yet crossed (before `CheckGoal` fires), and the toucher is `RoleGoalkeeper` OR inside its own goal-area box -> record `EvSave`. Capture the existing `physics.Collide` return on post/net to flag a woodwork hit on an on-target shot (read the bool already returned; do not change the call).
4. **Goals / assists / own-goals / deflections** — in `resolveGoal` (`scoring.go`), AFTER it computes the `ScoreEvent`. Call `m.rec.onGoal(ev, m)` and emit `EvGoal`/`EvOwnGoal` reading `ev.Scorer/Assist/OwnGoal/Deflected` — DO NOT re-derive (single source of truth shared with the HUD `goalText`).
5. **Per-tick possession / distance / thirds** — add `m.rec.sample(m, deltaTime)` in `Step` right after the possession-update loop (uses `m.ballCarrier()` from `zonerule.go`): add `deltaTime` to the firm carrier's and their team's `PossessionSeconds` (loose-ball ticks count toward neither); for every player add `geom.Dist(p.Position, prevPos[id])` to `DistanceCovered` then store `prevPos[id]`; classify ball X into per-team-relative thirds and add `deltaTime` to `ThirdSeconds`. Reads positions only; never writes them.
6. **Kickoff / clearance** — in `resetKickoff` (`match.go`) call `m.rec.onKickoff(m)` to emit `EvKickoff` and reset pass-derivation latches (so a pass is never attributed across a kickoff, mirroring the existing `touchHistory` clear). Classify a defender's `TouchKick` that sends the ball from the defending third past midfield as a `Clearance` (derive in `onKick`).
7. **Penalty kicks** — in `recordKick(scored)` (`penalties.go`) emit `EvPenaltyKick` with made/missed flag + per-player penalty tallies.

### A.4 Persistence (`internal/sim/record_json.go`, or a thin `internal/persist` package if you prefer sim to stay serialization-free)
- `type MatchRecord struct { Schema int; Seed int64; Geometry config.Geometry; Ruleset config.Ruleset; Teams [2]TeamInfo; Players []PlayerInfo; FinalScore [2]int; Winner Side; DurationSeconds float64; Events []Event; PlayerStats []PlayerStat; TeamStats []TeamStat }` with `TeamInfo{Name,Color,Side}` and `PlayerInfo{ID,Number,Role,Side}`.
- `func (r *Recorder) MatchRecord(m *Match) MatchRecord` and `func (mr MatchRecord) WriteJSON(w io.Writer) error` (encoding/json, struct tags). `json.Unmarshal` must round-trip.
- Output filename derived from `Seed` (+ a caller-supplied id), **never `time.Now`**, to stay reproducible.

### A.5 Transport & live HUD plumbing (coordinate with D & E)
- Add `Stats StatsSnapshot` (flattened per-player/per-team counters for the live HUD) and `Events []sim.Event` (THIS TICK's delta only, via `DrainNewEvents`) to `netcode.Snapshot`; project in `SnapshotOf` reading `m.Recorder()` when non-nil. Do NOT resend the full event log every tick.
- `menu.App` calls `EnableRecording()` in `startMatch` and writes a `MatchRecord` JSON on transition to `StateResult`. Headless server may enable + persist on Finished. Optional `-stats-out path` flag in `config.ParseGame` (default off).

**ACCEPTANCE CRITERIA (A):**
- With recording disabled, `Match.Step` is byte-identical to pre-change (golden replay test passes) — zero feel change.
- A scripted seeded match replayed twice with recording ENABLED yields `reflect.DeepEqual` `MatchStats` and identical `Events`; `grep` shows no `time.`/`rand.` in the new files.
- Each distinct-toucher contact increments exactly one `Touches` (repeat same-player contact collapses).
- Forward/square/backward completed passes produce `PassForward/PassSideways/PassBackward` via team attack axis (+X for `SideLeft`, -X for `SideRight`); an opponent intercepting a `TouchKick` increments the intended passer's `PassesAttempted` (not Completed) and the opponent's `Interceptions`.
- A kick whose post-kick velocity ray crosses the goal mouth between the posts counts `ShotsOnTarget`; a wide kick counts `Shots` only — both against the same `goalMouthRange`/`CheckGoal` geometry.
- A defending keeper/in-box defender touching an on-target shot before `CheckGoal` fires increments `Saves` + emits `EvSave`; an uncontested goal records no save; own-goals/deflections read `resolveGoal` and are not double-counted.
- Possession seconds sum correctly (loose-ball excluded); `PossessionPct` derived at read time. Distance for a player at known speed over N ticks equals `speed*dt*N` within float tolerance.
- `MatchRecord.WriteJSON` emits valid JSON with seed/rosters/score/full chronological events/aggregates; round-trips via `json.Unmarshal`; filename derived from `Seed`.
- **Boundary:** a test asserts `sim.View`/`PlayerView` expose NO recorder/stats accessor; the recorder is reachable ONLY via `Match.Recorder()`/`Stats()`.

---

## WORKSTREAM B — AI <-> sim API boundary & capability limiting

**Objective:** Keep the single `Controller.Intent(view sim.View)` seam and make it provably human-equivalent by closing three gaps: (1) narrow the observation surface so the AI sees no more about non-self players than a human's HUD shows; (2) pin charge/trap tick-rate limiting with tests; (3) fix the false `CancelCharge` invariant. No physics/feel change — the View split is a pure method-set narrowing; charge math is untouched.

### B.0 Rename first (mechanical, guarded by existing control tests)
Rename `sim.PlayerStats` -> `sim.PlayerTuning`, `PlayerView.Stats()` -> `PlayerView.Tuning()`, `StatsForRole` -> `TuningForRole`, `DefaultStats` -> `DefaultTuning` (avoid colliding with `config.DefaultTuning` — namespace it or pick a distinct name like `DefaultPlayerTuning`). Update `stats.go`, `view.go`, `entity.go`, `roles.go`, and all `internal/control` callers. This frees the "Stats" name for Workstream A. Land as ONE commit; control tests stay green.

### B.1 Observation audit & View split (`sim/view.go`)
Ground truth for "what a human perceives" is the rendered `Snapshot`: `netcode.EntityState` exposes only `{Position, Facing, Radius, Color, Number, ShootCharge, TrapCharge}`; `render.go` draws facing + both charge gauges for ALL players. NOT rendered anywhere: player `Velocity`, internal `moveHeading` (`PlayerView.Heading()`), `Possession()`, full tuning `Stats()`/`Tuning()`. Yet today the View exposes all four for every player and the AI READS opponent hidden state (e.g. `perception.go` `openDuration` uses `o.Velocity()`; `offball.go`/`keeper.go`/`teamplan.go` use `o.Heading()`; `predict.go` uses `o.Stats().MaxSpeed`/`TurnRate`). Fix:
- Introduce `type ObservedView interface { ID() int; Number() int; Role() Role; Side() Side; Position() geom.Vec; Facing() geom.Vec; Radius() float64; ShootCharge() float64; TrapCharge() float64; Same(ObservedView) bool; SameTeam(ObservedView) bool; AngleToFacing(geom.Vec) float64; BallAngleToFacing(BallView) float64 }` — EXACTLY the human-perceivable fields. (Charges stay because the HUD renders gauges for all players.)
- Introduce `type SelfView interface { ObservedView; Velocity() geom.Vec; Heading() geom.Vec; Possession() float64; Tuning() PlayerTuning; HomePosition() geom.Vec }`.
- Change `View`: `Me(id int) (SelfView, bool)`; `Carrier() (ObservedView, bool)`; `Teammates(of ObservedView) []ObservedView`; `Squad(of ObservedView) []ObservedView`; `Opponents(of ObservedView) []ObservedView`. `matchView.Me` returns `selfView{p}`; the rest return `observedView{p}`. `unwrapPlayer` must type-switch on BOTH wrapper types (unit-test this).
- Remove `View.Seed() int64`; add `View.NoiseSalt(id int) int64` returning a deterministic `hash(seed, id)` so AI variety survives without leaking the raw seed. (Keep the `Match.Seed` field; only remove the View method.)

### B.2 Migrate AI reads (`internal/control`)
The ONLY behavioural change to the AI, a deliberate de-powering toward human capability:
- Add `aiTuning.assumedOppSpeed float64` and `assumedOppTurn float64` (set in `defaultAITuning()` to the current shared `MaxSpeed`/`TurnRate` so the nominal case is closest to today).
- Replace every opponent/teammate `o.Velocity()`, `o.Heading()`, `o.Stats()/.Tuning()` read with `assumedOppSpeed`/`assumedOppTurn` constants and a heading that does NOT read an opponent's committed steering (use a vector toward the ball or the rate-unaware variant). Sites include `perception.go` `openDuration`, `offball.go`, `keeper.go`, `teamplan.go`, `predict.go`. SELF reads via `Me` (`p.me.Velocity()/.Heading()/.Tuning()/.ShootCharge()`) remain legal.
- `perception.go` consumes `NoiseSalt(id)` instead of `Seed()`.

### B.3 CancelCharge invariant fix
`intent.go` and `match.go` assert "the AI never sets CancelCharge", but `abilities.go` `shootAt` sets `in.CancelCharge = true` on the overtime branch. The action (abort a stuck charge) IS human-reachable (right-click). Keep the AI behaviour and CORRECT the comments to: "CancelCharge is a human-reachable signal (right-click); the AI may also use it to abort a stuck charge, exactly as a human can." Expand the `intent.go` doc to enumerate the human-input -> field mapping (WASD->Move/Throttle, cursor->Aim, LMB->ShootHeld, RMB->Trap, RMB rising edge->CancelCharge).

### B.4 Charge/trap tick-limiting (already correct — pin it)
No code change: `applyIntent` accumulates `shootCharge`/`trapCharge` by `deltaTime`, capped, driven only by Intent. The AI's `shootAt` only HOLDS `ShootHeld` across ticks. Pin with tests (F).

### B.5 Boundary test suite (`internal/control/boundary_test.go`, `internal/sim/chargerate_test.go`)
- Assert a non-self handle (from `Opponents`/`Teammates`/`Carrier`) does NOT satisfy an interface exposing `Velocity()/Heading()/Possession()/Tuning()` (runtime type-assertion must fail).
- Assert every Intent emitted over a scripted multi-second match is human-reachable: `Throttle in [0,1]` and finite, `Move`/`Aim` finite.
- Assert `shootCharge` after holding `ShootHeld` for k ticks of `dt` equals `min(k*dt, shootChargeMax)` — the AI cannot reach full charge faster than a human.
- Assert the AI's overtime cancel and a human right-click cancel produce the same sim result (charge dropped, no kick on release) — extend `sim/cancel_test.go`.
- Determinism: two AIs from the same seed + scripted view emit byte-identical Intent sequences — extend control `TestDeterminism`.

**ACCEPTANCE CRITERIA (B):**
- `grep` across `internal/control` shows NO `.Velocity()/.Heading()/.Possession()/.Tuning()/.Stats()` call on any opponent/teammate handle (only on the `SelfView` from `Me`); opponent motion estimates use `assumedOppSpeed`/`assumedOppTurn`.
- `View` has no `Seed()`; variety derives from `NoiseSalt(id)` (pure hash, no raw seed leak).
- The CancelCharge comments are corrected; the cancel-equivalence test passes.
- All boundary/charge-rate/determinism tests pass; existing suites stay green; headless deps check still passes.
- `ObservedView`'s field set is documented as matching `netcode.Snapshot`/`EntityState` + rendered gauges, with a cross-reference comment in both `sim/view.go` and `netcode/protocol.go`.

---

## WORKSTREAM C — AI quality improvements (still bounded by the API)

**Objective:** Improve clarity, testability, and tuning hygiene of the AI WITHOUT exceeding the human capability boundary from B and WITHOUT changing the sim. All changes act only through Intent over real ticks.

Tasks (file-referenced):
- Delete the dead `aiTuning` fields `shootGap`, `cornerInset`, `turnTrapSettled`, `trapApproachFactor` (verify they are truly unread), OR wire them to their intended use. No dead tuning surface.
- Lift inline magic numbers into named `aiTuning` fields: `onball.go` `const stick = 0.15`, the `bestPass` score weights (e.g. `advance*0.009`, `space*0.006`, `safety*0.5`, clamp `1.12`); `abilities.go` `aimProjectDist=1000`, throttle/blend `0.6`/`0.3`; `keeper.go` misread blend `0.7`/`0.3`, `/30`,`/50` quantization; `offball.go` `scale(60)`, `radius*3`. One place to tune.
- Split `onball.go`: separate pure SCORING (functions returning a typed `[]passCandidate` with named weighted terms) from EXECUTION (turning a chosen action into an Intent). `bestPass` becomes unit-testable. **Pure refactor — behaviour identical** (verify via AI harness pass%/shots/goals across >=6 seeds before/after).
- Harden nil/empty paths: `nearest()` returns `(nil, +Inf)` when the list is empty; guard `p.nearestOppToMe` usage in `bestDribble`/`shield`/`avoid`; add a defensive early return in `AI.Intent` for a foreign/nil View. Removes latent panics, no behaviour change with a normal roster.
- Document and ideally enforce the ID->formation-slot convention (`formation.go`/`teamplan.go`), or derive back-to-front ordering from `Role`/`HomePosition` instead of relying on PlayerID order.
- Replace the obscure `in.Move == (sim.Intent{}).Move` zero-vector check (`ai.go` `applyMoveJitter`) with a clear `geom.Vec{}` comparison.

**ACCEPTANCE CRITERIA (C):**
- No dead `aiTuning` fields; all listed magic numbers live in `aiTuning`.
- `onball.go` scoring is pure functions returning typed candidates; behaviour unchanged (AI harness metrics within tolerance across >=6 seeds, 6-a-side included).
- No panic with empty teammate/opponent lists; defensive early return for foreign View.
- All existing + new control tests pass; determinism preserved.

---

## WORKSTREAM D — API, code structure & production hardening

**Objective:** Production scaffolding and correctness landmines, all behaviour-preserving and guarded by the golden replay test.

### D-P0 (correctness/boundary landmines — do these right after F.1)
- **Config-tuning no-op trap:** `config.Tuning` is stamped onto `Match.Tuning` but only `BallFriction` is read; `match.go` hard-codes `ballWallRestitution=0.90`/`playerWallRestitution=0.50`/`obstacleRestitution`/`netRestitution`, and `NewBall` uses friction `-0.3`/mass `1.5`/radius `7.5`. Thread `m.Tuning.*` into every site (the 4 `Collide` calls in `resolveInteractions`, `field.go` `ConfineBall`/`ConfinePlayer`, `zonerule.go`, `penalties.go`, and `NewBall(center, cfg.Tuning.BallRadius)` with friction/mass from Tuning). Values are byte-equal to `DefaultTuning()`, so the golden trace must NOT change. Make `View.BallFriction()` read the actual ball body friction so it can't drift. Land incrementally; any golden diff = revert.
- **Render transform globals:** Replace the package-global transform (`view`, `worldW`, `worldH`, `camActive`, `camCenter`, `camZoom`) in `render.go` with a `type Viewport struct { worldW, worldH float64; camActive bool; camCenter geom.Vec; camZoom float64 }` plus `(Viewport) ScreenToWorld(x,y int) geom.Vec`; `render.Frame` returns the `Viewport`. `input.Human` and menu cursor mapping take the `Viewport` from the App instead of the package global. Presentational only.
- **Netcode `Client.Send` data race:** Guard `Client.Send` with a send mutex (or a single-writer goroutine fed by a buffered latest-Intent channel) so `Send` and `Close` can't touch the gob encoder/conn concurrently. Add a `-race` CI target.

### D-rest (P1/P2)
- **Netcode protocol robustness:** Add `const ProtoVersion = 1`, a `type Envelope struct { Kind MsgKind; Hello *Hello; Snapshot *Snapshot; Stats *StatsSnapshot }` tagged union, `type Hello struct { ProtoVersion int; AssignedPlayerID int }`. On accept the server sends `Hello` (client learns which entity it controls); reject version mismatch. Add per-conn `SetWriteDeadline`; move broadcast `Encode` off the tick goroutine into a per-conn sender goroutine fed by a latest-snapshot channel that drops stale frames. Expire stale intents (stamp arrival tick; fall back to neutral Intent after N ticks). Add TCP keepalive + read deadline. Log `Encode` failures at Warn. Keep `Step` single-threaded/authoritative (untouched).
- **View boundary narrowing (read-side):** Done structurally in B; ensure `View.Seed()` removal and self-only charge/possession exposure are reflected here and documented as human-equivalent.
- **Error handling/validation:** `menu.Settings.Config()` must surface the build error (store `lastError` shown on the setup screen + log at Warn) instead of silently falling back to `config.Default()`. Make `MatchSetup.Validate` the SINGLE validator; `geomFlags.fill`/`ruleFlags.fill` only populate. Centralize the `EvictGrace` default as a `config.DefaultEvictGrace` const applied in `MatchSetup.Ruleset`. Break difficulty duplication: add `control.ValidSkill(string) bool`/`SkillNames() []string` and validate difficulty in `cmd` (which imports control); remove `config.validDifficulty`. Reconcile flag help with accepted values (camera `follow`/`active` aliases; difficulty aliases; add `impossible` to menu presets or align CLI default).
- **CLI scaffolding:** Extract the triplicated `code(err)` exit-mapper + `signal.NotifyContext` scaffold into a new `internal/cliutil` package (`func Code(err error, prefix string, stderr io.Writer) int`), used by all three `cmd/*/main.go`.
- **Build/CI:** Add a `Makefile` (`build`, `test`, `test-race`, `vet`, `headless` = `go list -deps ./cmd/server | grep -q ebiten && exit 1 || exit 0`) and `.github/workflows/ci.yml` running vet + test + test-race + headless guard + the golden replay test on push/PR. Pin `go.mod`: bump the `go` directive and add a `toolchain` line matching installed go1.26. Document the `phootball` vs `haxball-clone` name divergence.
- **Docs:** Update `README.md` flag table (`-goalarea-box-max`, mark `-gk-box-max` deprecated), document the stats feature + JSON schema, the build/CI story, and the headless invariant. Note that `cmd/game` default lobby path ignores CLI geometry/rules unless a fast-path flag is set.
- **Dead API:** Document or remove unused `physics.ReflectInside`/`ClampInside`, or have `Field` confine helpers delegate to them. Do not change confine behaviour.

**ACCEPTANCE CRITERIA (D):**
- Threading `config.Tuning` changes NO golden-replay bytes (config now authoritative AND feel unchanged); `View.BallFriction()` reads the real ball friction.
- Render is reentrant (no package-global transform); `Viewport.ScreenToWorld` round-trips in a test.
- `go test -race ./...` clean; the `Client.Send` race is gone.
- Server sends `Hello{ProtoVersion, AssignedPlayerID}`; version-mismatched client rejected; a slow client no longer back-pressures the tick loop; a silent client's stale intent expires; `Encode` failures logged at Warn.
- `Settings.Config()` surfaces errors; single validator; `DefaultEvictGrace` centralizes evict behaviour; `config.validDifficulty` removed in favor of `control.ValidSkill`.
- `internal/cliutil` removes the three-way duplication; Makefile + CI workflow exist and are green; `go.mod` toolchain matches installed Go; headless deps check passes.

---

## WORKSTREAM E — Menus, UI/UX & rendering (incl. in-match stats HUD)

**Objective:** Surface live stats and harden the presentation layer, with no feel/determinism impact (all presentational, client-only, no wall-clock/RNG into `Step`).

Tasks:
- Collapse the three near-duplicate match-draw paths (`render.Frame`, `render.Match`, the hand-reassembled `cmd/client/main.go` variant) into ONE full-match renderer; factor the HUD (score/clock/shootout/banner) into a single `drawHUD(screen, hudModel)` taking a plain struct that both local and network paths build.
- Add `render.StatsPanel(vp Viewport, screen *ebiten.Image, model StatsModel)` and a `Tab` toggle in `menu/app.go` `updatePlaying`. `StatsModel` is a plain struct built from `match.Stats()` locally or from `Snapshot.Stats` on the client — identical numbers from both paths. Render at the existing HUD site (`ScoreboardWithClock`).
- Single source of truth for keybindings + control hints: a controls table consumed by both `app.go` input handling and the hint strings in `settings.go`/`render.go` (eliminate the drift between `Scoreboard`, `ScoreboardWithClock`, and the Settings list).
- Harden App state: enforce `match != nil` invariant for `Playing`/`Paused`/`Result` (guard or a small `goto(state)` transition helper managing `prevState` as a stack); ensure Quit-to-Menu clears stale controllers.
- Extract a `Theme`/layout module (the `ui.go` palette + named layout constants) replacing magic panel/button coordinates, enabling consistent spacing and the stats panel.
- Move shared helpers (`clampF`/`clampInt`/`absF`/`dir`/`itoa`) into one util (or reuse `geom`); replace ad-hoc text alignment (`len(s)*3`) with measured widths via a single text-face abstraction.
- Persist the `MatchRecord` JSON on transition to `StateResult` (coordinate with A.4) and show a final stat sheet on the Result screen (alongside Rematch/Quit).
- Keep all stats on the presentational side — do NOT feed any stats/recorder back into `sim.View`.

**ACCEPTANCE CRITERIA (E):**
- One full-match renderer + one `drawHUD`; `cmd/client` builds its HUD/stats model from the snapshot.
- `Tab` toggles a live stats HUD in a local match; the network client renders identical numbers from `Snapshot.Stats`.
- Single keybindings/control-hints source (no drift); App enforces `match != nil`; `Settings.Config()` errors are visible.
- Result screen shows the final stat sheet and a JSON file is written (filename derived from Seed).
- Pure UI logic (camera `clampHalf`/`clampZoom`/`smoothAlpha`, `formatClock`, `itoa`, `Viewport.ScreenToWorld`, settings `cycle`/`clamp*`/`seedSizesFromField`) is unit-tested. No stats exposed via `View`.

---

## WORKSTREAM F — Testing & verification strategy

**Objective:** A layered test foundation that pins the frozen feel, determinism, the boundary, and the new stats — all deterministic, fast, no wall-clock/RNG.

### F.1 — DO THIS FIRST: golden deterministic replay (`internal/sim/replay_test.go`)
Build a fixed-seed match (`config.Default()` via `BuildMatchFromConfig`), drive it with a scripted `[]map[int]Intent` for ~3600 ticks, and assert a recorded trace (per-N-tick ball `Position`/`Velocity`, scores, `LastGoal` attribution) byte-matches a golden file `internal/sim/testdata/replay_default.golden`. Provide a `-update` flag (via `flag.Bool`) to regenerate. This is the safety net for ALL refactors — run it after every change to physics-adjacent code.

### F.2 — Pure unit tests for the currently-untested packages
- `internal/geom` (vec math), `internal/physics` (circle-circle/circle-segment impulse, soft speed cap, static immovability characterization).
- `internal/config` (`ParseGame`/`ParseServer`/`ParseClient` range/`-version`/`-h`/exit-code sentinels; `DefaultMatchSetup().Build()` deep-equals `Default()`; preset `Min`/`Max`/`Center` + `Normalize` idempotency; README example round-trips).
- `internal/netcode` (table-test `sanitizeIntent` NaN/Inf/throttle clamp; `freeSlot` assignment+reuse; `Snapshot`/`Envelope` gob round-trip asserting every `SnapshotOf` field maps; `Run(ctx)` cancellation; handshake version mismatch).
- `internal/render`+`camera` (`clampHalf`/`clampZoom`/`smoothAlpha`/`Viewport.ScreenToWorld` round-trip, `formatClock`/`itoa`), and logging (`ParseLevel`/`ParseFormat`).

### F.3 — Stat-accuracy scenarios (`internal/sim/stats_test.go`)
Script known fixed-seed scenarios — a clean forward pass A->B, an opponent interception, a keeper save of an on-target shot, a deflected goal, an own goal — and assert `MatchStats` exactly equals hand-computed `StatLine`s. Cross-check the recorder's pass-completion% against the independent control harness (`measureKicks`/`passcompletion`) on the same seeds to catch double-counting.

### F.4 — Boundary & determinism (Workstream B tests) + the stats boundary test (`internal/sim/boundary_test.go`) asserting no `View`/`PlayerView` method returns `MatchStats`/`PlayerStat`/`Event`.

### F.5 — AI-vs-AI behavioural harness
Reuse/extend the existing `internal/control/ai_test.go` sweeps across >=6 seeds, INCLUDING a 6-a-side `LargeGeometry` formation match. Record pass%, shots, shots-on-target, goals, possession both before and after the B de-powering and the C `onball.go` refactor; treat large unexplained regressions as failures to investigate. Use these runs to validate the stats feature produces sane football numbers.

**ACCEPTANCE CRITERIA (F):**
- `go test ./...` and `go test -race ./...` pass; `go vet ./...` clean; headless deps check passes.
- The golden replay test is byte-identical before and after every behaviour-preserving refactor.
- Every previously-untested package (`geom`, `physics`, `config`, `netcode`, `render`/`camera`, `logging`) has meaningful tests.
- Stat-accuracy scenarios match hand-computed expectations and agree with the independent control harness; boundary tests pin constraints (2) and (3).
- The AI-vs-AI harness (incl. 6-a-side) runs deterministically and the recorded stats are football-plausible.

---

## DELIVERABLES & DONE CHECKLIST

- [ ] `internal/sim/record.go` + `record_json.go` (or `internal/persist`): deterministic, write-only recorder; `Event`/`PassDir`/`PlayerStat`/`TeamStat`/`MatchStats`/`Recorder`/`MatchRecord`; `Match.EnableRecording()`/`Recorder()`/`Stats()`; hooks at `recordTouch`, the kick loop, `resolveInteractions`, `resolveGoal`, the possession sampler, `resetKickoff`, `recordKick` — all `if m.rec != nil`, default off, byte-identical when disabled.
- [ ] Full stat list recorded: touches, passes (forward/side/back, attempted/completed/incomplete), key passes, assists, interceptions, tackles/possession-wins, saves, shots, shots-on-target, goals, own-goals, clearances, possession seconds/%, distance, time-in-thirds, penalty kicks — per player and per team — plus a chronological event log.
- [ ] JSON persistence per game, filename derived from `Seed`; round-trips; carries seed/rosters/ruleset/geometry/score/events/aggregates.
- [ ] Netcode `Snapshot` carries live `Stats` + per-tick `Events` delta; live stats HUD toggled with `Tab`; identical numbers local vs network; Result screen stat sheet.
- [ ] AI boundary: `SelfView`/`ObservedView` split; opponent `Velocity`/`Heading`/`Possession`/`Tuning` no longer readable; `Seed()` replaced by `NoiseSalt(id)`; `PlayerStats`->`PlayerTuning` rename; CancelCharge invariant corrected; boundary + charge-rate + determinism tests green.
- [ ] AI quality: dead tuning fields removed, magic numbers centralized in `aiTuning`, `onball.go` scoring split into pure testable functions, nil/empty paths hardened — behaviour unchanged (harness-verified across >=6 seeds incl. 6-a-side).
- [ ] Production hardening: `config.Tuning` authoritative (no golden diff); render `Viewport` (no globals); `Client.Send` race fixed; versioned enveloped netcode with handshake/deadlines/intent-expiry/keepalive; `Settings.Config()` surfaces errors; single validator; `internal/cliutil`; `Makefile` + GitHub Actions CI; `go.mod` toolchain pinned; README updated.
- [ ] Tests: golden replay (FIRST), `geom`/`physics`/`config`/`netcode`/`render`/`logging` units, stat-accuracy scenarios, View-excludes-stats boundary, charge-rate, determinism replays, AI-vs-AI sweeps incl. 6-a-side.
- [ ] All four HARD CONSTRAINTS hold: physics feel unchanged (golden green), AI<=human enforced & tested, determinism preserved (no `time.`/unseeded `rand.` in sim/AI — verify by grep), all tests + vet + `-race` + headless deps check green.
- [ ] Scoped commits on a feature branch (not `main`), each with the `Co-Authored-By` trailer; final summary of what changed per workstream and the before/after AI-harness stat numbers.

Begin with Workstream F.1 (golden replay test) and the D-P0 items, then proceed through the phase order in the playbook. Read the real source before editing. When any golden trace diffs, you have changed the feel — stop and revert.