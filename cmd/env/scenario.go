package main

import (
	"log"
	"math/rand"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/eval"
	"phootball/internal/policy"
	"phootball/internal/scenario"
	"phootball/internal/sim"
)

// handleScenario builds a drill match from an opScenario spec: kind, per-side sizes, field, offside,
// frame-skip, reward-profile id, controlled side, opponent kind (scripted/frozen), seed, episode
// length, and (if frozen) the snapshot path. The learner drives ctrlSide; the other side runs a
// lightweight scripted actor (a keeper or a presser -- NEVER the outdated rule AI) or a frozen
// self-snapshot. Positions are then arranged for the kind via internal/scenario.
func handleScenario(p []byte) *env {
	cur := newCursor(p)
	kind := int(cur.u8())
	homeSize := int(cur.u8()) // learner-side roster size
	awaySize := int(cur.u8()) // opponent-side roster size
	fieldIdx := cur.u8()
	offside := cur.u8() != 0
	frameSkip := int(cur.u8())
	if frameSkip < 1 {
		frameSkip = 1
	}
	profileID := cur.u8()
	ctrlSideByte := cur.u8()
	oppKind := cur.u8()
	seed := int64(cur.u64())
	_ = cur.u32()           // episodeLen: Python truncates; Go does not need it
	teacherKind := cur.u8() // 0 = no teacher; else a scenario.ScriptKind (3=collector,4=carrier,5=tikitaka)
	pOverride := cur.f32()  // probability THIS episode is a teacher demonstration (annealed by Python)
	var frozenPath string
	if oppKind == 4 {
		n := int(cur.u16())
		frozenPath = string(cur.take(n))
	}

	ctrlSide := sim.SideLeft
	if ctrlSideByte == 1 {
		ctrlSide = sim.SideRight
	}
	field := fieldPreset(fieldIdx)
	mutate := func(cfg *config.Config) {
		cfg.Geometry = field
		cfg.Ruleset.OffsideEnabled = offside
		if offside && cfg.Ruleset.OffsideFrac == 0 {
			cfg.Ruleset.OffsideFrac = 0.5
		}
	}

	embedded, err := policy.LoadDefault()
	if err != nil {
		log.Fatalf("env: load embedded net: %v", err)
	}
	if err := neural.ValidateNet(embedded); err != nil {
		log.Fatalf("env: %v", err)
	}
	var frozenNet *policy.Net
	if oppKind == 4 {
		frozenNet, err = loadFile(frozenPath)
		if err != nil {
			log.Fatalf("env: load frozen %s: %v", frozenPath, err)
		}
		if err := neural.ValidateNet(frozenNet); err != nil {
			log.Fatalf("env: frozen %v", err)
		}
	}

	e := &env{ctrl: map[int]*neural.Controller{}, opp: map[int]control.Controller{}, ctrlSide: ctrlSide, frameSkip: frameSkip}
	leftSize, rightSize := homeSize, awaySize
	if ctrlSide == sim.SideRight {
		leftSize, rightSize = awaySize, homeSize
	}
	em := eval.BuildSizedWith(leftSize, rightSize, seed, mutate, func(id int, side sim.Side) control.Controller {
		if side == ctrlSide {
			c := neural.New(id, embedded)
			e.ctrl[id] = c
			e.controlled = append(e.controlled, id)
			return c
		}
		oc := opponentController(kind, oppKind, id, frozenNet)
		e.opp[id] = oc
		return oc
	})
	scenario.Arrange(em.M, kind, ctrlSide, seed)
	e.scenKind, e.scenSeed, e.isScen = kind, seed, true

	// Guided bootstrapping. If the scenario has a teacher, install one per controlled player -- it
	// always provides per-state ADVICE (the BC/kickstart label, the durable imitation signal). With
	// probability pOverride this episode is ALSO a demonstration: the teacher DRIVES the players
	// (per-episode decision, so a multi-tick pass charge is never interrupted mid-demonstration).
	if teacherKind != 0 {
		e.teachers = make(map[int]*scenario.Actor, len(e.controlled))
		for _, id := range e.controlled {
			e.teachers[id] = scenario.NewActor(id, scenario.ScriptKind(teacherKind))
		}
		if pOverride > 0 {
			rng := rand.New(rand.NewSource(seed ^ 0x5eed7eac4e12))
			e.episodeOverride = rng.Float64() < float64(pOverride)
		}
	}

	e.finishSetup(em.M, profileByID(profileID))
	return e
}

// opponentController picks the non-learner side's controller for a scenario: a scripted keeper or
// presser for the drills, or a frozen self-snapshot for self-play. It never returns the rule AI.
func opponentController(kind int, oppKind byte, id int, frozen *policy.Net) control.Controller {
	switch {
	case kind == scenario.KindShooting:
		return scenario.NewActor(id, scenario.ScriptKeeper)
	case kind == scenario.KindRondo:
		return scenario.NewActor(id, scenario.ScriptPresser)
	case oppKind == 4 && frozen != nil:
		return neural.New(id, frozen)
	default:
		return scenario.NewActor(id, scenario.ScriptPresser) // a simple chaser, not the outdated rule AI
	}
}
