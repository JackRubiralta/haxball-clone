package sim

import "testing"

func TestZZCaptureBaselines(t *testing.T) {
	_, pos1, vel1 := dribbleRun(60, 0)
	t.Logf("dribble60: pos %.10f %.10f vel %.10f %.10f", pos1.X, pos1.Y, vel1.X, vel1.Y)
	_, pos2, vel2 := dribbleRun(20, 40)
	t.Logf("stopturn: pos %.10f %.10f vel %.10f %.10f", pos2.X, pos2.Y, vel2.X, vel2.Y)
}
