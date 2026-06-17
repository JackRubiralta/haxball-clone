// Command tune is a temporary, throwaway harness. DELETE after use.
package main

import (
	"fmt"
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

const dt = 1.0 / 60.0

func solo() (*sim.Match, *sim.Player, *sim.Ball) {
	m := sim.BuildSolo(sim.NewStandardField())
	p := m.Players[0]
	p.Position = geom.NewVec(500, 340)
	p.Velocity = geom.NewVec(0, 0)
	p.Facing = geom.NewVec(1, 0)
	return m, p, m.Ball
}

func gapOf(p *sim.Player, b *sim.Ball) float64 {
	return geom.Dist(p.Position, b.Position) - p.Radius() - b.Radius()
}

func buildPossession(m *sim.Match, p *sim.Player) {
	for i := 0; i < 110; i++ {
		m.Step(map[int]sim.Intent{0: {Move: geom.NewVec(1, 0), Throttle: 1, Aim: p.Position.Add(geom.NewVec(60, 0))}}, dt)
	}
}

// fastRotationTest: with possession, spin the aim (facing) at omega deg/s. The ball
// must STAY with the player (anti-fling at full original strength) -- it should not be
// flung off by a fast rotation.
func fastRotationTest(omegaDeg float64) {
	m, p, b := solo()
	b.Position = p.Position.Add(geom.NewVec(p.Radius()+b.Radius()+0.3, 0))
	buildPossession(m, p)
	theta, omega := 0.0, omegaDeg*math.Pi/180
	maxGap, lost := 0.0, false
	for i := 0; i < 120; i++ {
		theta += omega * dt
		aim := p.Position.Add(geom.NewVec(60*math.Cos(theta), 60*math.Sin(theta)))
		m.Step(map[int]sim.Intent{0: {Throttle: 0, Aim: aim}}, dt)
		if g := gapOf(p, b); g > maxGap {
			maxGap = g
		}
		if gapOf(p, b) > p.Stats.PullRange {
			lost = true
		}
	}
	fmt.Printf("spin %5.0f deg/s : max gap=%5.2f  lost=%-5v (touch<%.0f, pull<%.0f)\n",
		omegaDeg, maxGap, lost, p.Stats.TouchRange, p.Stats.PullRange)
}

// escapeMomentumTest: a ball orbiting the player at speed V with no input. A gentle
// orbit is re-caught and settled; a fast one overcomes the FULL hold and must fly off
// retaining most of its orbital momentum (the original bug fix must still hold).
func escapeMomentumTest(v float64) {
	m, p, b := solo()
	b.Position = p.Position.Add(geom.NewVec(p.Radius()+b.Radius()+0.2, 0))
	b.Velocity = geom.NewVec(0, v)
	leftSpeed, left := 0.0, false
	for i := 0; i < 180; i++ {
		m.Step(map[int]sim.Intent{0: {Throttle: 0, Aim: p.Position.Add(geom.NewVec(60, 0))}}, dt)
		if !left && gapOf(p, b) > p.Stats.TouchRange {
			left, leftSpeed = true, geom.Norm(b.Velocity)
		}
	}
	fmt.Printf("orbit V=%-5.0f : left=%-5v  speed-on-release=%5.1f  (retained %3.0f%% of orbit)\n",
		v, left, leftSpeed, 100*leftSpeed/v)
}

func main() {
	fmt.Println("-- fast rotation must KEEP the ball (anti-fling, original strength) --")
	for _, w := range []float64{360, 720, 1440, 2880} {
		fastRotationTest(w)
	}
	fmt.Println("\n-- genuine escape must still LEAVE with momentum (bug stays fixed) --")
	for _, v := range []float64{40, 120, 250} {
		escapeMomentumTest(v)
	}
}
