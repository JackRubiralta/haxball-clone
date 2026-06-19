package netcode

import (
	"encoding/gob"
	"fmt"
	"net"
	"sync"
	"time"

	"phootball/internal/sim"
)

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
				s.reservations[c.playerID] = reservation{token: c.token, expires: time.Now().Add(ReconnectGrace)}
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
