package sim

import (
	"encoding/json"
	"image/color"
	"io"

	"phootball/internal/config"
)

// recordSchema is the on-disk MatchRecord schema version; bump it on an incompatible change.
const recordSchema = 1

// TeamInfo is the persisted identity of a team.
type TeamInfo struct {
	Name  string     `json:"name"`
	Color color.RGBA `json:"color"`
	Side  Side       `json:"side"`
}

// PlayerInfo is the persisted identity of a roster slot.
type PlayerInfo struct {
	ID     int  `json:"id"`
	Number int  `json:"number"`
	Role   Role `json:"role"`
	Side   Side `json:"side"`
}

// MatchRecord is the complete, reproducible record of one game: the setup needed to replay
// it (seed, geometry, ruleset, rosters), the final result, and the full play-by-play plus
// aggregates. It is plain data with JSON tags and round-trips through encoding/json.
type MatchRecord struct {
	Schema          int             `json:"schema"`
	Seed            int64           `json:"seed"`
	Geometry        config.Geometry `json:"geometry"`
	Ruleset         config.Ruleset  `json:"ruleset"`
	Teams           [2]TeamInfo     `json:"teams"`
	Players         []PlayerInfo    `json:"players"`
	FinalScore      [2]int          `json:"finalScore"`
	Winner          Side            `json:"winner"`
	DurationSeconds float64         `json:"durationSeconds"`
	Events          []Event         `json:"events"`
	PlayerStats     []PlayerStat    `json:"playerStats"`
	TeamStats       []TeamStat      `json:"teamStats"`
}

// MatchRecord assembles a full match record from the recorder and the final match state. It
// is safe on a nil recorder (the event log and aggregates are simply empty).
func (r *Recorder) MatchRecord(m *Match) MatchRecord {
	stats := r.Snapshot()
	rec := MatchRecord{
		Schema:          recordSchema,
		Seed:            m.Seed,
		Geometry:        m.Field.Geo,
		Ruleset:         m.Rules,
		FinalScore:      [2]int{m.Teams[0].Score, m.Teams[1].Score},
		Winner:          m.Winner(),
		DurationSeconds: m.Clock,
		Events:          stats.Events,
		PlayerStats:     stats.Players,
		TeamStats:       stats.Teams,
	}
	for i, t := range m.Teams {
		rec.Teams[i] = TeamInfo{Name: t.Name, Color: t.Color, Side: t.Side}
	}
	for _, p := range m.Players {
		rec.Players = append(rec.Players, PlayerInfo{ID: p.PlayerID, Number: p.Number, Role: p.Role, Side: p.Team.Side})
	}
	return rec
}

// WriteJSON writes the record as indented JSON. It round-trips: json.Unmarshal of the output
// reproduces an equal MatchRecord.
func (mr MatchRecord) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(mr)
}

// StatsSnapshot is the flattened aggregate view (no event log) shipped to a live HUD, locally
// or over the network. The full chronological log stays server-side; only per-tick deltas are
// sent separately (see Recorder.DrainNewEvents).
type StatsSnapshot struct {
	Players []PlayerStat `json:"players"`
	Teams   []TeamStat   `json:"teams"`
}

// StatsSnapshot returns a deep copy of just the aggregates (Players/Teams), for the live HUD.
func (r *Recorder) StatsSnapshot() StatsSnapshot {
	if r == nil {
		return StatsSnapshot{}
	}
	ms := r.Snapshot()
	return StatsSnapshot{Players: ms.Players, Teams: ms.Teams}
}
