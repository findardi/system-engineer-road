package statesync

import (
	"encoding/hex"
	"testing"
)

// protoc --encode=HostBuffers.HostMessage of hostbytes "$ ls\r\n".
const wantHostDiff = "0a0a1208220624206c730d0a"

func TestHostStreamDiffMatchesProtoc(t *testing.T) {
	var base, h HostStream
	base.PushBytes([]byte("prompt"))
	h.PushBytes([]byte("prompt$ ls\r\n"))
	diff := h.DiffFrom(&base)
	if got := hex.EncodeToString(diff); got != wantHostDiff {
		t.Fatalf("diff:\n got %s\nwant %s", got, wantHostDiff)
	}
	peer := base.Clone()
	if err := peer.Apply(diff); err != nil || !peer.Equal(&h) {
		t.Fatalf("apply: %q %v", peer.Bytes(), err)
	}
	if h.DiffFrom(&h) != nil {
		t.Fatal("no change must give an empty diff")
	}
}
