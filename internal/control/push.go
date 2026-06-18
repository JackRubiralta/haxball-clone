package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Middle-click poke usage. The poke is an INSTANT, no-charge, no-aim-assist radial jab: it
// sends the ball straight along (ball - player_centre) at a fixed 70% of front shot power,
// reaching any ball within the PULL radius. Because it never charges, it is the AI's fastest
// way to act on the ball, so the AI reaches for it in exactly the two cases a human would:
//
//	(1) Get RID of it under pressure -- a keeper or last defender booting it clear before a
//	    closing attacker can dispossess, where dwelling to line up and charge a clear loses it.
//	(2) Toe-poke a tap-in -- jab the ball into the net from close range past a keeper set for
//	    a charged shot, when there is no time to wind one up.
//
// Its cost is total: no control over direction (pure radial) and no power scaling. So the AI
// only pokes when the radial ALREADY points where the ball needs to go (clear upfield, or into
// the goal mouth) AND it is under enough pressure that charging a real shot/clear would fail.

// canPoke reports whether a poke could physically fire: the ball must be within the (base)
// pull radius the poke reaches. (It nearly always is when we control the ball, but the
// fallthrough to a charged clear still needs the guard.)
func (a *AI) canPoke(p perception) bool {
	return p.gapToBall < p.me.Stats().PullRange
}

// pokeDir is the exact direction a poke would send the ball: the pure radial from the player
// centre to the ball, with no aim assist. Zero if the ball sits on top of us.
func (a *AI) pokeDir(p perception) geom.Vec {
	return geom.Unit(p.ball.Sub(p.me.Position()))
}

// pokeClears reports whether an instant poke would send the ball USEFULLY clear -- away from
// our own goal (upfield or wide), never back into our own danger. The poke direction is fixed
// (radial), so a poke is only safe to boot when the ball already sits on our upfield side.
func (a *AI) pokeClears(p perception) bool {
	if !a.canPoke(p) {
		return false
	}
	dir := a.pokeDir(p)
	if dir == (geom.Vec{}) {
		return false
	}
	return dir.X*p.attackX > a.tune.pokeClearMinForward
}

// pokeShotOn reports whether an instant poke would be a SHOT on target: from close range the
// radial launch crosses the enemy goal mouth. A toe-poke beats charging a shot a closing
// defender would block -- but only when we are already lined up at goal, since the poke has no
// aim assist to bend it in.
func (a *AI) pokeShotOn(p perception) bool {
	if !a.canPoke(p) {
		return false
	}
	if geom.Dist(p.me.Position(), p.enemyGoal) > a.tune.tapRange {
		return false // a genuine close-range tap-in, not a hopeful long jab
	}
	dir := a.pokeDir(p)
	if dir == (geom.Vec{}) || dir.X*p.attackX <= 0 {
		return false // must travel toward the enemy goal (forward along our attack axis)
	}
	// March the radial from the ball to the goal line and check it crosses inside the mouth.
	t := (p.enemyGoal.X - p.ball.X) / dir.X
	if t <= 0 {
		return false
	}
	crossY := p.ball.Y + dir.Y*t
	half := p.view.Field().GoalHeight()/2 - p.ballRadius
	return absFloat(crossY-p.enemyGoal.Y) < half
}

// pokeIntent produces the Intent that fires a middle-click poke this tick: an instant radial
// jab, no charge. It cancels any in-progress charge (so the same tick can't ALSO release a
// charged shot) and sets the post-kick cooldown so the player takes a real touch before
// kicking again. The poke is a one-shot edge action -- AI.Intent clears it from the cached
// intent so the reaction-delay replay can never re-fire (and compound) the jab.
func (a *AI) pokeIntent(p perception) sim.Intent {
	in := sim.Intent{Poke: true, Aim: a.aimToward(p, p.ball)} // face the jab line (cosmetic; the poke is radial)
	if a.charging {
		// Swallow the pending charge: holding + cancelling drops it without releasing a shot.
		in.ShootHeld = true
		in.CancelCharge = true
	}
	a.charging = false
	a.passReceiver = nil
	a.kickCooldown = p.view.Tick() + a.tune.kickCooldownTicks
	return in
}
