// Package netcode is the server-authoritative LAN layer. The server runs the one
// true sim.Match and broadcasts snapshots; clients send intents and render the last
// snapshot they received. Transport is TCP with encoding/gob (standard library
// only). The sim types ARE the protocol -- an Intent is the client->server message
// and a Snapshot is a projection of Match state -- so there is a single source of
// truth.
package netcode

import (
	"image/color"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// EntityKind distinguishes drawable entities in a snapshot.
type EntityKind int

const (
	KindBall EntityKind = iota
	KindPlayer
)

// EntityState is one renderable entity in a snapshot.
type EntityState struct {
	Kind        EntityKind
	Position    geom.Vec
	Facing      geom.Vec
	Radius      float64
	Color       color.RGBA
	Number      int
	ShootCharge float64 // 0..1 shoot charge (players only)
	TrapCharge  float64 // 0..1 trap charge (players only)
}

// Snapshot is the authoritative state the server broadcasts each tick. It carries
// enough field geometry for a fresh client to render without prior knowledge.
type Snapshot struct {
	Tick       uint64
	Entities   []EntityState
	FieldMin   geom.Vec
	FieldMax   geom.Vec
	GoalWidth  float64
	GoalHeight float64
	LeftName   string
	RightName  string
	LeftColor   color.RGBA
	RightColor  color.RGBA
	LeftScore   int
	RightScore  int
	Celebrating bool
}

// ClientMsg is what a client sends the server each tick.
type ClientMsg struct {
	Intent sim.Intent
}

// SnapshotOf projects a match into a wire snapshot.
func SnapshotOf(m *sim.Match) Snapshot {
	s := Snapshot{
		Tick:       m.Tick,
		FieldMin:   m.Field.Min,
		FieldMax:   m.Field.Max,
		GoalWidth:  m.Field.GoalWidth,
		GoalHeight: m.Field.GoalHeight,
		LeftName:   m.Teams[0].Name,
		RightName:  m.Teams[1].Name,
		LeftColor:   m.Teams[0].Color,
		RightColor:  m.Teams[1].Color,
		LeftScore:   m.Teams[0].Score,
		RightScore:  m.Teams[1].Score,
		Celebrating: m.Celebrating(),
	}
	s.Entities = append(s.Entities, EntityState{
		Kind:     KindBall,
		Position: m.Ball.Position,
		Radius:   m.Ball.Radius(),
		Color:    color.RGBA{255, 255, 255, 255},
	})
	for _, p := range m.Players {
		s.Entities = append(s.Entities, EntityState{
			Kind:        KindPlayer,
			Position:    p.Position,
			Facing:      p.Facing,
			Radius:      p.Radius(),
			Color:       p.Team.Color,
			Number:      p.Number,
			ShootCharge: sim.NormShootCharge(p.ShootCharge()),
			TrapCharge:  p.TrapCharge(),
		})
	}
	return s
}
