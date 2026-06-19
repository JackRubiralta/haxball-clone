package netcode

import (
	"encoding/gob"
	"fmt"
	"net"
	"sync"
	"time"

	"phootball/internal/config"
	"phootball/internal/sim"
)

// Client is a server connection: it sends the local player's frames (the per-tick intent plus
// lobby/control) and exposes the latest authoritative snapshot, the lobby roster, its assigned
// slot, and the connection's health. The first frame it sends is the handshake (a fresh join, a
// host-token claim, or a reconnect); the server replies with Hello.
type Client struct {
	conn net.Conn
	dec  *gob.Decoder

	sendMu sync.Mutex
	closed bool

	mu          sync.Mutex // guards everything below
	latest      Snapshot
	hasSnap     bool
	lobby       LobbyState
	hasLobby    bool
	assignedID  int
	haveID      bool
	isHost      bool
	token       string
	serverProto int
	down        bool
	err         error
	lastRecvAt  time.Time
	lastState   MsgKind // the most recent per-tick broadcast kind: MsgSnapshot or MsgLobby
	reason      string  // a friendly reject/host-closed banner, if any
	rttMs       int
	nextSeq     uint64
	pingSent    map[uint64]time.Time
}

// Dial connects as a fresh anonymous guest (CJoin). Used by the standalone client and tests.
func Dial(addr string) (*Client, error) {
	return dialWith(addr, ClientFrame{ProtoVersion: ProtoVersion, Kind: CJoin})
}

// DialJoin connects as a fresh guest with a display name.
func DialJoin(addr, name string) (*Client, error) {
	return dialWith(addr, ClientFrame{ProtoVersion: ProtoVersion, Kind: CJoin, Name: name})
}

// DialHost connects as the host by presenting the pending host token (it must match the one the
// server registered via SetPendingHostToken).
func DialHost(addr, name, hostToken string) (*Client, error) {
	return dialWith(addr, ClientFrame{ProtoVersion: ProtoVersion, Kind: CHostToken, HostToken: hostToken, Name: name})
}

// DialResume reconnects, presenting a prior session token to reclaim a reserved seat.
func DialResume(addr, name, resumeToken string) (*Client, error) {
	return dialWith(addr, ClientFrame{ProtoVersion: ProtoVersion, Kind: CResumeToken, ResumeToken: resumeToken, Name: name})
}

func dialWith(addr string, first ClientFrame) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.SetNoDelay(true) // input latency matters: never coalesce per-tick frames
	}
	c := &Client{
		conn:       conn,
		dec:        gob.NewDecoder(conn),
		assignedID: spectatorID,
		lastRecvAt: time.Now(),
		pingSent:   make(map[uint64]time.Time),
	}
	if err := writeFrame(conn, first); err != nil {
		conn.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	for {
		var env Envelope
		if err := c.dec.Decode(&env); err != nil {
			c.mu.Lock()
			if !c.down { // don't clobber a friendly reject/host-closed reason with the raw EOF
				c.down, c.err = true, err
			}
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		switch env.Kind {
		case MsgHello:
			if env.Hello != nil {
				c.assignedID, c.haveID = env.Hello.AssignedPlayerID, true
				c.isHost = env.Hello.IsHost
				c.token = env.Hello.SessionToken
				c.serverProto = env.Hello.ProtoVersion
			}
		case MsgSnapshot:
			if env.Snapshot != nil {
				c.latest, c.hasSnap = *env.Snapshot, true
				c.lastRecvAt = time.Now()
				c.lastState = MsgSnapshot
			}
		case MsgLobby:
			if env.Lobby != nil {
				c.lobby, c.hasLobby = *env.Lobby, true
				c.assignedID, c.haveID = env.Lobby.YouPlayerID, true
				c.isHost = env.Lobby.YouAreHost
				c.lastRecvAt = time.Now()
				c.lastState = MsgLobby
			}
		case MsgReject:
			if env.Reject != nil {
				c.reason, c.down = env.Reject.Reason, true
				c.err = fmt.Errorf("rejected: %s", env.Reject.Reason)
			}
		case MsgHostClosed:
			if env.HostClosed != nil {
				c.reason, c.down = env.HostClosed.Reason, true
				c.err = fmt.Errorf("host closed: %s", env.HostClosed.Reason)
			}
		case MsgPong:
			if env.Pong != nil {
				if t0, ok := c.pingSent[env.Pong.Seq]; ok {
					c.rttMs = int(time.Since(t0).Milliseconds())
					delete(c.pingSent, env.Pong.Seq)
				}
			}
		}
		c.mu.Unlock()
	}
}

// Send transmits the per-tick intent (the hot path).
func (c *Client) Send(in sim.Intent) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CIntent, Intent: in})
}

func (c *Client) sendFrame(f ClientFrame) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return writeFrame(c.conn, f)
}

// Lobby/control senders.
func (c *Client) PickSlot(team, slot int) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CPickSlot, Team: team, Slot: slot})
}
func (c *Client) SetReady(r bool) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CReady, Ready: r})
}
func (c *Client) SendConfig(setup config.MatchSetup) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CConfig, Setup: &setup})
}
func (c *Client) StartMatch() error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CStart})
}
func (c *Client) SetPaused(p bool) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CPause, Paused: p})
}
func (c *Client) ReturnToLobby() error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CReturnLobby})
}
func (c *Client) HostClose() error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CHostClose})
}
func (c *Client) Kick(playerID int) error {
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CKick, Slot: playerID})
}

// Ping sends a keepalive whose Pong reply measures round-trip latency.
func (c *Client) Ping() error {
	c.mu.Lock()
	c.nextSeq++
	seq := c.nextSeq
	c.pingSent[seq] = time.Now()
	c.mu.Unlock()
	return c.sendFrame(ClientFrame{ProtoVersion: ProtoVersion, Kind: CPing, Seq: seq})
}

// Snapshot returns the latest authoritative snapshot, if any.
func (c *Client) Snapshot() (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.hasSnap
}

// Lobby returns the latest lobby roster, if any.
func (c *Client) Lobby() (LobbyState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lobby, c.hasLobby
}

// AssignedID returns the player slot the server assigned this client (-1 = spectator).
func (c *Client) AssignedID() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.assignedID, c.haveID
}

// IsHost reports whether the server granted this connection host authority.
func (c *Client) IsHost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isHost
}

// SessionToken returns the reconnect token issued in Hello (kept in memory only).
func (c *Client) SessionToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// RTTms returns the most recent round-trip latency in milliseconds (0 until the first Pong).
func (c *Client) RTTms() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rttMs
}

// Reason returns a friendly banner for a reject or host-closed, if any.
func (c *Client) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// ServerProto returns the server's protocol version (from Hello), 0 until Hello arrives.
func (c *Client) ServerProto() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverProto
}

// InMatch reports that the most recent per-tick broadcast was a snapshot (the match is live).
func (c *Client) InMatch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastState == MsgSnapshot
}

// InLobby reports that the most recent per-tick broadcast was a lobby roster (pre-match).
func (c *Client) InLobby() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastState == MsgLobby
}

// ConnState reports whether the connection is effectively down: a hard read error / reject, or a
// stall (no snapshot or lobby for over a second once data has started flowing). The menu polls
// this to route to the reconnect screen instead of terminating the process.
func (c *Client) ConnState() (down bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return true, c.err
	}
	if (c.hasSnap || c.hasLobby) && time.Since(c.lastRecvAt) > time.Second {
		return true, fmt.Errorf("connection stalled: no data for over a second")
	}
	return false, nil
}

// Close shuts the connection down. Idempotent and serialized against Send.
func (c *Client) Close() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
