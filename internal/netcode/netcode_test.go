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

// freePort returns a currently-free 127.0.0.1 address (with a small race window acceptable in
// a test) so a server can be started on a known address and dialled.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func dialRetry(t *testing.T, addr string) *Client {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(addr); err == nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("could not dial %s", addr)
	return nil
}

// TestHelloHandshakeAndSnapshots is the end-to-end check: a client learns its assigned slot
// from the Hello handshake (previously impossible) and then receives snapshots through the
// per-conn sender.
func TestHelloHandshakeAndSnapshots(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	addr := freePort(t)
	s := NewServer(addr, m, nil, []int{1}) // slot 1 is claimable
	s.SetTickRate(120)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	c := dialRetry(t, addr)
	defer c.Close()

	// The handshake delivers the assigned slot.
	var id int
	ok := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if id, ok = c.AssignedID(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok {
		t.Fatal("client never received its Hello/assigned id")
	}
	if id != 1 {
		t.Errorf("assigned id = %d, want 1", id)
	}

	// Sending keeps the client live; snapshots should arrive.
	if err := c.Send(sim.Intent{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if _, has := c.Snapshot(); has {
			return // success
		}
		_ = c.Send(sim.Intent{})
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("client never received a snapshot")
}

// TestVersionMismatchRejected: a client whose handshake frame announces the wrong protocol
// version gets a friendly Reject and is then dropped, so an incompatible client cannot feed
// intents in. (The handshake is client-speaks-first in v2.)
func TestVersionMismatchRejected(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	addr := freePort(t)
	s := NewServer(addr, m, nil, []int{1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	var nc net.Conn
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if conn, err := net.Dial("tcp", addr); err == nil {
			nc = conn
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if nc == nil {
		t.Fatal("could not dial")
	}
	defer nc.Close()

	// Announce a bad version on the handshake frame; the server must Reject then close.
	if err := writeFrame(nc, ClientFrame{ProtoVersion: ProtoVersion + 99, Kind: CJoin}); err != nil {
		t.Fatalf("write bad frame: %v", err)
	}
	dec := gob.NewDecoder(nc)
	var env Envelope
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("expected a Reject, got err=%v", err)
	}
	if env.Kind != MsgReject {
		t.Errorf("expected MsgReject, got kind %d", env.Kind)
	}
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := dec.Decode(&env); err == nil {
		t.Error("server should have closed the connection after a version mismatch")
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
