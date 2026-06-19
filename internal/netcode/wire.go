package netcode

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
)

// maxClientFrameBytes caps a single client->server ClientFrame on the wire. The server reads
// UNTRUSTED frames, so each is length-prefixed and rejected past this bound -- a hostile peer
// cannot ship a multi-gigabyte gob string and OOM the server. 64KiB is far above any real frame
// (a ClientFrame is an intent plus, at most, a fixed-shape MatchSetup and a few short strings).
const maxClientFrameBytes = 1 << 16

// writeFrame gob-encodes v into a length-prefixed frame: a 4-byte big-endian length, then the
// gob bytes. The client uses this for every ClientFrame so the server can bound each read. (A
// fresh per-message encoder re-sends type info each frame -- a small, LAN-acceptable cost that
// buys a hard size bound on the untrusted path.)
func writeFrame(w io.Writer, v any) error {
	var body bytes.Buffer
	if err := gob.NewEncoder(&body).Encode(v); err != nil {
		return err
	}
	if body.Len() > maxClientFrameBytes {
		return fmt.Errorf("frame too large to send: %d bytes", body.Len())
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(body.Len()))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body.Bytes())
	return err
}

// readFrame reads one length-prefixed frame and gob-decodes it into v, rejecting any frame whose
// declared length exceeds max (so the allocation is bounded before any bytes are read).
func readFrame(r io.Reader, v any, max uint32) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > max {
		return fmt.Errorf("client frame too large: %d > %d bytes", n, max)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return gob.NewDecoder(bytes.NewReader(buf)).Decode(v)
}
