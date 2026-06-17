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

	mu      sync.Mutex
	latest  Snapshot
	hasSnap bool
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
		var snap Snapshot
		if err := dec.Decode(&snap); err != nil {
			return
		}
		c.mu.Lock()
		c.latest = snap
		c.hasSnap = true
		c.mu.Unlock()
	}
}

// Send transmits the local player's intent for this tick.
func (c *Client) Send(in sim.Intent) error {
	return c.enc.Encode(ClientMsg{Intent: in})
}

// Snapshot returns the latest authoritative snapshot and whether one has arrived.
func (c *Client) Snapshot() (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.hasSnap
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }
