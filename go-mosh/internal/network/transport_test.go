package network

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/findardi/system-engineer-road/go-mosh/internal/pb/transportpb"
	"github.com/findardi/system-engineer-road/go-mosh/internal/statesync"
)

type userTransport = Transport[*statesync.UserStream, *statesync.UserStream]

func transportPair(t *testing.T) (cli, srv *userTransport) {
	t.Helper()
	srvConn, cliConn := pair(t)
	empty := &statesync.UserStream{}
	cli = NewTransport(cliConn, empty, empty)
	srv = NewTransport(srvConn, empty, empty)
	return cli, srv
}

// pump runs both event loops, dropping each datagram with probability loss,
// until done reports true.
func pump(t *testing.T, cli, srv *userTransport, loss float64, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("states did not converge")
		}
		cli.Tick()
		srv.Tick()
		wait := min(cli.WaitTime(), srv.WaitTime(), 10*time.Millisecond)
		select {
		case d := <-cli.conn.Incoming():
			if rand.Float64() >= loss {
				cli.Receive(d)
			}
		case d := <-srv.conn.Incoming():
			if rand.Float64() >= loss {
				srv.Receive(d)
			}
		case <-time.After(wait):
		}
	}
}

func remoteBytes(tr *userTransport, got *[]byte) []byte {
	var u statesync.UserStream
	u.Apply(tr.RemoteDiff())
	for _, e := range u.Events() {
		*got = append(*got, e.Byte)
	}
	return *got
}

func TestSSPConvergesUnderLoss(t *testing.T) {
	cli, srv := transportPair(t)
	want := []byte("echo hello world; ls -la /tmp\r")
	var got []byte
	typed := 0
	pump(t, cli, srv, 0.3, func() bool {
		if typed < len(want) { // one keystroke per loop turn, like a fast typist
			cli.Current().PushBytes(want[typed : typed+1])
			typed++
		}
		return bytes.Equal(remoteBytes(srv, &got), want)
	})
	// Client learns of the server's ack and stops resending.
	pump(t, cli, srv, 0, func() bool { return cli.sent[0].state.Equal(cli.Current()) })
}

func TestSSPFragmentsLargeDiffUnderLoss(t *testing.T) {
	cli, srv := transportPair(t)
	want := make([]byte, 20000) // incompressible: forces many fragments
	for i := range want {
		want[i] = byte(rand.Uint32())
	}
	cli.Current().PushBytes(want)
	var got []byte
	pump(t, cli, srv, 0.1, func() bool { return bytes.Equal(remoteBytes(srv, &got), want) })
}

func TestSSPBothDirections(t *testing.T) {
	cli, srv := transportPair(t)
	var atSrv, atCli []byte
	cli.Current().PushBytes([]byte("ping"))
	pump(t, cli, srv, 0.2, func() bool {
		if bytes.Equal(remoteBytes(srv, &atSrv), []byte("ping")) && len(srv.Current().Events()) == 0 {
			srv.Current().PushBytes([]byte("pong")) // server reacts to input, like a shell echo
		}
		return bytes.Equal(remoteBytes(cli, &atCli), []byte("pong"))
	})
}

func TestFragmentAssembly(t *testing.T) {
	var f fragmenter
	diff := make([]byte, 3000) // incompressible, so it really needs several fragments
	for i := range diff {
		diff[i] = byte(rand.Uint32())
	}
	frags, err := f.split(&transportpb.Instruction{Diff: diff}, 600)
	if err != nil || len(frags) < 2 {
		t.Fatalf("want several fragments, got %d (%v)", len(frags), err)
	}

	var a assembly
	// Stale fragment of an older instruction must be discarded on a new id.
	a.add(fragment{id: frags[0].id - 1, num: 0, contents: []byte("junk")})
	// Deliver out of order (final first), with a duplicate in the middle.
	n := len(frags)
	order := append([]fragment{frags[n-1], frags[0]}, frags[:n-1]...)
	for i, fr := range order {
		out, err := a.add(parseMust(t, fr.marshal()))
		if err != nil {
			t.Fatal(err)
		}
		if i < len(order)-1 {
			if out != nil {
				t.Fatalf("assembled early at %d", i)
			}
			continue
		}
		if out == nil || !bytes.Equal(out.GetDiff(), diff) {
			t.Fatal("reassembled diff differs")
		}
	}
}

func parseMust(t *testing.T, b []byte) fragment {
	t.Helper()
	f, err := parseFragment(b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
