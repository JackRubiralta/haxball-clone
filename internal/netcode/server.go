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
	Intent(view *sim.Match) sim.Intent
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
	bots     map[int]Bot        // playerID -> AI controller
	intents  map[int]sim.Intent // latest intent per remote-controlled player
	humanIDs []int              // player slots a client may be assigned to
	assigned map[int]bool       // which human slots are currently taken
	conns    map[*conn]struct{}
}

type conn struct {
	enc      *gob.Encoder
	playerID int
}

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
		intents:  make(map[int]sim.Intent),
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
	s.log.Info("shutting down")
	return nil
}

func (s *Server) acceptLoop(listener net.Listener) {
	for {
		nc, err := listener.Accept()
		if err != nil {
			return
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
		c := &conn{enc: gob.NewEncoder(nc), playerID: playerID}
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		s.log.Info("client joined", "remote", nc.RemoteAddr().String(), "player", playerID)
		go s.readLoop(nc, c)
	}
}

// readLoop decodes a client's intents into the shared intent map until it
// disconnects.
func (s *Server) readLoop(nc net.Conn, c *conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		delete(s.intents, c.playerID)
		delete(s.assigned, c.playerID) // free the slot for a reconnect
		s.mu.Unlock()
		nc.Close()
		s.log.Info("client disconnected", "player", c.playerID)
	}()

	dec := gob.NewDecoder(nc)
	for {
		var msg ClientMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		in, ok := sanitizeIntent(msg.Intent)
		if !ok {
			s.log.Warn("dropping malformed intent", "player", c.playerID)
			continue // one NaN would desync every client
		}
		s.mu.Lock()
		s.intents[c.playerID] = in
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
		inputs := make(map[int]sim.Intent, len(s.match.Players))
		for _, p := range s.match.Players {
			if s.assigned[p.PlayerID] {
				inputs[p.PlayerID] = s.intents[p.PlayerID] // a client controls this player
			} else if bot, ok := s.bots[p.PlayerID]; ok {
				inputs[p.PlayerID] = bot.Intent(s.match) // AI until a client claims the slot
			}
		}
		s.match.Step(inputs, dt)
		snap := SnapshotOf(s.match)
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()

		for _, c := range conns {
			if err := c.enc.Encode(snap); err != nil {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}
		}
	}
}
