package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPredictionMatchesMosh replays scripts through the engine and compares
// every painted frame with mosh's own PredictionEngine (see
// predict_harness_test.go for the script format).
func TestPredictionMatchesMosh(t *testing.T) {
	scripts, _ := filepath.Glob("testdata/predict/*.script")
	if len(scripts) == 0 {
		t.Fatal("no testdata")
	}
	for _, f := range scripts {
		t.Run(filepath.Base(f), func(t *testing.T) {
			script, _ := os.ReadFile(f)
			want, _ := os.ReadFile(strings.TrimSuffix(f, ".script") + ".out")
			if got := runPredictScript(string(script)); got != string(want) {
				t.Errorf("frames differ from mosh\n--- got\n%s--- want\n%s", got, want)
			}
		})
	}
}

func TestPredictionEchoesTyping(t *testing.T) {
	e := NewEmulator(20, 3)
	e.Write([]byte("$ "))
	pe := NewPredictionEngine()
	pe.Mode = PredictAlways
	paint := func() *Framebuffer {
		fb := e.Framebuffer().Clone()
		pe.Cull(fb)
		pe.Apply(fb)
		return fb
	}
	shown := paint()
	// First keystroke confirms the epoch once echoed; afterwards typing shows at once.
	pe.SetFrameSent(0)
	pe.NewUserByte('l', shown)
	e.Write([]byte("l"))
	pe.SetFrameLateAcked(1)
	shown = paint()
	pe.SetFrameSent(1)
	pe.NewUserByte('s', shown)
	shown = paint()
	if got := shown.Cell(0, 3).contents; got != "s" {
		t.Fatalf("prediction not shown: %q", got)
	}
	if shown.DS.cursorCol != 4 {
		t.Fatalf("cursor not predicted: col %d", shown.DS.cursorCol)
	}
	if !shown.Cell(0, 3).renditions.attr(attrUnderlined) {
		t.Fatal("slow link (send interval 250ms) should underline predictions")
	}
	if e.Framebuffer().Cell(0, 3).contents != "" {
		t.Fatal("prediction leaked into the server's screen")
	}
	// A wrong guess is withdrawn once the server's echo arrives.
	e.Write([]byte("X"))
	pe.SetFrameLateAcked(2)
	if got := paint().Cell(0, 3).contents; got != "X" {
		t.Fatalf("wrong prediction not withdrawn: %q", got)
	}
}

func TestDrawNotice(t *testing.T) {
	fb := NewFramebuffer(12, 2)
	DrawNotice(fb, "gosh: hi 中 and more")
	c := fb.Cell(0, 0)
	if c.contents != "g" || !c.renditions.attr(attrBold) || c.renditions.fg != 37 || c.renditions.bg != 44 {
		t.Fatalf("bar cell: %+v", *c)
	}
	// "gosh: hi " fills columns 0-8, then the wide character takes 9-10.
	if !fb.Cell(0, 9).wide || fb.Cell(0, 11).contents != " " {
		t.Fatal("wide character or clipping wrong")
	}
	if fb.Cell(1, 0).contents != "" {
		t.Fatal("bar spilled onto row 1")
	}
}
