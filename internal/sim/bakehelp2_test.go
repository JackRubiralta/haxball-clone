package sim

import "phootball/internal/geom"

func vecFar(id int) geom.Vec { return geom.NewVec(-1e5, float64(id)*60) }
func zeroVec() geom.Vec      { return geom.NewVec(0, 0) }
