package sim

import "phootball/internal/geom"

// Intent is the single per-tick value every input source produces for one player:
// a local human, an AI, or a remote network client. It is the sole channel by which
// an actor influences the simulation, and it is plain value data so it can also be
// the client-to-server network message unchanged.
type Intent struct {
	Move      geom.Vec // desired movement direction (need not be unit; Move normalises it)
	Throttle  float64  // [0,1] how hard to accelerate along Move
	Aim       geom.Vec // world point to face (cursor for a human, ball or goal for AI)
	ShootHeld bool     // shoot button currently held; the sim charges while held and fires on release
	Trap      bool     // trap ("good touch") button currently held; the sim builds trap charge while held
	// CancelCharge cancels an in-progress shot charge this tick: the charge is dropped and
	// the player will NOT fire when the shoot button is released. A human sets it on the
	// rising edge of the cancel (right-click); the AI never sets it, so its own trap-while-
	// charging recover move can never be mistaken for a cancel.
	CancelCharge bool
	// AimFromCursor marks Aim as a raw human cursor target, so the sim turns the facing toward
	// it at the player's TurnRate (the disk can't instantly snap to the cursor). The AI leaves it
	// false: its facing is instant in the sim (its on-ball aim is smoothed in the control layer,
	// and its off-ball aim is rate-limited there too -- see AI.capAim).
	AimFromCursor bool
}
