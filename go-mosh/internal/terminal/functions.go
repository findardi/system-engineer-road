package terminal

import (
	"fmt"
	"strconv"
	"strings"
)

// Terminal functions run by CSI, escape and control characters, ported from
// mosh (src/terminal/terminalfunctions.cc, terminaldispatcher.cc).

const (
	paramMax         = 65535 // stops evil sequences from causing long loops
	maxClipboardSize = 16 * 1024
)

type functionType uint8

const (
	fnEscape functionType = iota
	fnCSI
	fnControl
)

type function struct {
	run        func(fb *Framebuffer, d *dispatcher)
	clearsWrap bool // most functions cancel a pending autowrap
}

// registry maps the collected dispatch characters (or the control
// character) to its function, one map per function type.
var registry = [3]map[string]function{fnEscape: {}, fnCSI: {}, fnControl: {}}

func register(t functionType, key string, run func(*Framebuffer, *dispatcher), clearsWrap bool) {
	registry[t][key] = function{run, clearsWrap}
}

type dispatcher struct {
	params        []byte
	parsed        []int
	isParsed      bool
	dispatchChars []byte
	osc           []rune
	toHost        []byte // replies to the application, e.g. cursor position reports
}

func (d *dispatcher) newParamChar(ch rune) {
	if len(d.params) < 100 { // enough for 16 five-char params plus 15 semicolons
		d.params = append(d.params, byte(ch))
	}
	d.isParsed = false
}

func (d *dispatcher) collect(ch rune) {
	if len(d.dispatchChars) < 8 && ch <= 255 { // never should need more than 2
		d.dispatchChars = append(d.dispatchChars, byte(ch))
	}
}

func (d *dispatcher) clear() {
	d.params, d.dispatchChars, d.isParsed = d.params[:0], d.dispatchChars[:0], false
}

func (d *dispatcher) parseParams() {
	if d.isParsed {
		return
	}
	d.parsed = d.parsed[:0]
	for _, seg := range strings.Split(string(d.params), ";") {
		v, err := strconv.Atoi(leadingDigits(seg))
		if err != nil || v > paramMax {
			v = -1
		}
		d.parsed = append(d.parsed, v)
	}
	d.isParsed = true
}

// leadingDigits mirrors strtol: parse the leading digits, ignore the rest.
func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// param returns parameter n, or def when it is missing, empty or zero.
func (d *dispatcher) param(n, def int) int {
	d.parseParams()
	v := def
	if n < len(d.parsed) {
		v = d.parsed[n]
	}
	if v < 1 {
		v = def
	}
	return v
}

func (d *dispatcher) paramCount() int {
	d.parseParams()
	return len(d.parsed)
}

func (d *dispatcher) dispatch(t functionType, ch rune, fb *Framebuffer) {
	var key string
	if t == fnControl {
		key = string([]byte{byte(ch)})
	} else {
		d.collect(ch) // the final character is part of the key
		key = string(d.dispatchChars)
	}
	f, ok := registry[t][key]
	if !ok || f.clearsWrap {
		fb.DS.NextPrintWillWrap = false
	}
	if ok {
		f.run(fb, d)
	}
}

func (d *dispatcher) oscPut(ch rune) {
	if len(d.osc) < maxClipboardSize {
		d.osc = append(d.osc, ch)
	}
}

func (d *dispatcher) oscStart() { d.osc = d.osc[:0] }

// oscDispatch handles window titles, OSC 52 clipboard copies and OSC 8 links.
func (d *dispatcher) oscDispatch(fb *Framebuffer) {
	s := d.osc
	if len(s) >= 5 && string(s[:5]) == "52;c;" {
		fb.clipboard = append([]rune(nil), s[5:]...)
		return
	}
	if len(s) == 0 {
		return
	}
	cmd, offset := -1, 0
	if s[0] == ';' {
		cmd, offset = 0, 1 // "ESC ] ; title BEL" counts as 0
	} else if len(s) >= 2 && s[1] == ';' {
		cmd, offset = int(s[0]-'0'), 2
	}
	if cmd == 8 {
		osc8(s, fb)
		return
	}
	icon, title := cmd == 0 || cmd == 1, cmd == 0 || cmd == 2
	if !icon && !title {
		return
	}
	fb.titleInitialized = true
	t := append([]rune(nil), s[offset:min(len(s), 256)]...)
	if icon {
		fb.iconName = t
	}
	if title {
		fb.windowTitle = t
	}
}

// osc8 parses "8;params;url".
func osc8(s []rune, fb *Framebuffer) {
	for _, c := range s {
		if c < 32 || c > 126 { // valid characters per the OSC 8 spec
			return
		}
	}
	str := string(s)
	if len(str) <= 2 || str[1] != ';' {
		return
	}
	i := strings.IndexByte(str[2:], ';')
	if i < 0 {
		return
	}
	fb.DS.link = newHyperlink(str[2:2+i], str[2+i+1:])
}

func clearLine(fb *Framebuffer, row, start, end int) {
	for col := start; col <= end; col++ {
		fb.resetCell(fb.mutableCell(row, col))
	}
}

// htN moves to the count-th next (negative: previous) tab stop.
func htN(fb *Framebuffer, count int) {
	col := fb.DS.nextTab(count)
	if col == -1 { // no tabs, go to end of line
		col = fb.DS.width - 1
	}
	// A tab is the only move that preserves, but does not set, the wrap state.
	wrap := fb.DS.NextPrintWillWrap
	fb.DS.moveCol(col, false, false)
	fb.DS.NextPrintWillWrap = wrap
}

// decMode returns the flag behind DEC private mode n; some modes act instead.
func decMode(n int, fb *Framebuffer) *bool {
	ds := &fb.DS
	switch n {
	case 1:
		return &ds.AppCursorKeys
	case 3: // 80/132 columns: ignored, but clears the screen
		ds.moveRow(0, false)
		ds.moveCol(0, false, false)
		for y := 0; y < ds.height; y++ {
			fb.resetRow(fb.mutableRow(y))
		}
	case 5:
		return &ds.ReverseVideo
	case 6:
		ds.moveRow(0, false)
		ds.moveCol(0, false, false)
		return &ds.OriginMode
	case 7:
		return &ds.AutoWrapMode
	case 25:
		return &ds.CursorVisible
	case 1004:
		return &ds.MouseFocusEvent
	case 1007:
		return &ds.MouseAltScroll
	case 2004:
		return &ds.BracketedPaste
	}
	return nil
}

func setDECModes(fb *Framebuffer, d *dispatcher, on bool) {
	for i := 0; i < d.paramCount(); i++ {
		p := d.param(i, 0)
		switch {
		case p == MouseX10 || (p >= MouseVT220 && p <= MouseAnyEvent):
			fb.DS.MouseReporting = MouseNone
			if on {
				fb.DS.MouseReporting = p
			}
		case p == MouseEncodeUTF8 || p == MouseEncodeSGR || p == MouseEncodeURXVT:
			fb.DS.MouseEncoding = MouseEncodeDefault
			if on {
				fb.DS.MouseEncoding = p
			}
		default:
			if m := decMode(p, fb); m != nil {
				*m = on
			}
		}
	}
}

func setANSIModes(fb *Framebuffer, d *dispatcher, on bool) {
	for i := 0; i < d.paramCount(); i++ {
		if d.param(i, 0) == 4 { // insert/replace
			fb.DS.InsertMode = on
		}
	}
}

func init() {
	csi := func(key string, run func(*Framebuffer, *dispatcher)) { register(fnCSI, key, run, true) }
	ctrl := func(key string, run func(*Framebuffer, *dispatcher)) { register(fnControl, key, run, true) }
	esc := func(key string, run func(*Framebuffer, *dispatcher)) { register(fnEscape, key, run, true) }

	csi("K", func(fb *Framebuffer, d *dispatcher) { // erase in line
		switch d.param(0, 0) {
		case 0:
			clearLine(fb, -1, fb.DS.cursorCol, fb.DS.width-1)
		case 1:
			clearLine(fb, -1, 0, fb.DS.cursorCol)
		case 2:
			fb.resetRow(fb.mutableRow(-1))
		}
	})
	csi("J", func(fb *Framebuffer, d *dispatcher) { // erase in display
		switch d.param(0, 0) {
		case 0:
			clearLine(fb, -1, fb.DS.cursorCol, fb.DS.width-1)
			for y := fb.DS.cursorRow + 1; y < fb.DS.height; y++ {
				fb.resetRow(fb.mutableRow(y))
			}
		case 1:
			for y := 0; y < fb.DS.cursorRow; y++ {
				fb.resetRow(fb.mutableRow(y))
			}
			clearLine(fb, -1, 0, fb.DS.cursorCol)
		case 2:
			for y := 0; y < fb.DS.height; y++ {
				fb.resetRow(fb.mutableRow(y))
			}
		}
	})
	cursorMove := func(fb *Framebuffer, d *dispatcher) {
		n := d.param(0, 1)
		switch d.dispatchChars[0] {
		case 'A':
			fb.DS.moveRow(-n, true)
		case 'B':
			fb.DS.moveRow(n, true)
		case 'C':
			fb.DS.moveCol(n, true, false)
		case 'D':
			fb.DS.moveCol(-n, true, false)
		case 'H', 'f':
			fb.DS.moveRow(d.param(0, 1)-1, false)
			fb.DS.moveCol(d.param(1, 1)-1, false, false)
		}
	}
	for _, k := range []string{"A", "B", "C", "D", "H", "f"} {
		csi(k, cursorMove)
	}
	csi("c", func(fb *Framebuffer, d *dispatcher) { d.toHost = append(d.toHost, "\033[?62c"...) })      // device attributes: vt220
	csi(">c", func(fb *Framebuffer, d *dispatcher) { d.toHost = append(d.toHost, "\033[>1;10;0c"...) }) // secondary DA
	esc("#8", func(fb *Framebuffer, d *dispatcher) {                                                    // screen alignment test: fill with E
		for y := 0; y < fb.DS.height; y++ {
			for x := 0; x < fb.DS.width; x++ {
				c := fb.mutableCell(y, x)
				fb.resetCell(c)
				c.append('E')
			}
		}
	})
	lf := func(fb *Framebuffer, d *dispatcher) { fb.moveRowsAutoscroll(1) }
	for _, k := range []string{"\x0a", "\x84", "\x0b", "\x0c"} { // LF, IND, VT, FF
		ctrl(k, lf)
	}
	ctrl("\x0d", func(fb *Framebuffer, d *dispatcher) { fb.DS.moveCol(0, false, false) }) // CR
	ctrl("\x08", func(fb *Framebuffer, d *dispatcher) { fb.DS.moveCol(-1, true, false) }) // BS
	ctrl("\x8d", func(fb *Framebuffer, d *dispatcher) { fb.moveRowsAutoscroll(-1) })      // RI
	ctrl("\x85", func(fb *Framebuffer, d *dispatcher) {                                   // NEL
		fb.DS.moveCol(0, false, false)
		fb.moveRowsAutoscroll(1)
	})
	register(fnControl, "\x09", func(fb *Framebuffer, d *dispatcher) { htN(fb, 1) }, false) // HT
	cxt := func(fb *Framebuffer, d *dispatcher) {
		n := d.param(0, 1)
		if d.dispatchChars[0] == 'Z' {
			n = -n
		}
		if n != 0 {
			htN(fb, n)
		}
	}
	register(fnCSI, "I", cxt, false)                                      // CHT
	register(fnCSI, "Z", cxt, false)                                      // CBT
	ctrl("\x88", func(fb *Framebuffer, d *dispatcher) { fb.DS.setTab() }) // HTS
	register(fnCSI, "g", func(fb *Framebuffer, d *dispatcher) {           // TBC
		switch d.param(0, 0) {
		case 0:
			fb.DS.clearTab(fb.DS.cursorCol)
		case 3:
			fb.DS.defaultTabs = false
			for x := range fb.DS.tabs {
				fb.DS.clearTab(x)
			}
		}
	}, false)
	register(fnCSI, "?h", func(fb *Framebuffer, d *dispatcher) { setDECModes(fb, d, true) }, false)
	register(fnCSI, "?l", func(fb *Framebuffer, d *dispatcher) { setDECModes(fb, d, false) }, false)
	csi("h", func(fb *Framebuffer, d *dispatcher) { setANSIModes(fb, d, true) })
	csi("l", func(fb *Framebuffer, d *dispatcher) { setANSIModes(fb, d, false) })
	csi("r", func(fb *Framebuffer, d *dispatcher) { // DECSTBM: scrolling margins
		top, bottom := d.param(0, 1), d.param(1, fb.DS.height)
		if bottom <= top || top > fb.DS.height || (top == 0 && bottom == 1) {
			return // invalid, xterm ignores
		}
		fb.DS.setScrollingRegion(top-1, bottom-1)
		fb.DS.moveRow(0, false)
		fb.DS.moveCol(0, false, false)
	})
	ctrl("\x07", func(fb *Framebuffer, d *dispatcher) { fb.bellCount++ })
	register(fnCSI, "m", func(fb *Framebuffer, d *dispatcher) { // SGR
		for i := 0; i < d.paramCount(); i++ {
			r := d.param(i, 0)
			// 38;5;N and 48;5;N: here N == 0 is a color, not a reset.
			if (r == 38 || r == 48) && d.paramCount()-i >= 3 && d.param(i+1, -1) == 5 {
				if r == 38 {
					fb.DS.renditions.setForeground(uint32(d.param(i+2, 0)))
				} else {
					fb.DS.renditions.setBackground(uint32(d.param(i+2, 0)))
				}
				i += 2
				continue
			}
			// 38;2;R;G;B and 48;2;R;G;B: true color.
			if (r == 38 || r == 48) && d.paramCount()-i >= 5 && d.param(i+1, -1) == 2 {
				c := makeTrueColor(uint32(d.param(i+2, 0)), uint32(d.param(i+3, 0)), uint32(d.param(i+4, 0)))
				if r == 38 {
					fb.DS.renditions.setForeground(c)
				} else {
					fb.DS.renditions.setBackground(c)
				}
				i += 4
				continue
			}
			fb.DS.renditions.setRendition(r)
		}
	}, false) // changing the pen does not cancel a pending wrap
	esc("7", func(fb *Framebuffer, d *dispatcher) { fb.DS.saveCursor() })
	esc("8", func(fb *Framebuffer, d *dispatcher) { fb.DS.restoreCursor() })
	csi("n", func(fb *Framebuffer, d *dispatcher) { // device status report
		switch d.param(0, 0) {
		case 5:
			d.toHost = append(d.toHost, "\033[0n"...)
		case 6:
			d.toHost = fmt.Appendf(d.toHost, "\033[%d;%dR", fb.DS.cursorRow+1, fb.DS.cursorCol+1)
		}
	})
	csi("L", func(fb *Framebuffer, d *dispatcher) { // insert line
		fb.insertLine(fb.DS.cursorRow, d.param(0, 1))
		fb.DS.moveCol(0, false, false)
	})
	csi("M", func(fb *Framebuffer, d *dispatcher) { // delete line
		fb.deleteLine(fb.DS.cursorRow, d.param(0, 1))
		fb.DS.moveCol(0, false, false)
	})
	csi("@", func(fb *Framebuffer, d *dispatcher) { // insert characters
		for i := d.param(0, 1); i > 0; i-- {
			fb.insertCell(fb.DS.cursorRow, fb.DS.cursorCol)
		}
	})
	csi("P", func(fb *Framebuffer, d *dispatcher) { // delete characters
		for i := d.param(0, 1); i > 0; i-- {
			fb.deleteCell(fb.DS.cursorRow, fb.DS.cursorCol)
		}
	})
	csi("d", func(fb *Framebuffer, d *dispatcher) { fb.DS.moveRow(d.param(0, 1)-1, false) }) // VPA
	hpa := func(fb *Framebuffer, d *dispatcher) { fb.DS.moveCol(d.param(0, 1)-1, false, false) }
	csi("G", hpa)                                   // CHA
	csi("`", hpa)                                   // HPA
	csi("X", func(fb *Framebuffer, d *dispatcher) { // erase characters
		clearLine(fb, -1, fb.DS.cursorCol, min(fb.DS.cursorCol+d.param(0, 1)-1, fb.DS.width-1))
	})
	esc("c", func(fb *Framebuffer, d *dispatcher) { fb.reset() })      // RIS
	csi("!p", func(fb *Framebuffer, d *dispatcher) { fb.softReset() }) // DECSTR
	csi("S", func(fb *Framebuffer, d *dispatcher) { fb.scroll(d.param(0, 1)) })
	csi("T", func(fb *Framebuffer, d *dispatcher) { fb.scroll(-d.param(0, 1)) })
}
