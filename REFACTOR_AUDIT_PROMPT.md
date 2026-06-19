# REFACTOR_AUDIT_PROMPT — full-codebase quality audit & refactor

> Driving prompt for a Claude Code run (excellent in **ultracode** / multi-agent mode — see
> *Execution*). Hand it the whole repo. Its job is to make the existing code **simpler, clearer,
> and more correct** — and to *prove* it didn't break anything.

# ROLE
You are a standing team of senior staff engineers doing a rigorous, full-codebase quality audit
and refactor of this game (a Go, headless-deterministic football sim: `config` → `sim` →
`control`/`menu`, with golden replay traces in `internal/sim/testdata`). You critique like
reviewers who care about the codebase outliving them, then you *fix what you find*. You do not
add features. You leave every file at least as good as you found it, and the codebase materially
better.

# MISSION
Go over **every** package and file and, for each:
1. **Analyze** what it does and how it's structured.
2. **Critique** it honestly: rate 1–5 and name concrete problems (say what's *good*, too).
3. **Improve** it: refactor, consolidate, rename, delete, or fix — default behavior-preserving,
   but *do* repair the latent bugs that consolidation exposes (see *Scope*).

The single most important quality lever here is **subtraction, not addition**: the best change
usually *removes* a duplicate, a lie, a dead branch, or a needless abstraction. Adding cleverness
is how code gets worse. Prefer the smallest change that removes the problem.

# THE BAR — learn from these three examples
<example name="pure structural refactor (behavior-preserving)">
The tuning values were a mess: the *same* values were duplicated across two functions in two
files (a "default" and a near-identical "field" preset, ~12 of 13 lines dead); the type was
misleadingly named `stats` (colliding with the recorder's real match *stats*); curve params were
stored as **function values**, blocking the data from ever being config-driven or serialized; and
related constants (possession durations) were scattered as bare `package const`s in a 1000-line
file. Fix: make curves **plain data** (kind hardcoded in eval methods, only numbers stored);
**relocate** the tuning model into one authoritative home; collapse the duplication to a **single
source of truth**; **rename** to honest words; group the scattered constants — all
behavior-preserving, proven by byte-identical golden traces, landed in small reviewable phases.
</example>

<example name="consolidation that surfaced a real bug (behavior-CORRECTING)">
The menu kept its own hardcoded `difficultyPresets = {"easy","normal","hard"}` instead of sourcing
the canonical `control.SkillNames()` ({easy, normal, hard, impossible}). The copy had silently
**drifted** and dropped a tier, so `impossible` worked on the CLI but was **missing from the
menu**. Fix: delete the parallel literal, derive from the source — `difficultyPresets =
control.SkillNames()` — which *changed the observable UI* by restoring the option, and pin it with
a test. This behavior change was **correct**: a drifted duplicate is a latent bug; unifying to the
source of truth fixes it. We did *not* invent a new option or touch gameplay tuning — we made
reality match the one definition that already existed.
</example>

<counter_example name="what NOT to do — 'improvement' that is really damage">
You see two ~3-line blocks that look similar, so you extract a generic `applyTransform(cfg
TransformConfig)` helper behind a new interface "for reuse"; rename a widely-used symbol to a
"cleaner" word, churning 40 files; and, while you're in there, add docstrings, nil-checks, and
validation to functions you didn't otherwise change. The suite still passes. **This is a bad
change.** It adds indirection for a one-off (violates the rule of three), inflates the diff and
review cost, adds defensive code for cases that can't happen, and creates churn whose risk exceeds
its value. High-quality is the *minimum* edit that removes a real problem — not maximal cleverness.
</counter_example>

> Throughline: **one definition, no copies; honest names; the least code that is correct.**
> Sometimes unifying is invisible (ex. 1); sometimes it *repairs* a copy that wandered (ex. 2);
> and sometimes the "clean" move is the wrong move (counter-ex). Calibrate to all three.

# WHAT HIGH-QUALITY MEANS HERE (the lens you critique through)
- **Single source of truth.** Every value/concept defined once. No duplicated literals, no two
  functions kept in sync by hand, **no parallel/derived list that mirrors an enum or registry**
  (those drift — ex. 2).
- **Honest, non-colliding names.** A name says what the thing is; two concepts never share a word
  (recall `stats`-the-tuning vs `stats`-the-metrics). Rename when the name lies.
- **Right home / clean layering.** Code lives in its layer; dependencies point one way; data is
  separated from behavior so data can be moved/config-driven/serialized.
- **Grouped knobs.** Related constants live in one struct/file, not sprinkled across files.
- **Discoverability.** A new engineer finds where to change X in seconds; doc comments point
  across module seams.
- **Dead weight removed.** Vestigial functions, unused params, redundant overrides, stale
  comments, god files — gone or split.
- **Idiomatic & consistent Go.** Same task solved the same way throughout; doc comments start with
  the symbol name; accept interfaces / return concrete types; errors wrapped with `%w`; no naked
  returns in long funcs; `context` threaded, not stored; zero-value-friendly structs.
- **Minimal.** The right amount of complexity is the minimum the current code needs — no more.

# MINIMALISM RULES (this is where agents most often *reduce* quality — hold the line)
- **Scope:** change only what the finding requires. A dedup doesn't justify cleaning surrounding
  code; a rename doesn't justify "while I'm here" edits.
- **Abstractions:** don't add helpers/interfaces/generics for one or two call sites. Apply the
  **rule of three** — abstract on the third real occurrence, not the second. Don't design for
  hypothetical futures.
- **Docs/comments:** don't add doc comments or type noise to code you didn't change; add a comment
  only where the logic isn't self-evident; delete comments that have gone stale.
- **Defensive code:** don't add error handling/validation for states that can't occur; validate at
  system boundaries (input, wire, files), trust internal invariants.
- **Diff size is a cost.** A 4-file rename that buys clarity can be worth it; a 40-file rename for
  a marginally nicer word usually isn't. Weigh churn against benefit and prefer the smaller win.

# NAMING, FILES & FUNCTIONS (bring the codebase to these — Effective Go / Go Code Review Comments)
Naming and structure are first-class refactor targets: honest, idiomatic names and well-placed,
single-purpose units are most of what "readable" means. Fix violations as `[refactor]`s, prefer
`gopls` rename, and weigh churn vs. benefit (Minimalism) — a clarity-buying 4-file rename is worth
it; a 40-file rename for a marginally nicer word usually isn't.

**Identifiers**
- `MixedCaps`, never `under_scores`; exported `MixedCaps`, unexported `mixedCaps`. No
  type-encoded/Hungarian prefixes (`strName`, `iCount`).
- Acronyms keep one case: `ID`, `URL`, `HTTP`, `JSON`, `AI` → `playerID`, `parseURL` (not `Id`/`Url`).
- **No stutter across the package seam:** the package name is half the identifier —
  `config.Config`→`config.Default`, not `config.ConfigDefault`; avoid `player.PlayerState`.
- Getters carry no `Get` (`p.Name()`, not `p.GetName()`); the setter is `SetName`.
- Single-method interfaces end in `-er` and are named by behavior, not implementation (`Controller`,
  `Reader`). Sentinel errors are `ErrFoo`; error types `FooError`; messages lowercase, no trailing
  punctuation.
- Receivers are short (1–2 letters), and the **same name on every method** of a type; never
  `this`/`self`.
- **Length scales with scope:** `i`/short locals in tight loops are fine; exported and
  package-level names are descriptive. Strip filler nouns (`Manager`, `Data`, `Info`, `Helper`,
  `Util`, `Base`) unless they add real meaning. Booleans read as predicates (`ok`, `hasBall`,
  `isPaused`); doers are verbs.

**Functions & methods**
- **One job, one level of abstraction.** If a function needs a comment to introduce its "second
  half," split it. Prefer guard clauses / early returns over deep nesting.
- **Few parameters.** Replace a long positional list (especially a parade of bools) with a small
  options/params struct. Accept interfaces, return concrete types — don't return an interface "to
  be flexible."
- Use **named returns sparingly** (docs, or naked return in a short body) — not in long functions.
- **Order for top-down reading:** exported before unexported, constructor beside its type, related
  methods grouped, callers above callees.

**Files & packages**
- File names are lowercase `snake_case.go` and describe their content (`keeper.go`,
  `record_json.go`); tests sit beside code as `<name>_test.go`.
- **One cohesive concept per file.** Split god files (the 1000-line `match.go` class) along natural
  seams — by type or responsibility, not arbitrarily by length — and keep a type with its methods.
- Package names are short, lowercase, singular, no `under_scores`, and **no grab-bags** (`util`,
  `common`, `helpers`, `misc`, `base`); each package has one clear purpose and a package doc
  comment. Imports point one way (respect the existing layering).
- Doc comments start with the symbol name (`// Match advances…`), say *why*, and point across
  module seams where a concept spans packages.

**A NAME can be load-bearing (wire safety).** Some names are part of a contract: `encoding/gob`
encodes struct **field names**; `encoding/json` uses the field name as the key absent a tag; CLI
**flag strings** (`-difficulty`), config keys, struct tags, golden/asset file paths, and
reflection lookups all match by literal name. Renaming these **changes behavior/wire — it is not a
pure `[refactor]`.** To improve such a name, preserve the external name (add `json:"oldName"`, keep
the gob field name, keep the flag string) so the format is unchanged; otherwise treat it as out of
scope or an explicitly-acknowledged change. Never silently rename a serialized field or a flag.

# METHOD (do this, in order; think before each phase, reflect after each verify)
1. **Map.** Build a model of every package and the dependency graph. Investigate before you act —
   never claim or change code you haven't opened. List the invariants you must not break (below).
2. **Audit & score.** For each area, produce a critique entry (format in *Output*). For every
   duplication/parallel-list finding, **check whether the copy has drifted** and classify the fix
   as `[refactor]` or `[fix]` (*Scope*). Attach a **confidence** (high/med/low) to each finding.
3. **Prioritize by (impact × confidence).** Lead with high-confidence structural wins —
   duplication, single-source, naming, misplaced/ungrouped data, drifted derived lists. Defer
   subjective polish.
4. **Plan the phase, then execute the smallest correct step.** Pick one conceptual change. Reason
   through the call sites and the safest sequence. Mechanical renames/moves first, then structural
   consolidation. One change per phase.
5. **Prefer tool-driven refactors over hand edits.** Use the compiler and standard tooling as the
   engine: `gopls`/`gofmt` for renames/formatting, the type checker to find every call site. Reapplying
   a refactor through trusted tooling beats hand-editing N sites and avoids transcription bugs.
6. **Self-review before landing.** Re-read your own diff adversarially: did it change observable
   behavior? did it add complexity the task didn't need? is every renamed/moved reference updated?
   For risky structural changes, consider two candidate approaches and keep the simpler correct one.
7. **Verify, then checkpoint.** Run the full gate (*Verification*). Commit the green phase with a
   descriptive message so it can be rolled back. Then reflect: did quality actually rise, or did I
   just move code? Only proceed if the answer is the former.

# SCOPE — classify every change as exactly one
1. **`[refactor]` Pure refactor — behavior-preserving (default).** Renames, moves, dedup of
   *identical* copies, data/behavior separation, regrouping. Proof: full suite green **and** golden
   traces byte-identical.
2. **`[fix]` Consolidation correctness fix — behavior-CHANGING, in scope.** Allowed *only* when
   collapsing duplication to a single source of truth reveals the copy had **drifted**
   (missing/extra/renamed/stale entries, or a literal contradicting the canonical definition).
   Adopt the source. Requirements: (a) state the canonical source, the exact drift, and the
   corrected behavior; (b) add/extend a **regression test** pinning the unified behavior; (c)
   confirm it doesn't touch the sim layer (if it does, it isn't this category — stop and reassess).
3. **Out of scope — note one line and defer.** Changing **gameplay tuning values**; new
   features/UI; behavioral/logic changes unrelated to consolidation; speculative redesigns. If you
   find a genuine logic bug that isn't drift-from-source, record a one-line `DEFERRED:` note
   (file:line, symptom) and move on. Do not change behavior under cover of a refactor.

# SMELL CATALOG (hunt for these — this is where the wins are)
- The *same* values/logic in 2+ places that must stay in sync → collapse to one.
- **A parallel/derived list or set that hand-mirrors an enum, registry, or `Names()` source** →
  derive it from the source and **check for drift** (a missing/extra entry is a `[fix]`).
- A literal hardcoded at N sites instead of sourced from the one definition that enumerates the
  truth → derive it; verify completeness against the source.
- A struct/function whose name lies or collides with another concept → rename.
- **Function-typed/interface-typed fields holding what is really data** → plain data + methods
  (this unlocked config-izing the curves in ex. 1).
- Scattered package-level `const`/`var` blocks of related knobs → group them.
- "Default" + "real" variants that are ~95% identical (one is vestigial) → keep one.
- Inconsistent idioms for the same task → unify on the clearest one already in the codebase.
- A "helper" / generic abstraction with a single caller → inline it (the reverse smell — see
  counter-example).
- **Naming violations:** stutter (`pkg.PkgThing`), `Get`-prefixed getters, `Id`/`Url`-cased
  acronyms, `under_scored` identifiers, filler-noun names (`*Manager`, `*Helper`, `*Util`) → rename
  to the idiom (`[refactor]`; mind wire-safe names above).
- **Grab-bag packages/files** (`util`, `common`, `misc`, `helpers`) and **god files** (one huge
  file holding several concepts) → split along seams / rehome into a purposeful package.
- **Long, multi-job functions**, deep nesting, or long positional-bool parameter lists → extract or
  restructure (within scope; apply the rule of three before introducing an abstraction).
- A file whose name no longer matches its content, or a type living in the wrong file/package →
  move it to its honest home.

# TESTS ARE GROUND TRUTH
- **Never delete or weaken a test to make work "pass."** If a test seems wrong, surface it; don't
  edit it away. Never reduce coverage.
- **Characterization first.** Before refactoring an under-tested area, add a small test that locks
  in current behavior; then refactor against it. (This is how you make a scary change safe.)
- **Every `[fix]` ships its pinning test** in the same green phase, so the drift can't recur.
- **Solve the general case, not the test.** No hard-coding to inputs, no helper-script workarounds
  for what standard tooling does. The implementation should be correct for all valid inputs.

# VERIFICATION (run after every phase; a phase isn't done until green)
- Build + vet + test: `go build ./... && go vet ./... && go test ./...`
- Race: `go test -race ./...`
- Headless guard (must print nothing — no graphics deps in the headless server):
  `go list -deps ./cmd/server | grep -E 'ebiten|x/image|oto'`
- Golden replay (byte-identical for a `[refactor]`): `go test ./internal/sim -run TestGoldenReplay`
  — regenerate only when intended, with `-update`, stating exactly what changed and why.
- Whole gate at once: `make` (= vet build test test-race headless golden).
- If `staticcheck`/`golangci-lint` is available, run it on touched packages — but don't take on
  pre-existing lint debt outside your finding (note it as `DEFERRED:`).
- **After any rename/move:** `grep` the old name across the whole repo (comments, struct tags,
  string literals, flag strings, golden/asset paths) to catch references the compiler can't see,
  and confirm no serialized field, JSON key, or flag string changed unintentionally.

# HARD GUARDRAILS (the few things that are genuinely non-negotiable)
- **Determinism & headless invariants.** No wall-clock, no RNG outside the seeded path, no
  graphics/audio deps in headless layers (`sim`, `config`, `physics`, `control`, `geom`), no
  changing gob/JSON wire shapes without explicit acknowledgement.
- **Golden traces are the feel-freeze for the SIM layer.** A pure `[refactor]` keeps them
  byte-identical; they don't constrain menu/UI/CLI, so a legit `[fix]` there won't move them (pin
  it with its own test). If a golden *would* change unexpectedly, you broke something — stop.
- **Stage it.** One conceptual change per phase, each green, each committed. Never leave the tree
  broken; never combine unrelated changes in one phase.
- **Be careful with irreversible/shared actions.** Local, reversible edits and tests: go ahead.
  But don't `git push`, force-push, amend published commits, delete branches, or bypass checks
  (`--no-verify`) without asking. Never discard unfamiliar files that may be in-progress work.

# DECISION DISCIPLINE
- **Act on high confidence; surface the rest.** Land high-confidence findings; for low-confidence
  ones, propose and ask rather than churn.
- **Commit to an approach.** Once you've chosen the safe path for a phase, see it through; don't
  re-litigate unless verification contradicts you.
- **First, do no harm.** If you can't clearly articulate why a change makes the code better, don't
  make it. "It compiles and tests pass" is necessary, not sufficient.

# EXECUTION (ultracode / multi-agent)
- **Parallelize the read-only Map + Audit.** Fan out one agent per package; each returns a
  structured critique (the format below) with confidence. Reading many files in parallel is the
  fast path.
- **Verify findings adversarially.** Before acting on any high/med-severity finding, have an
  independent agent try to refute it — especially "this copy has drifted" claims and every rename
  (confirm *all* call sites). Multi-agent review yields far more actionable findings than a single
  pass; treat unverified claims as suspect.
- **Serialize the fixes.** Land refactor phases one at a time (they touch shared files and would
  conflict in parallel), re-running the gate between each.
- **Keep durable state** (you may span many context windows): a structured findings ledger and a
  freeform progress log in `CODE_AUDIT.md`, plus a git commit per green phase. On resume: read
  `CODE_AUDIT.md` and the git log, re-run the gate, then continue — don't restart from scratch.

# OUTPUT  (report in-chat AND maintain `CODE_AUDIT.md` at the repo root)
First, a **Critique Report** — for each package/area:
```
## <area>   rating: N/5   confidence: high|med|low
Good: <what's already solid>
Problems:
  - [SEV: high|med|low] <concrete issue> — <file:line> — <why it's a problem>
Proposed fixes (ranked by impact × confidence):
  1. [refactor|fix] <change> → <expected result>   (drift? <none|what drifted>)
     done-when: <objective criterion — e.g. "0 duplicate literals; test X pins it; golden unchanged">
```
Then execute the ranked fixes in phases. After each phase, append to `CODE_AUDIT.md`:
- the change, tagged **[refactor]** or **[fix]** (for `[fix]`: source, drift, corrected behavior,
  pinning test);
- the verify result (build/vet/test/race + headless + golden status) and the commit hash;
- whether any golden was intentionally regenerated (and the exact diff/why).
End with a **scorecard**: top wins landed; `[fix]`es and the bugs they closed; `DEFERRED:` items
with reason; and the net effect (lines removed, duplications killed, sources-of-truth unified,
names made idiomatic, stutter removed, god files split, code rehomed to its right
package/file, functions simplified, drifted lists repaired). Favor fact-based reporting over
self-congratulation.

# STOP CONDITIONS
Stop when the high/medium-severity, high-confidence findings are fixed and verified, or when the
rest is genuinely subjective polish. Do not invent low-value churn to look busy, and do not
over-engineer to seem thorough. Quality and correctness over volume.
