package netcode

import (
	"bytes"
	"context"
	"encoding/gob"
	"net"
	"testing"
	"time"

	"phootball/internal/config"
	"phootball/internal/sim"
)

const testHostToken = "host-secret-token"

// buildTestMatch is a rebuild closure for a lobby-mode server: it builds a match (and claimable
// seats) from a setup. Bots are nil, so unassigned slots simply idle -- fine for protocol tests.
func buildTestMatch(setup config.MatchSetup) (*sim.Match, map[int]Bot, []int) {
	home, away := setup.HomeSize, setup.AwaySize
	if home <= 0 {
		home = setup.TeamSize
	}
	if away <= 0 {
		away = setup.TeamSize
	}
	cfg, _ := setup.Build()
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfigSized(field, home, away, cfg)
	humanIDs := make([]int, 0, 2)
	for _, t := range m.Teams {
		idx := 0
		if len(t.Players) > 1 {
			idx = 1
		}
		humanIDs = append(humanIDs, t.Players[idx].PlayerID)
	}
	return m, nil, humanIDs
}

func startLobby(t *testing.T, setup config.MatchSetup) (addr string, cancel func()) {
	t.Helper()
	addr = freePort(t)
	m, bots, humanIDs := buildTestMatch(setup)
	s := NewServer(addr, m, bots, humanIDs)
	s.SetTickRate(120)
	s.EnableLobby(setup, buildTestMatch)
	s.SetPendingHostToken(testHostToken)
	ctx, c := context.WithCancel(context.Background())
	go s.Run(ctx)
	return addr, c
}

func sizedSetup(home, away int) config.MatchSetup {
	ms := config.DefaultMatchSetup()
	ms.TeamSize, ms.HomeSize, ms.AwaySize = home, home, away
	return ms
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func mustDial(t *testing.T, dial func() (*Client, error)) *Client {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if c, err := dial(); err == nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("could not dial")
	return nil
}

// TestLobbyHostStartFlow: the host claims host via its token and the match is gated until CStart
// (lobby broadcasts before, snapshots after).
func TestLobbyHostStartFlow(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()

	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host flag", func() bool { return host.IsHost() })
	waitFor(t, "lobby broadcast", func() bool { _, ok := host.Lobby(); return ok })
	if _, ok := host.Snapshot(); ok {
		t.Error("no snapshot should arrive before the match starts (lobby gate)")
	}

	guest := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Guest") })
	defer guest.Close()
	waitFor(t, "guest lobby", func() bool { _, ok := guest.Lobby(); return ok })
	if guest.IsHost() {
		t.Error("a fresh guest must not be host")
	}

	if err := host.StartMatch(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "snapshots after start", func() bool { _, ok := host.Snapshot(); return ok })
	waitFor(t, "InMatch", host.InMatch)
}

// TestHostTokenWinsRace: a guest connecting BEFORE the host never becomes host -- authority is the
// token, not connection order.
func TestHostTokenWinsRace(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()

	guest := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Early") })
	defer guest.Close()
	waitFor(t, "guest lobby", func() bool { _, ok := guest.Lobby(); return ok })

	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host flag", func() bool { return host.IsHost() })

	// Give the guest a few lobby updates; it must still not be host.
	time.Sleep(50 * time.Millisecond)
	if guest.IsHost() {
		t.Error("an early guest must not win host over the token holder")
	}
}

// TestPickSlotConflictAndSeatID: two guests racing the same seat -- exactly one gets it, and the
// winner's AssignedID matches the seat's roster PlayerID.
func TestPickSlotConflictAndSeatID(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(3, 3))
	defer cancel()

	g1 := mustDial(t, func() (*Client, error) { return DialJoin(addr, "G1") })
	defer g1.Close()
	g2 := mustDial(t, func() (*Client, error) { return DialJoin(addr, "G2") })
	defer g2.Close()
	waitFor(t, "both in lobby", func() bool {
		_, a := g1.Lobby()
		_, b := g2.Lobby()
		return a && b
	})

	// The home slot-2 seat's PlayerID, from the roster the lobby reports.
	lob, _ := g1.Lobby()
	var wantPID int
	for _, s := range lob.Seats {
		if s.Team == 0 && s.Slot == 2 {
			wantPID = s.PlayerID
		}
	}
	g1.PickSlot(0, 2)
	g2.PickSlot(0, 2)
	time.Sleep(80 * time.Millisecond)

	id1, _ := g1.AssignedID()
	id2, _ := g2.AssignedID()
	got := 0
	if id1 == wantPID {
		got++
	}
	if id2 == wantPID {
		got++
	}
	if got != 1 {
		t.Fatalf("exactly one client should hold seat %d; g1=%d g2=%d", wantPID, id1, id2)
	}
}

// TestReconnectReservation: a dropped human's seat is reserved (not handed out) and reclaimed by the
// session token within the grace window, restoring the same PlayerID.
func TestReconnectReservation(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()

	guest := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Guest") })
	waitFor(t, "guest lobby", func() bool { _, ok := guest.Lobby(); return ok })
	guest.PickSlot(0, 1)
	waitFor(t, "seated", func() bool { id, _ := guest.AssignedID(); return id >= 0 })
	pid, _ := guest.AssignedID()
	token := guest.SessionToken()
	guest.Close()

	// A fresh joiner must NOT be handed the reserved seat.
	other := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Other") })
	defer other.Close()
	other.PickSlot(0, 1)
	time.Sleep(60 * time.Millisecond)
	if id, _ := other.AssignedID(); id == pid {
		t.Errorf("a reserved seat (%d) must not be handed to a new joiner", pid)
	}

	// The original reconnects with its token and reclaims the exact seat.
	back := mustDial(t, func() (*Client, error) { return DialResume(addr, "Guest", token) })
	defer back.Close()
	waitFor(t, "reclaimed seat", func() bool { id, ok := back.AssignedID(); return ok && id == pid })
}

// TestPingPongRTT: a CPing is answered with a Pong, yielding a measured RTT.
func TestPingPongRTT(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()
	c := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Pinger") })
	defer c.Close()
	waitFor(t, "lobby", func() bool { _, ok := c.Lobby(); return ok })
	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	waitFor(t, "rtt measured", func() bool { return c.RTTms() >= 0 && pongSeen(c) })
}

func pongSeen(c *Client) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pingSent) == 0 // the pong cleared the outstanding ping
}

// TestConfigShrinkEvictsSeat: a host CConfig that shrinks a team evicts a seated player whose slot
// no longer exists to spectator (and clears ready).
func TestConfigShrinkEvictsSeat(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(3, 3))
	defer cancel()

	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host", host.IsHost)

	guest := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Guest") })
	defer guest.Close()
	waitFor(t, "guest lobby", func() bool { _, ok := guest.Lobby(); return ok })
	guest.PickSlot(0, 2) // home slot 3 (index 2)
	waitFor(t, "seated at slot 2", func() bool { id, ok := guest.AssignedID(); return ok && id >= 0 })

	// Shrink home to 2 players (slots 0,1): slot-2's player vanishes.
	host.SendConfig(sizedSetup(2, 3))
	waitFor(t, "evicted to spectator", func() bool { id, ok := guest.AssignedID(); return ok && id == spectatorID })
}

// TestOversizedFrameRejected: a client frame larger than the cap is refused (the connection drops)
// rather than allocated.
func TestOversizedFrameRejected(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()

	var nc net.Conn
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
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
	// A length prefix beyond the cap, with no body: the server must reject without reading it all.
	hdr := []byte{0xff, 0xff, 0xff, 0xff} // ~4GiB declared
	if _, err := nc.Write(hdr); err != nil {
		t.Fatalf("write: %v", err)
	}
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := nc.Read(buf); err == nil {
		t.Error("server should have dropped the connection on an oversized frame")
	}
}

// TestPauseGateFreezesSim: a host CPause freezes the simulation (m.Tick stops) while broadcasts
// continue and the Paused flag is set; CPause(false) resumes it.
func TestPauseGateFreezesSim(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()
	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host", host.IsHost)
	host.StartMatch()
	waitFor(t, "snapshots", func() bool { _, ok := host.Snapshot(); return ok })

	s1, _ := host.Snapshot()
	time.Sleep(80 * time.Millisecond)
	s2, _ := host.Snapshot()
	if s2.Tick <= s1.Tick {
		t.Fatalf("sim tick should advance while running: %d -> %d", s1.Tick, s2.Tick)
	}

	host.SetPaused(true)
	waitFor(t, "paused flag", func() bool { s, _ := host.Snapshot(); return s.Paused })
	pa, _ := host.Snapshot()
	time.Sleep(100 * time.Millisecond)
	pb, _ := host.Snapshot()
	if pb.Tick != pa.Tick {
		t.Errorf("sim tick must be frozen while paused: %d -> %d", pa.Tick, pb.Tick)
	}
	if down, _ := host.ConnState(); down {
		t.Error("broadcasts should continue while paused (connection must stay up)")
	}

	host.SetPaused(false)
	waitFor(t, "resumed flag", func() bool { s, _ := host.Snapshot(); return !s.Paused })
	ra, _ := host.Snapshot()
	time.Sleep(80 * time.Millisecond)
	rb, _ := host.Snapshot()
	if rb.Tick <= ra.Tick {
		t.Errorf("sim tick should advance after resume: %d -> %d", ra.Tick, rb.Tick)
	}
}

// TestReturnToLobbyMidMatch: a host CReturnLobby from a running match sends everyone back to the
// lobby (no disconnect) with seats preserved.
func TestReturnToLobbyMidMatch(t *testing.T) {
	addr, cancel := startLobby(t, sizedSetup(2, 2))
	defer cancel()
	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host", host.IsHost)
	guest := mustDial(t, func() (*Client, error) { return DialJoin(addr, "Guest") })
	defer guest.Close()
	waitFor(t, "guest lobby", func() bool { _, ok := guest.Lobby(); return ok })
	guest.PickSlot(0, 0) // home keeper (the host auto-took the home outfielder slot)
	waitFor(t, "seated", func() bool { id, _ := guest.AssignedID(); return id >= 0 })
	pid, _ := guest.AssignedID()

	host.StartMatch()
	waitFor(t, "host in match", host.InMatch)
	waitFor(t, "guest in match", guest.InMatch)

	host.ReturnToLobby()
	waitFor(t, "host back in lobby", host.InLobby)
	waitFor(t, "guest back in lobby", guest.InLobby)

	// Connections survive and the guest keeps its seat.
	if down, _ := guest.ConnState(); down {
		t.Error("return-to-lobby must not disconnect the guest")
	}
	if id, _ := guest.AssignedID(); id != pid {
		t.Errorf("guest seat should survive return-to-lobby: got %d, want %d", id, pid)
	}
}

type panicBot struct{}

func (panicBot) Intent(sim.View) sim.Intent { panic("intentional test panic") }

// TestTickRecoversFromBotPanic: a panicking AI bot in the tick loop is recovered and ends the match
// cleanly (the host connection drops) instead of crashing the whole host process.
func TestTickRecoversFromBotPanic(t *testing.T) {
	setup := sizedSetup(2, 2)
	addr := freePort(t)
	m, _, humanIDs := buildTestMatch(setup)
	bots := map[int]Bot{}
	for _, tm := range m.Teams {
		for _, p := range tm.Players {
			bots[p.PlayerID] = panicBot{}
		}
	}
	s := NewServer(addr, m, bots, humanIDs)
	s.SetTickRate(120)
	s.EnableLobby(setup, buildTestMatch)
	s.SetPendingHostToken(testHostToken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	host := mustDial(t, func() (*Client, error) { return DialHost(addr, "Host", testHostToken) })
	defer host.Close()
	waitFor(t, "host", host.IsHost)
	host.StartMatch() // the tick loop now calls a panicking bot for the unassigned slots

	// If the panic were not recovered, this test binary would crash. Instead the match ends and the
	// host's connection drops.
	waitFor(t, "conn drops after recovered sim panic", func() bool { down, _ := host.ConnState(); return down })
}

// TestBindSurfacesAddrInUse: Bind on an already-bound address errors immediately (so the host can
// show a clear message) instead of the failure being swallowed inside a `go Run` goroutine.
func TestBindSurfacesAddrInUse(t *testing.T) {
	addr := freePort(t)
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	s1 := NewServer(addr, m, nil, nil)
	if err := s1.Bind(); err != nil {
		t.Fatalf("first Bind should succeed: %v", err)
	}
	defer s1.listener.Close()

	s2 := NewServer(addr, m, nil, nil)
	if err := s2.Bind(); err == nil {
		t.Error("Bind on an in-use address must return an error, not succeed silently")
	}
}

// TestRunUsesPreBoundListener: after a synchronous Bind, Addr() is resolved and Run serves on the
// pre-bound listener (no listen-vs-dial race).
func TestRunUsesPreBoundListener(t *testing.T) {
	addr := freePort(t)
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 2, config.Default())
	s := NewServer(addr, m, nil, []int{1})
	if err := s.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	resolved := s.Addr()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	c := mustDial(t, func() (*Client, error) { return Dial(resolved) })
	defer c.Close()
	c.Send(sim.Intent{})
	waitFor(t, "snapshot via pre-bound listener", func() bool { _, ok := c.Snapshot(); return ok })
}

// TestControlFrameGobRoundTrip locks the wire shape of the new message types.
func TestControlFrameGobRoundTrip(t *testing.T) {
	frame := ClientFrame{ProtoVersion: ProtoVersion, Kind: CPickSlot, Team: 1, Slot: 3, Name: "Ann", Ready: true, Seq: 9}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(frame); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	var got ClientFrame
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if got != frame {
		t.Errorf("ClientFrame round-trip mismatch: %+v vs %+v", got, frame)
	}

	env := Envelope{Kind: MsgLobby, Lobby: &LobbyState{
		Phase: 0, HostName: "Host", HomeSize: 2, AwaySize: 2, AllReady: true, YouPlayerID: 3,
		Seats: []SeatInfo{{Team: 0, Slot: 0, PlayerID: 0, Role: "GK", OccupantName: "Host", IsHuman: true, Ready: true}},
	}}
	buf.Reset()
	if err := gob.NewEncoder(&buf).Encode(env); err != nil {
		t.Fatalf("encode lobby env: %v", err)
	}
	var gotEnv Envelope
	if err := gob.NewDecoder(&buf).Decode(&gotEnv); err != nil {
		t.Fatalf("decode lobby env: %v", err)
	}
	if gotEnv.Lobby == nil || len(gotEnv.Lobby.Seats) != 1 || gotEnv.Lobby.Seats[0].PlayerID != 0 || gotEnv.Lobby.YouPlayerID != 3 {
		t.Errorf("LobbyState did not round-trip: %+v", gotEnv.Lobby)
	}
}
