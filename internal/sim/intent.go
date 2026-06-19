package sim

import "phootball/internal/geom"

// Intent is the single per-tick value every input source produces for one player:
// a local human, an AI, or a remote network client. It is the sole channel by which
// an actor influences the simulation, and it is plain value data so it can also be
// the client-to-server network message unchanged.
//
// Every field maps to a real human input, and the AI may set any of them -- the two are
// indistinguishable to the sim by design. The human mapping (see internal/input.Human):
//
//	WASD            -> Move + Throttle
//	cursor          -> Aim + AimFromCursor=true (face toward it at TurnRate)
//	LMB held        -> ShootHeld (charge while held, fire on release)
//	RMB held        -> Trap (and, while LMB is also held, CancelCharge)
//	MMB held        -> Push (the sim fires the jab on the rising edge it reconstructs)
type Intent struct {
	Move      geom.Vec // desired movement direction (need not be unit; Move normalises it)
	Throttle  float64  // [0,1] how hard to accelerate along Move
	Aim       geom.Vec // world point to face (cursor for a human, ball or goal for AI)
	ShootHeld bool     // shoot button currently held; the sim charges while held and fires on release
	Trap      bool     // trap ("good touch") button currently held; the sim builds trap charge while held
	// CancelCharge drops an in-progress shot charge this tick: the charge is cleared (and latched
	// canceled) so the player will NOT fire when the shoot button is released. It is a human-reachable
	// signal -- a human raises it as a LEVEL while a higher-priority ability (trap or push) is held
	// together with shoot -- and the AI uses it too, exactly as a human can: to abort a stuck/overtime
	// charge, and when a trap or push takes over a live charge (see control/abilities.go,
	// control/push.go, control/ai.go enforceAbilityExclusivity).
	CancelCharge bool
	// Push is the middle-click jab: an INSTANT, minimum-power radial push of the ball that reaches
	// any ball within the PULL radius (not just touching) and fires equally in every direction (no
	// aim assist, no charge). It is a LEVEL signal (true while middle is held); the sim fires the jab
	// once on the rising edge it reconstructs (Player.pushHeldPrev) -- so it is idempotent when an
	// intent is re-applied across network ticks. The AI sets it too (its keeper/carrier jab-away
	// move -- a one-tick pulse gated by a kick cooldown; see control/push.go).
	Push bool
	// AimFromCursor marks Aim as a raw human cursor target, so the sim turns the facing toward
	// it at the player's TurnRate (the disk can't instantly snap to the cursor). The AI leaves it
	// false: its facing is instant in the sim (its on-ball aim is smoothed in the control layer,
	// and its off-ball and keeper aim is rate-limited there too -- see AI.capAim).
	AimFromCursor bool
	// MoveRelativeToFacing marks Move as being in the player's FACING frame rather than world space:
	// the sim rotates it so +"up" drives toward Facing (W = forward, S = back, A/D = strafe). It is
	// the "heading-locked" human control scheme (input.Human.SetMoveRelative). The AI leaves it
	// false -- it sets Move directly in world space -- so its movement is never reframed.
	MoveRelativeToFacing bool
}
