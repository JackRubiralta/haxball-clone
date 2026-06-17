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
}
