# Directional Football AI — Ultracode Improvement Mission

> Paste this whole file as the first message of a fresh Claude **ultracode** session, working in the repo
> `/home/jack/Desktop/hax/haxball-clone` (Go module `phootball`). It is self-contained: everything you need to
> orient, measure, and improve the AI is below. Turn ultracode on (heavy multi-agent workflow orchestration)
> and go.

---

## 0. Mission

This repo is a HaxBall-style 2D football game with a **deterministic** physics sim (`internal/sim`), config
(`internal/config`), rendering (`internal/render`), and a **rule-based ("algo") AI controller** in
`internal/control`. The game has two movement models; **DIRECTIONAL is now the main and only mode we care
about** (it is the config default).

**Your job: refactor, improve, and tune the algo AI (`internal/control` ONLY) so it plays genuinely good
DIRECTIONAL football** — and prove it. "Good football" means, concretely:

1. **Fast, fluid movement** — players run at full speed by facing where they move; they do **not** crawl
   sideways/backwards. (This is the headline: directional movement done *well*.)
2. **Crisp passing** — high completion, **no overshoots / off-target passes**, sustained volume (real
   tiki-taka, not safe hoarding).
3. **Fast breaks** — on winning the ball, launch quick counters that finish.
4. **Clinical finishing** — in clear overloads (**2v1, 3v1**) it should **always score**.
5. **Sensible defending near our own goal** and an **active keeper** (not one that stands still).

This is **improve-on-existing**, not a rewrite. A lot of good machinery already exists (facing policy,
metrics, a parameterized sweep harness). Build on it, refactor it where it helps, and push the numbers.

---

## 1. Hard constraints — non-negotiable

1. **AI ≤ human (no cheating).** The AI may read ONLY:
   - its **own** state via `SelfView` (`view.Me(id)`): velocity, heading, possession, tuning, home position;
   - **observable** state of any player via `ObservedView`: position, facing, role, side, radius, the
     shoot/trap charge gauges, trap aura, possession bar, team buff/debuff (`TouchCoef`) — i.e. exactly what a
     human sees rendered;
   - the **ball** (position/velocity/radius), and **global settings**: `view.MoveModel()`, `view.Rules()`,
     `view.DirectionalSpeedMul(move, facing)`.

   It may **NEVER** read another player's velocity, heading, possession build-up, or tuning. This is enforced
   by `internal/control/boundary_test.go` (`TestObservedViewCannotSeeHiddenState`, `TestNoSeedExposure`,
   `TestAIIntentsAreHumanReachable`). **These must stay green. Do not weaken the boundary or the View surface
   to make the AI stronger** — that is the one cardinal sin here.

2. **Do NOT change physics or game mechanics.** All your changes live in `internal/control`. Treat
   `internal/sim` and `internal/config` as **frozen ground that you do not own** — the human edits them LIVE
   while you work (capture speeds, shot curves, etc. move under you). Consequence: **never "fix" a golden or
   physics test you didn't break** (`TestGoldenReplay`, `internal/sim/Test*Capture*`, etc. may be red from the
   human's edits — leave them). And **absolute metric numbers drift minute-to-minute** — see §5 on judging
   relative deltas.

3. **Directional only.** `config.Default().Tuning.MoveModel == config.MoveDirectional`. Under this model,
   speed AND acceleration scale by how aligned the player's **Move** vector is with its **Facing**: fast toward
   the aim (`MoveForward`), slow sideways (`MoveSide`), slowest backpedalling (`MoveBack`) — the curve is
   `internal/sim/movement.go directionalSpeedMul`, applied in `match.go`. **The core nuance to get right:
   _face your travel direction to move fast, and turn to face the ball only when you are about to receive it._**
   Standard mode exists but you should not optimise for it; just don't break it (facing is speed-neutral there,
   so the right policy is "always face the ball under Standard").

4. **Never lower a committed test gate.** The gate is `internal/control/passcompletion_test.go`
   `TestPassCompletionLargeMap`: pass completion **≥ 73%** over 30 seeds on the large pitch, plus a pass-volume
   floor, a clears cap, and hold-time guards. You may RAISE it once you've earned margin; never lower it.

5. **No commit / push unless explicitly asked.**

---

## 2. Codebase map (where things live)

AI controller — `internal/control/`:

| File | Owns |
|---|---|
| `ai.go` | `type AI`, `Intent(view) sim.Intent` — the per-tick dispatch: keeper → `keeper`; on the ball → `onBall`; elected presser → `press`; else `offBall`. Reaction-latency caching, `enforceAbilityExclusivity`, `applyMoveJitter`, `capAim`. |
| `abilities.go` | Move-model-aware **facing**: `faceAim(p, in, actionTarget)`, `faceLeadDist`; the trap/charge/shoot ability helpers, `aimToward`, `aimKeepingBall`, `steerReceive`. |
| `perception.go` | `type perception` (the per-tick read-only facts) incl. `moveModel`, `rules`, `myTrap`; `perceive(view, me, dt)`; helpers `nearest`/`pressure`/`space`/`goalwardness`/`openDuration`/`trapped`. |
| `tuning.go` | `type aiTuning` (≈90 commented behavioural constants) + `defaultAITuning()` — **the single tuning surface**. Also `type Skill`, `SkillFromString`, `SkillNames` (menu offers only `algo`/`neural`). |
| `teamplan.go` | `type teamPlan{presser, support}`, `assignRoles`, `electPresser` — deterministic anti-swarm role election (intercept-TIME, ID tie-break). |
| `offball.go` | `press` (the one elected ball-winner) and `offBall` (support runs / formation shape / marking); `avoidKeeperOnlyBox`. |
| `onball.go` | `onBall` (shoot/pass/dribble/clear/shield decision), `bestPass`, `bestDribble`, `bestShot`, `clearScore`; `LastAction()` (used by every measurement harness to label kicks). |
| `keeper.go` | `keeper`, `keeperGuardSpot`, `keeperSave`, `keeperShouldSweep`, `keeperShouldChallenge`/`keeperChallengeSpot`, `keeperDistribute`. |
| `formation.go` | `idealPosition`/`roleSlot`/`roleDepth` — per-role depth bands & shape; `confineSlot`. |
| `predict.go` | `predictBall`, `ballTravelTime`, intercept-time helpers — ball physics prediction (reuse, don't reinvent). |

Allowed read surface — `internal/sim/view.go`: `View` (global: `Ball`, `Me`, `Carrier`, `Teammates`,
`Squad`, `Opponents`, `Field`, `Rules()`, **`MoveModel()`**, **`DirectionalSpeedMul(move, facing)`**, ...),
`ObservedView` (others), `SelfView` (self only). Move model: `internal/config/tuning.go` (`MoveModel`,
`MoveDirectional` default); big pitch `config.LargeGeometry()`.

Situations / rollout: `internal/eval/eval.go` — **`BuildSizedWith(homeSize, awaySize, seed, mutate, factory)`**
(asymmetric rosters → this is how you make 2v1/3v1), `BuildWith`, `Match{M, Controllers}`, `.Step()`, `.Run()`,
`const DT`. Placement drills: `internal/scenario/scenario.go` — `Arrange(m, kind, side, seed)` with
`KindShooting`/`KindRondo`/`KindBuildup`/`KindDefend`/`KindCollect`/`KindCarry`. Visual: `cmd/watch`,
`cmd/screenshots`, `cmd/diag`.

---

## 3. The measurement layer (use it; don't rebuild it)

These already exist in `internal/control` and are the backbone of every decision. **Read them before
changing anything.**

- **Gate** — `passcompletion_test.go`: `go test ./internal/control/ -run TestPassCompletionLargeMap -count=1 -v`.
  30 seeds (1-30), large pitch, `SkillHard`, 120s each. Prints `passes / reached (%) | shots onTarget scored |
  clears | maxHold longHolds`. Must stay ≥73% with volume/clears/hold guards.

- **Diagnosis** — `passdiag_internal_test.go`: `go test ./internal/control/ -run TestPassDiagnosis -count=1 -v`.
  Runs `diagSweep` over the validation band (seeds 101-130, **disjoint from the gate's 1-30**) and `report`s:
  completion (mean/sd/min/max), volume (passes/clears/shots/onTarget/scored/push per game), hold-time,
  turnovers (+own-half), the **possession-outcome buckets** `shot/goal/clear/badPass/lostDribble` with
  **`shotEndedFrac`** (the north-star — want it to become the majority) and **`mistakeFrac`**, the
  **directional `speedEff`** (mean `DirectionalSpeedMul` of moving non-carrier players — 1.0 = running flat-out
  toward their aim; low = crawling off-axis), and the failed-pass **cause histogram** (intercept / miscontrol /
  over-hit / under-hit / bad-target ...). The classifier is total (buckets sum to fails — asserted).

- **Scrub** — `DIAG_SCRUB=1 go test ./internal/control/ -run TestPassScrub -count=1 -v`: per-failed-pass
  numeric trace (launch speed, lane length, receiver off-line / impact vs capture cap / alignment, where the
  opponent won it). This is how you SEE *why* a pass died.

- **Parameterized sweep harness** — `sweepspec_internal_test.go` `TestSweepSpec` (never fails; it's a
  measurement tool). This is your **workhorse for tuning and for ultracode workflow fan-out**:
  ```
  SWEEP_SPEC="shootRange=440,faceActionGap=120" SWEEP_SEEDS=val go test ./internal/control/ -run TestSweepSpec -count=1 -v
  ```
  - `SWEEP_SPEC` = comma-separated `key=value` `aiTuning` overrides (applied symmetrically to BOTH teams). Add
    new sweepable keys in `applyLever` (`sweepspec_internal_test.go`).
  - `SWEEP_SEEDS` ∈ `val` (101-130, default) | `gate` (1-30) | `adv` (201-230) | `adv2` (301-330) | `big`
    (101-150, 50 seeds). Use **disjoint** bands for adversarial verification.
  - `SWEEP_MODEL` defaults to **directional** (what you want); `standard` / `both` exist.
  - It prints one parseable line per model:
    `RESULT model=directional comp=.. vol=.. shots=.. scored=.. shotEnded=.. mistake=.. TO=.. ownTO=.. clears=.. hold5s=.. speedEff=..`

- **Boundary + determinism** — `go test ./internal/control/ -run 'TestObservedViewCannotSeeHiddenState|TestNoSeedExposure|TestAIIntentsAreHumanReachable|TestDeterminism' -v`.

- **Watch it play** (do this — numbers lie, eyes don't): `DISPLAY=:0 go run ./cmd/watch`; offscreen UI shots
  `DISPLAY=:0 go run ./cmd/screenshots`; non-graphical goal/shot breakdown `cmd/diag`.

- **Build / headless** — `go build ./...`, `go vet ./...`, `make headless` (asserts `cmd/server` doesn't link
  Ebiten), `make all`.

---

## 4. The NEW deliverable — committed scenario tests ("good football", not just %)

The aggregate completion metric does not capture "scores in a 2v1" or "finishes a fast break". **Build a small
committed scenario-test suite** (directional mode) proving the AI plays good football, on top of
`eval.BuildSizedWith` (+ manual `Position`/`Ball` placement, as `ai_test.go`'s `TestShootsWhenOpen` /
`TestKeeperSave` already do — there is **no generic N-vs-M builder or `internal/scenario` test yet**, so this
is genuine new infrastructure to add):

- **2v1 overload** — two attackers + a keeper vs one defender: the AI must **score within N ticks** across a
  band of seeds (combine: carry to commit the defender, then pass to the free man, finish).
- **3v1 overload** — must score even more reliably.
- **Fast break** — start from a regained ball in our half with numbers upfield: the AI must launch the counter
  and finish before the defence recovers.
- **Give-and-go** — pass-and-move that beats a single marker.

Make them seed-banded and assert a **pass rate / scored-within-N-ticks**, not a single brittle seed (the sim
is chaotic). These become the durable definition of "good football" and guard against regressions while you
tune. Force directional via the config mutate hook: `func(c *config.Config){ c.Tuning.MoveModel = config.MoveDirectional; c.Geometry = config.LargeGeometry() }`.

---

## 5. Method & discipline (this is where prior runs lived or died)

- **Diagnose first.** Run the diagnosis, the scrub, and `cmd/watch` before touching tuning. Find the dominant
  failure bucket; fix *that*.
- **One lever at a time.** Validate every change over **≥30 disjoint seeds** with `TestSweepSpec`. The
  completion metric is **CHAOTIC** — a 5-unit change can swing a single seed 10%, and a "win" on 6 seeds is
  noise.
- **Judge RELATIVE paired deltas, measured at the SAME build moment.** The human edits physics live, so the
  baseline drifts between two runs minutes apart. Always measure baseline-vs-candidate back to back; never
  trust an absolute number across a gap. If the build breaks mid-sweep, it's probably a live physics edit —
  rebuild and re-measure.
- **The `val` and `gate` bands disagree by ~2-3%.** A lever that looks great on `val` (101-130) can fail the
  `gate` (1-30). Before adopting anything, **verify it on the `gate` band specifically**, then re-verify on a
  third disjoint band (`adv`/`adv2`) to be sure it's not seed-luck.
- **Keep the guards honest.** Volume floor, clears cap, hold-time, shots/scored — a completion gain that tanks
  volume (the carrier just stops passing) or balloons clears (it hoofs everything) is the classic forbidden
  trap; the gate guards catch it, so respect them.

**Ultracode orchestration (use lots of agents):**
- Drive tuning with **Workflows** that fan out one `TestSweepSpec` arm per candidate across many parallel
  agents, then **adversarially re-verify** each surviving win on a disjoint seed band with independent skeptic
  agents, then synthesize/stack and re-validate over 50 seeds. **Loop until the metric converges.**
- **Do NOT edit source files while a sweep workflow is running** — the agents run `go test` against the shared
  working tree, so an edit corrupts their measurements. Edit, then sweep; never overlap.

**Refactor (the human explicitly wants this):** centralise the directional facing into a clean, well-named,
well-tested module with a crisp API, and push `speedEff` higher (it's ~0.78 today; aim higher) WITHOUT losing
reception — e.g. per-role facing policy, a smarter turn-time lead, a smoothed/stable travel heading, decoupling
"face to move" from "face to receive". Keep every behavioural constant in `aiTuning` (commented) and wired into
`applyLever` so it's sweepable.

---

## 6. Gotchas — hard-won, do NOT relearn these the hard way

- **Directional facing already exists** (`faceAim`/`faceLeadDist` in `abilities.go`, fed by
  `perception.moveModel`): face travel while transiting, turn to face the ball with a turn-time *lead* before
  receiving; Standard mode always faces the ball. Known sensitivities you must respect when you refactor it:
  - `faceLeadMargin` too low **fails the gate**; ~2.0 is safe.
  - A throttle-gate on facing (face-ball when slow) quietly **cost ~3% completion** — avoid.
  - The travel↔ball flip causes **facing JITTER** (`TestNoFacingJitter`, >4 reversals/s) → needs **hysteresis**
    (a sticky "facing the action" state with a release band), not a flat distance switch.
  - The player **closest of its side to a loose ball must face the ball** (to settle a rebound), not its run.
  - `faceActionGap` trades speed vs completion: ~150 is the gate-safe operating point, ~55 is max-speed but
    risks the gate. (Re-find this frontier under the *current* physics — it moves.)
- **settle-before-pass** (penalise a pass off a sideways-moving ball so the carrier takes a settling touch) is
  **physics-sensitive**: it helps when capture is TIGHT (ball sticks only when slow) and HURTS when capture is
  forgiving. There's a flag for it (`passSettleWeight`, default 0). Re-evaluate, don't assume.
- **Keeper:** making it rush out to intercept every shot **TANKS completion** (it leaves position and its
  distribution drags the %). The safe activity win is a **gated "challenge a close carrier" branch** (come out
  only when an enemy carries in close and no team-mate covers). Lowering `keeperSaveSpeed` makes it treat a
  firm back-pass as a shot and mishandle it — don't.
- **Self-play tension:** both teams run the SAME AI, so aggressive midfield pressing/marking forces opponent
  turnovers → **lowers `shotEndedFrac`** (the north-star). Keep defending **goal-area-LOCAL** (engage only when
  the enemy carries in your half / near your goal); don't add a second midfield presser.
- **The dominant pass failure is HOT first-touch reception** (the ball arrives faster than the capture wall and
  bounces; counted as a lost dribble). This is largely **physics-floor-bound** (the shot launch floor + capture
  speed), not a tuning bug — the lever is meeting the ball deeper / cleaner reception / settling before the
  pass, not magic.
- The big already-won levers you should keep unless you can beat them paired: `shootRange≈440` (attacks reach a
  shot on the large pitch) with a decoupled `dribbleCommitRange`, trap-energy rationing, the hold-time release
  valve (stops 10s hoarding), keeper-only box-awareness.

---

## 7. Definition of done & reporting

Improve the AI until, in DIRECTIONAL mode on the large pitch, over ≥30 seeds (50 for sign-off):
- `speedEff` is high (players visibly run, not crawl) and `TestNoFacingJitter` is green;
- pass completion ≥ the gate with volume held and overshoots/off-target visibly reduced (check the scrub);
- `shotEndedFrac` rises toward the majority and `mistakeFrac` falls;
- the **2v1 / 3v1 / fast-break scenario tests pass** (it scores);
- defending and the keeper look active and sensible on `cmd/watch`;
- all gates + boundary + determinism + build/vet/headless green (note any red that is the human's live physics
  edit, not yours).

**Report honestly:** before/after tables (paired, same build moment), the lever/refactor behind each delta and
the bucket it targeted, the scenario-test pass rates, the seed bands used, and any number that's drifting under
live physics edits. State plainly any residual ceiling (e.g. the physics-bound hot-reception floor). **No
commit or push unless asked.**

Now: turn on ultracode, read the measurement files in §3, run the diagnosis + scrub + watch it play, write the
scenario tests in §4, and start improving — directional movement first.
