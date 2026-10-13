package normalize

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// foldLetters transliterates letters that do not decompose into ASCII plus
// combining marks.
var foldLetters = map[rune]string{
	'ß': "ss", 'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE", 'ø': "o", 'Ø': "O",
	'đ': "d", 'Đ': "D", 'ð': "d", 'Ð': "D", 'þ': "th", 'Þ': "TH", 'ł': "l", 'Ł': "L",
	'ı': "i",
}

// symbolWords spells out symbols that carry meaning in column names.
var symbolWords = map[rune]string{'%': " pct ", '#': " num ", '&': " and "}

// SnakeCase turns header text into a lowercase snake_case SQL identifier:
// accents are transliterated, "%", "#", and "&" become words, camelCase
// boundaries are split, and every other run of characters outside [a-z0-9]
// becomes one "_". Names that start with a digit get a "c_" prefix. The
// result may be "" when s has no usable characters.
func SnakeCase(s string) string {
	var ascii []rune
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case foldLetters[r] != "":
			ascii = append(ascii, []rune(foldLetters[r])...)
		case symbolWords[r] != "":
			ascii = append(ascii, []rune(symbolWords[r])...)
		default:
			ascii = append(ascii, r)
		}
	}

	var b strings.Builder
	pendingSep := false
	for i, r := range ascii {
		lower := unicode.ToLower(r)
		if !(lower >= 'a' && lower <= 'z' || r >= '0' && r <= '9') {
			pendingSep = true
			continue
		}
		if b.Len() > 0 && (pendingSep || camelBoundary(ascii, i)) {
			b.WriteByte('_')
		}
		pendingSep = false
		b.WriteRune(lower)
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		out = "c_" + out
	}
	return out
}

// camelBoundary reports whether a word starts at rs[i]: "orderDate" splits
// before "D", "HTTPServer" before "S", and "Q1Revenue" before "R". A lone
// plural "s" never starts a word ("IDs" stays "ids").
func camelBoundary(rs []rune, i int) bool {
	if i == 0 || !isASCIIUpper(rs[i]) {
		return false
	}
	prev := rs[i-1]
	if isASCIILower(prev) {
		return true
	}
	if !(isASCIIUpper(prev) || prev >= '0' && prev <= '9') || i+1 >= len(rs) || !isASCIILower(rs[i+1]) {
		return false
	}
	// Upper (or digit) followed by Upper+lower: split unless the lowercase
	// run is just a plural "s".
	return !(rs[i+1] == 's' && (i+2 >= len(rs) || !unicode.IsLetter(rs[i+2])) && isASCIIUpper(prev))
}

func isASCIIUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isASCIILower(r rune) bool { return r >= 'a' && r <= 'z' }

// Slug turns a sheet or file name into a table name: SnakeCase, or "sheet"
// when nothing usable is left.
func Slug(s string) string {
	if out := SnakeCase(s); out != "" {
		return out
	}
	return "sheet"
}
