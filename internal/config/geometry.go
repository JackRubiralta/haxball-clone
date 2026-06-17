package config

import "phootball/internal/geom"

// Rect is an axis-aligned rectangle in world coordinates.
type Rect struct {
	Min, Max geom.Vec
}

// Contains reports whether p lies within the rectangle (edges inclusive).
func (r Rect) Contains(p geom.Vec) bool {
	return p.X >= r.Min.X && p.X <= r.Max.X && p.Y >= r.Min.Y && p.Y <= r.Max.Y
}

// Width returns the rectangle's extent along X.
func (r Rect) Width() float64 { return r.Max.X - r.Min.X }

// Height returns the rectangle's extent along Y.
func (r Rect) Height() float64 { return r.Max.Y - r.Min.Y }

// Geometry is the single source of truth for every pitch dimension. The play area is
// centred inside a ScreenWidth x ScreenHeight logical surface; the goal pockets live in
// the surrounding margin. Box sizes are given as their extent across the pitch (Width,
// along Y) and into the pitch from the goal line (Depth, along X). Everything is in the
// same fixed world units the simulation and renderer use.
type Geometry struct {
	Name string

	// Play area and the logical surface it is centred in.
	PlayWidth, PlayHeight     float64
	ScreenWidth, ScreenHeight float64

	// Goal mouth across the goal line, pocket depth behind it, and post radius.
	GoalMouthWidth  float64
	GoalPocketDepth float64
	PostRadius      float64

	// The two boxes: the outer penalty area and the inner goal area. The Has flags toggle
	// each box's existence without losing its dimensions, so a box can be turned off and
	// back on in the lobby.
	HasPenaltyArea               bool
	HasGoalArea                  bool
	PenaltyWidth, PenaltyDepth   float64
	GoalAreaWidth, GoalAreaDepth float64

	// Marking sizes.
	CenterCircleRadius    float64
	CenterSpotMarkRadius  float64
	PenaltySpotMarkRadius float64
}

// Min returns the top-left corner of the play area (the play area centred in the
// logical surface).
func (g Geometry) Min() geom.Vec {
	return geom.NewVec((g.ScreenWidth-g.PlayWidth)/2, (g.ScreenHeight-g.PlayHeight)/2)
}

// Max returns the bottom-right corner of the play area.
func (g Geometry) Max() geom.Vec {
	m := g.Min()
	return geom.NewVec(m.X+g.PlayWidth, m.Y+g.PlayHeight)
}

// Center returns the centre of the play area (the kickoff spot).
func (g Geometry) Center() geom.Vec {
	return g.Min().Add(g.Max()).Scale(0.5)
}

// Normalize fills in any sizes left at zero with sensible derived values, so a config
// assembled from a few command-line dimensions is still complete. It never shrinks a
// surface that already comfortably holds the play area and its goal pockets, so a fully
// specified preset (such as the standard pitch) passes through unchanged.
func (g Geometry) Normalize() Geometry {
	if g.PostRadius <= 0 {
		g.PostRadius = 6
	}
	if min := g.PlayWidth + 2*(g.GoalPocketDepth+20); g.ScreenWidth < min {
		g.ScreenWidth = min
	}
	if min := g.PlayHeight + 80; g.ScreenHeight < min {
		g.ScreenHeight = min
	}
	// A box flagged on but given no size cannot exist.
	if g.HasPenaltyArea && (g.PenaltyWidth <= 0 || g.PenaltyDepth <= 0) {
		g.HasPenaltyArea = false
	}
	if g.HasGoalArea && (g.GoalAreaWidth <= 0 || g.GoalAreaDepth <= 0) {
		g.HasGoalArea = false
	}
	return g
}

// StandardGeometry is the default pitch. Centred in its 1000x680 surface it spans
// Min=(60,100) Max=(940,580) with a 100-wide goal mouth and a 40-deep pocket, matching
// the original hard-coded field exactly.
func StandardGeometry() Geometry {
	return Geometry{
		Name:                  "standard",
		PlayWidth:             880,
		PlayHeight:            480,
		ScreenWidth:           1000,
		ScreenHeight:          680,
		GoalMouthWidth:        100,
		GoalPocketDepth:       40,
		PostRadius:            6,
		HasPenaltyArea:        true,
		HasGoalArea:           true,
		PenaltyWidth:          330,
		PenaltyDepth:          150,
		GoalAreaWidth:         150,
		GoalAreaDepth:         75,
		CenterCircleRadius:    72,
		CenterSpotMarkRadius:  5,
		PenaltySpotMarkRadius: 4,
	}
}

// SmallGeometry is a tight futsal-sized pitch.
func SmallGeometry() Geometry {
	g := StandardGeometry()
	g.Name = "small"
	g.PlayWidth = 600
	g.PlayHeight = 360
	g.ScreenWidth = 720
	g.ScreenHeight = 520
	g.GoalMouthWidth = 90
	g.GoalPocketDepth = 34
	g.PenaltyWidth = 240
	g.PenaltyDepth = 100
	g.GoalAreaWidth = 120
	g.GoalAreaDepth = 55
	g.CenterCircleRadius = 54
	return g
}

// LargeGeometry is a wide pitch for bigger matches (best played with the zoomed camera).
func LargeGeometry() Geometry {
	g := StandardGeometry()
	g.Name = "large"
	g.PlayWidth = 1320
	g.PlayHeight = 720
	g.ScreenWidth = 1480
	g.ScreenHeight = 940
	g.GoalMouthWidth = 130
	g.GoalPocketDepth = 50
	g.PenaltyWidth = 460
	g.PenaltyDepth = 210
	g.GoalAreaWidth = 210
	g.GoalAreaDepth = 105
	g.CenterCircleRadius = 96
	return g
}

// PresetByName returns the named geometry preset. An empty name selects the standard
// pitch. The second result is false for an unknown name.
func PresetByName(name string) (Geometry, bool) {
	switch name {
	case "", "standard":
		return StandardGeometry(), true
	case "small", "futsal":
		return SmallGeometry(), true
	case "large", "big":
		return LargeGeometry(), true
	default:
		return Geometry{}, false
	}
}
