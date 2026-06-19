package netcode

import (
	"context"
	"encoding/gob"
	"log/slog"
	"math"
	"net"
	"runtime/debug"
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
	ReconnectGrace    = 15 * time.Second // how long a dropped human's seat is held for a reconnect (exported so a client can size its reconnect window to match)
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
