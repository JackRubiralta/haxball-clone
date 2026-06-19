package sim

import (
	"math"

	"phootball/internal/geom"
)

// This file is the OPT-IN, WRITE-ONLY match recorder. It folds a chronological event log
// and per-player / per-team aggregates out of the existing authoritative sinks (recordTouch,
// the kick/shoot loop, the collision resolver, resolveGoal, the per-tick sampler, the kickoff
// reset, and the penalty taker), with ZERO effect on the simulation: every hook is a method
// on *Recorder that early-returns on a nil receiver, and Match holds the recorder behind a
// nil pointer that is only set by EnableRecording. With recording off the hooks are no-ops,
// so a disabled match is byte-identical to one with no recorder at all.
//
// It is deliberately NOT reachable through sim.View -- a controller (and therefore the AI)
// can never read aggregated match statistics, which a human cannot see either. The only way
// in is Match.Recorder()/Match.Stats(), used by the front end and netcode.

// EventKind classifies a play-by-play event.
type EventKind int

const (
	EvTouch EventKind = iota
	EvPass
	EvPassIncomplete
	EvInterception
	EvTackle
	EvShot
	EvShotOnTarget
	EvSave
	EvGoal
	EvOwnGoal
	EvClearance
	EvOutOfPlay
	EvKickoff
	EvPenaltyKick
)

// PassDir classifies a completed pass relative to the passing team's attack direction.
type PassDir int

const (
	PassForward PassDir = iota
	PassSideways
	PassBackward
)

// Event flag bits (in Event.Flags).
const (
	flagPenaltyScored uint8 = 1 << iota
	flagDeflected
)

// Event is one entry in the chronological play-by-play log.
type Event struct {
	Tick    uint64    `json:"tick"`
	Time    float64   `json:"time"`
	Kind    EventKind `json:"kind"`
	Player  int       `json:"player"`          // primary actor's PlayerID (-1 if none)
	Team    Side      `json:"team"`            // the actor's team
	Target  int       `json:"target"`          // recipient/victim PlayerID, -1 if none
	Pos     geom.Vec  `json:"pos"`             // ball position at the event
	BallVel geom.Vec  `json:"ballVel"`         // ball velocity at the event
	Dir     PassDir   `json:"dir,omitempty"`   // pass direction (EvPass only)
	Power   float64   `json:"power,omitempty"` // ball speed at a kick
	Flags   uint8     `json:"flags,omitempty"` // penalty scored / woodwork
}

// PlayerStat is one player's aggregated match line.
type PlayerStat struct {
	PlayerID int  `json:"playerID"`
	Number   int  `json:"number"`
	Role     Role `json:"role"`
	Side     Side `json:"side"`

	Touches         int `json:"touches"`
	PassesAttempted int `json:"passesAttempted"` // passes that were intercepted (failed)
	PassesCompleted int `json:"passesCompleted"`
	PassesForward   int `json:"passesForward"`
	PassesSideways  int `json:"passesSideways"`
	PassesBackward  int `json:"passesBackward"`
	KeyPasses       int `json:"keyPasses"` // a completed pass whose receiver then shot
	Assists         int `json:"assists"`
	Interceptions   int `json:"interceptions"`
	Tackles         int `json:"tackles"`
	PossessionWins  int `json:"possessionWins"`
	Saves           int `json:"saves"`
	Shots           int `json:"shots"`
	ShotsOnTarget   int `json:"shotsOnTarget"`
	Goals           int `json:"goals"`
	OwnGoals        int `json:"ownGoals"`
	Clearances      int `json:"clearances"`

	PossessionSeconds float64    `json:"possessionSeconds"`
	DistanceCovered   float64    `json:"distanceCovered"`
	ThirdSeconds      [3]float64 `json:"thirdSeconds"` // time this player spent in [own, mid, attacking] third
}

// TeamStat is one team's aggregated match line.
type TeamStat struct {
	Side            Side   `json:"side"`
	Name            string `json:"name"`
	Goals           int    `json:"goals"`
	Shots           int    `json:"shots"`
	ShotsOnTarget   int    `json:"shotsOnTarget"`
	Passes          int    `json:"passes"` // completed + intercepted (total attempts)
	PassesCompleted int    `json:"passesCompleted"`
	Interceptions   int    `json:"interceptions"`
	Saves           int    `json:"saves"`

	PossessionSeconds float64    `json:"possessionSeconds"`
	ThirdSeconds      [3]float64 `json:"thirdSeconds"` // time the ball spent in [own, mid, attacking] third
}

// PossessionPct returns this team's share of the given total possession time, as a 0..100
// percentage; 0 when total is non-positive.
func (t TeamStat) PossessionPct(total float64) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * t.PossessionSeconds / total
}

// MatchStats is a deep, stable-ordered snapshot of the recorder's state.
type MatchStats struct {
	Players []PlayerStat `json:"players"`
	Teams   []TeamStat   `json:"teams"`
	Events  []Event      `json:"events"`
}

// kickInfo remembers the most recent kick, so the receiving touch can be classified as a
// pass (and in which direction) and a defender's touch as a save.
type kickInfo struct {
	id         int      // kicker PlayerID (-1 = none)
	side       Side     // kicker's team
	pos        geom.Vec // ball position at the kick (the pass "from" point)
	tick       uint64
	wasShot    bool // the kick was goal-directed
	onTarget   bool // ...and on target (ray crosses the mouth)
	targetGoal Side // the side whose goal the shot was aimed at (the defending team)
}

// keyPass remembers the most recent completed pass so a subsequent shot by the receiver can
// retro-credit the passer with a key pass.
type keyPass struct {
	passer   int
	receiver int
}

// Recorder accumulates the event log and aggregates. All mutators are methods on *Recorder
// and nil-safe, so a disabled match (m.rec == nil) costs nothing at the call sites.
type Recorder struct {
	Events  []Event
	Players map[int]*PlayerStat
	Teams   map[Side]*TeamStat

	lastKick kickInfo
	keyCand  keyPass
	prevPos  map[int]geom.Vec
	drainIdx int

	playerOrder []int
	teamOrder   []Side
}

// NewRecorder pre-seeds a stat line for every roster slot in m's stable order, so the output
// is deterministic regardless of which players ever touch the ball.
func NewRecorder(m *Match) *Recorder {
	r := &Recorder{
		Players:  make(map[int]*PlayerStat, len(m.Players)),
		Teams:    make(map[Side]*TeamStat, len(m.Teams)),
		prevPos:  make(map[int]geom.Vec, len(m.Players)),
		lastKick: kickInfo{id: -1},
		keyCand:  keyPass{passer: -1, receiver: -1},
	}
	for _, t := range m.Teams {
		r.Teams[t.Side] = &TeamStat{Side: t.Side, Name: t.Name}
		r.teamOrder = append(r.teamOrder, t.Side)
	}
	for _, p := range m.Players {
		r.Players[p.PlayerID] = &PlayerStat{PlayerID: p.PlayerID, Number: p.Number, Role: p.Role, Side: p.Team.Side}
		r.playerOrder = append(r.playerOrder, p.PlayerID)
		r.prevPos[p.PlayerID] = p.Position
	}
	return r
}

func (r *Recorder) player(id int) *PlayerStat {
	if r == nil {
		return nil
	}
	return r.Players[id]
}

func (r *Recorder) team(s Side) *TeamStat {
	if r == nil {
		return nil
	}
	return r.Teams[s]
}

func (r *Recorder) emit(ev Event) {
	if r == nil {
		return
	}
	r.Events = append(r.Events, ev)
}

// DrainNewEvents returns the events appended since the last drain and advances the cursor, so
// netcode can ship only this tick's delta rather than the whole log every frame. It returns a
// COPY: the result must not alias the live Events backing array, because netcode encodes the
// snapshot on a separate goroutine while the next Step appends more events.
func (r *Recorder) DrainNewEvents() []Event {
	if r == nil || r.drainIdx >= len(r.Events) {
		return nil
	}
	out := append([]Event(nil), r.Events[r.drainIdx:]...)
	r.drainIdx = len(r.Events)
	return out
}

// resetDerivation clears the pass/shot/save derivation latches without emitting an event or
// touching distance/possession state. The shootout start uses it (it bypasses resetKickoff),
// so a still-live on-target shot from regulation cannot credit a phantom save in penalties.
func (r *Recorder) resetDerivation() {
	if r == nil {
		return
	}
	r.lastKick = kickInfo{id: -1}
	r.keyCand = keyPass{passer: -1, receiver: -1}
}

// Snapshot returns a deep, stable-ordered copy of the aggregates and the full event log. It
// never leaks the internal maps or pointers.
func (r *Recorder) Snapshot() MatchStats {
	if r == nil {
		return MatchStats{}
	}
	ms := MatchStats{
		Players: make([]PlayerStat, 0, len(r.playerOrder)),
		Teams:   make([]TeamStat, 0, len(r.teamOrder)),
		Events:  append([]Event(nil), r.Events...),
	}
	for _, id := range r.playerOrder {
		ms.Players = append(ms.Players, *r.Players[id])
	}
	for _, s := range r.teamOrder {
		ms.Teams = append(ms.Teams, *r.Teams[s])
	}
	return ms
}

// onTouch is called from recordTouch after the touch history is updated. isNew reports
// whether a genuinely new distinct toucher was appended (vs. a same-player collapse). It
// always checks for a save, then on a new distinct toucher counts the touch and derives a
// pass / interception / tackle from the previous distinct toucher.
func (r *Recorder) onTouch(m *Match, p *Player, kind TouchKind, isNew bool) {
	if r == nil {
		return
	}
	r.checkSave(m, p)
	if !isNew {
		return
	}
	ps := r.player(p.PlayerID)
	if ps != nil {
		ps.Touches++
	}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvTouch, Player: p.PlayerID, Team: p.Team.Side, Target: -1, Pos: m.Ball.Position, BallVel: m.Ball.Velocity})

	// Classify the transition from the previous distinct toucher.
	n := len(m.touchHistory)
	if n < 2 {
		return
	}
	prev := m.touchHistory[n-2]
	switch {
	case prev.Kind == TouchKick && prev.Side == p.Team.Side:
		r.completePass(m, prev.Player, p)
	case prev.Kind == TouchKick && prev.Side != p.Team.Side:
		r.intercept(m, prev.Player, p)
	case prev.Kind == TouchDribble && prev.Side != p.Team.Side:
		r.tackle(m, prev.Player, p)
	}
}

func (r *Recorder) completePass(m *Match, passerID int, receiver *Player) {
	passer := r.player(passerID)
	if passer == nil {
		return
	}
	dir := r.passDirection(passerID, receiver.Position)
	passer.PassesCompleted++
	switch dir {
	case PassForward:
		passer.PassesForward++
	case PassBackward:
		passer.PassesBackward++
	default:
		passer.PassesSideways++
	}
	if t := r.team(passer.Side); t != nil {
		t.PassesCompleted++
		t.Passes++
	}
	r.keyCand = keyPass{passer: passerID, receiver: receiver.PlayerID}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvPass, Player: passerID, Team: passer.Side, Target: receiver.PlayerID, Pos: m.Ball.Position, BallVel: m.Ball.Velocity, Dir: dir})
}

func (r *Recorder) intercept(m *Match, passerID int, interceptor *Player) {
	if passer := r.player(passerID); passer != nil {
		passer.PassesAttempted++
		if t := r.team(passer.Side); t != nil {
			t.Passes++
		}
		r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvPassIncomplete, Player: passerID, Team: passer.Side, Target: interceptor.PlayerID, Pos: m.Ball.Position, BallVel: m.Ball.Velocity})
	}
	if ip := r.player(interceptor.PlayerID); ip != nil {
		ip.Interceptions++
	}
	if t := r.team(interceptor.Team.Side); t != nil {
		t.Interceptions++
	}
	r.keyCand = keyPass{passer: -1, receiver: -1}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvInterception, Player: interceptor.PlayerID, Team: interceptor.Team.Side, Target: passerID, Pos: m.Ball.Position, BallVel: m.Ball.Velocity})
}

func (r *Recorder) tackle(m *Match, victimID int, winner *Player) {
	if wp := r.player(winner.PlayerID); wp != nil {
		wp.Tackles++
		wp.PossessionWins++
	}
	r.keyCand = keyPass{passer: -1, receiver: -1}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvTackle, Player: winner.PlayerID, Team: winner.Team.Side, Target: victimID, Pos: m.Ball.Position, BallVel: m.Ball.Velocity})
}

// passDirection classifies a pass from the last kick's origin to the receiver, relative to
// the passing team's attack axis (+X for SideLeft, -X for SideRight), using a 45-degree cone.
func (r *Recorder) passDirection(passerID int, receiver geom.Vec) PassDir {
	from := receiver // fallback: no recorded origin -> sideways
	if r.lastKick.id == passerID {
		from = r.lastKick.pos
	}
	d := receiver.Sub(from)
	fwd := d.X * attackSign(r.Players[passerID].Side)
	lat := math.Abs(d.Y)
	switch {
	case fwd > lat:
		return PassForward
	case fwd < -lat:
		return PassBackward
	default:
		return PassSideways
	}
}

// onKick is called after a successful shoot()/push() connect (and the penalty-rebound boot).
// It always records the kicker (for pass derivation), counts a goal-directed kick as a shot
// (on/off target by ray-casting the post-kick velocity at the attacking goal mouth), derives a
// clearance, and retro-credits a key pass when the receiver of the last pass is the shooter.
func (r *Recorder) onKick(m *Match, p *Player) {
	if r == nil {
		return
	}
	ballPos, ballVel := m.Ball.Position, m.Ball.Velocity
	speed := geom.Norm(ballVel)
	attackSide := p.Team.Side
	goalLineX := m.Field.Max.X
	if attackSide == SideRight {
		goalLineX = m.Field.Min.X
	}
	top, bot := m.Field.goalMouthRange()
	maxDist := shotReach(speed, m.Ball.Friction)
	shot, onTarget := shotRay(ballPos, ballVel, goalLineX, top, bot, m.Field.Min.Y, m.Field.Max.Y, maxDist)

	ps := r.player(p.PlayerID)
	if shot {
		if ps != nil {
			ps.Shots++
			if onTarget {
				ps.ShotsOnTarget++
			}
		}
		if t := r.team(attackSide); t != nil {
			t.Shots++
			if onTarget {
				t.ShotsOnTarget++
			}
		}
		kind := EvShot
		if onTarget {
			kind = EvShotOnTarget
		}
		r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: kind, Player: p.PlayerID, Team: attackSide, Target: -1, Pos: ballPos, BallVel: ballVel, Power: speed})
		// A key pass: the last completed pass's receiver is now the shooter.
		if r.keyCand.receiver == p.PlayerID {
			if kp := r.player(r.keyCand.passer); kp != nil {
				kp.KeyPasses++
			}
			r.keyCand = keyPass{passer: -1, receiver: -1}
		}
	} else if r.isClearance(m, p, ballVel) {
		if ps != nil {
			ps.Clearances++
		}
		r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvClearance, Player: p.PlayerID, Team: attackSide, Target: -1, Pos: ballPos, BallVel: ballVel, Power: speed})
	}

	r.lastKick = kickInfo{id: p.PlayerID, side: attackSide, pos: ballPos, tick: m.Tick, wasShot: shot, onTarget: onTarget, targetGoal: attackSide.Opponent()}
}

// isClearance reports a defensive clear: a non-shot kick by a player in their own defensive
// third that boots the ball up-field (along their attack axis).
func (r *Recorder) isClearance(m *Match, p *Player, ballVel geom.Vec) bool {
	if positionThird(m.Field, p.Position, p.Team.Side) != 0 {
		return false
	}
	return ballVel.X*attackSign(p.Team.Side) > 0
}

// checkSave records a save when a defending keeper (or any defender inside its own goal-area
// box) touches a shot that is on target and has not yet crossed the line. resolveInteractions
// runs before goal detection, so this naturally fires before the goal would count.
func (r *Recorder) checkSave(m *Match, p *Player) {
	if r == nil || !r.lastKick.wasShot || !r.lastKick.onTarget {
		return
	}
	if p.Team.Side != r.lastKick.targetGoal {
		return // only the defending team can save
	}
	inBox := p.Role == RoleKeeper || m.Field.GoalAreaBox(p.Team.Side).overlapsCircle(p.Position, p.Radius())
	if !inBox {
		return
	}
	if ps := r.player(p.PlayerID); ps != nil {
		ps.Saves++
	}
	if t := r.team(p.Team.Side); t != nil {
		t.Saves++
	}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvSave, Player: p.PlayerID, Team: p.Team.Side, Target: r.lastKick.id, Pos: m.Ball.Position, BallVel: m.Ball.Velocity})
	r.lastKick.wasShot, r.lastKick.onTarget = false, false // the shot is dealt with
}

// onGoal reads resolveGoal's already-computed attribution (the single source of truth shared
// with the HUD) -- it never re-derives the scorer/assist/own-goal/deflection.
func (r *Recorder) onGoal(m *Match, ev ScoreEvent) {
	if r == nil {
		return
	}
	if t := r.team(ev.Team); t != nil {
		t.Goals++
	}
	kind := EvGoal
	if ev.HasScorer {
		if ev.OwnGoal {
			kind = EvOwnGoal
			if ps := r.player(ev.Scorer); ps != nil {
				ps.OwnGoals++
			}
		} else {
			if ps := r.player(ev.Scorer); ps != nil {
				ps.Goals++
			}
			if ev.HasAssist {
				if as := r.player(ev.Assist); as != nil {
					as.Assists++
				}
			}
		}
	}
	flags := uint8(0)
	if ev.Deflected {
		flags |= flagDeflected // a deflected goal
	}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: kind, Player: ev.Scorer, Team: ev.Team, Target: assistOrNone(ev), Pos: m.Ball.Position, BallVel: m.Ball.Velocity, Flags: flags})
	r.lastKick = kickInfo{id: -1}
	r.keyCand = keyPass{passer: -1, receiver: -1}
}

func assistOrNone(ev ScoreEvent) int {
	if ev.HasAssist {
		return ev.Assist
	}
	return -1
}

// sample accumulates the per-tick possession, distance, and time-in-thirds. It reads
// positions only and never mutates the simulation.
//
// Possession is decided by the possession RADIUS, not firm carry: a team "has" the ball when
// at least one of its players has the ball within its possession reach (Player.possessionReach
// via Match.inPullRange). If exactly ONE team is in reach the ball is that team's (credited to
// its nearest in-reach player); if NEITHER is in reach the ball is loose; if BOTH are in reach
// it is contested. Loose and contested ticks count toward no team -- only clean single-team
// reach accrues possession time.
func (r *Recorder) sample(m *Match, dt float64) {
	if r == nil {
		return
	}
	var inReach [3]bool // indexed by Side (SideNone/SideLeft/SideRight)
	var closest [3]*Player
	var closestD [3]float64
	for _, p := range m.Players {
		if !m.inPullRange(p) {
			continue
		}
		s := p.Team.Side
		inReach[s] = true
		if d := geom.Dist(p.Position, m.Ball.Position); closest[s] == nil || d < closestD[s] {
			closest[s], closestD[s] = p, d
		}
	}
	poss := SideNone
	switch {
	case inReach[SideLeft] && !inReach[SideRight]:
		poss = SideLeft
	case inReach[SideRight] && !inReach[SideLeft]:
		poss = SideRight
	}
	if poss != SideNone {
		if t := r.team(poss); t != nil {
			t.PossessionSeconds += dt
		}
		if cp := closest[poss]; cp != nil {
			if ps := r.player(cp.PlayerID); ps != nil {
				ps.PossessionSeconds += dt
			}
		}
	}
	// Ball-position thirds accrue to the (team-relative) third for each team.
	for _, side := range r.teamOrder {
		if t := r.Teams[side]; t != nil {
			t.ThirdSeconds[positionThird(m.Field, m.Ball.Position, side)] += dt
		}
	}
	for _, p := range m.Players {
		ps := r.player(p.PlayerID)
		if ps == nil {
			continue
		}
		// Only accrue distance once we have a previous position for this player; a player with
		// no prior sample (one never seeded at construction) seeds it this tick instead of
		// spiking DistanceCovered by its distance from the origin.
		if prev, ok := r.prevPos[p.PlayerID]; ok {
			ps.DistanceCovered += geom.Dist(p.Position, prev)
		}
		ps.ThirdSeconds[positionThird(m.Field, p.Position, p.Team.Side)] += dt
		r.prevPos[p.PlayerID] = p.Position
	}
}

// onKickoff emits a kickoff marker and resets the pass-derivation latches so nothing is
// attributed across a kickoff. It also resyncs prevPos to the players' reset positions so the
// teleport home is not counted as distance covered.
func (r *Recorder) onKickoff(m *Match) {
	if r == nil {
		return
	}
	r.lastKick = kickInfo{id: -1}
	r.keyCand = keyPass{passer: -1, receiver: -1}
	for _, p := range m.Players {
		r.prevPos[p.PlayerID] = p.Position
	}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvKickoff, Player: -1, Team: m.KickoffSide(), Target: -1, Pos: m.Ball.Position})
}

// onPenaltyKick records a penalty attempt and its outcome (made/missed) for the taker.
func (r *Recorder) onPenaltyKick(m *Match, taker *Player, scored bool) {
	if r == nil || taker == nil {
		return
	}
	if ps := r.player(taker.PlayerID); ps != nil {
		ps.Shots++
		ps.ShotsOnTarget++
		if scored {
			ps.Goals++
		}
	}
	if t := r.team(taker.Team.Side); t != nil {
		t.Shots++
		t.ShotsOnTarget++
		if scored {
			t.Goals++
		}
	}
	flags := uint8(0)
	if scored {
		flags |= flagPenaltyScored
	}
	r.emit(Event{Tick: m.Tick, Time: m.Clock, Kind: EvPenaltyKick, Player: taker.PlayerID, Team: taker.Team.Side, Target: -1, Pos: m.Ball.Position, BallVel: m.Ball.Velocity, Flags: flags})
}

// --- pure geometry helpers (no simulation state mutated) ---

// attackSign is +1 for the left team (attacks toward +X) and -1 for the right team.
func attackSign(s Side) float64 {
	if s == SideRight {
		return -1
	}
	return 1
}

// positionThird returns 0 (own defensive), 1 (middle), or 2 (attacking) third for an x
// coordinate, relative to the given team's attack direction.
func positionThird(f *Field, pos geom.Vec, side Side) int {
	w := f.Width()
	if w <= 0 {
		return 1
	}
	rel := (pos.X - f.Min.X) / w // 0 at the left edge, 1 at the right
	t := 1
	switch {
	case rel < 1.0/3.0:
		t = 0
	case rel >= 2.0/3.0:
		t = 2
	}
	if side == SideRight {
		t = 2 - t // the right team's own third is the right side of the pitch
	}
	return t
}

// shotReach is how far a ball at the given speed travels before friction stops it. The body
// integrates v += v*friction*dt (friction negative), an exponential decay whose total travel
// is speed/|friction|; a straight ray longer than this would over-count shots that drop short.
func shotReach(speed, friction float64) float64 {
	f := math.Abs(friction)
	if f < 1e-6 {
		return math.Inf(1)
	}
	return speed / f
}

// shotRay casts the post-kick velocity ray at the goal line. shot reports the ray reaching the
// line within the field's vertical bounds and within maxDist (a genuine attempt at goal);
// onTarget additionally requires the crossing to fall between the posts.
func shotRay(ballPos, ballVel geom.Vec, goalLineX, mouthTop, mouthBot, fieldMinY, fieldMaxY, maxDist float64) (shot, onTarget bool) {
	dx := goalLineX - ballPos.X
	if ballVel.X == 0 || (dx > 0) != (ballVel.X > 0) {
		return false, false // not heading toward this goal line
	}
	t := dx / ballVel.X // > 0 since the signs match
	yAtLine := ballPos.Y + ballVel.Y*t
	dist := math.Hypot(dx, ballVel.Y*t)
	if dist > maxDist {
		return false, false // decelerates and drops short
	}
	if yAtLine < fieldMinY || yAtLine > fieldMaxY {
		return false, false // sails out of play -> not a goal attempt
	}
	return true, yAtLine >= mouthTop && yAtLine <= mouthBot
}
