package network

import (
	"errors"
	"io"
	"math"
	"net"
	"net/netip"
	"time"
)

const (
	receiveMTU         = 2048
	minRTO             = 50 * time.Millisecond
	maxRTO             = 1000 * time.Millisecond
	associationTimeout = 40 * time.Second
	portHopInterval    = 10 * time.Second
	maxOldSocketAge    = 60 * time.Second
	maxPortsOpen       = 10

	// sendMTU is the datagram budget: mosh's conservative IPv6 figure (1280
	// minus IPv6, two extension headers and UDP), used for every path.
	sendMTU = 1280 - 64
	// packetOverhead is what Packet.Seal adds: nonce, timestamps, OCB tag.
	packetOverhead = Overhead + 4
)

var errDirection = errors.New("packet has wrong direction")

// raw UDP datagram
type Datagram struct {
	b    []byte
	from netip.AddrPort
}

// Conn is not safe for concurrent use: one event loop calls Send and Receive.
// Only the socket readers run in their own goroutines.
type Conn struct {
	sess   *Session
	server bool
	out    Direction

	socks  []*net.UDPConn
	in     chan Datagram
	remote netip.AddrPort

	start       time.Time
	nextSeq     uint64
	expectedSeq uint64

	savedTS   uint16
	savedTSAt time.Time

	srtt, rttvar float64
	rttHit       bool

	lastHeard      time.Time
	lastPortChoice time.Time
	lastRoundTrip  time.Time

	dialStream StreamDialer // non-nil once UseStream switched to the SSH relay
	stream     io.ReadWriteCloser
	streamOut  chan []byte
}

func newConn(sess *Session, server bool) *Conn {
	c := &Conn{
		sess:   sess,
		server: server,
		out:    ToServer,
		in:     make(chan Datagram, 64),
		start:  time.Now(),
		srtt:   1000,
		rttvar: 500,
	}

	if server {
		c.out = ToClient
	}

	return c
}

func NewServer(sess *Session, sock *net.UDPConn) *Conn {
	c := newConn(sess, true)
	c.addSocket(sock)
	return c
}

func Dial(sess *Session, addr string) (*Conn, error) {
	remote, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, err
	}

	c := newConn(sess, false)
	c.remote = remote
	if err := c.hop(); err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Conn) Incoming() <-chan Datagram { return c.in }

func (c *Conn) addSocket(sock *net.UDPConn) {
	c.socks = append(c.socks, sock)
	go func() {
		for {
			b := make([]byte, receiveMTU)
			n, from, err := sock.ReadFromUDPAddrPort(b)
			if errors.Is(err, net.ErrClosed) {
				return
			}

			if err == nil {
				c.in <- Datagram{b[:n], from}
			}
		}
	}()
}

// hop gets a fresh path to the server: a new UDP source port, or a new SSH
// relay in stream mode.
func (c *Conn) hop() error {
	c.lastPortChoice = time.Now()
	if c.dialStream != nil {
		return c.redialStream()
	}

	sock, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}

	c.addSocket(sock)
	for len(c.socks) > maxPortsOpen {
		c.socks[0].Close()
		c.socks = c.socks[1:]
	}

	return nil
}

func (c *Conn) pruneSockets() {
	if len(c.socks) > 1 && time.Since(c.lastPortChoice) > maxOldSocketAge {
		for _, s := range c.socks[:len(c.socks)-1] {
			s.Close()
		}

		c.socks = c.socks[len(c.socks)-1:]
	}
}

func (c *Conn) Send(payload []byte) error {
	if !c.remote.IsValid() {
		return nil
	}

	p := Packet{
		Seq:            c.nextSeq,
		Dir:            c.out,
		Timestamp:      c.timestamp16(),
		TimestampReply: TimestampNone,
		Payload:        payload,
	}

	c.nextSeq++

	if held := time.Since(c.savedTSAt); held < time.Second {
		p.TimestampReply = c.savedTS + uint16(held.Milliseconds())
		c.savedTSAt = time.Time{}
	}

	wire := p.Seal(c.sess)
	var err error
	switch {
	case c.dialStream == nil:
		_, err = c.socks[len(c.socks)-1].WriteToUDPAddrPort(wire, c.remote)
	case c.stream == nil:
		err = errNoStream // last redial failed; the next hop retries
	default:
		select {
		case c.streamOut <- wire:
		default: // queue full: dropped, like a lost datagram
		}
	}

	if c.server {
		if time.Since(c.lastHeard) > associationTimeout {
			c.remote = netip.AddrPort{}
		}
	} else if time.Since(c.lastPortChoice) > portHopInterval && time.Since(c.lastRoundTrip) > portHopInterval {
		if herr := c.hop(); err == nil {
			err = herr
		}
	}

	return err
}

func (c *Conn) Receive(d Datagram) ([]byte, error) {
	p, err := Open(c.sess, d.b)
	if err != nil {
		return nil, err
	}

	if p.Dir == c.out {
		return nil, errDirection
	}

	// Out-of-order or replayed: hand the payload up, but never let it touch
	// timestamps or the remote address, or a replay could hijack the session.
	if p.Seq < c.expectedSeq {
		return p.Payload, nil
	}

	c.expectedSeq = p.Seq + 1

	now := time.Now()
	if p.Timestamp != TimestampNone {
		c.savedTS, c.savedTSAt = p.Timestamp, now
	}

	if p.TimestampReply != TimestampNone {
		c.updateRTT(float64(c.timestamp16() - p.TimestampReply))
	}

	c.lastHeard = now

	if c.server {
		c.remote = d.from
	} else {
		c.pruneSockets()
	}

	return p.Payload, nil
}

func (c *Conn) updateRTT(r float64) {
	if r >= 5000 {
		return
	}

	if !c.rttHit {
		c.srtt, c.rttvar, c.rttHit = r, r/2, true
		return
	}

	c.rttvar = 0.75*c.rttvar + 0.25*math.Abs(c.srtt-r)
	c.srtt = 0.875*c.srtt + 0.125*r
}

// ms is the connection clock in milliseconds, shared with Transport.
func (c *Conn) ms() int64 { return time.Since(c.start).Milliseconds() }

func (c *Conn) at(ms int64) time.Time { return c.start.Add(time.Duration(ms) * time.Millisecond) }

func (c *Conn) timestamp16() uint16 {
	ts := uint16(c.ms())
	if ts == TimestampNone {
		ts++
	}

	return ts
}

func (c *Conn) Timeout() time.Duration {
	rto := time.Duration(math.Ceil(c.srtt+4*c.rttvar)) * time.Millisecond
	return min(max(rto, minRTO), maxRTO)
}

func (c *Conn) SRTT() time.Duration {
	return time.Duration(c.srtt * float64(time.Millisecond))
}

func (c *Conn) RemoteAddr() netip.AddrPort { return c.remote }

func (c *Conn) LastHeard() time.Time { return c.lastHeard }
