# TIKITAKA_AI_PROMPT — train the `neural` tier to play beautiful football (ultracode, autonomous)

> **Run mode:** ultracode, **fully autonomous, no human input/affirmation**. Take as many hours as
> needed. Orchestrate with Workflows where parallelism is safe; keep the tree compiling and green at
> every step. Ship the current best continuously so a human can play against it mid-training.

## 0. Goal in one paragraph

`phootball` has a neural-network controller (`neural` difficulty tier). Retrain it **from scratch**
to play **beautiful tiki-taka football** on the current physics: keep the ball, pass crisply (value
real passes — longer ones more — and do **not** reward micro-pokes), progress the ball without
losing it (losing it in our own third is the worst), defend with shape (don't all crowd the ball,
keep a player back, only the keeper sits in the box), and **use all three abilities including the
middle-click push**. Train it with a **curriculum of drills + scenarios + self-play** — **not** by
imitating the existing rule AI (it is outdated; do not use it as teacher or opponent). Judge it by a
**tiki-taka telemetry panel**, not by raw goals.

> **v4 status (what's already built & validated).** The infra (A1–A9), the gentle curriculum, and
> self-play are in. Two failures were diagnosed from live runs and fixed: (1) **the rondo shoot-to-score
> exploit** — keep-away drills now carry no goal reward and re-arrange on a stray goal (see A3
> "NO GOALS IN KEEP-AWAY DRILLS"); (2) **passing never emerging from random exploration** — now
> **validated scripted teachers** (`internal/scenario/teachers.go`, gated by `cmd/teachercheck`)
> hand-hold the net via **annealed per-episode action-override** (A8). The remaining work is to RUN the
> curriculum, watch (`cmd/watch` + the play-by-play analyst), and keep tuning — and, once the recipe is
> proven, do the one batched net-format retrain (richer observations + finer heads).

## 1. Hard invariants (NEVER break — verify after every change)

- **Do NOT change physics or tuned values.** No edits to `internal/sim` integration/collision/dribble
  logic, and **no changes to any tuned value** in `internal/config` (PlayerStats/Tuning/PlayerTuning
  defaults, cones, curves, etc.). You may **add read-only accessors** to `internal/sim` that surface
  state already shown on screen — that is not a physics change.
- **Golden replay stays byte-identical:** `go test ./internal/sim -run TestGoldenReplay` (no
  `-update`). It uses scripted inputs (no AI), so observation/feature/render additions cannot affect
  it. Wire-format additions bump `netcode.ProtoVersion` only.
- **Headless cgo-free server:** `go list -deps ./cmd/server | grep -E 'ebiten|x/image|oto'` prints
  nothing; `CGO_ENABLED=0 go build ./cmd/server` succeeds.
- **Pure-Go `//go:embed` inference** in `internal/policy` (stdlib + `[]float32` only) with **bit-exact
  Go↔Python parity** (`go test ./internal/policy -run TestForwardGoldenVector`). float32 end-to-end,
  strict left-to-right accumulation, ReLU only, no FMA/BLAS/float64.
- **`go test -race ./...` clean; `make` gate green.**
- **AI ≤ human:** the net may observe only what a human sees **on screen**. The trap aura (glow size),
  per-player possession (white bar), and team buff/debuff (green/red `TouchCoefficient` bar) are drawn
  over **every** player by default (`render.go`), so exposing them is allowed. Keep **velocity,
  `moveHeading`, and tuning of *other* players self-only** (not rendered). Charge abilities at the
  **human per-tick rate**; one ability at a time (Trap > Push > Shoot exclusivity).

## 2. The game as it is now (read `README.md` "Physics & player variables" in full first)

- **Directional movement (default):** speed scales with the angle between your `Move` heading and your
  `Facing`/aim — **1.2× forward, 0.5× at 90°, 0.2× straight back** (`internal/sim/movement.go`).
  Moving where you look is fast; backpedalling is slow. The net must learn to face where it goes.
- **Trap = energy bar + aura:** `trapCharge` (0..1) is a stamina bar that drains while held and regens
  at ~⅓ rate; `trapAura` (0..1) is the **effective** trap strength that ramps up, holds, then fades —
  it drives close control, clean reception, turn grip, pull reach, and the **glow size** a human sees.
- **Three abilities** (`README.md` "Abilities"): **Shoot** (hold left, release; aimed where you face,
  front 180°, power scales with charge), **Trap** (hold right; the good touch / energy-aura above),
  **Push** (middle-click; instant ~70% poke in **any** direction on any ball in reach — for quick
  clears/escapes). The net must use all three.
- **Two possession systems** (`README.md` "Possession mechanics"): per-player `possession` (your grip)
  and a per-team **possession charge** that buffs the owning team's touches (`touchCoef` > 0, green
  bar) and debuffs the conceding team's (`touchCoef` < 0, red bar). Sustained possession earns clean
  receptions; pressing drains the carrier's buff. The net should perceive and exploit this.

## 3. Part A — code/API infrastructure (build first, gate green)

### A1. The FULL observation set (give the net everything a human can perceive)
The net must observe **every datum a human sees on screen** — nothing less, nothing hidden. Source it
all from `sim.View`; respect **AI≤human** (state NOT rendered — others' true velocity/steering-heading/
tuning, raw accelerations — stays hidden and must be *estimated* from observable motion).
- **DONE (shipped):** widened `ObservedView` with `TrapAura()`, `Possession()`, `TouchCoef()` (all drawn
  over every player by default in `render.go`), mirrored onto `netcode.EntityState` (ProtoVersion→3) and
  the networked render path. Features now carry, per the egocentric attacking-goal frame:
  - **self (19):** velocity (x,y,|v|), facing (cos,sin), steering heading (cos,sin), **dir-align =
    facing·heading** (the directional-speed driver), possession, shoot-charge, trap-charge, **trap-aura**,
    **touch-coef** (team buff/debuff), centre-spot, dist to both goals, is-keeper, gap-to-ball.
  - **ball (8):** position, velocity, speed, signed angle self→ball, in-pull-range.
  - **global (12):** clock, am-closest-to-ball, kickoff flags, carrier one-hot (me/mate/opp), ball-third
    one-hot, squad/opponent counts.
  - **per entity ×(10 mates + 11 opps), 15 each:** position, facing, **estimated velocity** (K-frame
    memory — a human infers motion across frames), dist-to-me, dist-to-ball, shoot-charge, trap-charge,
    **trap-aura**, **possession**, **touch-coef**, is-carrier, is-keeper.
- **Still to add (this run, all on-screen-legal):** **ball acceleration** (Δvelocity, visible as the ball
  speeding up/slowing — encode for self frame), **ability-in-use flags** per player (is-shooting from the
  charge gauge, is-trapping from the aura, **push-flash** `PushFlash()` is rendered → expose), **pressure /
  nearest-opponent distance** to the carrier and to me, **open-lane / angle-to-goal** and **angle-to-
  nearest-teammate** cues, **time-to-ball** estimate. Consider **K>2 velocity memory** for smoother
  opponent-velocity estimates. Recompute dims; update `dataset_meta.json` via `neural.Meta()`; keep the
  egocentric, side-symmetric frame and ID-sorted pooling; re-export the net + parity golden.

### A2. Scenario / drill system (`cmd/env` + `internal/eval`)
The env currently only does standard kickoff. Add an **`opScenario`** reset carrying: arbitrary
**ball** position+velocity; per-player **position / role / side**; a per-player **control mask**
(`learner` | `frozen-self-snapshot` | `scripted` | `idle`); team sizes (allow **asymmetric**, e.g.
3v1 rondo); field preset; **reward-profile id**; **episode length**; **done condition** (e.g. drill
ends on goal / ball-out / timeout). Implement `eval.BuildScenario(...)` that builds via the existing
sized builder and then **places** ball/players (post-build positioning is not a physics change).
Provide a few **lightweight scripted actors** (NOT the rule AI): a feeder that passes to a spot, a
runner drifting to space, a presser closing the carrier. These exist only to scaffold drills.

### A3. Configurable, staged reward profiles (`cmd/env`)
Replace the single fixed reward with **named profiles** (a weight per term), chosen per scenario.
Terms (potential-based where possible; **total dense capped below the ±1 goal** so it can't be
farmed):
- sparse **+1/−1 goal**; potential **ball-progress** (GRF checkpoint style);
- **possession-keep** (small per-step while we carry); **turnover penalty scaled by pitch location**
  (own third = much worse);
- **pass reward scaled by length with a floor** (below the floor it isn't a pass → no reward; longer =
  more, capped) + a **progressive/line-breaking** bonus;
- **anti-dawdle** (holding too long without pass/shot/progress);
- **anti-crowd** (too many teammates near the ball), **stay-back/cover** (formation spread, a deep
  player), **GK-box discipline** (penalize a non-keeper in our goal area; reward the keeper holding);
- **ability rewards**: effective trap reception, effective **push** clear/escape, on-target shot;
  small **ability-spam** penalty.
- **Eval-only tripwires** (never fed back): possession-hoard with 0 shots, dribble-the-ball-into-net,
  spin-in-place, never-passes, push-never-used.
- **NO GOALS IN KEEP-AWAY DRILLS (critical — this was a real failure).** Every drill profile inherited
  `goal:1.0`, so the rondo policy learned to just shoot into the net and farm goal-diff while
  `pass/min` stayed pinned at 0 (textbook specification-gaming). Fix: a profile has a `noScore` flag;
  the keep-away/possession profiles (`receive`, `hold`, `passing`, `possession`) set `goal=0` AND
  `noScore=true`. With `noScore`, the env **re-arranges the drill** (`scenario.Arrange`) when a stray
  ball crosses the line (waiting out the engine's `Celebrating()` kickoff), so scoring earns nothing
  and cannot even disrupt the drill. The ship/advance score (`tikitaka_score(..., no_score=True)`)
  also **drops the goal-diff/finishing term** for these stages, so a NEW-BEST can only come from
  possession + length-gated passing. The rule: *unless scoring is the lesson, there are no goals.*

### A4. Telemetry — understand how it's doing, steer correctly
Extend `internal/sim/record.go` (additive, nil-safe, zero sim effect) and/or compute in `cmd/env` and
emit per-episode: possession %, **passes + completion by length bucket + median pass length**,
turnovers **by third**, **crowding** (mean teammates within R of ball), formation spread, **GK-box
occupancy**, ability usage (shoots/traps/**pushes**, on-target, effective), ball-hold-time
distribution, distance, shots/SOT/goals. Add a **telemetry opcode** to `cmd/env`; make `cmd/eval` and
`cmd/diag` print the full **tiki-taka panel**.

### A5. Playable checkpoints
Add `-neural-weights <path>` to `cmd/game` (and a menu field) to load any checkpoint; the training
loop **ships the current tiki-taka-best** to the embedded `internal/policy/weights/neural_v1.bin` and
to a stable `training/checkpoints/latest_best.bin`, so at any moment:
`go run ./cmd/game -field large -neural-weights training/checkpoints/latest_best.bin` plays a human
vs the current best.

### A6. Abilities — thin assist, net aims, push used
Keep the 5 factored heads `[MoveDir9, Throttle3, AimBin16, Ability4{none,shoot,trap,push}, Cancel2]`.
The engine **holds** a shoot/trap charge tick-by-tick at the human rate (commit machine in
`controller.go`), but the **net aims** via the AimBin head throughout — **remove the auto-aim-at-goal
scaffold**. Push is instant; keep it selectable whenever a ball is in reach, and reward it so it is
actually used.

### A7. Directional-movement mastery (the game is in the Directional model)
Speed scales with how aligned `Move` is with `Facing` (1.2 fwd / 0.5 side / 0.2 back,
`movement.go: directionalSpeedMul`); `moveHeading` rotates toward `Move` at `TurnRate`. The net must
**move where it looks** to be fast and not backpedal. Done: the **dir-align feature** (facing·heading)
makes the speed driver explicit. To improve control further, evaluate (and adopt if it helps): making
`MoveDir` **relative to facing** via `Intent.MoveRelativeToFacing` (so "forward" = toward aim = fast),
finer move/throttle resolution, and a tiny shaping term for moving efficiently (high speed in the
intended direction). The net must "understand" the directional code: it observes velocity, facing,
heading, and dir-align, and the reward favours getting places efficiently — so efficient directional
movement is both perceivable and incentivised.

### A8. Guided bootstrapping — coach the known sequences (don't wait for random luck) — BUILT
For skills whose input sequence we KNOW, **seed the behaviour** instead of hoping exploration finds it
(coaching fundamentals, NOT cloning the outdated rule AI). **Scripted teachers** live in
`internal/scenario/teachers.go` as `ScriptKind` actors (pure `View→Intent`, so they obey AI≤human and
can be discretized): **`ScriptCollector`** (intercept a rolling ball on its line, trap to settle),
**`ScriptCarrier`** (dribble goalward under the directional model, finish near goal), and
**`ScriptTikitaka`** (the rondo brain: the man on the ball settles it then passes to the most-open
mate — `laneClearance` over opponents — charging power ∝ distance; off-ball mates present in space and
trap the feed). A pass holds the shoot button for distance-proportional ticks then releases, so the
same actor works in direct control and via discretize→override.

**TEST THE TEACHER BEFORE YOU TEACH (mandatory gate).** `cmd/teachercheck` drives the learner side
with each teacher over ≥30 seeds and reports the drill metric as an **IQM + 95% bootstrap CI** vs an
idle baseline; a teacher must clear its objective by a margin or it is not fit to teach. This caught a
genuinely broken first version (off-ball players drifted instead of settling → `pass/min` ≈ 2.6);
after fixing, the teachers clear decisively (rondo3v1 ≈ 10.9 passes/min at 0.91 possession). Always
re-run it after touching a teacher.

**BC / kickstart loss is the PRIMARY mechanism (this is the one that works).** A supervised term
`-λ·logp(teacher_advice)` on the policy's OWN visited states (DAgger): the env returns the teacher's
**stateless per-state advice** (`Actor.Advise`, emit ShootHeld at the moment a pass should start) in
OBS every step (`adviceIdx`, all-`-1` when none); `tikitaka.py` adds `bcc·bc` to the loss, `bcc`
decaying from `--bc-coef0` (1.0) to a **non-zero floor** `--bc-floor` (0.15) over `--bc-anneal` (700)
updates while a teacher stage runs. This DIRECTLY raises the log-prob of the pass action and is immune
to the failure below. **Validated:** warm-started from the converged hoarder, greedy-eval `pass/min`
went 0→2.4 in ~80 updates and the policy dropped hoard+shoot.

**Annealed action-override (JSRL) is a SECONDARY aid only.** Per-episode (so a multi-tick pass charge
isn't interrupted): `opScenario` carries `teacher` + `p_override`; with prob `p` the teacher drives the
whole episode and the **executed** discretized action is reported so PPO trains on it. `p` anneals
from `--override-p0` to 0. **It does NOT transfer skill on its own** — on demonstration states the
value baseline makes the PPO advantage ≈0, so it never maximizes the pass action's log-prob (observed:
`pass/min` pinned at 0 for 2000 updates with override-only). Keep it low (≈0.4) for early state
coverage; rely on BC for transfer. Also shape the reward so passing strictly dominates hoarding (small
per-tick possess, large completed-pass + release-toward-mate reward, an early/hard dawdle penalty).
**Eval always uses `p=0` and `bcc` is not applied at eval** (measures the pure policy); verify passing
PERSISTS as guidance fades.

**ANCHOR the skill into self-play, and make every gate un-gameable.** A taught skill must be held, not
just taught once: when the rondo→buildup transition dropped the teacher (no BC), the policy unlearned
passing and collapsed to a hoard within ~600 updates, then *passed a possession-only gate as a
hoarder*. Two rules: (1) **carry the teacher (BC anchor at the floor) into the self-play possession
stages** (buildup/possession) — a no-score possession reward is otherwise flat (dead critic, diffusing
entropy) and circulation decays; the BC floor supplies the missing gradient. (2) **No gate may be
passable by the dominant local optimum** — possession-only is gameable by collapsing onto the ball, so
the possession gates also require `pass_per_min` ≥ (3–6) AND `crowd_mean` ≤ (~0.8) AND low own-third
turnover. Also **clamp the BC per-sample logp at −20** (a teacher advising a currently-masked action
gives `-inf` → the BC loss spiked to ~1e5). NOTE: per-stage ship-best overwrites `latest_best.bin`/
`.best`, so a good policy can be lost when a later stage regresses — keep the per-stage snapshots
(`training/snapshots/snap_sN_*.bin`) and, ideally, roll the embedded net back to the last
genuinely-tiki-taka checkpoint if a later stage degrades it.

### A9. NN code quality (keep it clean + parity-exact)
Architecture is pure-Go Deep-Sets (`internal/policy`): per-entity φ MLP + sum/max pool (separate
teammate/opponent), trunk, factored heads + value; ReLU-only float32, strict left-to-right reductions
(`Forward` parity with the Python exporter is bit-exact — never break it). Keep `Net` weights-only and
shareable with a per-controller `Workspace` (no per-tick alloc). Size capacity to the task (currently
~335k params); raise only if eval shows underfitting. Any feature/head change flows through
`neural.Meta()` → `dataset_meta.json` → the Python model, and must re-export the parity golden.

## 4. Part B — training program (fundamentals first; from scratch; no teacher)

PPO (`training/phball/tikitaka.py` + `curriculum.py`, Deep-Sets `model.py`, `env_client.py`). **No
cloning of the outdated rule AI.** A **curriculum driver** runs ordered stages; each stage = a scenario
distribution + a reward profile + a **telemetry advance-gate measured on THAT stage's own scenario**
(not always 6v6 — else early lessons are invisible). **Eval/ship-best is GREEDY/argmax** to match how
the Go controller deploys (a stochastic eval mismeasures). Best ships continuously to
`latest_best.bin` + the embedded net. Self-play uses **frozen self-snapshots** (no rule anchors). The
early skill stages use **guided bootstrapping (A8)** so passing/receiving don't depend on random luck.

**The ladder is GENTLE — fundamentals first, then team play (rondo is NOT stage 2):**
0. **Move:** go to a target; collect a stationary then a loose ball — learns directional movement.
1. **Carry:** dribble the ball to a target zone (place-to-place), keeping it under control.
2. **Receive:** trap a fed/moving ball cleanly (scripted feeder) — kickstarted by the ideal-receiver
   demonstrator, then annealed off.
3. **Shoot:** from spots into an empty net, then beat a keeper.
4. **Pass (2v0 → give-and-go):** to an open teammate — kickstarted by the ideal-passer demonstrator
   (face → charge power ∝ distance → release), then annealed off.
5. **2v1 → rondo 3v1:** keep-away under pressure; length-gated pass reward + retention, punish
   turnovers/dawdling. Tiki-taka consolidates here.
6. **Possession & build-up:** progress the ball through the thirds without losing it (own-third loss
   is the worst).
7. **Defense & shape:** anti-crowd, stay-back/cover, GK-box discipline, win it back.
8. **Full-game self-play (PFSP):** 3v3 → **6v6 large** (primary target; net is size-invariant via Deep
   Sets); full tiki-taka reward vs frozen self-snapshots.
9. **Sharpen:** low-entropy crispening of the deterministic policy.

Each stage advances on its gate or a budget; on a stalled/degenerate stage, retune within caps and
continue (never stop for human input). All feature changes are dim changes (restart from scratch);
reward/curriculum tweaks resume warm via `--resume`.

## 5. Acceptance — the tiki-taka panel (≥30 seeds, telemetry, NOT goals-only)

- Possession **≥ ~55%** (vs balanced self-play / frozen baselines).
- Pass completion **~85–90%**, with a **healthy share of medium/long passes** (median length above the
  micro floor — it is genuinely passing, not poking).
- **Low turnovers**, especially in our own third.
- **Anti-crowd** satisfied (mean teammates near the ball below threshold) and **a player stays back**
  (formation spread); **GK-box ≈ keeper only**.
- **All three abilities used**, including **push > 0** at a sane rate; on-target shots reasonable.
- Sane shots/goals; **no tripwire fires**.
- All §1 invariants green (golden byte-identical, parity bit-exact, headless, cgo-free, `-race`).

## 6. Autonomy & supervision

Run training in the background (`Bash run_in_background`) with periodic monitors / `ScheduleWakeup`.
Each phase writes metrics; read them to gate stage progression and ship-best. **Auto-retry / adjust**
(bounded, logged) on: NaN/divergence (lower LR / re-clip / restart from last good), behavioral
plateau or a firing tripwire (retune reward weights within caps, add exploration, repace the
curriculum, lengthen the league), env/IPC stalls (restart workers, reproduce with a fixed seed). If a
stage's gate can't be met within budget, ship the best checkpoint reached and proceed — **never stop
for human input**. Final report: the full tiki-taka panel, which checkpoint shipped, and any gate
missed.

**Live watching & a play-by-play analyst.** `cmd/watch` (`go run ./cmd/watch`) is a rendered viewer
that follows the training: it reads the current stage, sets up that exact drill with the current-best
net, and renders it (Blue = the net) — switching drills with the stage and reloading weights as the
best ships. `training/status.sh` prints a text panel. In addition, run a **background play-by-play
analyst** that periodically reads the telemetry play-by-play + a greedy eval of the current best,
diagnoses against the tiki-taka goals (is it receiving / holding / carrying / passing / using push /
moving efficiently / not crowding?), and feeds concrete improvements back into the reward/curriculum —
so the loop keeps improving from what it actually does on the pitch, not just aggregate score.

## 7. Verification checklist (run repeatedly)

```
make                                            # vet+build+test+race+headless+golden
go test ./internal/sim -run TestGoldenReplay    # byte-identical (no -update)
go list -deps ./cmd/server | grep -E 'ebiten|x/image|oto'   # empty
CGO_ENABLED=0 go build ./cmd/server
go test ./internal/policy -run TestForwardGoldenVector      # bit-exact parity
go run ./cmd/teachercheck                                   # scripted teachers clear their drills (≥30 seeds, IQM+CI) BEFORE training on them
# tiki-taka panel:
go run ./cmd/diag  -sizes 6 -field large -seeds 30 ...      # possession/passes/crowd/GK/abilities
go run ./cmd/eval  ...                                      # aggregate gate
# human vs current best, any time during training:
go run ./cmd/game -field large -neural-weights training/checkpoints/latest_best.bin
```
