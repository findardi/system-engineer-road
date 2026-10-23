package statesync

import "testing"

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
