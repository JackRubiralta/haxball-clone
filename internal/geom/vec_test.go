package geom

import (
	"math"
	"testing"
)

const eps = 1e-9

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAddSubScale(t *testing.T) {
	a, b := NewVec(1, 2), NewVec(3, -1)
	if got := a.Add(b); got != (Vec{4, 1}) {
		t.Errorf("Add = %v", got)
	}
	if got := a.Sub(b); got != (Vec{-2, 3}) {
		t.Errorf("Sub = %v", got)
	}
	if got := a.Scale(2); got != (Vec{2, 4}) {
		t.Errorf("Scale = %v", got)
	}
}

func TestNormAndUnit(t *testing.T) {
	v := NewVec(3, 4)
	if !approx(Norm(v), 5) {
		t.Errorf("Norm(3,4) = %v, want 5", Norm(v))
	}
	u := Unit(v)
	if !approx(Norm(u), 1) {
		t.Errorf("Unit not length 1: %v", Norm(u))
	}
	// Unit of a (near) zero vector is the zero vector, not NaN.
	if got := Unit(NewVec(0, 0)); got != (Vec{}) {
		t.Errorf("Unit(0) = %v, want zero", got)
	}
	if got := Unit(NewVec(1e-12, 0)); got != (Vec{}) {
		t.Errorf("Unit(tiny) = %v, want zero", got)
	}
}

func TestDotCross(t *testing.T) {
	if got := Dot(NewVec(1, 0), NewVec(0, 1)); got != 0 {
		t.Errorf("Dot orthogonal = %v, want 0", got)
	}
	if got := Cross(NewVec(1, 0), NewVec(0, 1)); got != 1 {
		t.Errorf("Cross = %v, want 1 (b CCW of a)", got)
	}
}

func TestAngleBetweenAndSigned(t *testing.T) {
	if got := AngleBetween(NewVec(1, 0), NewVec(0, 1)); !approx(got, math.Pi/2) {
		t.Errorf("AngleBetween = %v, want pi/2", got)
	}
	// Degenerate inputs return 0 rather than NaN.
	if got := AngleBetween(NewVec(0, 0), NewVec(1, 0)); got != 0 {
		t.Errorf("AngleBetween(0,_) = %v, want 0", got)
	}
	if got := SignedAngle(NewVec(1, 0), NewVec(0, 1)); !approx(got, math.Pi/2) {
		t.Errorf("SignedAngle CCW = %v, want +pi/2", got)
	}
	if got := SignedAngle(NewVec(1, 0), NewVec(0, -1)); !approx(got, -math.Pi/2) {
		t.Errorf("SignedAngle CW = %v, want -pi/2", got)
	}
}

func TestRotate(t *testing.T) {
	// Rotate (1,0) by 90deg about the origin -> (0,1).
	got := NewVec(1, 0).Rotate(math.Pi/2, NewVec(0, 0))
	if !approx(got.X, 0) || !approx(got.Y, 1) {
		t.Errorf("Rotate 90deg = %v, want ~(0,1)", got)
	}
	// Rotating about a point leaves that point fixed.
	p := NewVec(5, 5)
	if got := p.Rotate(1.234, p); Dist(got, p) > eps {
		t.Errorf("Rotate about self moved the point: %v", got)
	}
}

func TestDist(t *testing.T) {
	if got := Dist(NewVec(0, 0), NewVec(3, 4)); !approx(got, 5) {
		t.Errorf("Dist = %v, want 5", got)
	}
}
