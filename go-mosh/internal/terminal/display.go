package terminal

import (
	"fmt"
	"strings"
)

// Display turns "the screen was A, now it is B" into the escape sequences
// that redraw it, ported from mosh (src/terminal/terminaldisplay.cc). The
// server uses it for the SSP diff; the client uses it to paint its terminal.
type Display struct {
	hasECH   bool // erase character (CSI X); tmux lacks it
	hasBCE   bool // erasing fills with the background color
	hasTitle bool // window title and icon name can be set
	smcup    string
	rmcup    string
}

// NewDisplay returns a display for the SSP diff, which may assume a capable
// terminal. A display for a real terminal is built from its terminfo instead.
func NewDisplay() *Display {
	return &Display{hasECH: true, hasBCE: true, hasTitle: true}
}

// Open is written once when the client takes over the terminal.
func (d *Display) Open() string { return d.smcup + "\033[?1h" }

// Close restores the terminal when the client exits.
func (d *Display) Close() string {
	return "\033[?1l\033[0m\033[?25h" +
		"\033[?1003l\033[?1002l\033[?1001l\033[?1000l" +
		"\033[?1015l\033[?1006l\033[?1005l" + d.rmcup
}

type frameState struct {
	b             strings.Builder
	cursorX       int // -1: unknown, must position explicitly
	cursorY       int
	rendition     Renditions
	link          *Hyperlink
	cursorVisible bool
	last          *Framebuffer
}

// silentMove moves the cursor, hiding it first so the move does not flicker.
func (fs *frameState) silentMove(y, x int) {
	if fs.cursorX == x && fs.cursorY == y {
		return
	}
	if fs.cursorVisible {
		fs.b.WriteString("\033[?25l")
		fs.cursorVisible = false
	}
	fs.move(y, x)
}

func (fs *frameState) move(y, x int) {
	lastX, lastY := fs.cursorX, fs.cursorY
	fs.cursorX, fs.cursorY = x, y
	if lastX != -1 && lastY != -1 { // only optimize from a known position
		// CR and LF are cheap and easy to trace.
		if x == 0 && y-lastY >= 0 && y-lastY < 5 {
			if lastX != 0 {
				fs.b.WriteByte('\r')
			}
			fs.b.WriteString(strings.Repeat("\n", y-lastY))
			return
		}
		// So are backspaces.
		if y == lastY && x-lastX < 0 && x-lastX > -5 {
			fs.b.WriteString(strings.Repeat("\b", lastX-x))
			return
		}
	}
	fmt.Fprintf(&fs.b, "\033[%d;%dH", y+1, x+1)
}

func (fs *frameState) updateRendition(r Renditions, force bool) {
	if force || fs.rendition != r {
		fs.b.WriteString(r.SGR())
		fs.rendition = r
	}
}

func (fs *frameState) updateLink(h *Hyperlink, force bool) {
	if force || !linkEqual(fs.link, h) {
		fs.b.WriteString(h.OSC8())
		fs.link = h
	}
}

func (fs *frameState) appendCell(c *Cell) { fs.b.WriteString(c.grapheme()) }

func (d *Display) canUseErase(fs *frameState) bool {
	return d.hasBCE || (fs.rendition == Renditions{} && fs.link == nil)
}

func runesEqual(a, b []rune) bool { return string(a) == string(b) }

// NewFrame returns the escape sequences that turn a terminal showing last into
// one showing f. With initialized false, nothing about the terminal is assumed.
func (d *Display) NewFrame(initialized bool, last, f *Framebuffer) string {
	fs := &frameState{last: last, cursorVisible: last.DS.CursorVisible}

	if f.bellCount != last.bellCount {
		fs.b.WriteByte('\a')
	}

	if d.hasTitle && f.titleInitialized &&
		(!initialized || !runesEqual(f.iconName, last.iconName) || !runesEqual(f.windowTitle, last.windowTitle)) {
		if runesEqual(f.iconName, f.windowTitle) {
			fs.b.WriteString("\033]0;" + string(f.windowTitle) + "\a")
		} else {
			fs.b.WriteString("\033]1;" + string(f.iconName) + "\a\033]2;" + string(f.windowTitle) + "\a")
		}
	}

	if !runesEqual(f.clipboard, last.clipboard) {
		fs.b.WriteString("\033]52;c;" + string(f.clipboard) + "\a")
	}

	if !initialized || f.DS.ReverseVideo != last.DS.ReverseVideo {
		if f.DS.ReverseVideo {
			fs.b.WriteString("\033[?5h")
		} else {
			fs.b.WriteString("\033[?5l")
		}
	}

	if !initialized || f.DS.width != last.DS.width || f.DS.height != last.DS.height {
		// Reset the scrolling region and clear the screen.
		fs.b.WriteString("\033[r\033[0m\033[H\033[2J")
		initialized = false
		fs.cursorX, fs.cursorY = 0, 0
		fs.rendition, fs.link = Renditions{}, nil
	} else {
		fs.cursorX, fs.cursorY = last.DS.cursorCol, last.DS.cursorRow
		fs.rendition, fs.link = last.DS.renditions, last.DS.link
	}

	if !initialized {
		fs.cursorVisible = false
		fs.b.WriteString("\033[?25l")
	}

	rows := append([]*Row(nil), last.rows...)
	// After a resize to wider, extend the old rows.
	if last.DS.width < f.DS.width {
		for i, r := range rows {
			r = r.clone()
			for len(r.cells) < f.DS.width {
				r.cells = append(r.cells, blankCell(f.DS.renditions.bg))
			}
			rows[i] = r
		}
	}
	var blank *Row
	// After a resize to taller, add blank rows.
	if len(rows) < f.DS.height {
		blank = newRow(f.DS.width, 0)
		for len(rows) < f.DS.height {
			rows = append(rows, blank)
		}
	}

	frameY := 0
	if initialized {
		frameY = d.scroll(fs, f, rows, &blank)
	}

	wrap := false
	for ; frameY < f.DS.height; frameY++ {
		wrap = d.putRow(initialized, fs, f, frameY, rows[frameY], wrap)
	}

	if !initialized || f.DS.cursorRow != fs.cursorY || f.DS.cursorCol != fs.cursorX {
		fs.move(f.DS.cursorRow, f.DS.cursorCol)
	}

	if !initialized || f.DS.CursorVisible != fs.cursorVisible {
		if f.DS.CursorVisible {
			fs.b.WriteString("\033[?25h")
		} else {
			fs.b.WriteString("\033[?25l")
		}
	}

	fs.updateRendition(f.DS.renditions, !initialized)
	fs.updateLink(f.DS.link, !initialized)

	if !initialized || f.DS.BracketedPaste != last.DS.BracketedPaste {
		if f.DS.BracketedPaste {
			fs.b.WriteString("\033[?2004h")
		} else {
			fs.b.WriteString("\033[?2004l")
		}
	}

	if !initialized || f.DS.MouseReporting != last.DS.MouseReporting {
		if f.DS.MouseReporting == MouseNone {
			fs.b.WriteString("\033[?1003l\033[?1002l\033[?1001l\033[?1000l")
		} else {
			if last.DS.MouseReporting != MouseNone {
				fmt.Fprintf(&fs.b, "\033[?%dl", last.DS.MouseReporting)
			}
			fmt.Fprintf(&fs.b, "\033[?%dh", f.DS.MouseReporting)
		}
	}

	if !initialized || f.DS.MouseFocusEvent != last.DS.MouseFocusEvent {
		if f.DS.MouseFocusEvent {
			fs.b.WriteString("\033[?1004h")
		} else {
			fs.b.WriteString("\033[?1004l")
		}
	}

	if !initialized || f.DS.MouseEncoding != last.DS.MouseEncoding {
		if f.DS.MouseEncoding == MouseEncodeDefault {
			fs.b.WriteString("\033[?1015l\033[?1006l\033[?1005l")
		} else {
			if last.DS.MouseEncoding != MouseEncodeDefault {
				fmt.Fprintf(&fs.b, "\033[?%dl", last.DS.MouseEncoding)
			}
			fmt.Fprintf(&fs.b, "\033[?%dh", f.DS.MouseEncoding)
		}
	}
	return fs.b.String()
}

// scroll detects that the screen moved up by some lines and replays that with
// one scroll instead of redrawing every row. It returns the first row that
// still needs drawing and updates rows to what the terminal now shows.
func (d *Display) scroll(fs *frameState, f *Framebuffer, rows []*Row, blank **Row) int {
	linesScrolled, scrollHeight := 0, 0
	for row := 0; row < f.DS.height; row++ {
		if !f.rows[0].Equal(rows[row]) {
			continue
		}
		if row == 0 { // looking at ourselves: probably no scroll
			break
		}
		linesScrolled, scrollHeight = row, 1
		// How big is the region that was scrolled?
		for h := 1; linesScrolled+h < f.DS.height; h++ {
			if !f.rows[h].Equal(rows[linesScrolled+h]) {
				break
			}
			scrollHeight = h + 1
		}
		break
	}
	if scrollHeight == 0 || linesScrolled == 0 {
		return scrollHeight
	}

	if *blank == nil {
		*blank = newRow(f.DS.width, 0)
	}
	fs.updateRendition(Renditions{}, true)
	fs.updateLink(nil, true)
	top, bottom := 0, linesScrolled+scrollHeight-1
	if scrollHeight+linesScrolled == f.DS.height && fs.cursorY+1 == f.DS.height {
		// Common case: already on the bottom line, whole screen scrolls.
		fs.b.WriteByte('\r')
		fs.b.WriteString(strings.Repeat("\n", linesScrolled))
		fs.cursorX = 0
	} else {
		fmt.Fprintf(&fs.b, "\033[%d;%dr", top+1, bottom+1) // scrolling region
		fs.cursorX, fs.cursorY = -1, -1
		fs.silentMove(bottom, 0)
		fs.b.WriteString(strings.Repeat("\n", linesScrolled))
		fs.b.WriteString("\033[r")
		fs.cursorX, fs.cursorY = -1, -1 // unsetting the region moves the cursor
	}
	for i := top; i <= bottom; i++ {
		if i+linesScrolled <= bottom {
			rows[i] = rows[i+linesScrolled]
		} else {
			rows[i] = *blank
		}
	}
	return scrollHeight
}

// putRow redraws one row against what the terminal shows (oldRow). It
// returns true when the row wrapped, so the next row must start by printing
// its first cell to let the real cursor wrap too.
func (d *Display) putRow(initialized bool, fs *frameState, f *Framebuffer, frameY int, oldRow *Row, wrap bool) bool {
	row := f.rows[frameY]
	cells, oldCells := row.cells, oldRow.cells
	frameX := 0

	if wrap { // forced to write the first column because of the wrap
		c := &cells[0]
		fs.updateRendition(c.renditions, false)
		fs.updateLink(c.link, false)
		fs.appendCell(c)
		frameX += c.width()
		fs.cursorX += c.width()
	}

	if initialized && row == oldRow { // same object: nothing changed
		return false
	}

	wrapThis := row.wrapped()
	width := f.DS.width
	clearCount := 0
	wroteLastCell := false
	var blankRend Renditions
	var blankLink *Hyperlink

	for frameX < width {
		c := &cells[frameX]
		if initialized && clearCount == 0 && c.Equal(&oldCells[frameX]) {
			frameX += c.width()
			continue
		}

		// Slurp up runs of empty cells with the same rendition.
		if c.empty() {
			if clearCount == 0 {
				blankRend, blankLink = c.renditions, c.link
			}
			if c.renditions == blankRend && linkEqual(c.link, blankLink) {
				clearCount++
				frameX++
				continue
			}
		}

		if clearCount > 0 { // clear a run inside the row
			fs.silentMove(frameY, frameX-clearCount)
			fs.updateRendition(blankRend, false)
			fs.updateLink(blankLink, false)
			if d.canUseErase(fs) && d.hasECH && clearCount > 4 {
				fmt.Fprintf(&fs.b, "\033[%dX", clearCount)
			} else {
				fs.b.WriteString(strings.Repeat(" ", clearCount))
				fs.cursorX = frameX
			}
			clearCount = 0
			// Another empty cell in a different rendition starts a new run.
			if c.empty() {
				blankRend, blankLink = c.renditions, c.link
				clearCount = 1
				frameX++
				continue
			}
		}

		cw := c.width()
		// Before the last character of a wrapping row, forget the cursor
		// position: our terminal state may have the cursor in the autowrap
		// column, the real one snaps to the last column, and the diff must
		// apply to both.
		if wrapThis && frameX+cw >= width {
			fs.cursorX, fs.cursorY = -1, -1
		}
		fs.silentMove(frameY, frameX)
		fs.updateRendition(c.renditions, false)
		fs.updateLink(c.link, false)
		fs.appendCell(c)
		frameX += cw
		fs.cursorX += cw
		if frameX >= width {
			wroteLastCell = true
		}
	}

	if clearCount > 0 { // clear to end of line
		fs.silentMove(frameY, frameX-clearCount)
		fs.updateRendition(blankRend, false)
		fs.updateLink(blankLink, false)
		if d.canUseErase(fs) && !wrapThis {
			fs.b.WriteString("\033[K")
		} else {
			fs.b.WriteString(strings.Repeat(" ", clearCount))
			fs.cursorX = frameX
			wroteLastCell = true
		}
	}

	if !wroteLastCell || frameY >= f.DS.height-1 {
		return false
	}
	// Let the real cursor wrap where ours did, so word selection in the
	// user's terminal joins the two lines.
	if wrapThis {
		fs.cursorX = 0
		fs.cursorY++
		return true
	}
	fs.b.WriteString("\r\n")
	fs.cursorX = 0
	fs.cursorY++
	return false
}
