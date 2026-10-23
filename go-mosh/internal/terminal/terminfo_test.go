package terminal

import (
	"os"
	"testing"
)

// Expected values come from `infocmp -1`.
func TestDisplayForTerminal(t *testing.T) {
	if _, err := os.Stat("/usr/share/terminfo/x/xterm-256color"); err != nil {
		t.Skip("no system terminfo")
	}
	for _, c := range []struct {
		term            string
		ech, bce, title bool
		smcup, rmcup    string
	}{
		{"xterm-256color", true, true, true, "\033[?1049h\033[22;0;0t", "\033[?1049l\033[23;0;0t"},
		{"tmux-256color", false, false, true, "\033[?1049h", "\033[?1049l"},
		{"linux", true, true, false, "", ""},
		{"dumb", false, false, false, "", ""},
		{"no-such-terminal", false, false, false, "", ""},
	} {
		d := NewDisplayForTerminal(c.term)
		if d.hasECH != c.ech || d.hasBCE != c.bce || d.hasTitle != c.title || d.smcup != c.smcup || d.rmcup != c.rmcup {
			t.Errorf("%s: got ech=%v bce=%v title=%v smcup=%q rmcup=%q", c.term, d.hasECH, d.hasBCE, d.hasTitle, d.smcup, d.rmcup)
		}
	}
}

func TestParseTerminfoRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("short"), make([]byte, 64)} {
		if _, err := parseTerminfo(b); err == nil {
			t.Errorf("accepted %q", b)
		}
	}
}

func TestStripPadding(t *testing.T) {
	for in, want := range map[string]string{"\033[H$<156>\033[J": "\033[H\033[J", "$<5*/>x$<2>": "x", "plain": "plain", "$<open": "$<open"} {
		if got := stripPadding(in); got != want {
			t.Errorf("stripPadding(%q) = %q, want %q", in, got, want)
		}
	}
}
