package netcode

import (
	"context"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"phootball/internal/config"
	"phootball/internal/sim"
)

// Bot is anything that can produce an Intent from match state -- control.AI
// satisfies it structurally, so netcode need not import the control package.
type Bot interface {
	Intent(view sim.View) sim.Intent
}

// spectatorID marks a connection that holds no seat (a viewer).
const spectatorID = -1

const (
	intentMaxAgeTicks = 30               // ~0.5s at 60Hz: after this, a silent client idles (neutral intent)
	writeTimeout      = 5 * time.Second  // a stuck client cannot block the sender forever
	readTimeout       = 10 * time.Second // a client that stops sending is dropped
	keepAlivePeriod   = 15 * time.Second
	reconnectGrace    = 15 * time.Second // how long a dropped human's seat is held for a reconnect
	defaultSpecCap    = 8                // max spectators (a flood would back-pressure the broadcast)
	maxNameLen        = 24
	ctrlRateBurst     = 20.0 // token-bucket burst for non-intent control messages
	ctrlRatePerSec    = 10.0 // sustained non-intent control rate per connection
)

// Server runs the authoritative simulation. It steps the match at a fixed rate, gathering intents
// from AI bots and from connected remote clients, and broadcasts a snapshot every tick. All
// collisions happen here, never on a client. In lobby mode it also serves a pre-match roster and
// holds the match until the host starts it.
type Server struct {
	addr     string
	listener net.Listener // pre-bound by Bind() (optional); Run binds itself if nil
	tickRate float64
	log      *slog.Logger

	mu       sync.Mutex
	match    *sim.Match
	bots     map[int]Bot           // playerID -> AI controller
	intents  map[int]stampedIntent // latest intent (+ arrival tick) per remote-controlled player
	humanIDs []int                 // player slots auto-assigned to clients (immediate mode / host seat)
	assigned map[int]bool          // which player slots are currently held by a human
	names    map[int]string        // seated playerID -> display name
	ready    map[int]bool          // seated playerID -> ready flag
	conns    map[*conn]struct{}    // post-handshake connections
	tick     uint64                // server tick counter, for intent-staleness checks

	// Lobby / host authority (set via EnableLobby before Run; zero in immediate/CLI mode).
	lobbyMode        bool
	started          bool
	paused           bool
	setup            config.MatchSetup
	rebuildMatch     func(config.MatchSetup) (*sim.Match, map[int]Bot, []int) // injected; keeps netcode free of control/menu
	pendingHostToken string
	hostConn         *conn
	specCap          int

	// Reconnect grace (lobby mode): a dropped human's seat is reserved (left UNASSIGNED so its AI
	// fallback covers it) so a network blip doesn't hand the slot to someone else.
	reservations map[int]reservation // playerID -> hold
}

type reservation struct {
	token   string
	expires time.Time
}

type conn struct {
	nc        net.Conn
	enc       *gob.Encoder
	playerID  int // seat, or spectatorID
	name      string
	isHost    bool
	token     string         // session token issued in Hello (reconnect identity)
	state     chan *Envelope // per-tick broadcast (snapshot/lobby); cap 1, latest-wins
	ctrl      chan *Envelope // reliable control (hello/reject/pong/hostclosed); cap 16
	done      chan struct{}  // closed once when the conn is being torn down
	closeOnce sync.Once

	// Non-intent control rate limit (touched only by this conn's reader goroutine).
	ctrlBucket float64
	ctrlLast   time.Time
}

// pushState hands the newest broadcast to the per-conn sender, dropping any stale frame still
// queued so a slow client always gets the freshest state and never back-pressures the tick.
func (c *conn) pushState(env *Envelope) {
	select {
	case c.state <- env:
	default:
		select { // buffer full: discard the stale frame, then enqueue the fresh one
		case <-c.state:
		default:
		}
		select {
		case c.state <- env:
		default:
		}
	}
}

// pushCtrl queues a reliable control message; returns false if the control backlog is full (the
// caller then tears the conn down rather than silently dropping a handshake/reject).
func (c *conn) pushCtrl(env *Envelope) bool {
	select {
	case c.ctrl <- env:
		return true
	default:
		return false
	}
}

// allowControl rate-limits non-intent control messages with a token bucket, so a client cannot
// flood CPickSlot/CReady and thrash the roster broadcast. Called only from the conn's reader.
func (c *conn) allowControl(now time.Time) bool {
	if c.ctrlLast.IsZero() {
		c.ctrlLast, c.ctrlBucket = now, ctrlRateBurst
	}
	c.ctrlBucket += now.Sub(c.ctrlLast).Seconds() * ctrlRatePerSec
	if c.ctrlBucket > ctrlRateBurst {
		c.ctrlBucket = ctrlRateBurst
	}
	c.ctrlLast = now
	if c.ctrlBucket < 1 {
		return false
	}
	c.ctrlBucket--
	return true
}

// stampedIntent is a client's latest intent plus the server tick it arrived on, so a silent
// client's stale intent can be expired to neutral.
type stampedIntent struct {
	in   sim.Intent
	tick uint64
}

// NewServer creates a server in IMMEDIATE mode: the match runs at once (no lobby), and a fresh
// client is auto-assigned the first free humanIDs slot (every player also has an AI fallback that
// runs until a client claims its slot). The menu host calls EnableLobby to opt into the lobby.
func NewServer(addr string, match *sim.Match, bots map[int]Bot, humanIDs []int) *Server {
	return &Server{
		addr:         addr,
		tickRate:     60,
		log:          slog.Default(),
		match:        match,
		bots:         bots,
		intents:      make(map[int]stampedIntent),
		humanIDs:     humanIDs,
		assigned:     make(map[int]bool),
		names:        make(map[int]string),
		ready:        make(map[int]bool),
		conns:        make(map[*conn]struct{}),
		reservations: make(map[int]reservation),
		started:      true,
		specCap:      defaultSpecCap,
	}
}

// EnableLobby puts the server in lobby mode: the match does not Step until the host sends CStart,
// fresh guests land as spectators and pick seats, and a host CConfig rebuilds the match via
// rebuild. Call before Run. rebuild produces a fresh (match, bots, humanIDs) from a config, and is
// the seam that keeps netcode free of the control/menu packages.
func (s *Server) EnableLobby(setup config.MatchSetup, rebuild func(config.MatchSetup) (*sim.Match, map[int]Bot, []int)) {
	s.lobbyMode = true
	s.started = false
	s.setup = setup
	s.rebuildMatch = rebuild
}

// SetPendingHostToken registers the token the host connection must present (via CHostToken) to
// claim host authority. Because the host dials its own listener and races external clients, host
// is decided by this token, not by connection order. Call before Run.
func (s *Server) SetPendingHostToken(tok string) { s.pendingHostToken = tok }

// SetSpectatorCap bounds the number of seat-less viewers. Call before Run.
func (s *Server) SetSpectatorCap(n int) {
	if n >= 0 {
		s.specCap = n
	}
}

// SetLogger replaces the server's logger. A nil logger is ignored.
func (s *Server) SetLogger(l *slog.Logger) {
	if l != nil {
		s.log = l
	}
}

// SetTickRate sets the simulation rate in ticks per second. It must be called before Run.
func (s *Server) SetTickRate(r float64) {
	if r < 1 {
		r = 1
	} else if r > 240 {
		r = 240
	}
	s.tickRate = r
}

// freeSlot returns the first unassigned, unreserved humanIDs slot, or (-1, false). Holds s.mu.
func (s *Server) freeSlot() (int, bool) {
	for _, id := range s.humanIDs {
		if s.assigned[id] {
			continue
		}
		if _, held := s.reservations[id]; held {
			continue
		}
		return id, true
	}
	return -1, false
}

// Run listens for clients and steps the simulation until the context is cancelled or a fatal
// listen error occurs. A cancelled context is a clean shutdown (returns nil). Single-shot.
func (s *Server) Run(ctx context.Context) error {
	listener := s.listener
	if listener == nil {
		var err error
		if listener, err = net.Listen("tcp", s.addr); err != nil {
			return err
		}
	}
	defer listener.Close()
	s.log.Info("listening", "addr", s.addr, "tickRate", s.tickRate, "lobby", s.lobbyMode)

	go s.acceptLoop(listener)
	go func() {
		<-ctx.Done()
		listener.Close() // unblock Accept so acceptLoop returns
	}()
	s.tickLoop(ctx)

	// Shutdown: tear down every remaining client so its reader/sender goroutines exit.
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		s.dropConn(c)
	}
	s.log.Info("shutting down")
	return nil
}

// Bind reserves the listening socket up front so a bind error (e.g. the port is already in use)
// surfaces IMMEDIATELY to the caller instead of being swallowed inside a `go Run` goroutine, and so
// the loopback host can dial without racing an async listen. After Bind, Addr() returns the resolved
// address (e.g. the concrete port when ":0" was requested). Run binds itself if Bind was not called,
// so the CLI server is unaffected. Call before Run.
func (s *Server) Bind() error {
	if s.listener != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.addr = ln.Addr().String()
	return nil
}

// Addr returns the address the server listens on -- the resolved host:port once Bind has run.
func (s *Server) Addr() string { return s.addr }

func (s *Server) acceptLoop(listener net.Listener) {
	for {
		nc, err := listener.Accept()
		if err != nil {
			return
		}
		if tcp, ok := nc.(*net.TCPConn); ok {
			tcp.SetKeepAlive(true)
			tcp.SetKeepAlivePeriod(keepAlivePeriod)
			tcp.SetNoDelay(true) // input latency matters: never coalesce per-tick frames (Nagle)
		}
		c := &conn{
			nc:    nc,
			enc:   gob.NewEncoder(nc),
			state: make(chan *Envelope, 1),
			ctrl:  make(chan *Envelope, 16),
			done:  make(chan struct{}),
		}
		// The conn is NOT added to s.conns until the handshake completes (in readLoop), so its
		// Hello is queued before any broadcast can be pushed -- Hello always arrives first.
		go s.senderLoop(nc, c)
		go s.readLoop(nc, c)
	}
}

// senderLoop is the SOLE writer for a conn. Its first message is a single control message (Hello on
// a good handshake, or Reject on a bad one); only then does it forward broadcasts. A write deadline
// bounds a stuck client; any error, or a terminal Reject/HostClosed, tears the conn down.
func (s *Server) senderLoop(nc net.Conn, c *conn) {
	// Phase 1: the handshake reply, before any broadcast.
	select {
	case <-c.done:
		return
	case env := <-c.ctrl:
		if !s.writeEnv(nc, c, env) || s.terminal(c, env) {
			return
		}
	}
	// Phase 2: broadcasts + later control, in arrival order.
	for {
		select {
		case <-c.done:
			return
		case env := <-c.ctrl:
			if !s.writeEnv(nc, c, env) || s.terminal(c, env) {
				return
			}
		case env := <-c.state:
			if !s.writeEnv(nc, c, env) {
				return
			}
		}
	}
}

func (s *Server) writeEnv(nc net.Conn, c *conn, env *Envelope) bool {
	nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.enc.Encode(*env); err != nil {
		s.dropConn(c)
		return false
	}
	return true
}

// terminal drops the conn after a Reject/HostClosed has been flushed, and reports whether it did.
func (s *Server) terminal(c *conn, env *Envelope) bool {
	if env.Kind == MsgReject || env.Kind == MsgHostClosed {
		s.dropConn(c)
		return true
	}
	return false
}

// dropConn tears a connection down exactly once. In lobby mode a seated human's slot is held by a
// reservation for the reconnect grace (left unassigned so its AI fallback covers it); immediate
// mode frees it at once.
func (s *Server) dropConn(c *conn) {
	c.closeOnce.Do(func() {
		s.mu.Lock()
		delete(s.conns, c)
		delete(s.intents, c.playerID)
		if s.hostConn == c {
			s.hostConn = nil
		}
		if c.playerID != spectatorID && s.assigned[c.playerID] {
			delete(s.assigned, c.playerID)
			delete(s.ready, c.playerID)
			delete(s.names, c.playerID)
			if s.lobbyMode && c.token != "" {
				s.reservations[c.playerID] = reservation{token: c.token, expires: time.Now().Add(reconnectGrace)}
			}
		}
		s.mu.Unlock()
		close(c.done)
		c.nc.Close()
		s.log.Info("client disconnected", "player", c.playerID)
	})
}

// readLoop decodes a client's frames until it disconnects. The first frame is the handshake (it
// validates the version and establishes identity); the rest are per-tick intents plus rate-limited
// control. Each frame is length-bounded (untrusted), and a read deadline drops a silent client.
func (s *Server) readLoop(nc net.Conn, c *conn) {
	defer s.dropConn(c)

	nc.SetReadDeadline(time.Now().Add(readTimeout))
	var first ClientFrame
	if err := readFrame(nc, &first, maxClientFrameBytes); err != nil {
		return
	}
	if first.ProtoVersion != ProtoVersion {
		s.reject(c, fmt.Sprintf("server is protocol v%d; your client is v%d -- update to play", ProtoVersion, first.ProtoVersion))
		return
	}
	if !s.handshake(c, first) {
		return // rejected (full); the reject was queued + flushed
	}
	for {
		nc.SetReadDeadline(time.Now().Add(readTimeout))
		var f ClientFrame
		if err := readFrame(nc, &f, maxClientFrameBytes); err != nil {
			return
		}
		if f.Kind == CIntent {
			in, ok := sanitizeIntent(f.Intent)
			if !ok {
				continue // one NaN would desync every client
			}
			s.mu.Lock()
			if c.playerID != spectatorID {
				s.intents[c.playerID] = stampedIntent{in: in, tick: s.tick}
			}
			s.mu.Unlock()
			continue
		}
		if !c.allowControl(time.Now()) {
			s.log.Warn("control rate limit exceeded; dropping client", "player", c.playerID)
			return
		}
		s.handleControl(c, f)
	}
}

// handshake establishes a connection's identity from its first frame (host token, reconnect token,
// or a fresh join), queues its Hello, and admits it to the broadcast set. Returns false if the
// connection was rejected (a Reject was queued + flushed).
func (s *Server) handshake(c *conn, f ClientFrame) bool {
	name := cleanName(f.Name)
	var stale *conn // a duplicate live conn whose seat we reclaimed; dropped after we unlock

	s.mu.Lock()
	switch {
	case f.Kind == CHostToken && s.lobbyMode && s.pendingHostToken != "" &&
		f.HostToken == s.pendingHostToken && s.hostConn == nil:
		c.isHost = true
		s.hostConn = c
		s.pendingHostToken = "" // single use
		if id, ok := s.freeSlot(); ok {
			c.playerID = id
			s.assigned[id] = true
			s.names[id] = orDefault(name, "Host")
		} else {
			c.playerID = spectatorID
		}
	case f.Kind == CResumeToken && f.ResumeToken != "":
		if pid, dup, ok := s.resumeLocked(f.ResumeToken, c); ok {
			c.playerID = pid
			s.assigned[pid] = true
			s.names[pid] = orDefault(name, defaultName(pid))
			stale = dup
		} else {
			c.playerID = s.joinSeatLocked(name)
		}
	default: // CJoin (or any other first frame): a fresh guest
		c.playerID = s.joinSeatLocked(name)
	}

	if c.playerID == spectatorID && !c.isHost && s.countSpectatorsLocked() >= s.specCap {
		s.mu.Unlock()
		s.reject(c, "the game is full")
		return false
	}
	c.name = orDefault(name, defaultName(c.playerID))
	c.token = s.newTokenLocked()
	c.pushCtrl(&Envelope{Kind: MsgHello, Hello: &Hello{
		ProtoVersion:     ProtoVersion,
		AssignedPlayerID: c.playerID,
		IsHost:           c.isHost,
		SessionToken:     c.token,
	}})
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	if stale != nil {
		s.dropConn(stale) // force-drop the partitioned duplicate now that we hold its seat
	}
	s.log.Info("client joined", "player", c.playerID, "host", c.isHost, "name", c.name)
	return true
}

// resumeLocked rebinds a reconnecting client to its prior seat by session token: first a held
// reservation (the common drop-then-return case), else a still-live duplicate connection (a
// network partition), whose seat is reclaimed and whose old conn is returned for force-drop.
func (s *Server) resumeLocked(token string, newc *conn) (pid int, stale *conn, ok bool) {
	now := time.Now()
	for id, r := range s.reservations {
		if r.token == token && now.Before(r.expires) {
			delete(s.reservations, id)
			return id, nil, true
		}
	}
	for oldc := range s.conns {
		if oldc != newc && oldc.token == token && oldc.playerID != spectatorID {
			id := oldc.playerID
			delete(s.assigned, id) // vacate so newc can take it; oldc is dropped after unlock
			return id, oldc, true
		}
	}
	return -1, nil, false
}

// joinSeatLocked seats a fresh guest: a spectator in lobby mode (it picks a seat later) or the
// first free slot in immediate mode.
func (s *Server) joinSeatLocked(name string) int {
	if s.lobbyMode {
		return spectatorID
	}
	if id, ok := s.freeSlot(); ok {
		s.assigned[id] = true
		s.names[id] = orDefault(name, defaultName(id))
		return id
	}
	return spectatorID
}

// handleControl dispatches a non-intent client frame. Host-only actions are ignored from non-host
// connections.
func (s *Server) handleControl(c *conn, f ClientFrame) {
	switch f.Kind {
	case CPing:
		s.mu.Lock()
		tick := s.tick
		s.mu.Unlock()
		c.pushCtrl(&Envelope{Kind: MsgPong, Pong: &Pong{Seq: f.Seq, ServerTick: tick}})
	case CPickSlot:
		s.pickSlot(c, f.Team, f.Slot)
	case CReady:
		s.mu.Lock()
		if c.playerID != spectatorID {
			s.ready[c.playerID] = f.Ready
		}
		s.mu.Unlock()
	case CConfig:
		if c.isHost {
			s.applyConfig(f.Setup)
		}
	case CStart:
		if c.isHost {
			s.mu.Lock()
			s.started, s.paused = true, false
			s.mu.Unlock()
		}
	case CPause:
		if c.isHost {
			s.mu.Lock()
			s.paused = f.Paused
			s.mu.Unlock()
		}
	case CReturnLobby:
		if c.isHost {
			s.returnToLobby()
		}
	case CHostClose:
		if c.isHost {
			s.hostClose("the host ended the match")
		}
	case CKick:
		if c.isHost {
			s.kick(f.Slot) // Slot carries the target PlayerID for a kick
		}
	}
}

// pickSlot claims a team+slot for a connection under s.mu (atomic check-set, so two clients racing
// the same seat can't both get it). Slot=-1 means the first open seat on the team; Team<0 spectates.
func (s *Server) pickSlot(c *conn, team, slot int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if team < 0 || team > 1 {
		s.vacateLocked(c) // spectate
		return
	}
	target := slot
	if target < 0 {
		if target = s.firstOpenSlotLocked(team); target < 0 {
			return // none open
		}
	}
	pid, ok := s.seatPlayerIDLocked(team, target)
	if !ok || s.assigned[pid] {
		return // out of range or taken: the client's optimistic UI snaps back on the next roster
	}
	if _, held := s.reservations[pid]; held {
		return
	}
	s.vacateLocked(c) // vacate the old seat only now the new claim is guaranteed
	c.playerID = pid
	s.assigned[pid] = true
	s.names[pid] = c.name
	s.ready[pid] = false
}

// vacateLocked releases whatever seat a connection holds, making it a spectator.
func (s *Server) vacateLocked(c *conn) {
	if c.playerID == spectatorID {
		return
	}
	delete(s.assigned, c.playerID)
	delete(s.names, c.playerID)
	delete(s.ready, c.playerID)
	delete(s.intents, c.playerID)
	c.playerID = spectatorID
}

func (s *Server) seatPlayerIDLocked(team, slot int) (int, bool) {
	if team < 0 || team > 1 {
		return 0, false
	}
	t := s.match.Teams[team]
	if slot < 0 || slot >= len(t.Players) {
		return 0, false
	}
	return t.Players[slot].PlayerID, true
}

func (s *Server) firstOpenSlotLocked(team int) int {
	t := s.match.Teams[team]
	for slot, p := range t.Players {
		if s.assigned[p.PlayerID] {
			continue
		}
		if _, held := s.reservations[p.PlayerID]; held {
			continue
		}
		return slot
	}
	return -1
}

// applyConfig (host only) re-validates a pushed config, rebuilds the authoritative match, and
// re-seats players. An invalid config is ignored, never applied -- the host is still a network peer.
func (s *Server) applyConfig(setup *config.MatchSetup) {
	if setup == nil || s.rebuildMatch == nil {
		return
	}
	if err := setup.Validate(); err != nil {
		s.log.Warn("ignoring invalid CConfig", "err", err)
		return
	}
	match, bots, humanIDs := s.rebuildMatch(*setup)
	s.mu.Lock()
	s.swapMatchLocked(match, bots, humanIDs)
	s.setup = *setup
	s.mu.Unlock()
}

// returnToLobby (host only) rebuilds a fresh match from the locked setup and reopens the lobby.
func (s *Server) returnToLobby() {
	if s.rebuildMatch == nil {
		return
	}
	match, bots, humanIDs := s.rebuildMatch(s.setup)
	s.mu.Lock()
	s.swapMatchLocked(match, bots, humanIDs)
	s.started, s.paused = false, false
	s.mu.Unlock()
}

// swapMatchLocked replaces the authoritative match and re-seats players by (team, slot) -- NOT by
// PlayerID, which is positional and renumbers across a rebuild. A seat whose (team, slot) still
// exists keeps its holder (re-bound to the new PlayerID there); one that vanished evicts its holder
// to spectator. All ready flags and reservations are cleared (the config changed; PlayerIDs moved).
func (s *Server) swapMatchLocked(m *sim.Match, bots map[int]Bot, humanIDs []int) {
	type heldSeat struct {
		c          *conn
		team, slot int
		name       string
	}
	var held []heldSeat
	for c := range s.conns {
		if c.playerID == spectatorID {
			continue
		}
		if team, slot, ok := s.locateLocked(c.playerID); ok {
			held = append(held, heldSeat{c, team, slot, s.names[c.playerID]})
		} else {
			c.playerID = spectatorID
		}
	}

	s.match, s.bots, s.humanIDs = m, bots, humanIDs
	s.intents = make(map[int]stampedIntent)
	s.assigned = make(map[int]bool)
	s.names = make(map[int]string)
	s.ready = make(map[int]bool)
	s.reservations = make(map[int]reservation)

	for _, h := range held {
		if pid, ok := s.seatPlayerIDLocked(h.team, h.slot); ok {
			h.c.playerID = pid
			s.assigned[pid] = true
			s.names[pid] = h.name
		} else {
			h.c.playerID = spectatorID // that (team, slot) no longer exists
		}
	}
}

// locateLocked finds the (team, slot) of a PlayerID in the current roster.
func (s *Server) locateLocked(pid int) (team, slot int, ok bool) {
	for ti, t := range s.match.Teams {
		for si, p := range t.Players {
			if p.PlayerID == pid {
				return ti, si, true
			}
		}
	}
	return 0, 0, false
}

// hostClose notifies every client that the host ended the match (a friendly message rather than a
// bare drop), then lets each sender tear its conn down.
func (s *Server) hostClose(reason string) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.pushCtrl(&Envelope{Kind: MsgHostClosed, HostClosed: &HostClosed{Reason: reason}})
	}
}

// kick removes the connection holding targetPlayerID and clears its reservation so it cannot be
// reclaimed.
func (s *Server) kick(targetPlayerID int) {
	s.mu.Lock()
	var victim *conn
	for c := range s.conns {
		if c.playerID == targetPlayerID && !c.isHost {
			victim = c
			break
		}
	}
	delete(s.reservations, targetPlayerID)
	s.mu.Unlock()
	if victim != nil {
		victim.pushCtrl(&Envelope{Kind: MsgReject, Reject: &Reject{Reason: "you were removed by the host"}})
	}
}

// reject queues a Reject and waits (briefly) for the sender to flush it before the reader's
// deferred dropConn closes the socket, so the client sees the reason rather than a bare drop.
func (s *Server) reject(c *conn, reason string) {
	c.pushCtrl(&Envelope{Kind: MsgReject, Reject: &Reject{Reason: reason}})
	select {
	case <-c.done:
	case <-time.After(writeTimeout):
	}
}

// sanitizeIntent rejects an intent with non-finite floats and clamps the throttle to [0,1].
func sanitizeIntent(in sim.Intent) (sim.Intent, bool) {
	if !finite(in.Move.X) || !finite(in.Move.Y) || !finite(in.Aim.X) || !finite(in.Aim.Y) || !finite(in.Throttle) {
		return sim.Intent{}, false
	}
	if in.Throttle < 0 {
		in.Throttle = 0
	} else if in.Throttle > 1 {
		in.Throttle = 1
	}
	return in, true
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// tickLoop advances the simulation at the fixed rate and broadcasts every tick: a snapshot once the
// match has started, the lobby roster before then. Stepping is gated by started && !paused; the
// listen/accept/teardown around it is unchanged from immediate mode.
func (s *Server) tickLoop(ctx context.Context) {
	dt := 1.0 / s.tickRate
	ticker := time.NewTicker(time.Duration(float64(time.Second) / s.tickRate))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !s.tickOnce(dt) {
			return // a sim/bot panic was recovered; end the match cleanly rather than crash the host
		}
	}
}

// tickOnce advances and broadcasts one tick. It recovers from any panic in the simulation or an AI
// bot (a panic in this goroutine would otherwise crash the whole host process), logging it and
// returning false so the loop ends the match cleanly. The locked work uses a deferred Unlock so the
// mutex is released even on panic — letting the subsequent shutdown teardown take the lock.
func (s *Server) tickOnce(dt float64) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("recovered from panic in simulation; ending match", "panic", r, "stack", string(debug.Stack()))
			ok = false
		}
	}()

	var snap *Snapshot
	var lobby *LobbyState
	var conns []*conn
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tick++
		s.expireReservationsLocked()
		if s.started && !s.paused {
			inputs := make(map[int]sim.Intent, len(s.match.Players))
			for _, p := range s.match.Players {
				if s.assigned[p.PlayerID] {
					// A client controls this player -- but only while its intent is fresh. A silent
					// or laggy client's stale intent expires to neutral.
					if si, ok := s.intents[p.PlayerID]; ok && s.tick-si.tick <= intentMaxAgeTicks {
						inputs[p.PlayerID] = si.in
					}
				} else if bot, ok := s.bots[p.PlayerID]; ok {
					inputs[p.PlayerID] = bot.Intent(s.match.View()) // AI until a client claims (or reclaims) the slot
				}
			}
			s.match.Step(inputs, dt)
		}
		if s.started {
			ss := SnapshotOf(s.match)
			ss.Paused = s.paused // SnapshotOf doesn't know the lobby/pause state
			snap = &ss
		} else {
			lobby = s.buildLobbyLocked()
		}
		conns = make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
	}()

	for _, c := range conns {
		if snap != nil {
			c.pushState(&Envelope{Kind: MsgSnapshot, Snapshot: snap})
		} else {
			ls := *lobby // per-conn copy: identity fields differ per recipient
			ls.YouPlayerID = c.playerID
			ls.YouAreHost = c.isHost
			c.pushState(&Envelope{Kind: MsgLobby, Lobby: &ls})
		}
	}
	return true
}

func (s *Server) expireReservationsLocked() {
	now := time.Now()
	for pid, r := range s.reservations {
		if !now.Before(r.expires) {
			delete(s.reservations, pid)
		}
	}
}

// buildLobbyLocked projects the current roster into a LobbyState. Each SeatInfo's PlayerID is the
// SAME m.Teams[t].Players[slot].PlayerID the server seats from, so "take seat N" claims the right
// player. Session tokens are NEVER included.
func (s *Server) buildLobbyLocked() *LobbyState {
	ls := &LobbyState{
		Phase:         0,
		HomeSize:      len(s.match.Teams[0].Players),
		AwaySize:      len(s.match.Teams[1].Players),
		SpectatorCap:  s.specCap,
		ConfigSummary: configSummary(s.setup),
	}
	allReady, anySeated := true, false
	for ti, t := range s.match.Teams {
		for slot, p := range t.Players {
			seat := SeatInfo{Team: ti, Slot: slot, PlayerID: p.PlayerID, Role: seatRole(slot)}
			if s.assigned[p.PlayerID] {
				seat.IsHuman = true
				seat.OccupantName = s.names[p.PlayerID]
				seat.Ready = s.ready[p.PlayerID]
				anySeated = true
				if !seat.Ready {
					allReady = false
				}
			}
			ls.Seats = append(ls.Seats, seat)
		}
	}
	ls.AllReady = anySeated && allReady
	for c := range s.conns {
		if c.isHost {
			ls.HostName = c.name
		}
		if c.playerID == spectatorID {
			ls.Spectators = append(ls.Spectators, c.name)
		}
	}
	sort.Strings(ls.Spectators)
	return ls
}

func (s *Server) countSpectatorsLocked() int {
	n := 0
	for c := range s.conns {
		if c.playerID == spectatorID {
			n++
		}
	}
	return n
}

func (s *Server) newTokenLocked() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d-%d", s.tick, len(s.conns)) // fallback; still unique per session
	}
	return hex.EncodeToString(b[:])
}

func seatRole(slot int) string {
	if slot == 0 {
		return "GK"
	}
	return "OUT"
}

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > maxNameLen {
		s = strings.TrimSpace(s[:maxNameLen])
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func defaultName(pid int) string {
	if pid == spectatorID {
		return "Spectator"
	}
	return fmt.Sprintf("Player %d", pid)
}

func configSummary(setup config.MatchSetup) string {
	home, away := setup.HomeSize, setup.AwaySize
	if home <= 0 {
		home = setup.TeamSize
	}
	if away <= 0 {
		away = setup.TeamSize
	}
	field := setup.Field
	if field == "" {
		field = "standard"
	}
	return fmt.Sprintf("%s pitch · %dv%d", field, home, away)
}
