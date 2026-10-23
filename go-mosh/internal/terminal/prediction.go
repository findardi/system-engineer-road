package terminal

import (
	"math"
	"time"
)

// Local echo prediction, ported from mosh's PredictionEngine
// (src/frontend/terminaloverlay.cc). The client guesses the effect of each
// keystroke and draws it before the server answers. Guesses are grouped in
// epochs: an epoch stays hidden ("tentative") until one of its guesses is
// confirmed by the server's screen, and a wrong guess kills its epoch.

// PredictionMode says when predictions are shown.
type PredictionMode int

const (
	PredictAdaptive     PredictionMode = iota // when the link is slow
	PredictAlways                             // on every keystroke
	PredictNever                              // off
	PredictExperimental                       // aggressive, mosh's "experimental"
)

// Timing thresholds in milliseconds, same as mosh.
const (
	srttTriggerLow          = 20   // send interval at or below this hides predictions
	srttTriggerHigh         = 30   // above this shows them
	flagTriggerLow          = 50   // at or below this stops underlining
	flagTriggerHigh         = 80   // above this underlines
	glitchThreshold         = 250  // a guess pending this long is a glitch
	glitchRepairCount       = 10   // quick confirmations needed to cure a glitch
	glitchRepairMinInterval = 150  // between those confirmations
	glitchFlagThreshold     = 5000 // a guess pending this long gets underlined
)

const noFrame = math.MaxUint64

type validity int

const (
	pending validity = iota
	correct
	correctNoCredit
	incorrectOrExpired
	inactive
)

type conditional struct {
	expirationFrame uint64 // the guess is judged once the server acks this input frame
	col             int
	active          bool
	tentativeUntil  uint64 // hidden until this epoch is confirmed
	predictionTime  int64
}

func (o *conditional) tentative(confirmedEpoch uint64) bool { return o.tentativeUntil > confirmedEpoch }

func (o *conditional) reset() {
	o.expirationFrame, o.tentativeUntil, o.active = noFrame, noFrame, false
}

func (o *conditional) expire(frame uint64, now int64) {
	o.expirationFrame, o.predictionTime = frame, now
}

type cursorMove struct {
	conditional
	row int
}

func (c *cursorMove) apply(fb *Framebuffer, confirmedEpoch uint64) {
	if !c.active || c.tentative(confirmedEpoch) {
		return
	}
	fb.DS.moveRow(c.row, false)
	fb.DS.moveCol(c.col, false, false)
}

func (c *cursorMove) validity(fb *Framebuffer, lateAck uint64) validity {
	switch {
	case !c.active:
		return inactive
	case c.row >= fb.DS.height || c.col >= fb.DS.width:
		return incorrectOrExpired
	case lateAck < c.expirationFrame:
		return pending
	case fb.DS.cursorCol == c.col && fb.DS.cursorRow == c.row:
		return correct
	}
	return incorrectOrExpired
}

type overlayCell struct {
	conditional
	replacement Cell
	unknown     bool   // we know the cell changes, not to what
	original    []Cell // no credit for guesses that match what was there before
}

func (c *overlayCell) reset() {
	c.unknown, c.original = false, nil
	c.conditional.reset()
}

func (c *overlayCell) resetWithOrig() {
	if !c.active || c.unknown {
		c.reset()
		return
	}
	c.original = append(c.original, c.replacement)
	c.conditional.reset()
}

func (c *overlayCell) apply(fb *Framebuffer, confirmedEpoch uint64, row int, flag bool) {
	if !c.active || row >= fb.DS.height || c.col >= fb.DS.width || c.tentative(confirmedEpoch) {
		return
	}
	if c.replacement.isBlank() && fb.Cell(row, c.col).isBlank() {
		flag = false
	}
	if c.unknown {
		if flag && c.col != fb.DS.width-1 {
			fb.mutableCell(row, c.col).renditions.setAttr(attrUnderlined, true)
		}
		return
	}
	if !fb.Cell(row, c.col).Equal(&c.replacement) {
		cell := fb.mutableCell(row, c.col)
		*cell = c.replacement
		if flag {
			cell.renditions.setAttr(attrUnderlined, true)
		}
	}
}

func (c *overlayCell) validity(fb *Framebuffer, row int, lateAck uint64) validity {
	switch {
	case !c.active:
		return inactive
	case row >= fb.DS.height || c.col >= fb.DS.width:
		return incorrectOrExpired
	case lateAck < c.expirationFrame:
		return pending // the server has not echoed it yet
	case c.unknown, c.replacement.isBlank(): // a blank is too easy to match by accident
		return correctNoCredit
	}
	if !fb.Cell(row, c.col).contentsMatch(&c.replacement) {
		return incorrectOrExpired
	}
	for i := range c.original {
		if c.original[i].contentsMatch(&c.replacement) {
			return correctNoCredit
		}
	}
	return correct
}

type overlayRow struct {
	rowNum int
	cells  []overlayCell
}

// PredictionEngine guesses the screen effect of keystrokes. Feed it every
// keystroke with NewUserByte and run Cull then Apply on each frame to show.
type PredictionEngine struct {
	Mode      PredictionMode
	Overwrite bool // predict typing as overwrite instead of insert

	lastByte         byte
	parser           parser
	actions          []action
	rows             []*overlayRow
	cursors          []cursorMove // the last one is the current guess
	frameSent        uint64       // newest input frame sent
	frameAcked       uint64       // newest input frame the server acked
	frameLateAcked   uint64       // newest input frame the server's screen reflects (echo ack)
	predictionEpoch  uint64
	confirmedEpoch   uint64
	flagging         bool // underline predictions
	srttTrigger      bool // show predictions because the round trip is slow
	glitchTrigger    int  // show predictions because one took long to confirm
	lastQuickConfirm int64
	sendInterval     int
	lastWidth        int
	lastHeight       int
	now              func() int64
}

var processStart = time.Now()

func NewPredictionEngine() *PredictionEngine {
	return &PredictionEngine{
		predictionEpoch: 1,
		sendInterval:    250,
		// A large base keeps "now - interval" positive, as with mosh's uptime clock.
		now: func() int64 { return time.Since(processStart).Milliseconds() + 1<<30 },
	}
}

func (p *PredictionEngine) SetFrameSent(n uint64)      { p.frameSent = n }
func (p *PredictionEngine) SetFrameAcked(n uint64)     { p.frameAcked = n }
func (p *PredictionEngine) SetFrameLateAcked(n uint64) { p.frameLateAcked = n }
func (p *PredictionEngine) SetSendInterval(ms int)     { p.sendInterval = ms }

// WaitTime is how soon timing-based triggers need another look.
func (p *PredictionEngine) WaitTime() time.Duration {
	if !(p.glitchTrigger > 0 && p.flagging) && p.active() {
		return 50 * time.Millisecond
	}
	return time.Hour
}

func (p *PredictionEngine) cursor() *cursorMove { return &p.cursors[len(p.cursors)-1] }

func (p *PredictionEngine) active() bool {
	if len(p.cursors) > 0 {
		return true
	}
	for _, r := range p.rows {
		for i := range r.cells {
			if r.cells[i].active {
				return true
			}
		}
	}
	return false
}

func (p *PredictionEngine) becomeTentative() {
	if p.Mode != PredictExperimental {
		p.predictionEpoch++
	}
}

// Reset drops every guess, e.g. after a resize or a paste.
func (p *PredictionEngine) Reset() {
	p.cursors, p.rows = nil, nil
	p.becomeTentative()
}

func (p *PredictionEngine) newCursor(row, col int) {
	c := cursorMove{conditional{expirationFrame: p.frameSent + 1, col: col, tentativeUntil: p.predictionEpoch, predictionTime: math.MaxInt64}, row}
	c.active = true
	p.cursors = append(p.cursors, c)
}

func (p *PredictionEngine) killEpoch(epoch uint64, fb *Framebuffer) {
	kept := p.cursors[:0]
	for _, c := range p.cursors {
		if !c.tentative(epoch - 1) {
			kept = append(kept, c)
		}
	}
	p.cursors = kept
	p.newCursor(fb.DS.cursorRow, fb.DS.cursorCol)
	for _, r := range p.rows {
		for i := range r.cells {
			if r.cells[i].tentative(epoch - 1) {
				r.cells[i].reset()
			}
		}
	}
	p.becomeTentative()
}

func (p *PredictionEngine) initCursor(fb *Framebuffer) {
	if len(p.cursors) == 0 {
		p.newCursor(fb.DS.cursorRow, fb.DS.cursorCol)
	} else if p.cursor().tentativeUntil != p.predictionEpoch {
		p.newCursor(p.cursor().row, p.cursor().col)
	}
}

func (p *PredictionEngine) row(rowNum, cols int) *overlayRow {
	for _, r := range p.rows {
		if r.rowNum == rowNum {
			return r
		}
	}
	r := &overlayRow{rowNum: rowNum, cells: make([]overlayCell, cols)}
	for i := range r.cells {
		r.cells[i] = overlayCell{conditional: conditional{col: i, tentativeUntil: p.predictionEpoch, predictionTime: math.MaxInt64}}
	}
	p.rows = append(p.rows, r)
	return r
}

// Apply draws the visible guesses onto fb, a copy of the server's screen.
func (p *PredictionEngine) Apply(fb *Framebuffer) {
	if p.Mode == PredictNever || !(p.srttTrigger || p.glitchTrigger > 0 || p.Mode == PredictAlways || p.Mode == PredictExperimental) {
		return
	}
	for i := range p.cursors {
		p.cursors[i].apply(fb, p.confirmedEpoch)
	}
	for _, r := range p.rows {
		for i := range r.cells {
			r.cells[i].apply(fb, p.confirmedEpoch, r.rowNum, p.flagging)
		}
	}
}

// Cull judges guesses against the server's latest screen fb: confirmed ones
// are retired, wrong ones dropped along with their epoch.
func (p *PredictionEngine) Cull(fb *Framebuffer) {
	if p.Mode == PredictNever {
		return
	}
	if p.lastHeight != fb.DS.height || p.lastWidth != fb.DS.width {
		p.lastHeight, p.lastWidth = fb.DS.height, fb.DS.width
		p.Reset()
	}
	now := p.now()

	// Hysteresis on both triggers.
	if p.sendInterval > srttTriggerHigh {
		p.srttTrigger = true
	} else if p.srttTrigger && p.sendInterval <= srttTriggerLow && !p.active() {
		p.srttTrigger = false // only once no guesses are shown
	}
	if p.sendInterval > flagTriggerHigh {
		p.flagging = true
	} else if p.sendInterval <= flagTriggerLow {
		p.flagging = false
	}
	if p.glitchTrigger > glitchRepairCount { // big glitches also underline
		p.flagging = true
	}

	kept := p.rows[:0]
	for _, r := range p.rows {
		if r.rowNum >= 0 && r.rowNum < fb.DS.height {
			kept = append(kept, r)
		}
	}
	p.rows = kept
	for _, r := range p.rows {
		for j := range r.cells {
			c := &r.cells[j]
			switch c.validity(fb, r.rowNum, p.frameLateAcked) {
			case incorrectOrExpired:
				if p.Mode == PredictExperimental {
					c.reset()
				} else if c.tentative(p.confirmedEpoch) {
					p.killEpoch(c.tentativeUntil, fb)
				} else {
					p.Reset()
					return
				}
			case correct:
				if c.tentativeUntil > p.confirmedEpoch {
					p.confirmedEpoch = c.tentativeUntil
				}
				// Quick confirmations slowly take away the glitch trigger.
				if now-c.predictionTime < glitchThreshold && p.glitchTrigger > 0 && now-glitchRepairMinInterval >= p.lastQuickConfirm {
					p.glitchTrigger--
					p.lastQuickConfirm = now
				}
				// Match the rest of the row to the real renditions.
				actual := fb.Cell(r.rowNum, c.col).renditions
				for k := j; k < len(r.cells); k++ {
					r.cells[k].replacement.renditions = actual
				}
				c.reset()
			case correctNoCredit:
				c.reset()
			case pending:
				// A guess that takes long to confirm turns predictions on even
				// on a fast link.
				if now-c.predictionTime >= glitchFlagThreshold {
					p.glitchTrigger = glitchRepairCount * 2 // show and underline
				} else if now-c.predictionTime >= glitchThreshold && p.glitchTrigger < glitchRepairCount {
					p.glitchTrigger = glitchRepairCount // just show
				}
			}
		}
	}

	if len(p.cursors) > 0 && p.cursor().validity(fb, p.frameLateAcked) == incorrectOrExpired {
		if p.Mode == PredictExperimental {
			p.cursors = nil
		} else {
			p.Reset()
			return
		}
	}
	keptCursors := p.cursors[:0]
	for _, c := range p.cursors {
		if c.validity(fb, p.frameLateAcked) == pending {
			keptCursors = append(keptCursors, c)
		}
	}
	p.cursors = keptCursors
}

// NewUserByte records one keystroke. fb is the screen as currently shown.
func (p *PredictionEngine) NewUserByte(b byte, fb *Framebuffer) {
	if p.Mode == PredictNever {
		return
	}
	if p.Mode == PredictExperimental {
		p.predictionEpoch = p.confirmedEpoch
	}
	p.Cull(fb)
	now := p.now()

	// The user's terminal is in application cursor mode: read ESC O as ESC [.
	if p.lastByte == 0x1b && b == 'O' {
		b = '['
	}
	p.lastByte = b

	p.actions = p.parser.inputByte(b, p.actions[:0])
	for _, a := range p.actions {
		switch a.kind {
		case actPrint:
			p.initCursor(fb)
			switch {
			case a.ch == 0x7f:
				p.backspace(fb, now)
			case a.ch < 0x20 || runeWidth(a.ch) != 1:
				p.becomeTentative() // unknown print, e.g. a wide character
			default:
				p.print(a.ch, fb, now)
			}
		case actExecute:
			p.becomeTentative()
			if a.ch == '\r' {
				p.newlineCarriageReturn(fb)
			}
		case actEscDispatch:
			p.becomeTentative()
		case actCSIDispatch:
			switch a.ch {
			case 'C': // right arrow
				p.initCursor(fb)
				if p.cursor().col < fb.DS.width-1 {
					p.cursor().col++
					p.cursor().expire(p.frameSent+1, now)
				}
			case 'D': // left arrow
				p.initCursor(fb)
				if p.cursor().col > 0 {
					p.cursor().col--
					p.cursor().expire(p.frameSent+1, now)
				}
			default:
				p.becomeTentative()
			}
		}
	}
}

// startCell turns cell into a fresh guess in the current epoch.
func (p *PredictionEngine) startCell(c *overlayCell, now int64) {
	c.resetWithOrig()
	c.active = true
	c.tentativeUntil = p.predictionEpoch
	c.expire(p.frameSent+1, now)
}

func (p *PredictionEngine) backspace(fb *Framebuffer, now int64) {
	cur := p.cursor()
	r := p.row(cur.row, fb.DS.width)
	if cur.col <= 0 {
		return
	}
	cur.col--
	cur.expire(p.frameSent+1, now)
	if p.Overwrite {
		c := &r.cells[cur.col]
		p.startCell(c, now)
		orig := *fb.Cell(-1, -1)
		c.original = append(c.original, orig)
		c.replacement = orig
		c.replacement.contents = " "
		return
	}
	// Everything right of the cursor shifts left by one.
	for i := cur.col; i < fb.DS.width; i++ {
		c := &r.cells[i]
		p.startCell(c, now)
		c.original = append(c.original, *fb.Cell(cur.row, i))
		if i+2 >= fb.DS.width {
			c.unknown = true
			continue
		}
		next := &r.cells[i+1]
		switch {
		case next.active && next.unknown:
			c.unknown = true
		case next.active:
			c.unknown, c.replacement = false, next.replacement
		default:
			c.unknown, c.replacement = false, *fb.Cell(cur.row, i+1)
		}
	}
}

func (p *PredictionEngine) print(ch rune, fb *Framebuffer, now int64) {
	cur := p.cursor()
	r := p.row(cur.row, fb.DS.width)
	if cur.col+1 >= fb.DS.width {
		// The last column is tricky: emacs shows a wrap mark, a shell
		// just puts the character there.
		p.becomeTentative()
	}

	// Insert: everything right of the cursor shifts right by one.
	rightmost := fb.DS.width - 1
	if p.Overwrite {
		rightmost = cur.col
	}
	for i := rightmost; i > cur.col; i-- {
		c := &r.cells[i]
		p.startCell(c, now)
		c.original = append(c.original, *fb.Cell(cur.row, i))
		prev := &r.cells[i-1]
		switch {
		case i == fb.DS.width-1:
			c.unknown = true
		case prev.active && prev.unknown:
			c.unknown = true
		case prev.active:
			c.unknown, c.replacement = false, prev.replacement
		default:
			c.unknown, c.replacement = false, *fb.Cell(cur.row, i-1)
		}
	}

	c := &r.cells[cur.col]
	p.startCell(c, now)
	c.replacement.renditions = fb.DS.renditions
	if cur.col > 0 { // heuristic: match the renditions of the character to the left
		prev := &r.cells[cur.col-1]
		if prev.active && !prev.unknown {
			c.replacement.renditions = prev.replacement.renditions
		} else {
			c.replacement.renditions = fb.Cell(cur.row, cur.col-1).renditions
		}
	}
	c.replacement.contents = ""
	c.replacement.append(ch)
	c.original = append(c.original, *fb.Cell(cur.row, cur.col))

	cur.expire(p.frameSent+1, now)
	if cur.col < fb.DS.width-1 {
		cur.col++
	} else {
		p.becomeTentative()
		p.newlineCarriageReturn(fb)
	}
}

func (p *PredictionEngine) newlineCarriageReturn(fb *Framebuffer) {
	now := p.now()
	p.initCursor(fb)
	cur := p.cursor()
	cur.col = 0
	if cur.row < fb.DS.height-1 {
		cur.row++
		return
	}
	// On the last row, predict a blank row; scrolling is not predicted.
	r := p.row(cur.row, fb.DS.width)
	for i := range r.cells {
		c := &r.cells[i]
		c.active = true
		c.tentativeUntil = p.predictionEpoch
		c.expire(p.frameSent+1, now)
		c.replacement.contents = ""
	}
}

// DrawNotice writes a notification bar over the top-left of fb, bold white
// on blue, like mosh's NotificationEngine.
func DrawNotice(fb *Framebuffer, text string) {
	col := 0
	combining := fb.mutableCell(0, 0)
	for _, ch := range text {
		if col >= fb.DS.width {
			break
		}
		switch w := runeWidth(ch); w {
		case 1, 2:
			c := fb.mutableCell(0, col)
			fb.resetCell(c)
			c.renditions.setAttr(attrBold, true)
			c.renditions.setForeground(7)
			c.renditions.setBackground(4)
			c.append(ch)
			c.wide = w == 2
			combining = c
			col += w
		case 0:
			if combining.empty() {
				combining.fallback = true
				col++
			}
			if !combining.full() {
				combining.append(ch)
			}
		}
	}
}
