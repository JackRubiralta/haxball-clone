package config

import (
	"fmt"
	"math"

	"phootball/internal/geom"
)

// MinCenterCircleRadius is the smallest the centre circle may be: large enough for a player to
// stand inside, off the ball, with room to spare. The maximum is derived per pitch in Normalize
// (a diameter of half the pitch length, also kept within the pitch width).
const MinCenterCircleRadius = 50.0

// MaxCenterCircleRadius returns the largest centre-circle radius that fits a pitch: a diameter of
// half the pitch length (the user-facing cap), also constrained to fit within the pitch width.
func MaxCenterCircleRadius(playWidth, playHeight float64) float64 {
	return math.Min(playWidth/4, playHeight/2)
}

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

// Validate checks the relational constraints between a RESOLVED geometry's dimensions:
// the two boxes must nest (goal mouth <= goal area <= penalty area, in both the across-pitch
// width and the into-pitch depth), the pitch must be longer than it is wide (PlayWidth is the
// goal-to-goal length, PlayHeight the across-pitch width), the goal mouth must fit across the
// pitch, and each enabled box must fit within the pitch. It is meant to run on the output of
// MatchSetup.Geometry() (preset + overrides applied); Normalize remains the last-line guard.
func (g Geometry) Validate() error {
	if g.PlayWidth <= 0 || g.PlayHeight <= 0 {
		return fmt.Errorf("pitch dimensions must be positive")
	}
	if g.PlayWidth < g.PlayHeight {
		return fmt.Errorf("pitch length (%.0f) must be at least its width (%.0f)", g.PlayWidth, g.PlayHeight)
	}
	if g.GoalMouthWidth <= 0 {
		return fmt.Errorf("goal mouth width must be positive")
	}
	if g.GoalMouthWidth > g.PlayHeight {
		return fmt.Errorf("goal mouth (%.0f) must not exceed the pitch width (%.0f)", g.GoalMouthWidth, g.PlayHeight)
	}
	// Goal pocket depth is part of the goal; treat it as the goal's into-pitch reference depth.
	if g.HasGoalArea {
		if g.GoalAreaWidth < g.GoalMouthWidth {
			return fmt.Errorf("goal-area width (%.0f) must be at least the goal mouth (%.0f)", g.GoalAreaWidth, g.GoalMouthWidth)
		}
		if g.GoalAreaDepth < g.GoalPocketDepth {
			return fmt.Errorf("goal-area depth (%.0f) must be at least the goal depth (%.0f)", g.GoalAreaDepth, g.GoalPocketDepth)
		}
		if g.GoalAreaWidth > g.PlayHeight {
			return fmt.Errorf("goal-area width (%.0f) must not exceed the pitch width (%.0f)", g.GoalAreaWidth, g.PlayHeight)
		}
		if 2*g.GoalAreaDepth > g.PlayWidth {
			return fmt.Errorf("goal-area depth (%.0f) must fit within the pitch length (%.0f)", g.GoalAreaDepth, g.PlayWidth)
		}
	}
	if g.HasPenaltyArea {
		if g.HasGoalArea {
			if g.PenaltyWidth < g.GoalAreaWidth {
				return fmt.Errorf("penalty-area width (%.0f) must be at least the goal-area width (%.0f)", g.PenaltyWidth, g.GoalAreaWidth)
			}
			if g.PenaltyDepth < g.GoalAreaDepth {
				return fmt.Errorf("penalty-area depth (%.0f) must be at least the goal-area depth (%.0f)", g.PenaltyDepth, g.GoalAreaDepth)
			}
		} else {
			if g.PenaltyWidth < g.GoalMouthWidth {
				return fmt.Errorf("penalty-area width (%.0f) must be at least the goal mouth (%.0f)", g.PenaltyWidth, g.GoalMouthWidth)
			}
			if g.PenaltyDepth < g.GoalPocketDepth {
				return fmt.Errorf("penalty-area depth (%.0f) must be at least the goal depth (%.0f)", g.PenaltyDepth, g.GoalPocketDepth)
			}
		}
		if g.PenaltyWidth > g.PlayHeight {
			return fmt.Errorf("penalty-area width (%.0f) must not exceed the pitch width (%.0f)", g.PenaltyWidth, g.PlayHeight)
		}
		if 2*g.PenaltyDepth > g.PlayWidth {
			return fmt.Errorf("penalty-area depth (%.0f) must fit within the pitch length (%.0f)", g.PenaltyDepth, g.PlayWidth)
		}
	}
	return nil
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
	// Centre circle: clamp to fit. Cap the radius so the DIAMETER is at most half the pitch
	// length (the user-facing max) and the circle still fits the pitch width; never below the
	// one-player minimum, unless the pitch is too small to hold even that (then the fit wins).
	if maxR := MaxCenterCircleRadius(g.PlayWidth, g.PlayHeight); maxR > 0 {
		if g.CenterCircleRadius > maxR {
			g.CenterCircleRadius = maxR
		}
		if g.CenterCircleRadius < MinCenterCircleRadius && MinCenterCircleRadius <= maxR {
			g.CenterCircleRadius = MinCenterCircleRadius
		}
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
// pitch. The "custom" name also resolves to the standard pitch as a BASE: it is the
// marker the lobby uses once every editable dimension has been populated explicitly
// (so no dimension is bundled), and the base only supplies the non-editable markings
// (post radius, centre-circle/spot radii) that the explicit overrides do not cover.
// The second result is false for an unknown name.
func PresetByName(name string) (Geometry, bool) {
	switch name {
	case "", "standard", "medium", "custom":
		// "medium" is the user-facing name for the standard (default, middle) pitch.
		return StandardGeometry(), true
	case "small", "futsal":
		return SmallGeometry(), true
	case "large", "big":
		return LargeGeometry(), true
	default:
		return Geometry{}, false
	}
}
