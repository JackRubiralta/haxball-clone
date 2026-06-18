package netcode

import (
	"encoding/gob"
	"net"
	"sync"

	"phootball/internal/sim"
)

// Client is a thin connection to the authoritative server: it sends the local
// player's intent and holds the most recent snapshot for rendering. It runs no
// gameplay collisions.
type Client struct {
	conn net.Conn
	enc  *gob.Encoder

	// sendMu serializes the write path. Send (enc.Encode) and Close (conn.Close) both
	// touch the shared encoder/connection, and Send may be called concurrently; without
	// this they race. It is separate from mu so a slow send never blocks a snapshot read.
	sendMu sync.Mutex
	closed bool

	mu         sync.Mutex // guards latest/hasSnap/assignedID/haveID only
	latest     Snapshot
	hasSnap    bool
	assignedID int
	haveID     bool
}

// Dial connects to a server and starts receiving snapshots in the background.
func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, enc: gob.NewEncoder(conn)}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	dec := gob.NewDecoder(c.conn)
	for {
		var env Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		switch env.Kind {
		case MsgHello:
			if env.Hello != nil {
				c.mu.Lock()
				c.assignedID, c.haveID = env.Hello.AssignedPlayerID, true
				c.mu.Unlock()
			}
		case MsgSnapshot:
			if env.Snapshot != nil {
				c.mu.Lock()
				c.latest, c.hasSnap = *env.Snapshot, true
				c.mu.Unlock()
			}
		}
	}
}

// Send transmits the local player's intent for this tick (stamped with the protocol version,
// which the server validates). It is safe to call from multiple goroutines and after Close
// (which returns net.ErrClosed rather than writing to a closed connection).
func (c *Client) Send(in sim.Intent) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return c.enc.Encode(ClientMsg{ProtoVersion: ProtoVersion, Intent: in})
}

// Snapshot returns the latest authoritative snapshot and whether one has arrived.
func (c *Client) Snapshot() (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.hasSnap
}

// AssignedID returns the player slot the server assigned this client in the Hello handshake,
// and whether that handshake has arrived yet. It lets the client know which entity is "me".
func (c *Client) AssignedID() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.assignedID, c.haveID
}

// Close ends the connection. It is idempotent and serialized against Send so the two can
// never touch the connection concurrently.
func (c *Client) Close() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
