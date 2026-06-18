package sim

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// soloRecorded runs the deterministic solo-scoring scenario from replay_test.go with
// recording on, and returns the finished match.
func soloRecorded() *Match {
	field := NewFieldFromGeometry(config.Default().Geometry)
	m := BuildSolo(field)
	m.EnableRecording()
	m.Players[0].Position = geom.NewVec(field.Max.X-180, field.CenterSpot.Y)
	m.Players[0].Facing = geom.NewVec(1, 0)
	m.Ball.Position = geom.NewVec(field.Max.X-150, field.CenterSpot.Y)
	for tick := 0; tick < 1200; tick++ {
		m.Step(soloInputs(m, uint64(tick)), 1.0/60.0)
	}
	return m
}

// TestRecorderDeterministic asserts the recorder is fully deterministic: the same seeded,
// scripted match recorded twice yields DeepEqual stats and an identical event log.
func TestRecorderDeterministic(t *testing.T) {
	a := soloRecorded().Stats()
	b := soloRecorded().Stats()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("recorder is not deterministic across identical runs")
	}
	if len(a.Events) == 0 {
		t.Fatal("expected a non-empty event log")
	}
}

// TestRecorderSoloScoring cross-checks the recorder against the authoritative score: the lone
// left-team player's recorded Goals and the left team's TeamStat.Goals must equal the match
// score, and the scenario must produce shots, touches, distance, and possession time.
func TestRecorderSoloScoring(t *testing.T) {
	m := soloRecorded()
	st := m.Stats()
	if len(st.Players) != 1 {
		t.Fatalf("expected 1 player stat, got %d", len(st.Players))
	}
	p := st.Players[0]
	leftScore := m.Teams[0].Score
	if leftScore == 0 {
		t.Fatal("scenario scored no goals; nothing to validate")
	}
	if p.Goals != leftScore {
		t.Errorf("player Goals=%d, want match score %d", p.Goals, leftScore)
	}
	if st.Teams[0].Goals != leftScore {
		t.Errorf("left TeamStat.Goals=%d, want %d", st.Teams[0].Goals, leftScore)
	}
	if p.Shots == 0 {
		t.Error("expected the goal-directed push jabs to record shots")
	}
	if p.DistanceCovered <= 0 {
		t.Error("expected non-zero distance covered")
	}
	if p.PossessionSeconds <= 0 {
		t.Error("expected non-zero possession time for the lone in-reach player")
	}
}

// customMatch builds a controlled match from explicit players (no formation), applies the
// default config, and enables recording.
func customMatch(left, right []*Player) *Match {
	lt := &Team{Side: SideLeft, Name: "Blue"}
	rt := &Team{Side: SideRight, Name: "Red"}
	m := &Match{Field: NewStandardField(), Teams: [2]*Team{lt, rt}, Ball: NewBall(geom.NewVec(0, 0), 7.5)}
	lt.Players, rt.Players = left, right
	m.Players = append(append([]*Player{}, left...), right...)
	m.applyConfig(config.Default())
	m.EnableRecording()
	return m
}

// step pushes the named player and idles the rest for one tick.
func pushBy(m *Match, id int) {
	m.Step(map[int]Intent{id: {Push: true, Aim: geom.NewVec(1, 0)}}, 1.0/60.0)
}

func idle(m *Match, n int) {
	for i := 0; i < n; i++ {
		m.Step(nil, 1.0/60.0)
	}
}

// TestRecorderCompletedPass: a left player jabs the ball to a left team-mate just below it.
// The kicker is credited a completed (sideways) pass; the receiver records a touch.
func TestRecorderCompletedPass(t *testing.T) {
	lt := &Team{Side: SideLeft}
	a := NewPlayer(0, geom.NewVec(500, 300), DefaultPlayerTuning(500), lt)
	b := NewPlayer(1, geom.NewVec(500, 345), DefaultPlayerTuning(500), lt)
	m := customMatch([]*Player{a, b}, nil)
	m.Ball.Position = geom.NewVec(500, 315)

	pushBy(m, 0) // A jabs the ball downward toward B
	idle(m, 30)  // let B receive it

	st := m.Stats()
	pa := statFor(st, 0)
	pb := statFor(st, 1)
	if pa.PassesCompleted != 1 {
		t.Fatalf("passer PassesCompleted=%d, want 1 (events: %v)", pa.PassesCompleted, kinds(st.Events))
	}
	if pa.PassesSideways != 1 {
		t.Errorf("passer PassesSideways=%d, want 1", pa.PassesSideways)
	}
	if pb.Touches == 0 {
		t.Errorf("receiver recorded no touch")
	}
	if st.Teams[0].PassesCompleted != 1 || st.Teams[0].Passes != 1 {
		t.Errorf("left team Passes=%d PassesCompleted=%d, want 1/1", st.Teams[0].Passes, st.Teams[0].PassesCompleted)
	}
}

// TestRecorderInterception: a left player jabs the ball to a right-team player, who is
// credited an interception and the passer an attempted (not completed) pass.
func TestRecorderInterception(t *testing.T) {
	lt := &Team{Side: SideLeft}
	rt := &Team{Side: SideRight}
	a := NewPlayer(0, geom.NewVec(500, 300), DefaultPlayerTuning(500), lt)
	c := NewPlayer(1, geom.NewVec(500, 345), DefaultPlayerTuning(500), rt)
	m := customMatch([]*Player{a}, []*Player{c})
	m.Ball.Position = geom.NewVec(500, 315)

	pushBy(m, 0)
	idle(m, 30)

	st := m.Stats()
	pa := statFor(st, 0)
	pc := statFor(st, 1)
	if pa.PassesAttempted != 1 || pa.PassesCompleted != 0 {
		t.Errorf("passer Attempted=%d Completed=%d, want 1/0 (events: %v)", pa.PassesAttempted, pa.PassesCompleted, kinds(st.Events))
	}
	if pc.Interceptions != 1 {
		t.Errorf("interceptor Interceptions=%d, want 1", pc.Interceptions)
	}
}

// TestRecorderJSONRoundTrip: a recorded match writes valid JSON that unmarshals to an equal
// MatchRecord.
func TestRecorderJSONRoundTrip(t *testing.T) {
	m := soloRecorded()
	rec := m.Recorder().MatchRecord(m)
	var buf bytes.Buffer
	if err := rec.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got MatchRecord
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(rec, got) {
		t.Error("MatchRecord did not round-trip through JSON")
	}
	if got.Seed != m.Seed || got.Schema != recordSchema {
		t.Errorf("round-tripped seed/schema = %d/%d, want %d/%d", got.Seed, got.Schema, m.Seed, recordSchema)
	}
}

// TestRecorderSave: a left player jabs an on-target shot at the right goal; the right keeper,
// standing in its goal area in the ball's path, touches it before it crosses -> a save.
func TestRecorderSave(t *testing.T) {
	lt := &Team{Side: SideLeft}
	rt := &Team{Side: SideRight}
	shooter := NewPlayer(0, geom.NewVec(880, 340), DefaultPlayerTuning(500), lt)
	keeper := NewPlayer(1, geom.NewVec(925, 340), DefaultPlayerTuning(500), rt)
	keeper.Role = RoleGoalkeeper
	m := customMatch([]*Player{shooter}, []*Player{keeper})
	m.Ball.Position = geom.NewVec(900, 340) // between shooter and the right goal mouth

	pushBy(m, 0) // on-target jab at the goal centre
	idle(m, 12)  // the keeper, in the path, stops it

	st := m.Stats()
	pk := statFor(st, 1)
	if pk.Saves != 1 {
		t.Fatalf("keeper Saves=%d, want 1 (events: %v)", pk.Saves, kinds(st.Events))
	}
	if st.Teams[1].Saves != 1 {
		t.Errorf("right TeamStat.Saves=%d, want 1", st.Teams[1].Saves)
	}
	if m.Teams[0].Score != 0 {
		t.Errorf("shot should have been saved, but a goal was scored (left score=%d)", m.Teams[0].Score)
	}
}

func statFor(st MatchStats, id int) PlayerStat {
	for _, p := range st.Players {
		if p.PlayerID == id {
			return p
		}
	}
	return PlayerStat{PlayerID: -1}
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}
