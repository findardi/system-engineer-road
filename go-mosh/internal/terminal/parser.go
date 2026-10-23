package terminal

import "unicode/utf8"

// The parser is the DEC/ANSI state machine from vt100.net/emu/dec_ansi_parser,
// ported from mosh (src/terminal/parser*.cc). It turns characters into
// actions; the Emulator executes them.

type actionKind uint8

const (
	actIgnore actionKind = iota
	actPrint
	actExecute
	actClear
	actCollect
	actParam
	actEscDispatch
	actCSIDispatch
	actHook
	actPut
	actUnhook
	actOSCStart
	actOSCPut
	actOSCEnd
)

type action struct {
	kind actionKind
	ch   rune
}

type parserState uint8

const (
	stGround parserState = iota
	stEscape
	stEscapeIntermediate
	stCSIEntry
	stCSIParam
	stCSIIntermediate
	stCSIIgnore
	stDCSEntry
	stDCSParam
	stDCSIntermediate
	stDCSPassthrough
	stDCSIgnore
	stOSCString
	stSOSPMAPCString

	stNone parserState = 255 // no transition
)

func enterAction(s parserState) actionKind {
	switch s {
	case stEscape, stCSIEntry, stDCSEntry:
		return actClear
	case stDCSPassthrough:
		return actHook
	case stOSCString:
		return actOSCStart
	}
	return actIgnore
}

func exitAction(s parserState) actionKind {
	switch s {
	case stDCSPassthrough:
		return actUnhook
	case stOSCString:
		return actOSCEnd
	}
	return actIgnore
}

func in(ch, lo, hi rune) bool { return lo <= ch && ch <= hi }

// c0Prime is C0 minus the controls handled by the "anywhere" rule.
func c0Prime(ch rune) bool { return ch <= 0x17 || ch == 0x19 || in(ch, 0x1C, 0x1F) }

// anywhere holds the transitions valid from every state.
func anywhere(ch rune) (actionKind, parserState) {
	switch {
	case ch == 0x18, ch == 0x1A, in(ch, 0x80, 0x8F), in(ch, 0x91, 0x97), ch == 0x99, ch == 0x9A:
		return actExecute, stGround
	case ch == 0x9C:
		return actIgnore, stGround
	case ch == 0x1B:
		return actIgnore, stEscape
	case ch == 0x98, ch == 0x9E, ch == 0x9F:
		return actIgnore, stSOSPMAPCString
	case ch == 0x90:
		return actIgnore, stDCSEntry
	case ch == 0x9D:
		return actIgnore, stOSCString
	case ch == 0x9B:
		return actIgnore, stCSIEntry
	}
	return actIgnore, stNone
}

// rule is the per-state transition table.
func rule(s parserState, ch rune) (actionKind, parserState) {
	switch s {
	case stGround:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x20, 0x7F), in(ch, 0xA0, 0xFF):
			return actPrint, stNone
		}
	case stEscape:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x20, 0x2F):
			return actCollect, stEscapeIntermediate
		case in(ch, 0x30, 0x4F), in(ch, 0x51, 0x57), ch == 0x59, ch == 0x5A, ch == 0x5C, in(ch, 0x60, 0x7E):
			return actEscDispatch, stGround
		case ch == 0x5B:
			return actIgnore, stCSIEntry
		case ch == 0x5D:
			return actIgnore, stOSCString
		case ch == 0x50:
			return actIgnore, stDCSEntry
		case ch == 0x58, ch == 0x5E, ch == 0x5F:
			return actIgnore, stSOSPMAPCString
		}
	case stEscapeIntermediate:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x20, 0x2F):
			return actCollect, stNone
		case in(ch, 0x30, 0x7E):
			return actEscDispatch, stGround
		}
	case stCSIEntry:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x40, 0x7E):
			return actCSIDispatch, stGround
		case in(ch, 0x30, 0x39), ch == 0x3B:
			return actParam, stCSIParam
		case in(ch, 0x3C, 0x3F):
			return actCollect, stCSIParam
		case ch == 0x3A:
			return actIgnore, stCSIIgnore
		case in(ch, 0x20, 0x2F):
			return actCollect, stCSIIntermediate
		}
	case stCSIParam:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x30, 0x39), ch == 0x3B:
			return actParam, stNone
		case ch == 0x3A, in(ch, 0x3C, 0x3F):
			return actIgnore, stCSIIgnore
		case in(ch, 0x20, 0x2F):
			return actCollect, stCSIIntermediate
		case in(ch, 0x40, 0x7E):
			return actCSIDispatch, stGround
		}
	case stCSIIntermediate:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x20, 0x2F):
			return actCollect, stNone
		case in(ch, 0x40, 0x7E):
			return actCSIDispatch, stGround
		case in(ch, 0x30, 0x3F):
			return actIgnore, stCSIIgnore
		}
	case stCSIIgnore:
		switch {
		case c0Prime(ch):
			return actExecute, stNone
		case in(ch, 0x40, 0x7E):
			return actIgnore, stGround
		}
	case stDCSEntry:
		switch {
		case in(ch, 0x20, 0x2F):
			return actCollect, stDCSIntermediate
		case ch == 0x3A:
			return actIgnore, stDCSIgnore
		case in(ch, 0x30, 0x39), ch == 0x3B:
			return actParam, stDCSParam
		case in(ch, 0x3C, 0x3F):
			return actCollect, stDCSParam
		case in(ch, 0x40, 0x7E):
			return actIgnore, stDCSPassthrough
		}
	case stDCSParam:
		switch {
		case in(ch, 0x30, 0x39), ch == 0x3B:
			return actParam, stNone
		case ch == 0x3A, in(ch, 0x3C, 0x3F):
			return actIgnore, stDCSIgnore
		case in(ch, 0x20, 0x2F):
			return actCollect, stDCSIntermediate
		case in(ch, 0x40, 0x7E):
			return actIgnore, stDCSPassthrough
		}
	case stDCSIntermediate:
		switch {
		case in(ch, 0x20, 0x2F):
			return actCollect, stNone
		case in(ch, 0x40, 0x7E):
			return actIgnore, stDCSPassthrough
		case in(ch, 0x30, 0x3F):
			return actIgnore, stDCSIgnore
		}
	case stDCSPassthrough:
		switch {
		case c0Prime(ch), in(ch, 0x20, 0x7E):
			return actPut, stNone
		case ch == 0x9C:
			return actIgnore, stGround
		}
	case stDCSIgnore, stSOSPMAPCString:
		if ch == 0x9C {
			return actIgnore, stGround
		}
	case stOSCString:
		switch {
		case in(ch, 0x20, 0x7F):
			return actOSCPut, stNone
		case ch == 0x9C, ch == 0x07: // BEL is the xterm variant
			return actIgnore, stGround
		}
	}
	return actIgnore, stNone
}

type parser struct {
	state parserState
	buf   [utf8.UTFMax]byte // pending bytes of a multibyte character
	n     int
}

// input feeds one character through the state machine, appending the
// resulting actions (at most three: exit, transition, enter) to out.
func (p *parser) input(ch rune, out []action) []action {
	k, next := anywhere(ch)
	if next == stNone {
		r := ch
		if r >= 0xA0 {
			r = 0x41 // high code points parse like 'A'
		}
		k, next = rule(p.state, r)
	}
	if next != stNone {
		if e := exitAction(p.state); e != actIgnore {
			out = append(out, action{kind: e})
		}
	}
	if k != actIgnore {
		out = append(out, action{k, ch})
	}
	if next != stNone {
		if e := enterAction(next); e != actIgnore {
			out = append(out, action{kind: e})
		}
		p.state = next
	}
	return out
}

// inputByte decodes UTF-8 one byte at a time, following mosh's handling of
// invalid input (Unicode 6.0 section 3.9, "Best Practices for using U+FFFD").
// Unlike glibc, which mosh relies on, Go rejects surrogates and code points
// above U+10FFFF byte by byte, so those yield more U+FFFD than in mosh.
func (p *parser) inputByte(b byte, out []action) []action {
	if p.n == 0 && b <= 0x7F {
		return p.input(rune(b), out)
	}
	p.buf[p.n] = b
	p.n++
	for p.n > 0 {
		if !utf8.FullRune(p.buf[:p.n]) {
			return out // incomplete but valid so far
		}
		r, size := utf8.DecodeRune(p.buf[:p.n])
		if r == utf8.RuneError && size <= 1 {
			// Invalid: emit U+FFFD and retry the last byte on its own.
			if p.n > 1 {
				p.buf[0], p.n = p.buf[p.n-1], 1
			} else {
				p.n = 0
			}
			out = p.input(utf8.RuneError, out)
			continue
		}
		copy(p.buf[:], p.buf[size:p.n])
		p.n -= size
		out = p.input(r, out)
	}
	return out
}
