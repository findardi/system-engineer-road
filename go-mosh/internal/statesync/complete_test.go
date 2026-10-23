package statesync

import (
	"testing"
	"time"
)

func TestCompleteDiffApply(t *testing.T) {
	srv := NewComplete(20, 4)
	cli := srv.Clone()
	if srv.DiffFrom(cli) != nil {
		t.Fatal("equal states must give an empty diff")
	}

	base := srv.Clone()
	if reply := srv.Act([]byte("hi\x1b[31m red\x1b[6n")); string(reply) != "\x1b[1;7R" {
		t.Fatalf("reply to the program: %q", reply)
	}
	srv.Emulator().Resize(30, 6)
	srv.echoAck = 5
	if err := cli.Apply(srv.DiffFrom(base)); err != nil {
		t.Fatal(err)
	}
	fb, cfb := srv.Framebuffer(), cli.Framebuffer()
	if cfb.DS.Width() != 30 || cfb.DS.Height() != 6 {
		t.Fatalf("resize not applied: %dx%d", cfb.DS.Width(), cfb.DS.Height())
	}
	if cli.EchoAck() != 5 {
		t.Fatalf("echo ack %d", cli.EchoAck())
	}
	for x, want := range "hi red" {
		if got := cfb.Cell(0, x); got.Equal(fb.Cell(0, x)) == false {
			t.Fatalf("cell %d: want %q", x, want)
		}
	}
}

func TestCompleteCloneIsIndependent(t *testing.T) {
	a := NewComplete(10, 3)
	a.Act([]byte("one"))
	b := a.Clone()
	a.Act([]byte("\r\ntwo"))
	if b.Equal(a) || b.Framebuffer().Cell(1, 0).Equal(a.Framebuffer().Cell(1, 0)) {
		t.Fatal("clone changed with the original")
	}
}

func TestEchoAck(t *testing.T) {
	c := NewComplete(10, 2)
	t0 := time.Now()
	if c.SetEchoAck(t0) || c.EchoWait(t0) != time.Hour {
		t.Fatal("no input yet: nothing to ack, nothing to wait for")
	}
	c.RegisterInputFrame(3, t0)
	c.RegisterInputFrame(4, t0.Add(30*time.Millisecond))
	if c.SetEchoAck(t0.Add(40*time.Millisecond)) || c.EchoAck() != 0 {
		t.Fatal("acked before the program had echoTimeout to echo")
	}
	if !c.SetEchoAck(t0.Add(50*time.Millisecond)) || c.EchoAck() != 3 {
		t.Fatalf("frame 3 should be acked after 50ms, ack=%d", c.EchoAck())
	}
	if w := c.EchoWait(t0.Add(50 * time.Millisecond)); w != 30*time.Millisecond {
		t.Fatalf("frame 4 is due in 30ms, got %v", w)
	}
	if !c.SetEchoAck(t0.Add(time.Second)) || c.EchoAck() != 4 || c.SetEchoAck(t0.Add(2*time.Second)) {
		t.Fatalf("frame 4 acked once, then stable; ack=%d", c.EchoAck())
	}
	// The ack travels in the diff.
	cli := NewComplete(10, 2)
	if err := cli.Apply(c.DiffFrom(NewComplete(10, 2))); err != nil || cli.EchoAck() != 4 {
		t.Fatalf("client echo ack %d, %v", cli.EchoAck(), err)
	}
}
