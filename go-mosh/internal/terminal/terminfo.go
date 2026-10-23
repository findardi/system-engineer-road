package terminal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewDisplayForTerminal returns a display for the user's real terminal,
// with capabilities read from terminfo like mosh's Display(true)
// (src/terminal/terminaldisplayinit.cc). If the terminal is unknown it falls
// back to plain sequences every terminal understands.
func NewDisplayForTerminal(term string) *Display {
	d := &Display{}
	// terminfo has no reliable title capability; mosh uses this list.
	for _, p := range []string{"xterm", "rxvt", "kterm", "Eterm", "alacritty", "screen", "tmux"} {
		if strings.HasPrefix(term, p) {
			d.hasTitle = true
		}
	}
	ti, err := loadTerminfo(term)
	if err != nil {
		return d
	}
	d.hasBCE = ti.flag(capBCE)
	d.hasECH = ti.str(capECH) != ""
	if os.Getenv("GOSH_NO_TERM_INIT") == "" {
		d.smcup, d.rmcup = ti.str(capSmcup), ti.str(capRmcup)
	}
	return d
}

// Capability indices in the compiled terminfo format, see term(5).
const (
	capBCE   = 28 // boolean: back_color_erase
	capSmcup = 28 // string: enter_ca_mode
	capECH   = 37 // string: erase_chars
	capRmcup = 40 // string: exit_ca_mode
)

type terminfo struct {
	bools   []byte
	strings []string
}

func (t *terminfo) flag(i int) bool { return i < len(t.bools) && t.bools[i] == 1 }

func (t *terminfo) str(i int) string {
	if i < len(t.strings) {
		return t.strings[i]
	}
	return ""
}

func terminfoDirs() []string {
	var dirs []string
	if d := os.Getenv("TERMINFO"); d != "" {
		dirs = append(dirs, d)
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".terminfo"))
	}
	if v := os.Getenv("TERMINFO_DIRS"); v != "" {
		dirs = append(dirs, strings.Split(v, ":")...)
	}
	return append(dirs, "/etc/terminfo", "/lib/terminfo", "/usr/share/terminfo", "/usr/lib/terminfo")
}

func loadTerminfo(term string) (*terminfo, error) {
	if term == "" || strings.ContainsAny(term, "/") {
		return nil, errors.New("bad TERM")
	}
	for _, dir := range terminfoDirs() {
		// Linux uses the first letter as directory, macOS its hex code.
		for _, sub := range []string{term[:1], fmt.Sprintf("%x", term[0])} {
			if b, err := os.ReadFile(filepath.Join(dir, sub, term)); err == nil {
				return parseTerminfo(b)
			}
		}
	}
	return nil, fmt.Errorf("no terminfo for %q", term)
}

// parseTerminfo reads the booleans and strings of a compiled entry.
func parseTerminfo(b []byte) (*terminfo, error) {
	bad := errors.New("malformed terminfo")
	if len(b) < 12 {
		return nil, bad
	}
	h := make([]int, 6)
	for i := range h {
		h[i] = int(binary.LittleEndian.Uint16(b[2*i:]))
	}
	numSize := 2
	switch h[0] {
	case 0o432:
	case 0o1036: // ncurses 6.1+: 32-bit numbers
		numSize = 4
	default:
		return nil, bad
	}
	namesSize, boolCount, numCount, strCount, tableSize := h[1], h[2], h[3], h[4], h[5]
	pos := 12 + namesSize
	bools := pos
	pos += boolCount
	pos += pos % 2 // numbers start on an even byte
	pos += numCount * numSize
	offsets := pos
	table := offsets + 2*strCount
	if table+tableSize > len(b) {
		return nil, bad
	}
	t := &terminfo{bools: b[bools : bools+boolCount], strings: make([]string, strCount)}
	for i := range t.strings {
		off := int(int16(binary.LittleEndian.Uint16(b[offsets+2*i:])))
		if off < 0 || off >= tableSize { // -1 absent, -2 cancelled
			continue
		}
		s := b[table+off : table+tableSize]
		if end := strings.IndexByte(string(s), 0); end >= 0 {
			s = s[:end]
		}
		t.strings[i] = stripPadding(string(s))
	}
	return t, nil
}

// stripPadding removes delay specifications like "$<5>", which are timing
// hints for slow terminals, not bytes to send.
func stripPadding(s string) string {
	for {
		i := strings.Index(s, "$<")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}
