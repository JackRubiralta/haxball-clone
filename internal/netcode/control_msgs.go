package netcode

import (
	"phootball/internal/config"
	"phootball/internal/sim"
)

// CMsgKind tags a ClientFrame (the client->server message) as exactly one of its variants. The
// client->server stream is a SINGLE gob type (ClientFrame) because a gob.Decoder is bound to one
// concrete type per stream -- it cannot decode an Intent message then a control message off the
// same stream. CIntent stays the hot path (one per tick); the rest are infrequent control.
// Append-only for wire stability.
type CMsgKind uint8

const (
	CIntent      CMsgKind = iota // the per-tick movement/aim/throttle intent (hot path)
	CJoin                        // fresh guest handshake: carries the display name
	CHostToken                   // present the pending-host token to claim host (loopback-vs-remote race)
	CPickSlot                    // claim a team+slot (Slot=-1 means "first open on Team"); spectate via Team=-1
	CConfig                      // host pushes a new authoritative match config
	CReady                       // toggle this client's ready flag
	CStart                       // host starts the match
	CPause                       // host pause/resume toggle (gates the sim Step)
	CHostClose                   // host ends the match for everyone
	CKick                        // host kicks an occupant (by PlayerID in Slot)
	CReturnLobby                 // host returns everyone to the lobby after a result
	CPing                        // keepalive; server replies MsgPong with the same Seq
	CResumeToken                 // reconnect: present a prior SessionToken to reclaim a reserved slot
)

// ClientFrame is the one message a client ever sends. Only the fields relevant to Kind are read;
// the rest are ignored. ProtoVersion is validated on the first frame. Append-only fields.
type ClientFrame struct {
	ProtoVersion int
	Kind         CMsgKind
	Intent       sim.Intent // CIntent

	HostToken   string // CHostToken
	ResumeToken string // CResumeToken
	Name        string // CHostToken / CResumeToken: this client's display name
	Team        int    // CPickSlot/CSpectate: 0=home,1=away,-1=spectate; CKick: unused
	Slot        int    // CPickSlot: roster index, or -1 for "first open"; CKick: target PlayerID

	Setup *config.MatchSetup // CConfig: the host-authored match configuration

	Ready  bool   // CReady
	Paused bool   // CPause
	Seq    uint64 // CPing: echoed back in MsgPong for round-trip timing
}

// Reject is sent (before the socket closes) when the server refuses or terminates a connection,
// so the client can show a friendly reason instead of a silent drop. Stable gob shape so a
// version-mismatched peer can still decode it.
type Reject struct{ Reason string }

// HostClosed tells every client the host deliberately ended the match (vs a crash, which arrives
// as a bare connection drop). There is no host migration -- clients hold only snapshots.
type HostClosed struct{ Reason string }

// Pong is the keepalive reply used to measure round-trip latency. Seq echoes the client's CPing.
type Pong struct {
	Seq        uint64
	ServerTick uint64
}

// SeatInfo is one roster seat in a LobbyState. Slot is the index into the team's Players and
// PlayerID is the SAME m.Teams[Team].Players[Slot].PlayerID the server assigns from -- never an
// independent counter -- so "take seat 2" claims the right player.
type SeatInfo struct {
	Team         int    // 0=home (Blue/left), 1=away (Red/right)
	Slot         int    // index into that team's roster
	PlayerID     int    // == m.Teams[Team].Players[Slot].PlayerID
	Role         string // "GK" or "OUT"
	OccupantName string // "" = open (AI fills it); otherwise the human's display name
	IsHuman      bool   // a human currently holds this seat
	Ready        bool   // that human is ready
}

// LobbyState is the pre-match roster broadcast (MsgLobby), sent every tick while the match has
// not started. It NEVER carries session tokens. Phase lets a late joiner know it missed kickoff.
type LobbyState struct {
	Phase         uint8      // 0 = lobby, 1 = in-match
	HostName      string     // display name of the host
	Seats         []SeatInfo // both teams, home first
	Spectators    []string   // names of connected viewers without a seat
	HomeSize      int        // roster sizes (so the UI lays out columns before any seat is filled)
	AwaySize      int
	ConfigSummary string // one-line human-readable match config (host-authored)
	SpectatorCap  int    // max spectators (informational)
	AllReady      bool   // every seated human is ready (host Start hint)

	// Per-connection fields, set freshly for each recipient at send time (NOT shared).
	YouPlayerID int  // the receiving client's current seat (-1 = spectator); how it finds "me"
	YouAreHost  bool // the receiving client is the host (shows host-only controls)
}
