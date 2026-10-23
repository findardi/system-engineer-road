package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dump prints a framebuffer in the same format as the C++ oracle that
// produced testdata/*.out by feeding testdata/*.in to mosh's own emulator.
func dump(e *Emulator, reply []byte) string {
	fb := e.Framebuffer()
	ds := &fb.DS
	var b strings.Builder
	b01 := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	hex := func(s string) string { return fmt.Sprintf("%x", s) }
	fmt.Fprintf(&b, "size %d %d\n", ds.width, ds.height)
	fmt.Fprintf(&b, "cursor %d %d visible %d wrapnext %d\n", ds.cursorRow, ds.cursorCol, b01(ds.CursorVisible), b01(ds.NextPrintWillWrap))
	fmt.Fprintf(&b, "modes origin %d autowrap %d insert %d reverse %d paste %d mouse %d focus %d altscroll %d enc %d appcursor %d\n",
		b01(ds.OriginMode), b01(ds.AutoWrapMode), b01(ds.InsertMode), b01(ds.ReverseVideo), b01(ds.BracketedPaste),
		ds.MouseReporting, b01(ds.MouseFocusEvent), b01(ds.MouseAltScroll), ds.MouseEncoding, b01(ds.AppCursorKeys))
	fmt.Fprintf(&b, "scroll %d %d pen %s link %s\n", ds.scrollTop, ds.scrollBottom, ds.renditions.SGR()[1:], hex(ds.link.OSC8()))
	fmt.Fprintf(&b, "title %s icon %s clip %s bell %d\n", hex(string(fb.windowTitle)), hex(string(fb.iconName)), hex(string(fb.clipboard)), fb.bellCount)
	fmt.Fprintf(&b, "reply %s\n", hex(string(reply)))
	blank := Renditions{}.SGR()
	for y, row := range fb.rows {
		var line strings.Builder
		for x := range row.cells {
			c := &row.cells[x]
			sgr := c.renditions.SGR()
			if c.empty() && sgr == blank && !c.wide && !c.fallback && !c.wrap && c.link == nil {
				continue
			}
			fmt.Fprintf(&line, " %d:", x)
			if c.empty() {
				line.WriteString("_")
			} else {
				line.WriteString(hex(c.grapheme()))
			}
			if sgr != blank {
				line.WriteString("/" + sgr[2:])
			}
			for _, f := range []struct {
				on  bool
				tag string
			}{{c.wide, "/W"}, {c.fallback, "/F"}, {c.wrap, "/R"}} {
				if f.on {
					line.WriteString(f.tag)
				}
			}
			if c.link != nil {
				line.WriteString("/L" + hex(c.link.OSC8()))
			}
		}
		if line.Len() > 0 {
			fmt.Fprintf(&b, "row %d%s\n", y, line.String())
		}
	}
	return b.String()
}

// TestMatchesMosh replays each input and compares with mosh's framebuffer.
// Input names are NAME.WIDTHxHEIGHT.in.
func TestMatchesMosh(t *testing.T) {
	ins, _ := filepath.Glob("testdata/*.in")
	if len(ins) == 0 {
		t.Fatal("no testdata")
	}
	for _, in := range ins {
		t.Run(filepath.Base(in), func(t *testing.T) {
			var w, h int
			parts := strings.Split(filepath.Base(in), ".")
			fmt.Sscanf(parts[len(parts)-2], "%dx%d", &w, &h)
			data, _ := os.ReadFile(in)
			want, _ := os.ReadFile(strings.TrimSuffix(in, ".in") + ".out")
			e := NewEmulator(w, h)
			got := dump(e, e.Write(data))
			if got != string(want) {
				t.Errorf("framebuffer differs from mosh\n--- got\n%s--- want\n%s", got, want)
			}
		})
	}
}

func TestCloneIsIndependent(t *testing.T) {
	e := NewEmulator(10, 4)
	e.Write([]byte("hello\r\nworld"))
	snap := &Emulator{fb: e.Framebuffer().Clone()}
	before := dump(snap, nil)
	e.Write([]byte("\x1b[1;1Hxx\x1b[2J\x1b[31mnew\x1b[L"))
	e.Resize(6, 2)
	if dump(snap, nil) != before {
		t.Fatal("clone changed when the original was written to")
	}
	if snap.Framebuffer().Equal(e.Framebuffer()) {
		t.Fatal("frames should differ")
	}
}

func TestUserByteCursorKeys(t *testing.T) {
	e := NewEmulator(10, 2)
	keys := func() string {
		var out []byte
		for _, c := range []byte("\x1bOAx\x1bOH") {
			out = append(out, e.UserByte(c)...)
		}
		return string(out)
	}
	if got := keys(); got != "\x1b[Ax\x1bOH" { // only A-D are cursor keys
		t.Fatalf("normal mode: %q", got)
	}
	e.Write([]byte("\x1b[?1h")) // application cursor keys
	if got := keys(); got != "\x1bOAx\x1bOH" {
		t.Fatalf("application mode: %q", got)
	}
}

func TestRuneWidth(t *testing.T) {
	for r, want := range map[rune]int{'a': 1, 'é': 1, '中': 2, '\U0001F600': 2, '́': 0, '‍': 0, '­': 1, '\a': -1, '؀': 1} {
		if got := runeWidth(r); got != want {
			t.Errorf("runeWidth(%U) = %d, want %d", r, got, want)
		}
	}
}
