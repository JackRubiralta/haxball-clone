package config

import "math"

// AngleCurve maps an angle within [startAngle, endAngle] to a value within
// [startValue, endValue] using some shape. Every curve shares this signature, so a
// PlayerTuning can pick which one drives each angle-dependent quantity (restitution,
// the front pull, shot power, ...). startValue is returned at startAngle and endValue
// at endAngle; the named curves differ only in how they move between the two.
type AngleCurve func(startValue, endValue, startAngle, endAngle, angle float64) float64

// CurveSpec holds the front (0 rad) and back (pi rad) endpoints of an angle-dependent
// quantity (restitution, the front pull, shot power, ...). The curve SHAPE that interpolates
// between the endpoints is FIXED per quantity and hardcoded in the PlayerTuning evaluator
// methods (RestitutionAt, CaptureSpeedAt, CenterPullAt, StickinessAt, ControlAt) -- it is not
// stored here, so a CurveSpec is plain data (no function value) and the shape is never a
// tunable knob. Only the two endpoints are editable.
type CurveSpec struct {
	Front float64
	Back  float64
}

// ExponentialSteepness controls how sharply ExponentialCurve bends.
const ExponentialSteepness = 3.0

// curveProgress returns how far angle sits between startAngle and endAngle, as a
// fraction clamped to [0, 1].
func curveProgress(startAngle, endAngle, angle float64) float64 {
	if endAngle == startAngle {
		return 0
	}
	t := (angle - startAngle) / (endAngle - startAngle)
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// LinearCurve moves at a constant rate from startValue to endValue.
func LinearCurve(startValue, endValue, startAngle, endAngle, angle float64) float64 {
	t := curveProgress(startAngle, endAngle, angle)
	return startValue + (endValue-startValue)*t
}

// QuadraticCurve eases in: it lingers near startValue, then accelerates to endValue.
func QuadraticCurve(startValue, endValue, startAngle, endAngle, angle float64) float64 {
	t := curveProgress(startAngle, endAngle, angle)
	return startValue + (endValue-startValue)*t*t
}

// InverseQuadraticCurve eases out: it jumps toward endValue early, then flattens.
func InverseQuadraticCurve(startValue, endValue, startAngle, endAngle, angle float64) float64 {
	t := curveProgress(startAngle, endAngle, angle)
	return startValue + (endValue-startValue)*(1-(1-t)*(1-t))
}

// SmoothstepCurve eases in and out, staying flat at both ends.
func SmoothstepCurve(startValue, endValue, startAngle, endAngle, angle float64) float64 {
	t := curveProgress(startAngle, endAngle, angle)
	return startValue + (endValue-startValue)*t*t*(3-2*t)
}

// ExponentialCurve hugs startValue and then climbs steeply toward endValue.
func ExponentialCurve(startValue, endValue, startAngle, endAngle, angle float64) float64 {
	t := curveProgress(startAngle, endAngle, angle)
	shaped := (math.Exp(ExponentialSteepness*t) - 1) / (math.Exp(ExponentialSteepness) - 1)
	return startValue + (endValue-startValue)*shaped
}
