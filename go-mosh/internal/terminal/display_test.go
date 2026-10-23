package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewFrameMatchesMosh checks Display.NewFrame byte for byte against
// mosh's Display::new_frame. Each testdata/frames/NAME.WxH.SPLIT.W2xH2.INIT.out
// holds mosh's output for: frame A = the first SPLIT bytes of
// testdata/NAME.WxH.in, then resize to W2xH2, frame B = all of it.
func TestNewFrameMatchesMosh(t *testing.T) {
	outs, _ := filepath.Glob("testdata/frames/*.out")
	if len(outs) == 0 {
		t.Fatal("no testdata")
	}
	d := NewDisplay()
	for _, out := range outs {
		t.Run(filepath.Base(out), func(t *testing.T) {
			f := strings.Split(strings.TrimSuffix(filepath.Base(out), ".out"), ".")
			name, size := strings.Join(f[:len(f)-4], "."), f[len(f)-4]
			var w, h, split, w2, h2, init int
			fmt.Sscanf(size, "%dx%d", &w, &h)
			fmt.Sscanf(f[len(f)-3], "%d", &split)
			fmt.Sscanf(f[len(f)-2], "%dx%d", &w2, &h2)
			fmt.Sscanf(f[len(f)-1], "%d", &init)
			data, err := os.ReadFile(fmt.Sprintf("testdata/%s.%s.in", name, size))
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(out)

			e := NewEmulator(w, h)
			e.Write(data[:split])
			a := e.Clone()
			e.Resize(w2, h2)
			e.Write(data[split:])
			if got := d.NewFrame(init == 1, a.Framebuffer(), e.Framebuffer()); got != string(want) {
				t.Errorf("differs from mosh\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestNewFrameRedrawsScreen applies diffs the way an SSP client does: its
// copy of the screen is built only from diffs.
func TestNewFrameRedrawsScreen(t *testing.T) {
	d := NewDisplay()
	srv, cli := NewEmulator(20, 5), NewEmulator(20, 5)
	steps := []string{
		"$ ls\r\nfoo  bar  \x1b[1;34mdir\x1b[0m\r\n$ ",
		"\x1b[?1049h\x1b[2J\x1b[H\x1b[7m top \x1b[0m\x1b[2;1Hpid 1\x1b[3;1Hpid 2",
		"\x1b[2;1Hpid 9\x1b[5;1H\x1b[?25l",
		"\r\n\r\n\r\nscrolled\r\n",
		"\x1b]0;title\x07\x1b[?2004h\x1b[?1000h\x1b[?25h中文",
	}
	for i, s := range steps {
		before := srv.Clone()
		srv.Write([]byte(s))
		cli.Write([]byte(d.NewFrame(true, before.Framebuffer(), srv.Framebuffer())))
		for y := range srv.fb.rows {
			for x := range srv.fb.rows[y].cells {
				c, o := &srv.fb.rows[y].cells[x], &cli.fb.rows[y].cells[x]
				if c.grapheme() != o.grapheme() || c.renditions != o.renditions || c.wide != o.wide {
					t.Fatalf("step %d: cell %d,%d is %q, want %q", i, y, x, o.grapheme(), c.grapheme())
				}
			}
		}
		if !cli.fb.DS.Equal(&srv.fb.DS) || string(cli.fb.windowTitle) != string(srv.fb.windowTitle) {
			t.Fatalf("step %d: cursor, modes or title not reproduced", i)
		}
	}
}
