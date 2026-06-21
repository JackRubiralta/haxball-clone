package main

import (
	"encoding/json"

	"phootball/internal/eval"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// tracker watches the match tick-by-tick to detect the events the tiki-taka reward and telemetry
// need: carrier handovers (passes vs turnovers, with length and pitch-third), how long one player
// dawdles on the ball, team clustering near the ball, goal-box discipline, deep cover, and ability
// use. observe() returns the dense event-reward for that tick (per the active profile) and folds the
// same signals into the telemetry panel.
type tracker struct {
	ctrlSide        sim.Side
	fieldW          float64
	ownGoalX        float64
	attGoalX        float64
	prevCarrierID   int
	prevCarrierSide sim.Side
	prevCarrierPos  geom.Vec
	holdTicks       int
	prevPush        map[int]bool
	prevBallSpeed   float64 // |ball velocity| last tick, to detect receiving a MOVING ball (a feed)
	attackSign      float64 // +1 if our attacking goal is at larger x than our own goal, else -1
	prevInstID      int     // the instantaneous radius-owner last tick (-1 = loose), for release edges
	prevInstSide    sim.Side
}

// telemetry is the per-episode tiki-taka panel, emitted as JSON on request. All counts are over the
// CONTROLLED team unless noted; rates are computed Python-side from these raw counters.
type telemetry struct {
	TotalTicks         int     `json:"total_ticks"`
	PossessionTicks    int     `json:"possession_ticks"`
	OppPossessionTicks int     `json:"opp_possession_ticks"`
	Passes             int     `json:"passes"`             // completed passes (>= passMinLen) by us
	PassLenSum         float64 `json:"pass_len_sum"`       // summed pass length (world units)
	PassBuckets        [3]int  `json:"pass_buckets"`       // short / medium / long counts
	ProgressivePasses  int     `json:"progressive_passes"` // passes that advanced the ball
	ShortPokes         int     `json:"short_pokes"`        // sub-floor handovers (NOT counted as passes)
	Turnovers          int     `json:"turnovers"`          // we lost possession
	Regains            int     `json:"regains"`            // we won possession
	TurnoverByThird    [3]int  `json:"turnover_by_third"`  // own / middle / attacking third where we lost it
	DawdleTicks        int     `json:"dawdle_ticks"`
	StayBackTicks      int     `json:"stay_back_ticks"`
	CrowdSum           float64 `json:"crowd_sum"`        // sum over ticks of teammates within crowd radius of ball
	GKBoxSum           float64 `json:"gk_box_sum"`       // sum over ticks of NON-keepers in our goal area
	ReceiveAttempts    int     `json:"receive_attempts"` // times we gained a moving ball (a feed/loose)
	ReceiveClean       int     `json:"receive_clean"`    // of those, gained while trapping (clean first touch)
	Collections        int     `json:"collections"`      // times a controlled player gained a previously-loose ball
	PassAttempts       int     `json:"pass_attempts"`    // releases by us that travelled toward a teammate (may not complete)
	Pushes             int     `json:"pushes"`           // middle-click pushes by us (rising edges)
	TrapTicks          int     `json:"trap_ticks"`       // ticks a controlled player held trap
	ShootHoldTicks     int     `json:"shoot_hold_ticks"`
	Shots              int     `json:"shots"` // recorder shots (filled at emit time)
	ShotsOnTarget      int     `json:"shots_on_target"`
	GoalsFor           int     `json:"goals_for"`
	GoalsAgainst       int     `json:"goals_against"`
}

func newTracker(m *sim.Match, ctrlSide sim.Side) tracker {
	t := tracker{ctrlSide: ctrlSide, fieldW: m.Field.Width(), prevCarrierID: -1,
		prevCarrierSide: sim.SideNone, prevPush: map[int]bool{}, prevInstID: -1, prevInstSide: sim.SideNone}
	if t.fieldW <= 0 {
		t.fieldW = 1
	}
	for _, tm := range m.Teams {
		if tm.Side == ctrlSide {
			t.ownGoalX = m.DefendingGoal(tm).Center.X
			t.attGoalX = m.AttackingGoal(tm).Center.X
		}
	}
	t.attackSign = 1
	if t.attGoalX < t.ownGoalX {
		t.attackSign = -1
	}
	return t
}

// progress maps a world point to our normalized own->attacking axis position in [0,1].
func (t *tracker) progress(p geom.Vec) float64 {
	den := t.attGoalX - t.ownGoalX
	if den == 0 {
		return 0.5
	}
	v := (p.X - t.ownGoalX) / den
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (t *tracker) thirdOf(p geom.Vec) int {
	i := int(t.progress(p) * 3)
	if i > 2 {
		i = 2
	}
	return i
}

func (te *telemetry) bucketPass(length, fieldW float64) {
	switch {
	case length < 0.12*fieldW:
		te.PassBuckets[0]++
	case length < 0.30*fieldW:
		te.PassBuckets[1]++
	default:
		te.PassBuckets[2]++
	}
}

// observe runs once per simulation tick (inside the env's frame-skip loop). It returns the dense
// event reward for this tick under profile pr and folds the signals into te.
func (t *tracker) observe(m *sim.Match, pr *rewardProfile, te *telemetry, intents map[int]sim.Intent, controlled []int) float64 {
	r := 0.0
	ball := m.Ball.Position
	te.TotalTicks++

	curSide, curID, curPos := t.owner(m)

	// Release-toward-a-teammate = a pass ATTEMPT. The tick we release the ball loose (we owned it
	// last tick, loose now) with it travelling toward an open teammate, pay a small bonus (< a
	// completed pass) so passing has a GRADIENT before any >=passMinLen completion lands. Fires once
	// per release (prevInst goes None next tick).
	if curID == -1 && t.prevInstSide == t.ctrlSide && t.prevInstID != -1 && pr.releaseMate != 0 {
		bv := m.Ball.Velocity
		if geom.Norm(bv) > pr.feedFloor {
			bdir := geom.Unit(bv)
			for _, p := range m.Players {
				if p.Team.Side != t.ctrlSide || p.PlayerID == t.prevInstID {
					continue
				}
				to := p.Position.Sub(m.Ball.Position)
				if geom.Norm(to) >= pr.passMinLen && geom.Dot(bdir, geom.Unit(to)) >= releaseConeCos {
					te.PassAttempts++
					r += pr.releaseMate
					break
				}
			}
		}
	}

	// Pass/turnover detection compares to the last DEFINITE owner and PERSISTS through loose /
	// in-flight gaps: a real pass ALWAYS has a moment where the ball is loose (owner=None) while it
	// travels from passer to receiver, so we must not reset on None or every pass is missed. We only
	// update the remembered owner when a definite owner exists.
	if curID != -1 {
		if curID != t.prevCarrierID {
			if t.prevCarrierID != -1 {
				length := geom.Dist(t.prevCarrierPos, curPos)
				if curSide == t.prevCarrierSide {
					// same-team handover (across the flight) = a pass, if it cleared the micro floor
					if length >= pr.passMinLen {
						te.Passes++
						te.PassLenSum += length
						te.bucketPass(length, t.fieldW)
						if curSide == t.ctrlSide {
							add := pr.passBase + pr.passPerLen*(length/t.fieldW)
							if t.progress(curPos) > t.progress(t.prevCarrierPos)+0.02 {
								add += pr.progressive
								te.ProgressivePasses++
							}
							r += add
						}
					} else if curSide == t.ctrlSide {
						te.ShortPokes++ // telemetry only -- deliberately NOT rewarded
					}
				} else {
					// the ball changed teams = a turnover
					te.Turnovers++
					if t.prevCarrierSide == t.ctrlSide {
						third := t.thirdOf(t.prevCarrierPos)
						te.TurnoverByThird[third]++
						pen := pr.turnover
						if third == 0 { // lost in our OWN third -- the worst
							pen *= pr.turnoverOwn
						}
						r += pen
					}
					if curSide == t.ctrlSide {
						r += -pr.turnover // winning the ball back mirrors the loss penalty (positive)
						te.Regains++
					}
				}
			}
			// One-shot RECEPTION: a controlled player just GAINED a ball that was MOVING and the
			// contact DEADENED it (a good first touch) -- credited by the speed DROP, not absolute
			// speed, so a friction-slowed loose ball still counts. Skip when we won it off the
			// OPPONENT (that tackle is already paid by the regain bonus). Bonus for trapping.
			if curSide == t.ctrlSide && pr.receive != 0 &&
				(t.prevCarrierSide == t.ctrlSide || t.prevCarrierSide == sim.SideNone) {
				te.Collections++
				curSpeed := geom.Norm(m.Ball.Velocity)
				if t.prevBallSpeed > pr.feedFloor && curSpeed < t.prevBallSpeed {
					te.ReceiveAttempts++
					drop := (t.prevBallSpeed - curSpeed) / t.prevBallSpeed
					rec := pr.receive * drop
					if in, ok := intents[curID]; ok && in.Trap {
						rec *= 1.5
						te.ReceiveClean++
					}
					r += rec
				}
			}
			t.prevCarrierID, t.prevCarrierSide, t.prevCarrierPos = curID, curSide, curPos
			t.holdTicks = 0
		} else {
			t.prevCarrierPos = curPos
			t.holdTicks++
		}
	}

	// possession + anti-dawdle (our carrier)
	switch {
	case curSide == t.ctrlSide:
		r += pr.possess
		te.PossessionTicks++
		if float64(t.holdTicks)*eval.DT > pr.dawdleAfter {
			r += pr.dawdle
			te.DawdleTicks++
		}
		// Directional efficiency: reward the carrier for moving where it faces (fast under the
		// directional speed model), so it learns crisp on-the-ball movement instead of strafing.
		if pr.forwardAlign != 0 && curID != -1 {
			if op := m.PlayerByID(curID); op != nil && geom.Norm(op.Velocity) > 5 {
				vu := geom.Unit(op.Velocity)
				al := geom.Dot(vu, geom.Unit(op.Facing)) // moving where it faces (fast, directional)
				goalward := vu.X * t.attackSign          // and TOWARD the attacking goal (productive)
				if al > 0 && goalward > 0 {
					r += pr.forwardAlign * al * goalward // gated so sprinting any direction can't farm it
				}
			}
		}
	case curSide != sim.SideNone:
		te.OppPossessionTicks++
	}

	// clustering / box discipline / deep cover, sampled over our team
	nearBall, inBox := 0, 0
	deepest := 1.0
	for _, p := range m.Players {
		if p.Team.Side != t.ctrlSide {
			continue
		}
		if geom.Dist(p.Position, ball) <= pr.crowdRadius {
			nearBall++
		}
		if p.Role != sim.RoleKeeper {
			if pg := t.progress(p.Position); pg < deepest {
				deepest = pg
			}
			if m.Field.GoalArea(t.ctrlSide).Contains(p.Position) {
				inBox++
			}
		}
	}
	te.CrowdSum += float64(nearBall)
	te.GKBoxSum += float64(inBox)
	if pr.crowd != 0 && nearBall > pr.crowdMax {
		r += pr.crowd * float64(nearBall-pr.crowdMax)
	}
	if pr.gkBox != 0 && inBox > 0 {
		r += pr.gkBox * float64(inBox)
	}
	// stay-back: a deep outfielder covering while the ball is forward (we attack, but one stayed home)
	if pr.stayBack != 0 && deepest < 0.34 && t.progress(ball) > 0.5 {
		r += pr.stayBack
		te.StayBackTicks++
	}

	// ability use from our controlled intents (push reward + telemetry)
	for _, id := range controlled {
		in, ok := intents[id]
		if !ok {
			continue
		}
		if in.Push && !t.prevPush[id] {
			te.Pushes++
			if pr.push != 0 && t.pushEffective(m, id) {
				r += pr.push
			}
		}
		t.prevPush[id] = in.Push
		if in.Trap {
			te.TrapTicks++ // good-touch value is captured by the one-shot RECEIVE edge, not per-tick
		}
		if in.ShootHeld {
			te.ShootHoldTicks++
		}
	}
	t.prevInstID, t.prevInstSide = curID, curSide
	t.prevBallSpeed = geom.Norm(m.Ball.Velocity)
	return r
}

// releaseConeCos = cos(~21 deg): how tightly the released ball must point at a teammate to count as
// a pass ATTEMPT (the release-toward-mate shaping above).
const releaseConeCos = 0.93

// owner returns which team is in possession by the RADIUS model (the project's possession
// directive / what cmd/diag reports): the ball is a team's while exactly one team has a player
// within possession reach of it; neither (loose) or both (contested) belongs to no one. It also
// returns that side's nearest in-reach player as the de-facto carrier (for pass/turnover and
// dawdle), without requiring the strict multi-second possession build of BallCarrier().
func (t *tracker) owner(m *sim.Match) (sim.Side, int, geom.Vec) {
	ball := m.Ball.Position
	bR := m.Ball.Radius()
	leftIn, rightIn := false, false
	var best *sim.Player
	bestGap := 1e18
	for _, p := range m.Players {
		gap := geom.Dist(p.Position, ball) - p.Radius() - bR
		if gap <= p.Tuning.PullRange {
			if p.Team.Side == sim.SideLeft {
				leftIn = true
			} else {
				rightIn = true
			}
			if gap < bestGap {
				bestGap, best = gap, p
			}
		}
	}
	if leftIn == rightIn { // neither, or both -> loose / contested
		return sim.SideNone, -1, geom.Vec{}
	}
	side := sim.SideLeft
	if rightIn {
		side = sim.SideRight
	}
	// best is the globally-nearest in-reach player; since only one side is in reach, it is on `side`.
	return side, best.PlayerID, best.Position
}

// pushEffective reports whether player id had a ball within push reach (so the poke actually did
// something), used to reward only effective pushes.
func (t *tracker) pushEffective(m *sim.Match, id int) bool {
	p := m.PlayerByID(id)
	if p == nil {
		return false
	}
	gap := geom.Dist(p.Position, m.Ball.Position) - p.Radius() - m.Ball.Radius()
	return gap <= p.PushRange()
}

// telemetryMsg serializes the current telemetry panel (with recorder shots and the score folded in)
// as opTeleOut + a JSON payload, for the Python curriculum gate.
func (e *env) telemetryMsg() []byte {
	te := e.tele
	te.Shots, te.ShotsOnTarget = e.shots()
	te.GoalsFor, te.GoalsAgainst = e.scores()
	body, err := json.Marshal(te)
	if err != nil {
		return []byte{opTeleOut}
	}
	return append([]byte{opTeleOut}, body...)
}
