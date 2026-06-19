package netcode

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"phootball/internal/config"
	"phootball/internal/sim"
)

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
