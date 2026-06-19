package sim

import (
	"image/color"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// newTeams returns the two standard teams (home = left/Blue, away = right/Red) so the
// Blue/Red identity -- side, name, colour -- is defined once and shared by every builder.
func newTeams() (left, right *Team) {
	return &Team{Side: SideLeft, Name: "Blue", Color: color.RGBA{80, 140, 255, 255}},
		&Team{Side: SideRight, Name: "Red", Color: color.RGBA{255, 100, 100, 255}}
}

// ClaimableHumanIDs returns one claimable human slot per team: an outfielder (roster index 1)
// when the team has one, else its only player. The single definition of the "one seat per team,
// prefer an outfielder over the keeper" rule shared by the CLI server and the lobby host.
func (m *Match) ClaimableHumanIDs() []int {
	ids := make([]int, 0, len(m.Teams))
	for _, t := range m.Teams {
		idx := 0
		if len(t.Players) > 1 {
			idx = 1
		}
		ids = append(ids, t.Players[idx].PlayerID)
	}
	return ids
}

// BuildMatch creates a standard match: a centred field with a goal on each side,
// two teams of teamSize players in a simple formation, and the ball on the spot.
func BuildMatch(field *Field, teamSize int) *Match {
	return BuildMatchSized(field, teamSize, teamSize)
}

// BuildMatchSized builds a standard match with per-team roster sizes (home = left/Blue,
// away = right/Red). buildFormation already lays out an arbitrary count per team, so the
// two teams may differ in size.
func BuildMatchSized(field *Field, homeSize, awaySize int) *Match {
	left, right := newTeams()

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, config.DefaultTuning().BallRadius),
	}

	id := 0
	left.Players = buildFormation(field, left, homeSize, &id)
	right.Players = buildFormation(field, right, awaySize, &id)
	m.Players = append(m.Players, left.Players...)
	m.Players = append(m.Players, right.Players...)
	m.clearCenterCircle(nil) // match start: the ball sits alone in the centre circle, players outside
	m.applyConfig(config.Default())
	return m
}

// BuildMatchFromConfig builds a standard match and applies a full config (ruleset,
// physics tuning, RNG seed). The field is expected to be built from cfg.Geometry.
func BuildMatchFromConfig(field *Field, teamSize int, cfg config.Config) *Match {
	return BuildMatchFromConfigSized(field, teamSize, teamSize, cfg)
}

// BuildMatchFromConfigSized builds a per-team-sized match and applies a full config
// (ruleset, physics tuning, RNG seed). The field is expected to be built from
// cfg.Geometry.
func BuildMatchFromConfigSized(field *Field, homeSize, awaySize int, cfg config.Config) *Match {
	m := BuildMatchSized(field, homeSize, awaySize)
	m.applyConfig(cfg)
	return m
}

// BuildSolo creates a single-player testing match: one human-controlled player with
// the default tuning and the ball, with no opponents and no obstacles. The opposing
// team exists but has an empty roster so scoring and rendering still work.
func BuildSolo(field *Field) *Match {
	left, right := newTeams()

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, config.DefaultTuning().BallRadius),
	}

	start := geom.NewVec(field.Min.X+field.Width()*0.25, field.CenterSpot.Y)
	p := NewPlayer(0, start, config.DefaultPlayerTuning(), left)
	p.Role = RoleMidfielder
	p.Number = 10
	left.Players = []*Player{p}
	m.Players = []*Player{p}
	m.applyConfig(config.Default())
	return m
}

// BuildDuo creates a two-player testing match: one player on each side (no AI) that
// the human alternates control of. Good for testing dribbling, passing, and stealing
// by switching between them.
func BuildDuo(field *Field) *Match {
	left, right := newTeams()

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, config.DefaultTuning().BallRadius),
	}

	c := field.CenterSpot
	p0 := NewPlayer(0, geom.NewVec(c.X-120, c.Y), config.DefaultPlayerTuning(), left)
	p0.Role = RoleMidfielder
	p0.Number = 1
	p1 := NewPlayer(1, geom.NewVec(c.X+120, c.Y), config.DefaultPlayerTuning(), right)
	p1.Role = RoleMidfielder
	p1.Number = 2
	p1.Facing = geom.NewVec(-1, 0)

	left.Players = []*Player{p0}
	right.Players = []*Player{p1}
	m.Players = []*Player{p0, p1}
	m.applyConfig(config.Default())
	return m
}

// formationLine groups the outfield players into depth-banded lines: defenders, then
// midfielders, then forwards. The keeper is always added separately at index 0.
type formationLine struct {
	role  Role
	count int
	// depth is the fraction of the team's OWN half (0 = on the goal line, 1 = at the
	// halfway line) at which the line sits.
	depth float64
}

// outfieldLines returns the DEF/MID/FWD line breakdown for k outfield players (the
// roster minus the keeper). The shapes mirror real small-sided formations and scale
// across the supported team sizes; any larger count keeps adding midfielders. Depths
// deepen the further forward a line plays.
func outfieldLines(k int) []formationLine {
	switch k {
	case 0:
		return nil
	case 1: // GK + lone striker
		return []formationLine{{RoleAttacker, 1, 0.78}}
	case 2: // 1-0-1
		return []formationLine{{RoleDefender, 1, 0.45}, {RoleAttacker, 1, 0.82}}
	case 3: // 1-1-1
		return []formationLine{{RoleDefender, 1, 0.35}, {RoleMidfielder, 1, 0.6}, {RoleAttacker, 1, 0.85}}
	case 4: // 2-1-1
		return []formationLine{{RoleDefender, 2, 0.35}, {RoleMidfielder, 1, 0.62}, {RoleAttacker, 1, 0.86}}
	case 5: // 2-2-1
		return []formationLine{{RoleDefender, 2, 0.35}, {RoleMidfielder, 2, 0.62}, {RoleAttacker, 1, 0.86}}
	case 6: // 2-3-1
		return []formationLine{{RoleDefender, 2, 0.32}, {RoleMidfielder, 3, 0.58}, {RoleAttacker, 1, 0.86}}
	default: // 7+ : 3 at the back, the surplus in midfield, 1 up top
		fwd := 1
		def := 3
		mid := k - def - fwd
		return []formationLine{{RoleDefender, def, 0.3}, {RoleMidfielder, mid, 0.56}, {RoleAttacker, fwd, 0.86}}
	}
}

// buildFormation lays out one team across its own half in role-based, depth-banded lines:
// a keeper on the goal line at index 0 (number 1) -- always, so penaltyTaker/humanSlot's
// index-0-is-keeper convention holds -- then DEF/MID/FWD lines per outfieldLines, each
// line spread evenly across the pitch via (i+1)/(count+1) so a player never sits on a
// touchline. A team of 1 is a lone midfielder with no keeper. PlayerID order follows the
// *id sequence (keeper first, then each line). Every player faces the opponent goal.
func buildFormation(f *Field, team *Team, n int, id *int) []*Player {
	if n < 1 {
		return nil
	}
	players := make([]*Player, 0, n)
	center := f.CenterSpot
	halfWidth := f.Width() / 2 // distance from the goal line to the halfway line

	var ownX, dir float64
	face := geom.NewVec(1, 0)
	if team.Side == SideLeft {
		ownX, dir = f.Min.X, 1
	} else {
		ownX, dir, face = f.Max.X, -1, geom.NewVec(-1, 0)
	}

	number := 1
	add := func(role Role, pos geom.Vec) {
		p := NewPlayer(*id, pos, TuningForRole(role), team)
		p.Role = role
		p.Number = number
		p.Facing = face
		players = append(players, p)
		*id++
		number++
	}

	// A lone player (n == 1) is an outfielder, not a keeper. Otherwise the keeper is
	// always index 0 / number 1, parked just off its own goal line.
	k := n
	if n >= 2 {
		add(RoleKeeper, geom.NewVec(ownX+dir*40, center.Y))
		k = n - 1
	}

	for _, line := range outfieldLines(k) {
		depth := 60 + line.depth*(halfWidth-60) // keep a margin off the goal line
		x := ownX + dir*depth
		for i := 0; i < line.count; i++ {
			// Even Y-spread: (i+1)/(count+1) places count players inside the pitch height
			// with equal gaps, so a line never sits on a touchline.
			y := f.Min.Y + f.Height()*float64(i+1)/float64(line.count+1)
			add(line.role, geom.NewVec(x, y))
		}
	}
	return players
}
