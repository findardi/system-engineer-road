package terminal

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Framebuffer types ported from mosh (src/terminal/terminalframebuffer.*).

// Attribute bits in Renditions.attrs.
const (
	attrBold = 1 << iota
	attrFaint
	attrItalic
	attrUnderlined
	attrBlink
	attrInverse
	attrInvisible
)

const trueColorMask = 0x1000000

// Renditions is the pen: colors and attributes. Colors are encoded as in mosh:
// 0 = default, 30..37/40..47 = ANSI, 38+n/48+n = 256-color index n (n >= 8
// means 30+n), or trueColorMask|RGB.
type Renditions struct {
	fg, bg uint32
	attrs  uint8
}

func makeTrueColor(r, g, b uint32) uint32 { return trueColorMask | r<<16 | g<<8 | b }

func isTrueColor(c uint32) bool { return c&trueColorMask != 0 }

func (r *Renditions) attr(a uint8) bool { return r.attrs&a != 0 }

func (r *Renditions) setAttr(a uint8, on bool) {
	if on {
		r.attrs |= a
	} else {
		r.attrs &^= a
	}
}

// setRendition applies one SGR parameter; it cannot set 256 or true colors.
func (r *Renditions) setRendition(num int) {
	switch {
	case num == 0:
		*r = Renditions{}
		return
	case num == 39:
		r.fg = 0
		return
	case num == 49:
		r.bg = 0
		return
	case 30 <= num && num <= 37:
		r.fg = uint32(num)
		return
	case 40 <= num && num <= 47:
		r.bg = uint32(num)
		return
	case 90 <= num && num <= 97:
		r.fg = uint32(num - 90 + 38)
		return
	case 100 <= num && num <= 107:
		r.bg = uint32(num - 100 + 48)
		return
	}
	on := num < 9
	switch num {
	case 1, 22:
		r.setAttr(attrBold, on)
	case 3, 23:
		r.setAttr(attrItalic, on)
	case 4, 24:
		r.setAttr(attrUnderlined, on)
	case 5, 25:
		r.setAttr(attrBlink, on)
	case 7, 27:
		r.setAttr(attrInverse, on)
	case 8, 28:
		r.setAttr(attrInvisible, on)
	}
}

func (r *Renditions) setForeground(num uint32) {
	if num <= 255 {
		r.fg = 30 + num
	} else if isTrueColor(num) {
		r.fg = num
	}
}

func (r *Renditions) setBackground(num uint32) {
	if num <= 255 {
		r.bg = 40 + num
	} else if isTrueColor(num) {
		r.bg = num
	}
}

// SGR is the escape sequence that sets exactly this pen from scratch.
func (r Renditions) SGR() string {
	var b strings.Builder
	b.WriteString("\033[0")
	for _, a := range []struct {
		bit  uint8
		code string
	}{{attrBold, ";1"}, {attrItalic, ";3"}, {attrUnderlined, ";4"}, {attrBlink, ";5"}, {attrInverse, ";7"}, {attrInvisible, ";8"}} {
		if r.attr(a.bit) {
			b.WriteString(a.code)
		}
	}
	color := func(c uint32, base uint32, ext string) {
		switch {
		case c == 0:
		case isTrueColor(c):
			fmt.Fprintf(&b, ";%s;2;%d;%d;%d", ext, c>>16&0xff, c>>8&0xff, c&0xff)
		case c > base+7:
			fmt.Fprintf(&b, ";%s;5;%d", ext, c-base)
		default:
			fmt.Fprintf(&b, ";%d", c)
		}
	}
	color(r.fg, 30, "38")
	color(r.bg, 40, "48")
	b.WriteByte('m')
	return b.String()
}

// Hyperlink is an OSC 8 link; nil means no link.
type Hyperlink struct {
	params, url string
}

func newHyperlink(params, url string) *Hyperlink {
	if url == "" {
		return nil
	}
	return &Hyperlink{params, url}
}

func linkEqual(a, b *Hyperlink) bool { return a == b || (a != nil && b != nil && *a == *b) }

// OSC8 is the sequence that starts this link, or ends any link when h is nil.
func (h *Hyperlink) OSC8() string {
	if h == nil {
		return "\033]8;;\033\\"
	}
	return "\033]8;" + h.params + ";" + h.url + "\033\\"
}

type Cell struct {
	contents   string // UTF-8: one base character plus combining characters
	renditions Renditions
	link       *Hyperlink
	wide       bool // occupies two columns
	fallback   bool // starts with a combining character
	wrap       bool // last cell of a row that wrapped onto the next
}

func blankCell(bg uint32) Cell { return Cell{renditions: Renditions{bg: bg}} }

func (c *Cell) reset(bg uint32) { *c = blankCell(bg) }

func (c *Cell) Equal(o *Cell) bool {
	return c.contents == o.contents && c.fallback == o.fallback && c.wide == o.wide &&
		c.renditions == o.renditions && linkEqual(c.link, o.link) && c.wrap == o.wrap
}

func (c *Cell) empty() bool { return c.contents == "" }

// full: 32 bytes is plenty for a base character and its combining marks.
func (c *Cell) full() bool { return len(c.contents) >= 32 }

func (c *Cell) isBlank() bool { return c.contents == "" || c.contents == " " || c.contents == " " }

func (c *Cell) contentsMatch(o *Cell) bool {
	return (c.isBlank() && o.isBlank()) || c.contents == o.contents
}

func (c *Cell) append(ch rune) { c.contents = string(utf8.AppendRune([]byte(c.contents), ch)) }

// isPrintISO8859_1 is a cheap test for common narrow characters.
func isPrintISO8859_1(ch rune) bool { return (0xa0 <= ch && ch <= 0xff) || (0x20 <= ch && ch <= 0x7e) }

// grapheme is what to print for this cell; a leading combining character is
// attached to a no-break space.
func (c *Cell) grapheme() string {
	if c.contents == "" {
		return " "
	}
	if c.fallback {
		return " " + c.contents
	}
	return c.contents
}

func (c *Cell) width() int {
	if c.wide {
		return 2
	}
	return 1
}

var genCounter uint64

func nextGen() uint64 {
	genCounter++
	return genCounter
}

type Row struct {
	cells []Cell
	// gen quickly rules out two rows being identical, which helps the
	// display find scrolled regions.
	gen    uint64
	shared bool // may be referenced by more than one framebuffer slot: copy before writing
}

func newRow(width int, bg uint32) *Row {
	r := &Row{cells: make([]Cell, width), gen: nextGen()}
	for i := range r.cells {
		r.cells[i] = blankCell(bg)
	}
	return r
}

func (r *Row) clone() *Row {
	return &Row{cells: append([]Cell(nil), r.cells...), gen: r.gen}
}

func (r *Row) Equal(o *Row) bool {
	if r == o {
		return true
	}
	if r.gen != o.gen || len(r.cells) != len(o.cells) {
		return false
	}
	for i := range r.cells {
		if !r.cells[i].Equal(&o.cells[i]) {
			return false
		}
	}
	return true
}

func (r *Row) insertCell(col int, bg uint32) {
	copy(r.cells[col+1:], r.cells[col:])
	r.cells[col] = blankCell(bg)
}

func (r *Row) deleteCell(col int, bg uint32) {
	copy(r.cells[col:], r.cells[col+1:])
	r.cells[len(r.cells)-1] = blankCell(bg)
}

func (r *Row) reset(bg uint32) {
	r.gen = nextGen()
	for i := range r.cells {
		r.cells[i].reset(bg)
	}
}

func (r *Row) wrapped() bool { return r.cells[len(r.cells)-1].wrap }

func (r *Row) setWrap(w bool) { r.cells[len(r.cells)-1].wrap = w }

type savedCursor struct {
	col, row   int
	renditions Renditions
	autoWrap   bool
	originMode bool
}

func defaultSavedCursor() savedCursor { return savedCursor{autoWrap: true} }

// Mouse reporting and encoding modes, by their DEC private mode numbers.
const (
	MouseNone          = 0
	MouseX10           = 9
	MouseVT220         = 1000
	MouseVT220Hilight  = 1001
	MouseButtonEvent   = 1002
	MouseAnyEvent      = 1003
	MouseEncodeDefault = 0
	MouseEncodeUTF8    = 1005
	MouseEncodeSGR     = 1006
	MouseEncodeURXVT   = 1015
)

// DrawState is the cursor, pen, margins, tabs and modes.
type DrawState struct {
	width, height        int
	cursorCol, cursorRow int
	combiningCol         int // where the next combining character attaches
	combiningRow         int
	defaultTabs          bool
	tabs                 []bool
	scrollTop            int
	scrollBottom         int
	renditions           Renditions
	link                 *Hyperlink
	save                 savedCursor

	NextPrintWillWrap bool
	OriginMode        bool
	AutoWrapMode      bool
	InsertMode        bool
	CursorVisible     bool
	ReverseVideo      bool
	BracketedPaste    bool
	MouseReporting    int
	MouseFocusEvent   bool // 1004
	MouseAltScroll    bool // 1007
	MouseEncoding     int
	AppCursorKeys     bool
}

func newDrawState(width, height int) DrawState {
	ds := DrawState{
		width: width, height: height,
		defaultTabs: true, tabs: make([]bool, width),
		scrollBottom: height - 1,
		save:         defaultSavedCursor(),
		AutoWrapMode: true, CursorVisible: true,
	}
	ds.reinitializeTabs(0)
	return ds
}

func (ds *DrawState) clone() DrawState {
	c := *ds
	c.tabs = append([]bool(nil), ds.tabs...)
	return c
}

func (ds *DrawState) reinitializeTabs(start int) {
	for i := start; i < len(ds.tabs); i++ {
		ds.tabs[i] = i%8 == 0
	}
}

func (ds *DrawState) Width() int     { return ds.width }
func (ds *DrawState) Height() int    { return ds.height }
func (ds *DrawState) CursorCol() int { return ds.cursorCol }
func (ds *DrawState) CursorRow() int { return ds.cursorRow }

func (ds *DrawState) Renditions() Renditions { return ds.renditions }
func (ds *DrawState) Hyperlink() *Hyperlink  { return ds.link }

func (ds *DrawState) newGrapheme() {
	ds.combiningCol, ds.combiningRow = ds.cursorCol, ds.cursorRow
}

func (ds *DrawState) snapCursorToBorder() {
	ds.cursorRow = min(max(ds.cursorRow, ds.limitTop()), ds.limitBottom())
	ds.cursorCol = min(max(ds.cursorCol, 0), ds.width-1)
}

func (ds *DrawState) moveRow(n int, relative bool) {
	if relative {
		ds.cursorRow += n
	} else {
		ds.cursorRow = n + ds.limitTop()
	}
	ds.snapCursorToBorder()
	ds.newGrapheme()
	ds.NextPrintWillWrap = false
}

// moveCol: implicit moves come from printing and may arm the wrap flag.
func (ds *DrawState) moveCol(n int, relative, implicit bool) {
	if implicit {
		ds.newGrapheme()
	}
	if relative {
		ds.cursorCol += n
	} else {
		ds.cursorCol = n
	}
	if implicit {
		ds.NextPrintWillWrap = ds.cursorCol >= ds.width
	}
	ds.snapCursorToBorder()
	if !implicit {
		ds.newGrapheme()
		ds.NextPrintWillWrap = false
	}
}

func (ds *DrawState) setTab()          { ds.tabs[ds.cursorCol] = true }
func (ds *DrawState) clearTab(col int) { ds.tabs[col] = false }

func (ds *DrawState) nextTab(count int) int {
	if count >= 0 {
		for i := ds.cursorCol + 1; i < ds.width; i++ {
			if ds.tabs[i] {
				count--
				if count == 0 {
					return i
				}
			}
		}
		return -1
	}
	for i := ds.cursorCol - 1; i > 0; i-- {
		if ds.tabs[i] {
			count++
			if count == 0 {
				return i
			}
		}
	}
	return 0
}

func (ds *DrawState) setScrollingRegion(top, bottom int) {
	if ds.height < 1 {
		return
	}
	ds.scrollTop = max(top, 0)
	ds.scrollBottom = min(bottom, ds.height-1)
	if ds.scrollBottom < ds.scrollTop {
		ds.scrollBottom = ds.scrollTop
	}
	if ds.OriginMode {
		ds.snapCursorToBorder()
		ds.newGrapheme()
	}
}

func (ds *DrawState) limitTop() int {
	if ds.OriginMode {
		return ds.scrollTop
	}
	return 0
}

func (ds *DrawState) limitBottom() int {
	if ds.OriginMode {
		return ds.scrollBottom
	}
	return ds.height - 1
}

func (ds *DrawState) saveCursor() {
	ds.save = savedCursor{ds.cursorCol, ds.cursorRow, ds.renditions, ds.AutoWrapMode, ds.OriginMode}
}

func (ds *DrawState) restoreCursor() {
	ds.cursorCol, ds.cursorRow = ds.save.col, ds.save.row
	ds.renditions = ds.save.renditions
	ds.AutoWrapMode, ds.OriginMode = ds.save.autoWrap, ds.save.originMode
	ds.snapCursorToBorder() // we could have resized in between
	ds.newGrapheme()
}

func (ds *DrawState) resize(width, height int) {
	if ds.width != width || ds.height != height {
		// Reset the scrolling region on any resize, as xterm does.
		ds.scrollTop, ds.scrollBottom = 0, height-1
	}
	if width > len(ds.tabs) {
		ds.tabs = append(ds.tabs, make([]bool, width-len(ds.tabs))...)
	} else {
		ds.tabs = ds.tabs[:width]
	}
	if ds.defaultTabs {
		ds.reinitializeTabs(ds.width)
	}
	ds.width, ds.height = width, height
	ds.snapCursorToBorder()
	if ds.combiningCol >= width || ds.combiningRow >= height {
		ds.combiningCol, ds.combiningRow = -1, -1
	}
}

// Equal compares only the fields that affect what is displayed.
func (ds *DrawState) Equal(o *DrawState) bool {
	return ds.width == o.width && ds.height == o.height && ds.cursorCol == o.cursorCol &&
		ds.cursorRow == o.cursorRow && ds.CursorVisible == o.CursorVisible &&
		ds.ReverseVideo == o.ReverseVideo && ds.renditions == o.renditions &&
		ds.BracketedPaste == o.BracketedPaste && ds.MouseReporting == o.MouseReporting &&
		ds.MouseFocusEvent == o.MouseFocusEvent && ds.MouseAltScroll == o.MouseAltScroll &&
		ds.MouseEncoding == o.MouseEncoding && linkEqual(ds.link, o.link)
}

// Framebuffer is the screen. Rows are shared copy-on-write between clones,
// which keeps SSP's many state copies cheap.
type Framebuffer struct {
	rows             []*Row
	iconName         []rune
	windowTitle      []rune
	clipboard        []rune
	bellCount        uint
	titleInitialized bool // the title was set by an OSC
	DS               DrawState
}

// sameRows returns n slots all pointing at one blank row.
func sameRows(n, width int, bg uint32) []*Row {
	r := newRow(width, bg)
	r.shared = n > 1
	rows := make([]*Row, n)
	for i := range rows {
		rows[i] = r
	}
	return rows
}

func NewFramebuffer(width, height int) *Framebuffer {
	return &Framebuffer{rows: sameRows(height, width, 0), DS: newDrawState(width, height)}
}

func (fb *Framebuffer) Clone() *Framebuffer {
	for _, r := range fb.rows {
		r.shared = true
	}
	c := *fb
	c.rows = append([]*Row(nil), fb.rows...)
	c.DS = fb.DS.clone()
	return &c
}

func (fb *Framebuffer) Rows() []*Row { return fb.rows }

func (fb *Framebuffer) newRow() *Row { return newRow(fb.DS.width, fb.DS.renditions.bg) }

func (fb *Framebuffer) Row(row int) *Row {
	if row == -1 {
		row = fb.DS.cursorRow
	}
	return fb.rows[row]
}

func (fb *Framebuffer) Cell(row, col int) *Cell {
	if row == -1 {
		row = fb.DS.cursorRow
	}
	if col == -1 {
		col = fb.DS.cursorCol
	}
	return &fb.rows[row].cells[col]
}

func (fb *Framebuffer) mutableRow(row int) *Row {
	if row == -1 {
		row = fb.DS.cursorRow
	}
	if fb.rows[row].shared {
		fb.rows[row] = fb.rows[row].clone()
	}
	return fb.rows[row]
}

func (fb *Framebuffer) mutableCell(row, col int) *Cell {
	if col == -1 {
		col = fb.DS.cursorCol
	}
	return &fb.mutableRow(row).cells[col]
}

func (fb *Framebuffer) scroll(n int) {
	if n >= 0 {
		fb.deleteLine(fb.DS.scrollTop, n)
	} else {
		fb.insertLine(fb.DS.scrollTop, -n)
	}
}

func (fb *Framebuffer) moveRowsAutoscroll(rows int) {
	ds := &fb.DS
	// Don't scroll if outside the scrolling region.
	if ds.cursorRow < ds.scrollTop || ds.cursorRow > ds.scrollBottom {
		ds.moveRow(rows, true)
		return
	}
	if ds.cursorRow+rows > ds.scrollBottom {
		n := ds.cursorRow + rows - ds.scrollBottom
		fb.scroll(n)
		ds.moveRow(-n, true)
	} else if ds.cursorRow+rows < ds.scrollTop {
		n := ds.cursorRow + rows - ds.scrollTop
		fb.scroll(n)
		ds.moveRow(-n, true)
	}
	ds.moveRow(rows, true)
}

// combiningCell is nil if a resize moved it off screen.
func (fb *Framebuffer) combiningCell() *Cell {
	ds := &fb.DS
	if ds.combiningCol < 0 || ds.combiningRow < 0 || ds.combiningCol >= ds.width || ds.combiningRow >= ds.height {
		return nil
	}
	return fb.mutableCell(ds.combiningRow, ds.combiningCol)
}

func (fb *Framebuffer) insertLine(beforeRow, count int) {
	ds := &fb.DS
	if beforeRow < ds.scrollTop || beforeRow > ds.scrollBottom+1 {
		return
	}
	n := min(ds.scrollBottom+1-beforeRow, count)
	if n <= 0 {
		return
	}
	// Drop n rows at the bottom of the region, insert n blank rows before beforeRow.
	region := append([]*Row(nil), fb.rows[beforeRow:ds.scrollBottom+1-n]...)
	copy(fb.rows[beforeRow:], sameRows(n, ds.width, ds.renditions.bg))
	copy(fb.rows[beforeRow+n:], region)
}

func (fb *Framebuffer) deleteLine(row, count int) {
	ds := &fb.DS
	if row < ds.scrollTop || row > ds.scrollBottom {
		return
	}
	n := min(ds.scrollBottom+1-row, count)
	if n <= 0 {
		return
	}
	// Remove rows row..row+n-1, append n blank rows at the bottom of the region.
	copy(fb.rows[row:], fb.rows[row+n:ds.scrollBottom+1])
	copy(fb.rows[ds.scrollBottom+1-n:], sameRows(n, ds.width, ds.renditions.bg))
}

func (fb *Framebuffer) insertCell(row, col int) {
	fb.mutableRow(row).insertCell(col, fb.DS.renditions.bg)
}

func (fb *Framebuffer) deleteCell(row, col int) {
	fb.mutableRow(row).deleteCell(col, fb.DS.renditions.bg)
}

func (fb *Framebuffer) resetCell(c *Cell) { c.reset(fb.DS.renditions.bg) }

func (fb *Framebuffer) resetRow(r *Row) { r.reset(fb.DS.renditions.bg) }

func (fb *Framebuffer) reset() {
	w, h := fb.DS.width, fb.DS.height
	fb.DS = newDrawState(w, h)
	fb.rows = sameRows(h, w, 0)
	fb.windowTitle, fb.clipboard = nil, nil
	// bell count is not reset
}

func (fb *Framebuffer) softReset() {
	ds := &fb.DS
	ds.InsertMode, ds.OriginMode = false, false
	ds.CursorVisible = true // per xterm and gnome-terminal
	ds.AppCursorKeys = false
	ds.setScrollingRegion(0, ds.height-1)
	ds.renditions.setRendition(0)
	ds.link = nil
	ds.save = defaultSavedCursor()
}

func (fb *Framebuffer) Resize(width, height int) {
	oldHeight, oldWidth := fb.DS.height, fb.DS.width
	fb.DS.resize(width, height)
	blank := fb.newRow()
	blank.shared = true
	if oldHeight != height {
		if height < len(fb.rows) {
			fb.rows = fb.rows[:height]
		} else {
			for len(fb.rows) < height {
				fb.rows = append(fb.rows, blank)
			}
		}
	}
	if oldWidth == width {
		return
	}
	for i, r := range fb.rows {
		if r == blank {
			break
		}
		r = r.clone()
		r.setWrap(false)
		if width < len(r.cells) {
			r.cells = r.cells[:width]
		} else {
			for len(r.cells) < width {
				r.cells = append(r.cells, blankCell(fb.DS.renditions.bg))
			}
		}
		fb.rows[i] = r
	}
}

func (fb *Framebuffer) prefixWindowTitle(s []rune) {
	if string(fb.iconName) == string(fb.windowTitle) {
		fb.iconName = append(append([]rune(nil), s...), fb.iconName...) // preserve equivalence
	}
	fb.windowTitle = append(append([]rune(nil), s...), fb.windowTitle...)
}

func (fb *Framebuffer) IconName() []rune       { return fb.iconName }
func (fb *Framebuffer) WindowTitle() []rune    { return fb.windowTitle }
func (fb *Framebuffer) Clipboard() []rune      { return fb.clipboard }
func (fb *Framebuffer) BellCount() uint        { return fb.bellCount }
func (fb *Framebuffer) TitleInitialized() bool { return fb.titleInitialized }

func (fb *Framebuffer) Equal(o *Framebuffer) bool {
	if len(fb.rows) != len(o.rows) {
		return false
	}
	for i := range fb.rows {
		if !fb.rows[i].Equal(o.rows[i]) {
			return false
		}
	}
	return string(fb.windowTitle) == string(o.windowTitle) && string(fb.clipboard) == string(o.clipboard) &&
		fb.bellCount == o.bellCount && fb.DS.Equal(&o.DS)
}
