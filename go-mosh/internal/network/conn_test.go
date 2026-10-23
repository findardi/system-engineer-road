package network

import (
	"net"
	"testing"
	"time"
)

func recv(t *testing.T, c *Conn) ([]byte, Datagram, error) {
	t.Helper()
	select {
	case d := <-c.Incoming():
		p, err := c.Receive(d)
		return p, d, err
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for datagram")
		return nil, Datagram{}, nil
	}
}

func pair(t *testing.T) (srv, cli *Conn) {
	t.Helper()
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv = NewServer(session(t, moshKey), sock)
	cli, err = Dial(session(t, moshKey), sock.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return srv, cli
}

func TestPingPongAndRTT(t *testing.T) {
	srv, cli := pair(t)
	cli.Send([]byte("ping"))
	if p, _, err := recv(t, srv); err != nil || string(p) != "ping" {
		t.Fatalf("server got %q, %v", p, err)
	}
	srv.Send([]byte("pong"))
	if p, _, err := recv(t, cli); err != nil || string(p) != "pong" {
		t.Fatalf("client got %q, %v", p, err)
	}
	if !cli.rttHit || cli.SRTT() > 100*time.Millisecond {
		t.Fatalf("no RTT sample on loopback: hit=%v srtt=%v", cli.rttHit, cli.SRTT())
	}
}

func TestRoamingAndReplay(t *testing.T) {
	srv, cli := pair(t)
	cli.Send([]byte("a"))
	_, first, _ := recv(t, srv)

	// Client "changes network": new source port, same session.
	cli.hop()
	cli.Send([]byte("b"))
	recv(t, srv)
	if srv.RemoteAddr() == first.from {
		t.Fatal("server did not follow the client to its new address")
	}
	roamed := srv.RemoteAddr()

	// Attacker replays the old datagram from yet another address.
	attacker, _ := net.ListenUDP("udp", nil)
	attacker.WriteToUDPAddrPort(first.b, cli.remote)
	if p, _, err := recv(t, srv); err != nil || string(p) != "a" {
		t.Fatalf("replay payload: %q, %v", p, err)
	}
	if srv.RemoteAddr() != roamed {
		t.Fatalf("replay hijacked session to %v", srv.RemoteAddr())
	}

	// Server reply reaches the client on its new port.
	srv.Send([]byte("c"))
	if p, _, err := recv(t, cli); err != nil || string(p) != "c" {
		t.Fatalf("client got %q, %v", p, err)
	}
}

func TestRejectsReflectedAndForged(t *testing.T) {
	srv, cli := pair(t)
	evil, _ := net.ListenUDP("udp", nil)
	to := cli.remote

	// Server's own ToClient packet bounced back to it.
	reflected := Packet{Seq: 99, Dir: ToClient, Payload: []byte("x")}.Seal(session(t, moshKey))
	evil.WriteToUDPAddrPort(reflected, to)
	if _, _, err := recv(t, srv); err != errDirection {
		t.Fatalf("reflected packet: err=%v", err)
	}

	evil.WriteToUDPAddrPort([]byte("garbage that is long enough to try"), to)
	if _, _, err := recv(t, srv); err != ErrAuth {
		t.Fatalf("forged packet: err=%v", err)
	}
	if srv.RemoteAddr().IsValid() {
		t.Fatal("unauthenticated packets set the remote address")
	}
}

func TestPortHopWhenStuck(t *testing.T) {
	_, cli := pair(t)
	cli.lastPortChoice = time.Now().Add(-portHopInterval - time.Second)
	cli.lastRoundTrip = time.Time{}
	cli.Send([]byte("x"))
	if len(cli.socks) != 2 {
		t.Fatalf("want 2 sockets after hop, got %d", len(cli.socks))
	}
	for range maxPortsOpen + 5 {
		cli.hop()
	}
	if len(cli.socks) != maxPortsOpen {
		t.Fatalf("sockets not capped: %d", len(cli.socks))
	}
}
