# NEURAL_AI_PROMPT — build & train a neural-network AI for `phootball`

> **This is a driving prompt for a Claude Code run in ultracode (multi-agent Workflow) mode.**
> You are the lead of a team of world-class reinforcement-learning and neural-network engineers.
> Your job: build a neural-network controller for this Go HaxBall-style game (`phootball`) that
> plays **brilliantly** — it passes (tiki-taka, ~90% completion), defends, retains the ball, uses
> all three abilities well, and shoots well — trained **first by imitating the existing rule-based
> AI**, then by **self-play** until it decisively beats that teacher. **It must never cheat.**
>
> Work in phases. **Author and run Workflows** for the parallelizable parts (research fan-out +
> adversarial verification, design judging, evaluation sweeps, per-config data generation). Stay in
> the loop between phases: read each phase's results before launching the next. Verify findings
> adversarially — do not trust a single agent's claim about the codebase or the literature.

---

## 0. Locked decisions (do not relitigate; design around them)

1. **Full pipeline.** Behavioral Cloning + DAgger (warm start) → **PPO self-play with an opponent
   league** to surpass the rule-based teacher. BC+DAgger alone is a valid shippable milestone; the
   league phase is the headline goal.
2. **Compute: 1 GPU + multicore CPU.** The simulation is the bottleneck, not the (small) net, so RL
   is **CPU-env-bound**: many CPU env workers feed a single-GPU PPO learner.
3. **Inference: pure-Go.** A deterministic `float32` forward pass over `//go:embed`-ed weights. **No
   cgo, no ONNX, no BLAS.** This is mandatory — it is the only way to preserve the project's
   headless-server, byte-exact-replay, and cross-platform-determinism invariants.
4. **Integration: a new `neural` difficulty tier**, threaded through the existing difficulty channel
   (selectable in the menu and CLI alongside easy/normal/hard/impossible).

---

## 1. Mission & non-negotiable acceptance gates

The deliverable is a neural controller that satisfies **all** of these. Treat them as hard gates —
a milestone is not "done" until they are green.

- **AI ≤ human (no cheating).** The net may observe **only** what `sim.View` exposes and act **only**
  through `sim.Intent`. It must pass the existing boundary tests (extended to cover it). Specifically:
  it reads opponents/teammates as `ObservedView` (position, facing, radius, role/side/number, the two
  charge gauges — **no** velocity, steering heading, possession, or tuning); it charges shots/traps
  only by holding the input over real ticks (no shortcut to full power); it uses at most one ability
  per tick (Trap > Push > Shoot); and its aim is turn-rate limited like a human cursor (no snap-aim).
- **Headless server stays headless.** `go list -deps ./cmd/server | grep -q ebiten` must print
  nothing, and the server must pull in **no cgo**. Inference is pure Go.
- **Determinism.** The shipped controller is a deterministic function of the view + weights
  (argmax decode; any exploration noise seeded only from `view.NoiseSalt(id)` or the match seed).
  `go test -race ./...` is clean. The golden replay trace is **byte-identical** — so keep every new
  file in `internal/control/*`, a new `internal/policy`, or `cmd/*`, and **never** modify
  `internal/sim`, `internal/physics`, or `internal/geom`.
- **The full gate is green:** `make` = `vet build test test-race headless golden`.

---

## 2. Required research (do this FIRST, as a verified Workflow fan-out)

Before writing code, run a research Workflow: fan out parallel agents, each owning one topic, each
returning a cited summary; then an adversarial pass that refutes shaky claims. Topics:

- **Google Research Football** — env design, observation/feature sets ("SMM"/"simple115" style),
  reward shaping, checkpoint & academy curricula. The closest analog to this game.
- **TiZero** (AAMAS 2023) — curriculum + self-play training of 11v11 football agents from scratch.
- **OpenAI Five** — large-scale PPO self-play for a team game; team reward shaping; "surgery".
- **AlphaStar** — **PFSP** league (main agents, main exploiters, league exploiters) to prevent
  strategy collapse and cyclic forgetting.
- **PPO** (Schulman et al.) with **GAE** — clipping, value loss, entropy bonus, advantage norm.
- **Deep Sets** (Zaheer et al.) and **entity/self-attention encoders** — permutation-invariant,
  variable-count multi-agent observations.
- **DAgger** (Ross et al.) — curing behavioral-cloning compounding error.
- **Invalid-action masking** in policy gradients ("A Closer Look at Invalid Action Masking").
- **Potential-based reward shaping** (Ng, Harada, Russell) — the policy-invariance guarantee that
  prevents reward hacking.
- **Deterministic float inference & exporting nets to Go** — manual MLP export, `float32`
  reproducibility, why ONNX/cgo are inappropriate in a determinism-critical headless server.

Output: a short, cited "prior art → our choices" design note that the rest of the build references.

---

## 3. The codebase contract (read these; they are the whole interface)

The simulation cannot tell a bot from a human — the funnel below is the entire surface. **Read each
file to confirm** (line numbers are anchors, current as of writing; verify them):

### Plug-in point — one method
- `internal/control/controller.go:14` — `type Controller interface { Intent(view sim.View) sim.Intent }`.
  Implementing this **also** satisfies `netcode.Bot` (`internal/netcode/server.go:25`, identical
  signature), so the neural controller is a drop-in for both local play and the authoritative server.
- Reference implementation to study and mirror: `internal/control/ai.go` — `NewAISkill` (`:65`),
  `Intent` (`:96`, note the nil/foreign-view idle guards), `enforceAbilityExclusivity` (`:81`,
  Trap>Push>Shoot with the cancel-on-takeover idiom), `capAim` (`:183`, the off-ball aim rate-limit).

### Observation API — `internal/sim/view.go`
- `View` (`:16`): `Ball() BallView`; `Me(id) (SelfView, bool)`; `Carrier() (ObservedView, bool)`;
  `Teammates/Squad/Opponents(of) []ObservedView`; `Field() FieldView`;
  `AttackingGoalCenter/DefendingGoalCenter(of)`; `KickoffSide()`; `KickoffArmed()`; `Tick()`;
  `Clock()`; `NoiseSalt(id) int64` (deterministic per-player variety — **not** the raw seed);
  `BallFriction()`; `Rules()`. Obtain it with `(*Match).View()` (`:137`).
- `ObservedView` (`:76`) — the legal view of **any** player: `ID, Number, Role, Side, Position,
  Facing, Radius, ShootCharge, TrapCharge, Same, SameTeam, AngleToFacing(pt), BallAngleToFacing(b)`.
  **No** velocity, steering heading, possession build, or tuning. This is the rendered, on-screen
  subset — the anti-cheat boundary.
- `SelfView` (`:105`) — **only for your own player** (via `Me`): `ObservedView` plus `Velocity()`,
  `Heading()`, `Possession()`, `Tuning()`, `HomePosition()`. The type system blocks asserting an
  `ObservedView` up to `SelfView`, so you literally cannot read another player's hidden state.

### Action API — `internal/sim/intent.go:18`
`Intent{ Move geom.Vec (need not be unit), Throttle float64 [0,1], Aim geom.Vec (world point to
face), ShootHeld bool (charges while held, FIRES ON RELEASE), Trap bool, CancelCharge bool (drops
the live charge this tick; release then does NOT fire), Push bool (instant min-power radial poke;
LEVEL signal, fires on the rising edge the sim reconstructs), AimFromCursor bool (leave FALSE for
the AI → facing applies instantly in the sim; rate-limit in the control layer instead) }`.
Charge timing is fixed by the sim: full shot needs **0.75 s** of held `ShootHeld`; full trap 1.25 s;
`dt = 1/60`. Throttle is clamped and all floats must be finite (the server's `sanitizeIntent`).

### Anti-cheat tests you must pass (and extend for the neural controller)
- `internal/control/boundary_test.go`: `TestObservedViewCannotSeeHiddenState` (`:17`),
  `TestNoSeedExposure` (`:52`), `TestAIIntentsAreHumanReachable` (`:65` — over 900 ticks every emitted
  Intent has finite Move/Aim, Throttle ∈ [0,1], never Trap && Push together).
- `internal/sim/chargerate_test.go`: `TestShootChargeAccumulatesByDeltaTime` (`:16` — charge grows
  only by real held ticks, capped), `TestCancelChargeEquivalentForHumanAndAI` (`:44` — cancel
  suppresses the kick).

### Data-generation & evaluation harness to REUSE (do not reinvent)
- `internal/control/ai_test.go` — the pattern: build `map[int]sim.Intent` by calling each controller's
  `Intent(m.View())` (`stepAll`), then `m.Step(in, dt)`; `aiMatch`/`teamMatch` builders; `run(...)`
  loop; `gather(...)` derives goals/passes/turnovers from the carrier sequence.
- Recorder for metrics: `(*Match).EnableRecording()` (`internal/sim/match.go:96`) then
  `(*Match).Stats()` (`:103`) → `MatchStats` (`internal/sim/record.go:127`) with per-player
  `PlayerStat` (`:72`: Touches, PassesAttempted/Completed/Forward/Sideways/Backward, KeyPasses,
  Assists, Interceptions, Tackles, PossessionWins, Saves, Shots, ShotsOnTarget, Goals, OwnGoals,
  Clearances, PossessionSeconds, DistanceCovered, ThirdSeconds[3]) and `TeamStat` (`:102`, incl.
  `PossessionPct`). There is **no** turnover counter — derive it from the event log / carrier
  sequence. The recorder is deliberately unreachable through `View` (the AI can't read stats).
- Determinism/headless invariants live in `internal/sim/match.go` (`Step` is deterministic; the only
  RNG is one seeded `*rand.Rand` used solely for coin tosses).

---

## 4. Architecture — pure-Go inference

Create two new **pure-Go, no-cgo, no-Ebiten** packages:

- **`internal/policy`** — game-agnostic tensor/layer ops, a versioned weight loader, and
  `Forward([]float32) []float32`. It imports nothing from the game (`[]float32` in, `[]float32` out).
  Weights live in `internal/policy/weights/` and are `//go:embed`-ed.
- **`internal/control/neural`** — `Featurize(view, me) []float32`, `Decode(logits, view, me)
  sim.Intent` (action masking + aim rate-limit), cross-tick charge/hold state, and the
  `Controller`/`Bot` implementation. It reuses `enforceAbilityExclusivity` and `capAim` from the
  `control` package.

**Determinism rules for `Forward` (float determinism is the #1 risk — treat it as a contract):**
- Compute in **`float32`** end to end, with a **`float32` accumulator**, summed **strictly
  left-to-right in index order**. No `gonum`/BLAS, no SIMD-autovectorized reductions, no `float64`-
  then-round, nothing FMA-sensitive.
- Hidden activations: **ReLU only** (exact, no transcendentals). Avoid tanh/gelu/sigmoid in hidden
  layers; the policy decodes via **argmax** (ties → lowest index), so no output squashing is needed.
- Add a `Forward` **golden-vector parity test**: the Python exporter dumps N `(input, output)`
  `float32` pairs; a Go test asserts bit-exact agreement. This is the contract that "Go inference ==
  the trained net." Also pin a cross-platform determinism test (same input → same bytes).
- Budget: **≤ ~300k params**; target tens of µs/tick for 11 players. Preallocate scratch buffers on
  the controller; do not allocate per tick.

---

## 5. Observation featurization (cheat-safe, normalized, variable-roster)

Build the feature vector **only** from the View API.

- **Egocentric, attacking-goal-aligned frame:** origin = self position; x-axis =
  `unit(AttackingGoalCenter(me) - self.Position)`, y = left-perp. (This makes left/right teams share
  weights and removes "which way is goal".) Also feed `self.Facing` as a feature *within* this frame.
- **Normalization:** distances by `Field.Width()` (or a fixed reference); speeds by
  `self.Tuning().MaxSpeed`; **angles as `(cos, sin)` pairs**, never raw radians.
- **Permutation-invariant entity encoder (Deep Sets):** a per-entity MLP + symmetric pool
  (concatenate **sum and max**), **separate pools for teammates and opponents**, then concat with the
  self vector. This handles rosters **1..11** natively and beats fixed padded slots. A single
  self-attention block over entities is an allowed stretch goal.
- **Self vector** (~14): velocity in egoframe; heading-vs-facing `(cos,sin)`; possession; own
  ShootCharge/TrapCharge; signed dist to each goal; dist to the four walls; offside-line offset;
  home-position offset; the few `Tuning` scalars that vary (MaxSpeed, TurnRate, PullRange,
  PossessionRange).
- **Ball block** (~7): position & velocity in egoframe; speed; `BallAngleToFacing` `(cos,sin)`;
  in-pull-range flag; `BallFriction`.
- **Per other player** (~12–16, fed to the set encoder): position & facing in egoframe; dist to self;
  dist to ball; ShootCharge/TrapCharge; role one-hot; is-carrier flag; their `AngleToFacing(ball)`.
  **No opponent velocity exists in `ObservedView`** — recover motion from a **short stack (K≈3–4) of
  observed positions** held in the controller's own memory (cheat-safe: a human also infers motion
  across frames from what's on screen). Keep the stack small to stay in the µs budget.
- **Global context** (~10): score differential (clamped); clock fraction; KickoffArmed / my-kickoff
  flags; am-I-closest-to-ball; which side (if any) is the carrier; roster counts; ball's third.

**Go is the source of truth for features.** The Go data-gen and RL-env binaries emit the *already-
featurized* vector; Python only consumes `float32` arrays. This eliminates Go/Python feature skew.

---

## 6. Action space (factored discrete heads + masking + aim cap)

Use **fully factored discrete heads**, argmax-decoded (no continuous heads → easier determinism,
cloning, masking):

- **Move direction:** 8 (or 16) bins + idle.  **Throttle:** {0, 0.5, 1.0} (idle forces 0).
- **Aim:** **relative** angle bins around current facing, spanning only a capped arc per tick — this
  **bakes the human turn-rate limit into the action space** (snap-aim is structurally impossible).
  Decode to a far-projected world point (mirror `capAim`'s projection idiom).
- **Ability:** 4-way {none, shoot-hold, trap, push} + a separate **cancel** flag (masked off unless a
  charge is live).
- **Charging is an emergent multi-tick decision:** own ShootCharge/TrapCharge are inputs, so the
  policy can choose to keep `ShootHeld` true to accumulate, then release (hold→not-hold) to fire.
  Optionally add an explicit "release-now" token to sharpen RL credit assignment.
- **Masking:** forbid Trap+Push at the logit level (cleaner gradients); enforce cancel validity; keep
  the priority Trap>Push>Shoot via the categorical. The decoded intent still passes
  `enforceAbilityExclusivity` and (off-ball) `capAim` as belt-and-suspenders.

---

## 7. Phase 1 — Behavioral Cloning, then DAgger

**P1 (BC).** Write a pure-Go `cmd/datagen` that reuses the `stepAll`/`aiMatch`/`teamMatch` pattern to
roll out **`SkillHard`/`SkillImpossible` self-play** across a coverage sweep — team sizes {1..11},
field presets {small/medium/large}, rulesets {offside on/off, box caps} — emitting Go-featurized
`(obs, action-label)` shards (label = the teacher's `Intent` discretized into the head space). Target
~**20–50M pairs** (CPU-cheap; generate in parallel via a Workflow, one stage per config). Train a
multi-head cross-entropy policy in PyTorch (class-weight the ability head so shoot/trap/push aren't
swamped by "none"); **split by seed/size** (never by frame); evaluate **behaviorally** by dropping the
clone back into the Go harness, not by label accuracy alone.

**P1.5 (DAgger) — required.** 2–4 rounds: roll out the *current clone* in the Go env, label each
visited state with the teacher (`control.NewAISkill`), append, retrain. Stop when behavioral metrics
plateau near the teacher's.

**Milestone M1/M2 (shippable):** BC+DAgger reaches ≈ parity with `SkillHard`/`SkillImpossible` across
≥30 seeds, and passes all §1 gates.

---

## 8. Phase 2 — PPO self-play league

**Env bridge.** A pure-Go `cmd/env` exposes a gym-like reset/step over **length-prefixed binary IPC**
(stdin/stdout or a socket — **not** JSON: it's a throughput and determinism hazard). Per step Python
sends action indices; Go applies `Decode`, calls `m.Step`, and returns the next featurized obs,
reward, done, and the action mask, for each controlled agent. Go owns featurization and masking.
Run **many CPU env-worker processes** feeding the single-GPU learner; the sim is cheap, so scale
throughput on cores.

**Algorithm.** **PPO + GAE**, initialized from the BC weights (avoids sparse-reward cold start).
**Parameter sharing**: one policy drives all controlled players via per-agent egocentric obs (roster-
size agnostic).

**League (prevent collapse).** PFSP over a population: the learner + periodic frozen snapshots + the
**rule-AI anchors** (`SkillEasy..Impossible`). Sample opponents weighted toward those the learner
currently struggles against. Anchors guarantee a skill floor and that the net never forgets how to
beat the teacher.

**Reward (sparse core + careful, hack-proof shaping).** +1 goal-for / −1 goal-against dominates. All
dense terms are **potential-based** (`γΦ(s′) − Φ(s)`, e.g. Φ = ball x-progress toward the attacking
goal) or one-shot-on-event, never per-tick "holding the ball" (that breeds hoarding): completed pass
(+), shot-on-target (+), clean trap reception (+), turnover (−), conceding position (−), ability spam
(−, e.g. trap > ~35% of moving ticks, push spam, impossible-angle shots). Cap total dense magnitude
well below the goal reward. **Watch for reward hacking** by running the eval suite periodically and
flagging degenerate stats (e.g. 99% possession with zero shots).

**Curriculum.** Beat frozen rule-AI (Normal→Hard) → introduce self-play snapshots/league → widen team
sizes, field presets, and rulesets.

**Milestone M3/M4:** league-trained net beats `SkillImpossible` > 60% over ≥30 seeds × multiple sizes,
with healthy stats and no eval regressions.

---

## 9. Export & integration (the `neural` tier)

- Python exporter writes a **versioned binary weights file** (`magic`, version, per-layer shapes, then
  row-major `float32` little-endian) into `internal/policy/weights/`, `//go:embed`-ed; the Go loader
  validates magic/version/shapes loudly. Include the `Forward` parity reference vectors.
- Add `SkillNeural` to the `Skill` enum and thread the name through `SkillFromString`
  (`internal/control/tuning.go:320`, e.g. `"neural"`/`"nn"`) and `SkillNames()` (`:347`) — this
  auto-updates `cliutil.CheckDifficulty` and the `-difficulty` help. At each controller-construction
  site, branch on `SkillNeural` to build the neural controller, else `NewAISkill`:
  `cmd/game/main.go` (`vsAI`), `internal/menu/settings.go` (`BuildMatch`), `internal/menu/net.go`
  (`buildHostMatch`), `cmd/server/main.go`. Add it to the menu's difficulty presets.
- `cmd/server` must remain headless (inference is pure-Go `internal/policy`); re-run the headless
  guard after wiring.

---

## 10. Anti-cheat verification (prove it doesn't cheat)

Extend the existing pins against the neural controller and add new ones:
- A `TestNeuralIntentsAreHumanReachable` mirroring `boundary_test.go:65` (finite Move/Aim, Throttle ∈
  [0,1], never Trap && Push) over ≥900 ticks of neural play.
- Keep `TestObservedViewCannotSeeHiddenState`/`TestNoSeedExposure` posture: an **import/architecture
  guard** that `internal/control/neural` and `internal/policy` import nothing exposing hidden state
  (no `internal/sim` internals beyond the `View`/`Intent` types, no `internal/physics`).
- A charge-rate test: a held streak of neural `ShootHeld` produces exactly the sim's charge curve, and
  a chosen cancel suppresses the kick (mirror `chargerate_test.go`).
- `TestNeuralDeterminism` (same seed+weights → identical score/ball trajectory) and the `Forward`
  golden-vector test; whole suite under `go test -race`.
- A no-facing-jitter test (≤ ~4 reversals/sec), guaranteed by the relative-aim head + `capAim`.

---

## 11. Evaluation protocol & "super good" acceptance

Headless, via `EnableRecording()` + `Stats()`, over **≥30 seeds** × team sizes {1,2,3,4,5,6} × fields
{medium, large} × rulesets {offside on/off}. **Always aggregate** — single matches are chaotic and
often goalless by design. Matchups: NN vs rule-AI (each tier) and NN vs NN (and old-snapshot vs new
for regression). Run these as an evaluation **Workflow** (fan out over the seed×config grid, then
synthesize).

**Acceptance ("super good"):** beats `SkillHard` ≥ 60% and `SkillImpossible` ≥ 55% win rate across the
sweep; **pass completion ~85–90%** (tiki-taka; derive from `PassesCompleted/(…+PassesAttempted)` and
the carrier sequence); possession ≥ 55% vs Hard; shots-on-target ratio healthy (~30–60%); ball-loss/
turnover rate below the teacher's; ability usage sane (trap ≤ ~35% of moving ticks, push not spammed,
charges actually used). Plus **all §1 gates green** and the golden replay byte-identical.

---

## 12. Milestones, risks, and how to run this in ultracode

**Milestones (each gated on the relevant tests):**
- **M0 Scaffolding** — `internal/policy` (Forward + loader + golden test) and `internal/control/neural`
  (featurizer + decoder + masking, random weights wired in), `SkillNeural` threaded; §10 anti-cheat
  tests green against the random-weight net.
- **M1 Datagen + BC** — `cmd/datagen`; PyTorch BC; ≈ parity with `SkillHard`. Shippable.
- **M2 DAgger** — drift removed; ≈ `SkillImpossible`.
- **M3 RL env** — `cmd/env` + binary IPC + vectorized workers; PPO from BC init vs frozen rule-AI.
- **M4 League** — PFSP + anchors; widen sizes/fields/rules; exceed `SkillImpossible`.
- **M5 Final export + eval gate** — parity-gated embedded weights; full §11 sweep passes; `-race`
  clean; golden unchanged.

**Top risks (own them explicitly):** (1) float-inference determinism / Go↔PyTorch parity — *highest*;
(2) no opponent velocity in `ObservedView` (frame-stack it, cheat-safe); (3) BC distribution shift
(DAgger is mandatory); (4) reward hacking (potential-based + event rewards + eval tripwires); (5) sim
throughput for RL (binary IPC, many workers); (6) variable-roster generalization (Deep Sets + train
across sizes, hold one out); (7) multi-tick charge representation; (8) self-play collapse (league +
PFSP + anchors); (9) headless/cgo regression (keep `internal/policy` dependency-free); (10) chaotic
eval metrics (≥30 seeds always).

**ultracode guidance:** use a Workflow for the §2 research fan-out (one agent per topic →
adversarial-verify → synthesize); a judge-panel Workflow to pick the obs/action/architecture design
from independent proposals; a pipeline Workflow for §7 data generation (one stage per config); and a
fan-out Workflow for the §11 eval grid. Between phases, **read the results and decide** — do not chain
phases blindly. Keep BC+DAgger as a complete, shippable fallback if the RL phase underdelivers.
