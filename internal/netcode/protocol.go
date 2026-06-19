// Package netcode is the server-authoritative LAN layer. The server runs the one
// true sim.Match and broadcasts snapshots; clients send intents and render the last
// snapshot they received. Transport is TCP with encoding/gob (standard library
// only). The sim types ARE the protocol -- an Intent is the client->server message
// and a Snapshot is a projection of Match state -- so there is a single source of
// truth.
package netcode

import (
	"image/color"
	"strconv"

	"phootball/internal/config"
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
	PlayerID    int // players only; lets a client mark which entity is "me"
	Position    geom.Vec
	Facing      geom.Vec
	Radius      float64
	Color       color.RGBA
	Number      int
	ShootCharge float64 // 0..1 shoot charge (players only)
	TrapCharge  float64 // 0..1 trap ENERGY bar (players only)
	TrapAura    float64 // 0..1 effective trap strength / glow (players only; 0 when not trapping)
}

// Snapshot is the authoritative state the server broadcasts each tick. It carries
// enough field geometry for a fresh client to render without prior knowledge.
type Snapshot struct {
	Tick        uint64
	Entities    []EntityState
	FieldMin    geom.Vec
	FieldMax    geom.Vec
	GoalWidth   float64
	GoalHeight  float64
	LeftName    string
	RightName   string
	LeftColor   color.RGBA
	RightColor  color.RGBA
	LeftScore   int
	RightScore  int
	Celebrating bool

	// Full pitch geometry, so a client draws the boxes/markings and builds its field
	// from one source of truth instead of the loose Field* fields above.
	Geometry config.Geometry

	// Match state for the HUD.
	ClockSeconds float64
	PhaseLabel   string
	Finished     bool
	Paused       bool   // the host paused the match (set by the server, not SnapshotOf)
	WinnerText   string // result message when finished
	GoalText     string // scorer/assist/own-goal message during a celebration

	// Penalty shootout tally.
	InShootout                                               bool
	PenLeftGoals, PenLeftTaken, PenRightGoals, PenRightTaken int

	// Positional-rule state, so the client can draw the offside/box indicators.
	OffsideEnabled       bool
	OffsideFrac          float64
	PenaltyBoxMaxPlayers int
	GoalAreaMaxPlayers   int

	// Sound events emitted this tick, played once by the client.
	Sounds []sim.SoundEvent

	// Live match statistics for the in-match stats HUD, and the play-by-play events emitted
	// THIS TICK only (a delta, not the whole log). Both are empty unless the server enabled
	// recording. The HUD renders identical numbers locally and over the network from Stats.
	Stats  sim.StatsSnapshot
	Events []sim.Event
}

// ProtoVersion is the wire-protocol version. The client stamps it on its first message and the
// server rejects a mismatch, so an old client cannot silently desync a new server. v2 added the
// lobby/control messages (ClientFrame, MsgLobby/MsgReject/MsgHostClosed/MsgPong) and the Hello
// IsHost/SessionToken fields. Hello and Reject keep a STABLE gob shape across versions so a
// mismatched peer can still decode the rejection.
const ProtoVersion = 2

// MsgKind tags an Envelope (the server->client message) as exactly one of its variants. NEVER
// reorder/remove MsgHello/MsgSnapshot -- append only (gob has no Register here; wire stability
// depends on stable shapes).
type MsgKind uint8

const (
	MsgHello      MsgKind = iota // the once-per-connection handshake
	MsgSnapshot                  // a per-tick state broadcast
	MsgReject                    // server refuses the connection (full / version / kicked)
	MsgLobby                     // pre-match lobby roster broadcast
	MsgHostClosed                // the host deliberately ended the match
	MsgPong                      // keepalive reply, for round-trip latency
)

// Hello is the server's handshake: protocol version, the assigned player slot (-1 = spectator),
// whether this connection is the host, and a per-connection reconnect token. The token is sent
// ONLY here (never in any roster broadcast). Fields are append-only for gob stability.
type Hello struct {
	ProtoVersion     int
	AssignedPlayerID int
	IsHost           bool
	SessionToken     string // crypto-random; lets this client reclaim its slot within the grace window
}

// Envelope is the server->client message: a tagged union with exactly one variant set per Kind.
// New variants are append-only concrete pointer fields (no gob.Register in this package).
type Envelope struct {
	Kind       MsgKind
	Hello      *Hello      `json:",omitempty"`
	Snapshot   *Snapshot   `json:",omitempty"`
	Reject     *Reject     `json:",omitempty"`
	Lobby      *LobbyState `json:",omitempty"`
	HostClosed *HostClosed `json:",omitempty"`
	Pong       *Pong       `json:",omitempty"`
}

// SnapshotOf projects a match into a wire snapshot.
func SnapshotOf(m *sim.Match) Snapshot {
	s := Snapshot{
		Tick:        m.Tick,
		FieldMin:    m.Field.Min,
		FieldMax:    m.Field.Max,
		GoalWidth:   m.Field.GoalWidth,
		GoalHeight:  m.Field.GoalHeight,
		LeftName:    m.Teams[0].Name,
		RightName:   m.Teams[1].Name,
		LeftColor:   m.Teams[0].Color,
		RightColor:  m.Teams[1].Color,
		LeftScore:   m.Teams[0].Score,
		RightScore:  m.Teams[1].Score,
		Celebrating: m.Celebrating(),

		Geometry:     m.Field.Geo,
		ClockSeconds: m.ClockSeconds(),
		PhaseLabel:   m.PhaseLabel(),
		Finished:     m.Finished(),
		WinnerText:   winnerText(m),
		GoalText:     goalText(m),
		InShootout:   m.InShootout(),
		// COPY the live sound buffer: the snapshot is encoded on a separate sender goroutine,
		// while the next Step reuses (truncates + appends to) m.sounds -- aliasing it would be a
		// data race and could ship a half-overwritten batch.
		Sounds: append([]sim.SoundEvent(nil), m.Sounds()...),

		OffsideEnabled:       m.Rules.OffsideEnabled,
		OffsideFrac:          m.Rules.OffsideFrac,
		PenaltyBoxMaxPlayers: m.Rules.PenaltyBoxMaxPlayers,
		GoalAreaMaxPlayers:   m.Rules.GoalAreaMaxPlayers,
	}
	if m.InShootout() {
		s.PenLeftGoals, s.PenRightGoals = m.ShootoutScore()
		s.PenLeftTaken, s.PenRightTaken = m.ShootoutTaken()
	}
	if rec := m.Recorder(); rec != nil {
		s.Stats = rec.StatsSnapshot()
		s.Events = rec.DrainNewEvents() // this tick's delta only -- never resend the whole log
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
			PlayerID:    p.PlayerID,
			Position:    p.Position,
			Facing:      p.Facing,
			Radius:      p.Radius(),
			Color:       p.Team.Color,
			Number:      p.Number,
			ShootCharge: sim.NormShootCharge(p.ShootCharge()),
			TrapCharge:  p.TrapCharge(),
			TrapAura:    p.TrapAura(),
		})
	}
	return s
}

// winnerText describes a finished match's result for the HUD.
func winnerText(m *sim.Match) string {
	if !m.Finished() {
		return ""
	}
	switch m.Winner() {
	case sim.SideLeft:
		return m.Teams[0].Name + " WINS"
	case sim.SideRight:
		return m.Teams[1].Name + " WINS"
	default:
		return "DRAW"
	}
}

// goalText describes the most recent goal during a celebration.
func goalText(m *sim.Match) string {
	if !m.Celebrating() || m.LastGoal == nil {
		return ""
	}
	g := m.LastGoal
	team := teamNameFor(m, g.Team)
	scorer := ""
	if g.HasScorer {
		scorer = " #" + playerNumber(m, g.Scorer)
	}
	if g.OwnGoal {
		return "OWN GOAL  " + team + scorer
	}
	msg := "GOAL!  " + team + scorer
	if g.HasAssist {
		msg += " (assist #" + playerNumber(m, g.Assist) + ")"
	}
	if g.Deflected {
		msg += " (deflected)"
	}
	return msg
}

func teamNameFor(m *sim.Match, side sim.Side) string {
	if m.Teams[0].Side == side {
		return m.Teams[0].Name
	}
	return m.Teams[1].Name
}

func playerNumber(m *sim.Match, id int) string {
	if p := m.PlayerByID(id); p != nil {
		return strconv.Itoa(p.Number)
	}
	return "?"
}
