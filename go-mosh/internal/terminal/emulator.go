package terminal

// Emulator is a parser feeding a framebuffer, ported from mosh
// (src/terminal/terminal.cc, terminaluserinput.cc, statesync/completeterminal.cc).
type Emulator struct {
	fb       *Framebuffer
	parser   parser
	dispatch dispatcher
	user     userInputState
	actions  []action // reused between calls
}

func NewEmulator(width, height int) *Emulator {
	return &Emulator{fb: NewFramebuffer(width, height)}
}

func (e *Emulator) Framebuffer() *Framebuffer { return e.fb }

// Write feeds host output (what a program prints) into the terminal. It
// returns the terminal's replies to the program, e.g. a cursor position report.
func (e *Emulator) Write(p []byte) []byte {
	for _, b := range p {
		e.actions = e.parser.inputByte(b, e.actions[:0])
		for _, a := range e.actions {
			e.act(a)
		}
	}
	return e.takeReplies()
}

func (e *Emulator) takeReplies() []byte {
	r := e.dispatch.toHost
	e.dispatch.toHost = nil
	return r
}

func (e *Emulator) act(a action) {
	switch a.kind {
	case actPrint:
		e.print(a.ch)
	case actExecute:
		e.dispatch.dispatch(fnControl, a.ch, e.fb)
	case actClear:
		e.dispatch.clear()
	case actCollect:
		e.dispatch.collect(a.ch)
	case actParam:
		e.dispatch.newParamChar(a.ch)
	case actEscDispatch:
		// 7-bit ESC-encoded C1 controls, e.g. ESC D is IND.
		if len(e.dispatch.dispatchChars) == 0 && 0x40 <= a.ch && a.ch <= 0x5F {
			e.dispatch.dispatch(fnControl, a.ch+0x40, e.fb)
		} else {
			e.dispatch.dispatch(fnEscape, a.ch, e.fb)
		}
	case actCSIDispatch:
		e.dispatch.dispatch(fnCSI, a.ch, e.fb)
	case actOSCStart:
		e.dispatch.oscStart()
	case actOSCPut:
		e.dispatch.oscPut(a.ch)
	case actOSCEnd:
		e.dispatch.oscDispatch(e.fb)
	}
}

func (e *Emulator) print(ch rune) {
	fb := e.fb
	ds := &fb.DS
	w := -1
	switch {
	case ch == 0:
	case isPrintISO8859_1(ch):
		w = 1
	default:
		w = runeWidth(ch)
	}

	switch w {
	case 1, 2:
		cell := fb.mutableCell(-1, -1)
		if ds.AutoWrapMode && ds.NextPrintWillWrap {
			fb.mutableRow(-1).setWrap(true)
			ds.moveCol(0, false, false)
			fb.moveRowsAutoscroll(1)
			cell = nil
		} else if ds.AutoWrapMode && w == 2 && ds.cursorCol == ds.width-1 {
			// No room for a wide character: wrap it, even without the flag.
			fb.resetCell(cell)
			fb.mutableRow(-1).setWrap(false)
			ds.moveCol(0, false, false)
			fb.moveRowsAutoscroll(1)
			cell = nil
		}
		if ds.InsertMode {
			for i := 0; i < w; i++ {
				fb.insertCell(ds.cursorRow, ds.cursorCol)
			}
			cell = nil
		}
		if cell == nil {
			cell = fb.mutableCell(-1, -1)
		}
		fb.resetCell(cell)
		cell.append(ch)
		cell.wide = w == 2
		cell.renditions = ds.renditions
		cell.link = ds.link
		if w == 2 && ds.cursorCol+1 < ds.width { // erase the cell it overlaps
			fb.resetCell(fb.mutableCell(ds.cursorRow, ds.cursorCol+1))
		}
		ds.moveCol(w, true, true)
	case 0: // combining character
		cell := fb.combiningCell()
		if cell == nil {
			return // off screen after a resize
		}
		if cell.empty() {
			// A cell that starts with a combining character, e.g. after
			// the base was erased.
			cell.fallback = true
			ds.moveCol(1, true, true)
		}
		if !cell.full() {
			cell.append(ch)
		}
	}
}

// Resize changes the screen size, e.g. when the client's window changes.
func (e *Emulator) Resize(width, height int) { e.fb.Resize(width, height) }

type userInputState uint8

const (
	userGround userInputState = iota
	userESC
	userSS3
)

// UserByte translates one keystroke byte for the application. The client's
// terminal always sends cursor keys in application mode (ESC O A); if the
// application has not asked for that mode, convert them to ESC [ A.
func (e *Emulator) UserByte(c byte) []byte {
	switch e.user {
	case userESC:
		if c == 'O' {
			e.user = userSS3
			return nil
		}
		e.user = userGround
		return []byte{c}
	case userSS3:
		e.user = userGround
		if !e.fb.DS.AppCursorKeys && c >= 'A' && c <= 'D' {
			return []byte{'[', c}
		}
		return []byte{'O', c}
	}
	if c == 0x1b {
		e.user = userESC
	}
	return []byte{c}
}

// Clone copies the whole terminal, parser state included. Rows are shared
// copy-on-write, so this is cheap.
func (e *Emulator) Clone() *Emulator {
	c := *e
	c.fb = e.fb.Clone()
	d := &c.dispatch
	d.params = append([]byte(nil), d.params...)
	d.parsed = append([]int(nil), d.parsed...)
	d.dispatchChars = append([]byte(nil), d.dispatchChars...)
	d.osc = append([]rune(nil), d.osc...)
	d.toHost = append([]byte(nil), d.toHost...)
	c.actions = nil
	return &c
}
