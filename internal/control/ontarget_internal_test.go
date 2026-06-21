package control

import (
	"math"
	"sort"
	"testing"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestPassOnTarget measures whether the AI's passes actually REACH the open man they were aimed at
// -- the user's complaint ("passes a bit off"). For every deliberate pass in real 6v6 games it tracks
// the intended receiver (the kicker's own committed pass target/receiver, write-only diag) and records,
// over the ball's flight, the closest the ball came to that receiver and how far OFF the ball's line the
// receiver was. It reports the reached-the-man rate and the miss distribution. Committed as a gate on
// pass ACCURACY (which the completion% gate cannot see -- a pass that blows past the man but is recovered
// still counts "complete"). Run: go test ./internal/control/ -run TestPassOnTarget -count=1 -v
func TestPassOnTarget(t *testing.T) {
	const reachRadius = 30.0 // ball within ~player+ball radius of the receiver = it reached him
	var minDists, offLines, launchErrDeg []float64
	reached := 0
	for seed := int64(1); seed <= 12; seed++ {
		m, ais := sweepMatch(6, seed, SkillHard, largeGeom, nil)
		type live struct {
			recvID            int
			minDist, offAtMin float64
		}
		var cur *live
		prevSpeed := 0.0
		flush := func() {
			if cur != nil && cur.recvID >= 0 && !math.IsInf(cur.minDist, 1) {
				minDists = append(minDists, cur.minDist)
				offLines = append(offLines, cur.offAtMin)
				if cur.minDist <= reachRadius {
					reached++
				}
			}
			cur = nil
		}
		for i := 0; i < diagTicks; i++ {
			in := make(map[int]sim.Intent, len(ais))
			for id, ai := range ais {
				in[id] = ai.Intent(m.View())
			}
			// detect a pass launch (ball-speed jump by a player whose last action is "pass")
			sp := geom.Norm(m.Ball.Velocity)
			if lt := m.LastTouch; lt != nil && sp-prevSpeed > 60 {
				if ai := ais[lt.Player]; ai != nil && ai.LastAction() == "pass" && ai.diagPassSet {
					flush()
					cur = &live{recvID: ai.diagPassRecvID, minDist: math.Inf(1)}
					// pure LAUNCH aim error: angle between the launched ball velocity and the line from
					// the ball to the committed target (isolates aim from receiver drift).
					if want := ai.diagPassTarget.Sub(m.Ball.Position); geom.Norm(want) > 1 && geom.Norm(m.Ball.Velocity) > 1 {
						launchErrDeg = append(launchErrDeg, geom.AngleBetween(m.Ball.Velocity, want)*180/math.Pi)
					}
				}
			}
			// track the intended receiver vs the ball during flight
			if cur != nil && cur.recvID >= 0 {
				var r *sim.Player
				for _, pl := range m.Players {
					if pl.PlayerID == cur.recvID {
						r = pl
						break
					}
				}
				if r != nil {
					d := geom.Dist(r.Position, m.Ball.Position) - r.Radius() - m.Ball.Radius()
					if d < cur.minDist {
						cur.minDist = d
						if bd := geom.Unit(m.Ball.Velocity); bd != (geom.Vec{}) {
							rel := r.Position.Sub(m.Ball.Position)
							cur.offAtMin = geom.Norm(rel.Sub(bd.Scale(geom.Dot(rel, bd))))
						}
					}
				}
			}
			prevSpeed = sp
			m.Step(in, diagDt)
		}
		flush()
	}
	if len(minDists) == 0 {
		t.Fatal("no passes measured")
	}
	sort.Float64s(minDists)
	sort.Float64s(offLines)
	pct := func(s []float64, p float64) float64 { return s[int(float64(len(s)-1)*p)] }
	mean := func(s []float64) float64 {
		t := 0.0
		for _, v := range s {
			t += v
		}
		return t / float64(len(s))
	}
	t.Logf("ON-TARGET over %d passes: reached-the-man (ball within %.0f of receiver) = %d/%d (%.0f%%)",
		len(minDists), reachRadius, reached, len(minDists), 100*float64(reached)/float64(len(minDists)))
	t.Logf("  ball-to-receiver minDist: median=%.0f p90=%.0f mean=%.0f", pct(minDists, 0.5), pct(minDists, 0.9), mean(minDists))
	t.Logf("  receiver off the ball-line: median=%.0f p90=%.0f mean=%.0f", pct(offLines, 0.5), pct(offLines, 0.9), mean(offLines))
	if len(launchErrDeg) > 0 {
		sort.Float64s(launchErrDeg)
		t.Logf("  LAUNCH aim error (deg, ball vs target-at-launch): median=%.1f p90=%.1f mean=%.1f", pct(launchErrDeg, 0.5), pct(launchErrDeg, 0.9), mean(launchErrDeg))
	}
}
