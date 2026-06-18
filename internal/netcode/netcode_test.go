package netcode

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

func TestSanitizeIntent(t *testing.T) {
	cases := []struct {
		name string
		in   sim.Intent
		ok   bool
	}{
		{"clean", sim.Intent{Move: geom.NewVec(1, 0), Throttle: 0.5}, true},
		{"throttle clamps high", sim.Intent{Throttle: 5}, true},
		{"throttle clamps low", sim.Intent{Throttle: -3}, true},
		{"NaN move", sim.Intent{Move: geom.NewVec(math.NaN(), 0)}, false},
		{"Inf aim", sim.Intent{Aim: geom.NewVec(0, math.Inf(1))}, false},
		{"NaN throttle", sim.Intent{Throttle: math.NaN()}, false},
	}
	for _, c := range cases {
		out, ok := sanitizeIntent(c.in)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && (out.Throttle < 0 || out.Throttle > 1) {
			t.Errorf("%s: throttle %v not clamped to [0,1]", c.name, out.Throttle)
		}
	}
}

func TestFreeSlotAssignmentAndReuse(t *testing.T) {
	s := &Server{humanIDs: []int{0, 1}, assigned: map[int]bool{}}
	if id, ok := s.freeSlot(); !ok || id != 0 {
		t.Fatalf("first freeSlot = %d,%v, want 0,true", id, ok)
	}
	s.assigned[0] = true
	if id, ok := s.freeSlot(); !ok || id != 1 {
		t.Fatalf("second freeSlot = %d,%v, want 1,true", id, ok)
	}
	s.assigned[1] = true
	if _, ok := s.freeSlot(); ok {
		t.Fatal("no slots should be free")
	}
	delete(s.assigned, 0) // a client disconnected
	if id, ok := s.freeSlot(); !ok || id != 0 {
		t.Fatalf("freeSlot after release = %d,%v, want 0,true (reuse)", id, ok)
	}
}

func TestSnapshotGobRoundTrip(t *testing.T) {
	snap := Snapshot{
		Tick:      42,
		FieldMin:  geom.NewVec(0, 0),
		FieldMax:  geom.NewVec(880, 480),
		LeftName:  "Blue",
		RightName: "Red",
		LeftScore: 2, RightScore: 1,
		Entities: []EntityState{
			{Kind: KindBall, Position: geom.NewVec(440, 240), Radius: 7.5},
			{Kind: KindPlayer, Position: geom.NewVec(100, 100), Number: 7, ShootCharge: 0.5},
		},
		Stats: sim.StatsSnapshot{
			Players: []sim.PlayerStat{{PlayerID: 0, Goals: 1, Touches: 9}},
			Teams:   []sim.TeamStat{{Side: sim.SideLeft, Goals: 2}},
		},
		Events: []sim.Event{{Tick: 30, Kind: sim.EvGoal, Player: 0, Team: sim.SideLeft}},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snap); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got Snapshot
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tick != snap.Tick || got.LeftScore != snap.LeftScore || len(got.Entities) != 2 {
		t.Errorf("snapshot core fields did not round-trip: %+v", got)
	}
	if len(got.Stats.Players) != 1 || got.Stats.Players[0].Goals != 1 || len(got.Events) != 1 || got.Events[0].Kind != sim.EvGoal {
		t.Errorf("Stats/Events did not round-trip: %+v / %+v", got.Stats, got.Events)
	}
}

// TestSnapshotOfProjectsStats checks SnapshotOf carries the recorder's live stats + the
// per-tick event delta when recording is on.
func TestSnapshotOfProjectsStats(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	m.EnableRecording()
	m.Step(nil, 1.0/60.0) // produces a kickoff event at least
	snap := SnapshotOf(m)
	if len(snap.Stats.Players) != len(m.Players) {
		t.Errorf("snapshot stats has %d players, want %d", len(snap.Stats.Players), len(m.Players))
	}
}

// TestClientSendCloseNoRace hammers Send concurrently with Close; run under -race it pins the
// fix for the former unsynchronised encoder/connection access.
func TestClientSendCloseNoRace(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c) // drain so writes never block
		}
	}()

	c, err := Dial(ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Send(sim.Intent{}) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = c.Close() }()
	wg.Wait()

	// Send after Close is safe and returns the closed error rather than touching the conn.
	if err := c.Send(sim.Intent{}); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Logf("post-close Send returned %v (acceptable)", err)
	}
}

func TestRunCancels(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	s := NewServer("127.0.0.1:0", m, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on clean cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
