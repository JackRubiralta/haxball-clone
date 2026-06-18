package netcode

import (
	"context"
	"encoding/gob"
	"log/slog"
	"math"
	"net"
	"sync"
	"time"

	"phootball/internal/sim"
)

// Bot is anything that can produce an Intent from match state -- control.AI
// satisfies it structurally, so netcode need not import the control package.
type Bot interface {
	Intent(view sim.View) sim.Intent
}

// Server runs the authoritative simulation. It steps the match at a fixed rate,
// gathering intents from AI bots and from connected remote clients, and broadcasts a
// snapshot every tick. All collisions happen here, never on a client.
type Server struct {
	addr     string
	tickRate float64
	log      *slog.Logger

	mu       sync.Mutex
	match    *sim.Match
	bots     map[int]Bot           // playerID -> AI controller
	intents  map[int]stampedIntent // latest intent (+ arrival tick) per remote-controlled player
	humanIDs []int                 // player slots a client may be assigned to
	assigned map[int]bool          // which human slots are currently taken
	conns    map[*conn]struct{}
	tick     uint64 // server tick counter, for intent-staleness checks
}

type conn struct {
	nc        net.Conn
	enc       *gob.Encoder
	playerID  int
	send      chan *Snapshot // latest snapshot to write; cap 1, stale frames dropped
	done      chan struct{}  // closed once when the conn is being torn down
	closeOnce sync.Once
}

// push hands the newest snapshot to the per-conn sender, dropping any stale frame still
// queued so a slow client always gets the freshest state and never back-pressures the tick.
func (c *conn) push(snap *Snapshot) {
	select {
	case c.send <- snap:
	default:
		select { // buffer full: discard the stale frame, then enqueue the fresh one
		case <-c.send:
		default:
		}
		select {
		case c.send <- snap:
		default:
		}
	}
}

// stampedIntent is a client's latest intent plus the server tick it arrived on, so a silent
// client's stale intent can be expired to neutral.
type stampedIntent struct {
	in   sim.Intent
	tick uint64
}

const (
	intentMaxAgeTicks = 30               // ~0.5s at 60Hz: after this, a silent client idles (neutral intent)
	writeTimeout      = 5 * time.Second  // a stuck client cannot block the sender forever
	readTimeout       = 10 * time.Second // a client that stops sending is dropped
	keepAlivePeriod   = 15 * time.Second
)

// NewServer creates a server. bots maps player IDs to AI controllers; humanIDs are
// the player slots assigned to remote clients in connection order (any remaining
// human slots are also driven by their AI fallback if a bot is provided for them).
func NewServer(addr string, match *sim.Match, bots map[int]Bot, humanIDs []int) *Server {
	return &Server{
		addr:     addr,
		tickRate: 60,
		log:      slog.Default(),
		match:    match,
		bots:     bots,
		intents:  make(map[int]stampedIntent),
		humanIDs: humanIDs,
		assigned: make(map[int]bool),
		conns:    make(map[*conn]struct{}),
	}
}

// SetLogger replaces the server's logger. A nil logger is ignored, so the default
// stays in place.
func (s *Server) SetLogger(l *slog.Logger) {
	if l != nil {
		s.log = l
	}
}

// SetTickRate sets the simulation rate in ticks per second. It must be called before
// Run. Out-of-range values are clamped to a sane band.
func (s *Server) SetTickRate(r float64) {
	if r < 1 {
		r = 1
	} else if r > 240 {
		r = 240
	}
	s.tickRate = r
}

// freeSlot returns the first unassigned human player slot, or (-1, false).
func (s *Server) freeSlot() (int, bool) {
	for _, id := range s.humanIDs {
		if !s.assigned[id] {
			return id, true
		}
	}
	return -1, false
}

// Run listens for clients and steps the simulation until the context is cancelled or a
// fatal listen error occurs. A cancelled context is a clean shutdown (returns nil).
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	s.log.Info("listening", "addr", s.addr, "tickRate", s.tickRate)

	go s.acceptLoop(listener)
	go func() {
		<-ctx.Done()
		listener.Close() // unblock Accept so acceptLoop returns
	}()
	s.tickLoop(ctx)

	// Shutdown: tear down every remaining client so its reader/sender goroutines exit instead
	// of lingering (a leak across server restarts / in tests).
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

func (s *Server) acceptLoop(listener net.Listener) {
	for {
		nc, err := listener.Accept()
		if err != nil {
			return
		}
		if tcp, ok := nc.(*net.TCPConn); ok {
			tcp.SetKeepAlive(true)
			tcp.SetKeepAlivePeriod(keepAlivePeriod)
		}
		s.mu.Lock()
		playerID, ok := s.freeSlot()
		if !ok {
			s.mu.Unlock()
			s.log.Warn("rejecting client: no free player slots", "remote", nc.RemoteAddr().String())
			nc.Close()
			continue
		}
		s.assigned[playerID] = true
		c := &conn{nc: nc, enc: gob.NewEncoder(nc), playerID: playerID, send: make(chan *Snapshot, 1), done: make(chan struct{})}
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		s.log.Info("client joined", "remote", nc.RemoteAddr().String(), "player", playerID)
		// The sender owns all writes to this conn (the handshake Hello, then snapshots) so the
		// tick goroutine never blocks on a slow client; the reader feeds intents.
		go s.senderLoop(nc, c)
		go s.readLoop(nc, c)
	}
}

// senderLoop is the SOLE writer for a conn. It first sends the Hello handshake (telling the
// client its assigned slot and the protocol version), then forwards the latest snapshot
// whenever one is queued. A write deadline bounds a stuck client; any error tears the conn down.
func (s *Server) senderLoop(nc net.Conn, c *conn) {
	hello := Envelope{Kind: MsgHello, Hello: &Hello{ProtoVersion: ProtoVersion, AssignedPlayerID: c.playerID}}
	nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.enc.Encode(hello); err != nil {
		s.log.Warn("hello encode failed", "player", c.playerID, "err", err)
		s.dropConn(c)
		return
	}
	for {
		select {
		case <-c.done:
			return
		case snap := <-c.send:
			nc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.enc.Encode(Envelope{Kind: MsgSnapshot, Snapshot: snap}); err != nil {
				s.log.Warn("snapshot encode failed; dropping client", "player", c.playerID, "err", err)
				s.dropConn(c)
				return
			}
		}
	}
}

// dropConn tears a connection down exactly once: removes it from the shared maps, signals its
// sender to stop, and closes the socket. Safe to call from both the reader and the sender.
func (s *Server) dropConn(c *conn) {
	c.closeOnce.Do(func() {
		s.mu.Lock()
		delete(s.conns, c)
		delete(s.intents, c.playerID)
		delete(s.assigned, c.playerID) // free the slot for a reconnect
		s.mu.Unlock()
		close(c.done)
		c.nc.Close()
		s.log.Info("client disconnected", "player", c.playerID)
	})
}

// readLoop decodes a client's intents into the shared intent map until it disconnects. It
// validates the protocol version on the first message, refreshes a read deadline each message
// (so a silent client is dropped), and stamps each intent with the current tick for expiry.
func (s *Server) readLoop(nc net.Conn, c *conn) {
	defer s.dropConn(c)

	dec := gob.NewDecoder(nc)
	first := true
	for {
		nc.SetReadDeadline(time.Now().Add(readTimeout))
		var msg ClientMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if first {
			first = false
			if msg.ProtoVersion != ProtoVersion {
				s.log.Warn("rejecting client: protocol mismatch", "player", c.playerID,
					"client", msg.ProtoVersion, "server", ProtoVersion)
				return
			}
		}
		in, ok := sanitizeIntent(msg.Intent)
		if !ok {
			s.log.Warn("dropping malformed intent", "player", c.playerID)
			continue // one NaN would desync every client
		}
		s.mu.Lock()
		s.intents[c.playerID] = stampedIntent{in: in, tick: s.tick}
		s.mu.Unlock()
	}
}

// sanitizeIntent rejects an intent with non-finite floats and clamps the throttle to
// [0,1], so a buggy or hostile client cannot inject NaNs into the shared simulation.
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

// tickLoop advances the simulation at the fixed rate and broadcasts snapshots until the
// context is cancelled.
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
		s.mu.Lock()
		s.tick++
		inputs := make(map[int]sim.Intent, len(s.match.Players))
		for _, p := range s.match.Players {
			if s.assigned[p.PlayerID] {
				// A client controls this player -- but only while its intent is fresh. A silent
				// or laggy client's stale intent expires to neutral so its player doesn't keep
				// coasting on the last command.
				if si, ok := s.intents[p.PlayerID]; ok && s.tick-si.tick <= intentMaxAgeTicks {
					inputs[p.PlayerID] = si.in
				}
			} else if bot, ok := s.bots[p.PlayerID]; ok {
				inputs[p.PlayerID] = bot.Intent(s.match.View()) // AI until a client claims the slot
			}
		}
		s.match.Step(inputs, dt)
		snap := SnapshotOf(s.match)
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()

		// Hand the snapshot to each per-conn sender (non-blocking, latest-wins). The Encode now
		// happens on the sender goroutines, so a slow client never stalls the tick loop.
		for _, c := range conns {
			c.push(&snap)
		}
	}
}
