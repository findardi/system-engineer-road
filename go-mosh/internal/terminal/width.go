package terminal

import (
	"unicode"

	"golang.org/x/text/width"
)

// runeWidth is wcwidth(3): columns a character takes, 0 for combining
// characters, -1 for unprintable ones. It matches glibc, which mosh uses, on
// every code point except a few hundred added after Go's Unicode tables.
//
// ponytail: unassigned code points count as printable (glibc says -1); refresh
// when Go's unicode package catches up with glibc.
func runeWidth(r rune) int {
	switch {
	case unicode.IsControl(r), unicode.Is(unicode.Cs, r), r == 0x2028, r == 0x2029:
		return -1
	case r == 0xAD, unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return 1 // format characters that glibc still draws
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf),
		r >= 0x1160 && r <= 0x11FF, r >= 0xD7B0 && r <= 0xD7FF, // Hangul medial and final jamo
		r == 0x200B, r == 0x3164, r == 0xFFA0: // zero-width space, Hangul fillers
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}
