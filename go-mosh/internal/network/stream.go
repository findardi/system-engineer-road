package network

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
)

// Fallback transport for networks where UDP cannot reach the server: the
// client tunnels the same encrypted datagrams through an SSH session to
// "gosh-server relay", which hands them to the daemon over local UDP.
// Frame on the stream: [2B length, big-endian][datagram].

var errNoStream = errors.New("relay not connected")

// StreamDialer opens a fresh byte stream to the server's relay. It is called
// again on every hop, so a dead SSH session after a network change is replaced.
type StreamDialer func() (io.ReadWriteCloser, error)

// UseStream switches c to the relay transport. It keeps the same Conn, so
// sequence numbers continue: restarting them would reuse OCB nonces, which
// breaks the encryption.
func (c *Conn) UseStream(dial StreamDialer) error {
	for _, s := range c.socks {
		s.Close()
	}
	c.socks = nil
	c.dialStream = dial
	return c.hop()
}

func (c *Conn) redialStream() error {
	if c.stream != nil {
		c.stream.Close()
		close(c.streamOut)
		c.stream = nil
	}
	s, err := c.dialStream()
	if err != nil {
		return err
	}
	// Writes go through a goroutine with a bounded queue so a stalled TCP
	// connection can never block the event loop; a full queue drops the
	// datagram, which the protocol already tolerates.
	out := make(chan []byte, 64)
	c.stream, c.streamOut = s, out
	go func() {
		for b := range out {
			if WriteFrame(s, b) != nil {
				for range out { // stream is dead: drain until the next hop closes it
				}
				return
			}
		}
	}()
	go func() {
		for {
			b, err := ReadFrame(s)
			if err != nil {
				return
			}
			c.in <- Datagram{b: b}
		}
	}()
	return nil
}

func WriteFrame(w io.Writer, b []byte) error {
	f := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(f, uint16(len(b)))
	copy(f[2:], b)
	_, err := w.Write(f)
	return err
}

func ReadFrame(r io.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(h[:]))
	_, err := io.ReadFull(r, b)
	return b, err
}

// Relay runs on the server: it bridges frames on r/w (the SSH session) to the
// daemon's UDP socket on this host. Packets stay encrypted end to end; the
// relay never holds the key.
func Relay(r io.Reader, w io.Writer, daemon string) error {
	conn, err := net.Dial("udp", daemon)
	if err != nil {
		return err
	}
	defer conn.Close()

	go func() {
		b := make([]byte, receiveMTU)
		for {
			n, err := conn.Read(b)
			if err != nil || WriteFrame(w, b[:n]) != nil {
				return
			}
		}
	}()
	for {
		b, err := ReadFrame(r)
		if errors.Is(err, io.EOF) {
			return nil // SSH session closed
		}
		if err != nil {
			return err
		}
		conn.Write(b) // datagram semantics: a lost write is a lost packet
	}
}
