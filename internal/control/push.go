package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Middle-click push usage. The push is an INSTANT, no-charge, no-aim-assist radial jab: it
// sends the ball straight along (ball - player_centre) at a fixed 70% of front shot power,
// reaching any ball within the PULL radius. Because it never charges, it is the AI's fastest
// way to act on the ball, so the AI reaches for it in exactly the two cases a human would:
//
//	(1) Get RID of it under pressure -- a keeper or last defender booting it clear before a
//	    closing attacker can dispossess, where dwelling to line up and charge a clear loses it.
//	(2) Toe-push a tap-in -- jab the ball into the net from close range past a keeper set for
//	    a charged shot, when there is no time to wind one up.
//
// Its cost is total: no control over direction (pure radial) and no power scaling. So the AI
// only pushes when the radial ALREADY points where the ball needs to go (clear upfield, or into
// the goal mouth) AND it is under enough pressure that charging a real shot/clear would fail.

// canPush reports whether a push could physically fire: the ball must be within the (base)
// pull radius the push reaches. (It nearly always is when we control the ball, but the
// fallthrough to a charged clear still needs the guard.)
func (a *AI) canPush(p perception) bool {
	return p.gapToBall < p.me.Tuning().PullRange
}

// pushDir is the exact direction a push would send the ball: the pure radial from the player
// centre to the ball, with no aim assist. Zero if the ball sits on top of us.
func (a *AI) pushDir(p perception) geom.Vec {
	return geom.Unit(p.ball.Sub(p.me.Position()))
}

// pushClears reports whether an instant push would send the ball USEFULLY clear -- away from
// our own goal (upfield or wide), never back into our own danger. The push direction is fixed
// (radial), so a push is only safe to boot when the ball already sits on our upfield side.
func (a *AI) pushClears(p perception) bool {
	if !a.canPush(p) {
		return false
	}
	dir := a.pushDir(p)
	if dir == (geom.Vec{}) {
		return false
	}
	return dir.X*p.attackX > a.tune.pushClearMinForward
}

// pushShotOn reports whether an instant push would be a SHOT on target: from close range the
// radial launch crosses the enemy goal mouth. A toe-push beats charging a shot a closing
// defender would block -- but only when we are already lined up at goal, since the push has no
// aim assist to bend it in.
func (a *AI) pushShotOn(p perception) bool {
	if !a.canPush(p) {
		return false
	}
	if geom.Dist(p.me.Position(), p.enemyGoal) > a.tune.tapRange {
		return false // a genuine close-range tap-in, not a hopeful long jab
	}
	dir := a.pushDir(p)
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

// shouldPokeSteal reports whether the presser should nick the ball off an opponent it is
// pressing with a quick middle-click poke (a tackle): the opponent controls the ball, a push
// reaches it, and the pure-radial jab sends it AWAY from our goal (upfield/wide) -- never a poke
// into our own danger. It only fires at very close range so the radial reliably points off the
// opponent. A clean, visible use of the push ability that disrupts the opponent and springs the
// ball into space ahead, rather than dwelling to set up a trap-steal.
func (a *AI) shouldPokeSteal(p perception) bool {
	if !a.tune.pokeSteal || !p.carrierEnemy || !a.canPush(p) {
		return false
	}
	if p.gapToBall > a.tune.pokeStealRange {
		return false
	}
	// Only in our OWN half: a poke-tackle is a defensive tool (win the ball back and spring it
	// upfield). In the attacking half we would rather dispossess cleanly and keep possession, so
	// poking there just hands the ball back and starves our own attack (it cost goals in a sweep).
	if !ballInOwnHalf(p) {
		return false
	}
	dir := a.pushDir(p)
	return dir != (geom.Vec{}) && dir.X*p.attackX > a.tune.pushClearMinForward
}

// pushIntent produces the Intent that fires a middle-click push this tick: an instant radial
// jab, no charge. It cancels any in-progress charge (so the same tick can't ALSO release a
// charged shot) and sets the post-kick cooldown so the player takes a real touch before
// kicking again. The push is a one-shot edge action -- AI.Intent clears it from the cached
// intent so the reaction-delay replay can never re-fire (and compound) the jab.
func (a *AI) pushIntent(p perception) sim.Intent {
	in := sim.Intent{Push: true, Aim: a.aimToward(p, p.ball)} // face the jab line (cosmetic; the push is radial)
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
