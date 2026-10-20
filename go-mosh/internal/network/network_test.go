package network

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Vectors produced by mosh's own src/examples/encrypt (C++, OpenSSL AES-OCB).
const (
	moshKey    = "9XiKEBLQ1Klh+dyQ1DOOnA"
	moshWire   = "0000000000000005baa1b767830727f3725c61cdf033831d487bed9fa9b0ef022f02" // nonce 5, "hello mosh"
	moshPktKey = "SjN44OCwuNPD1IUwMyO5bA"
	moshPkt    = "80000000000000078dc93a17cf463e89af048329e9f238cc6eeb5e4031867c" // ToClient seq 7, ts 0x1234, reply none, "ls\r"
)

func session(t *testing.T, key string) *Session {
	t.Helper()
	s, err := NewSession(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCryptoMatchesMosh(t *testing.T) {
	want, _ := hex.DecodeString(moshWire)
	if got := session(t, moshKey).Encrypt(5, []byte("hello mosh")); !bytes.Equal(got, want) {
		t.Fatalf("encrypt:\n got %x\nwant %x", got, want)
	}
	nonce, pt, err := session(t, moshKey).Decrypt(want)
	if err != nil || nonce != 5 || string(pt) != "hello mosh" {
		t.Fatalf("decrypt: nonce=%d pt=%q err=%v", nonce, pt, err)
	}
}

func TestPacketMatchesMosh(t *testing.T) {
	wire, _ := hex.DecodeString(moshPkt)
	p, err := Open(session(t, moshPktKey), wire)
	if err != nil {
		t.Fatal(err)
	}
	if p.Seq != 7 || p.Dir != ToClient || p.Timestamp != 0x1234 || p.TimestampReply != TimestampNone || string(p.Payload) != "ls\r" {
		t.Fatalf("open: %+v", p)
	}
	if got := p.Seal(session(t, moshPktKey)); !bytes.Equal(got, wire) {
		t.Fatalf("seal:\n got %x\nwant %x", got, wire)
	}
}

func TestRejects(t *testing.T) {
	s := session(t, moshKey)
	wire, _ := hex.DecodeString(moshWire)
	for i := range wire { // any flipped bit, nonce included, must fail auth
		bad := bytes.Clone(wire)
		bad[i] ^= 1
		if _, _, err := s.Decrypt(bad); err != ErrAuth {
			t.Fatalf("flipped byte %d accepted", i)
		}
	}
	if _, _, err := s.Decrypt(wire[:Overhead-1]); err != ErrAuth {
		t.Fatal("short datagram accepted")
	}
	if _, _, err := session(t, moshPktKey).Decrypt(wire); err != ErrAuth {
		t.Fatal("wrong key accepted")
	}
	for _, k := range []string{"", "short", moshKey + "A", "9XiKEBLQ1Klh+dyQ1DOOnB"} { // last: non-canonical trailing bits
		if _, err := NewSession(k); err == nil {
			t.Fatalf("bad key %q accepted", k)
		}
	}
}
