package statesync

import (
	"encoding/hex"
	"testing"
)

// protoc --encode=ClientBuffers.UserMessage of keystroke "ab", resize 80x24, keystroke "c".
const wantDiff = "0a061204220261620a061a04285030180a051203220163"

func TestUserStreamDiffMatchesProtoc(t *testing.T) {
	var u UserStream
	u.PushBytes([]byte("ab"))
	u.PushResize(80, 24)
	u.PushBytes([]byte("c"))
	diff := u.DiffFrom(&UserStream{})
	if got := hex.EncodeToString(diff); got != wantDiff {
		t.Fatalf("diff:\n got %s\nwant %s", got, wantDiff)
	}
	var v UserStream
	if err := v.Apply(diff); err != nil || !v.Equal(&u) {
		t.Fatalf("apply: %+v %v", v.events, err)
	}
}

func TestUserStreamIncrementalAndSubtract(t *testing.T) {
	var base, u UserStream
	base.PushBytes([]byte("ls"))
	u.PushBytes([]byte("ls -la\r"))
	peer := base.Clone()
	peer.Apply(u.DiffFrom(&base))
	if !peer.Equal(&u) {
		t.Fatal("incremental diff did not reproduce state")
	}
	u.Subtract(&base)
	if string(bytesOf(u.events)) != " -la\r" {
		t.Fatalf("subtract: %q", bytesOf(u.events))
	}
	u.Subtract(&u)
	if len(u.events) != 0 || u.DiffFrom(&UserStream{}) != nil {
		t.Fatal("self-subtract should empty the stream")
	}
}

func bytesOf(es []UserEvent) []byte {
	var b []byte
	for _, e := range es {
		b = append(b, e.Byte)
	}
	return b
}
