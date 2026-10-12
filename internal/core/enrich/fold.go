package enrich

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/arosl/nhm/internal/redaction"
)

// Text from CM and CASS is checked in a folded form and shown as it came
// (review of fd98368, acfs-9hkh.65): folding the shown text broke ZWJ
// emoji and Persian and Indic text, and NFKC rewrote "…", "²" and "ﬁ". So
// the fold only finds the secrets and the frame markers, and each one is
// mapped back to the bytes of the original that it came from.

// ignorable reports the code points that render as nothing, so they can
// hide a marker or a secret: Default_Ignorable_Code_Point (format
// characters, variation selectors such as U+FE0F, the combining grapheme
// joiner U+034F, the Hangul fillers, …).
func ignorable(r rune) bool {
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r) || r == 0x034F
}

// folded is s without its ignorable code points, each rune in NFKD (a
// full-width "［" is "[") without its nonspacing marks, and for each byte
// of text the byte range of s it came from.
type folded struct {
	text       string
	start, end []int
}

func fold(s string) folded {
	var b strings.Builder
	var f folded
	for i, r := range s {
		size := utf8.RuneLen(r)
		if r == utf8.RuneError {
			_, size = utf8.DecodeRuneInString(s[i:])
		}
		var piece string
		switch {
		case blank(r):
			piece = " "
		case ignorable(r):
			continue
		default:
			piece = bare(norm.NFKD.String(s[i : i+size]))
		}
		for range len(piece) {
			f.start, f.end = append(f.start, i), append(f.end, i+size)
		}
		b.WriteString(piece)
	}
	f.text = b.String()
	return f
}

// blank reports the code points that render as an empty cell, so they
// separate words as a space does (reviews of 4424fa6 and b53018e): the
// blank braille pattern U+2800 and the Hangul fillers. Dropped, they
// would join the words of a marker; as spaces, the marker check sees
// them.
func blank(r rune) bool {
	switch r {
	case 0x2800, 0x115F, 0x1160, 0x3164, 0xFFA0:
		return true
	}
	return false
}

// bare drops the marks of a rune's NFKD form (reviews of 4424fa6 and
// b53018e): nonspacing (a combining dot under "h" or "F", or the mark of
// a precomposed "ḥ"), spacing (U+0903, U+1D165) and enclosing (U+20DD,
// U+0488), none of which changes how the letters read. The fold works
// rune by rune, so NFKC would never compose a mark into its letter;
// decomposing and dropping the marks leaves the base letters.
func bare(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.M, r) {
			return -1
		}
		return r
	}, s)
}

// original maps a byte range of the folded text to the range of s it
// came from.
func (f folded) original(start, end int) (int, int) {
	return f.start[start], f.end[end-1]
}

// redact replaces each secret found in the folded text with its
// placeholder, in the original text, and returns the count.
func redact(s string) (string, int) {
	f := fold(s)
	spans, n := redaction.Find(f.text)
	if len(spans) == 0 {
		return s, n
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		start, end := f.original(sp.Start, sp.End)
		if start < last {
			// Two folded spans from one original rune: they share it.
			start = last
		}
		b.WriteString(s[last:start])
		b.WriteString(redaction.Placeholder(sp.Category))
		last = max(last, end)
	}
	b.WriteString(s[last:])
	return b.String(), n
}

// skeleton is the folded text as runes for the marker check: ASCII
// letters in lower case, spaces as ' ', any other letter as '*' (it
// matches any letter: a Cyrillic "е" or a Greek "ο" can read as Latin),
// any opening bracket but "(" as '[', the rest as it is; and for each
// rune the start of the original rune it came from.
func skeleton(s string) ([]rune, []int) {
	f := fold(s)
	var sk []rune
	var at []int
	for i, r := range f.text {
		switch {
		case r < utf8.RuneSelf && unicode.IsLetter(r):
			r = unicode.ToLower(r)
		case unicode.IsSpace(r):
			r = ' '
		case unicode.IsLetter(r):
			r = '*'
		case r != '(' && unicode.Is(unicode.Ps, r):
			// Any opening bracket (⟦, ⁅, 【, {, …) can read as "[". Not
			// "(": that is what neutralize writes in a marker's place.
			r = '['
		}
		sk, at = append(sk, r), append(at, f.start[i])
	}
	return sk, at
}

// markers are the frame lines' openings that a text must not hold:
// "[nhm context" and "[end of nhm context", with any spacing.
func markerAt(sk []rune, i int) bool {
	if sk[i] != '[' {
		return false
	}
	j := spaces(sk, i+1, 0)
	if k := word(sk, j, "end"); k >= 0 {
		if k = word(sk, spaces(sk, k, 1), "of"); k >= 0 && nhmContext(sk, spaces(sk, k, 1)) {
			return true
		}
	}
	return nhmContext(sk, j)
}

func nhmContext(sk []rune, i int) bool {
	k := word(sk, i, "nhm")
	return k >= 0 && word(sk, spaces(sk, k, 1), "context") >= 0
}

// spaces skips the spaces at i; -1 when there are fewer than min.
func spaces(sk []rune, i, min int) int {
	if i < 0 {
		return -1
	}
	j := i
	for j < len(sk) && sk[j] == ' ' {
		j++
	}
	if j-i < min {
		return -1
	}
	return j
}

// word matches w at i, a '*' for any letter; the index after it, or -1.
func word(sk []rune, i int, w string) int {
	if i < 0 || i+len(w) > len(sk) {
		return -1
	}
	for k, c := range w {
		if r := sk[i+k]; r != c && r != '*' {
			return -1
		}
	}
	return i + len(w)
}

// neutralize turns the "[" of each marker in s into "(", so the text can
// never open or close the frame early, whatever hides in it.
func neutralize(s string) string {
	sk, at := skeleton(s)
	var cut []int
	for i := range sk {
		if markerAt(sk, i) {
			cut = append(cut, at[i])
		}
	}
	if len(cut) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, c := range cut {
		if c < last {
			continue
		}
		_, size := utf8.DecodeRuneInString(s[c:])
		b.WriteString(s[last:c])
		b.WriteString("(")
		last = c + size
	}
	b.WriteString(s[last:])
	return b.String()
}
