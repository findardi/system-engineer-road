package network

import (
	"io"
	"net"
	"testing"
)

// pipeStream is one end of an in-process "SSH session".
type pipeStream struct {
	io.Reader
	io.WriteCloser
}

// relayDialer runs Relay in-process against daemon, like "gosh-server relay" over SSH.
func relayDialer(daemon string) StreamDialer {
	return func() (io.ReadWriteCloser, error) {
		toRelay, fromClient := io.Pipe()
		toClient, fromRelay := io.Pipe()
		go func() {
			Relay(toRelay, fromRelay, daemon)
			fromRelay.Close()
		}()
		return pipeStream{toClient, fromClient}, nil
	}
}

func TestRelayFallbackAndRedial(t *testing.T) {
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(session(t, moshKey), sock)
	// Client "dials" an unreachable UDP address, as behind a NAT that drops UDP.
	cli, err := Dial(session(t, moshKey), "192.0.2.1:9")
	if err != nil {
		t.Fatal(err)
	}
	cli.Send([]byte("lost over udp"))

	if err := cli.UseStream(relayDialer(sock.LocalAddr().String())); err != nil {
		t.Fatal(err)
	}
	cli.Send([]byte("via relay"))
	if p, _, err := recv(t, srv); err != nil || string(p) != "via relay" {
		t.Fatalf("server got %q, %v", p, err)
	}
	firstRelay := srv.RemoteAddr()
	srv.Send([]byte("back"))
	if p, _, err := recv(t, cli); err != nil || string(p) != "back" {
		t.Fatalf("client got %q, %v", p, err)
	}

	// Network change: old SSH session dies, hop starts a new relay. The server
	// must follow it, which only happens if sequence numbers kept increasing.
	cli.hop()
	cli.Send([]byte("after redial"))
	if p, _, err := recv(t, srv); err != nil || string(p) != "after redial" {
		t.Fatalf("server got %q, %v", p, err)
	}
	if srv.RemoteAddr() == firstRelay {
		t.Fatal("server did not follow the new relay")
	}
	srv.Send([]byte("back again"))
	if p, _, err := recv(t, cli); err != nil || string(p) != "back again" {
		t.Fatalf("client got %q, %v", p, err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		WriteFrame(w, []byte("abc"))
		WriteFrame(w, nil)
		w.Close()
	}()
	if b, err := ReadFrame(r); err != nil || string(b) != "abc" {
		t.Fatalf("frame 1: %q %v", b, err)
	}
	if b, err := ReadFrame(r); err != nil || len(b) != 0 {
		t.Fatalf("frame 2: %q %v", b, err)
	}
	if _, err := ReadFrame(r); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}
