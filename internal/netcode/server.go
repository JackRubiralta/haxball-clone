package netcode

import (
	"encoding/gob"
	"log"
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
		match:    match,
		bots:     bots,
		intents:  make(map[int]sim.Intent),
		humanIDs: humanIDs,
		assigned: make(map[int]bool),
		conns:    make(map[*conn]struct{}),
	}
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

// Run listens for clients and steps the simulation until an error occurs.
func (s *Server) Run() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	log.Printf("server listening on %s", s.addr)

	go s.acceptLoop(listener)
	s.tickLoop()
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
			log.Printf("rejecting client %s: no free player slots", nc.RemoteAddr())
			nc.Close()
			continue
		}
		s.assigned[playerID] = true
		c := &conn{enc: gob.NewEncoder(nc), playerID: playerID}
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		log.Printf("client %s joined as player %d", nc.RemoteAddr(), playerID)
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
		log.Printf("player %d disconnected", c.playerID)
	}()

	dec := gob.NewDecoder(nc)
	for {
		var msg ClientMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		s.mu.Lock()
		s.intents[c.playerID] = msg.Intent
		s.mu.Unlock()
	}
}

// tickLoop advances the simulation at the fixed rate and broadcasts snapshots.
func (s *Server) tickLoop() {
	dt := 1.0 / s.tickRate
	ticker := time.NewTicker(time.Duration(float64(time.Second) / s.tickRate))
	defer ticker.Stop()

	for range ticker.C {
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
