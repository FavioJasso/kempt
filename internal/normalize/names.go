package normalize

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
// accents and compatibility forms ("ﬁ", "²") are transliterated, "%", "#",
// and "&" become words, camelCase boundaries are split, and every other run
// of characters outside [a-z0-9] becomes one "_". Names that start with a
// digit get a "c_" prefix. The result may be "" when s has no usable
// characters.
func SnakeCase(s string) string {
	var ascii []rune
	for _, r := range norm.NFKD.String(s) {
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

// ColumnBase names a column from its header parts (one per header row):
// each part loses its unit ("Sales ($000s)" → sales) and is snake_cased, the
// parts join with "_", and a blank header becomes col_<letter>. It also
// returns the unit and the part it came from.
func ColumnBase(parts []string, letter string) (name, unit, unitPart string) {
	var names []string
	for _, part := range parts {
		rest, u := ExtractUnit(part)
		if u != "" {
			unit, unitPart = u, part
		}
		if sn := SnakeCase(rest); sn != "" {
			names = append(names, sn)
		}
	}
	name = strings.Join(names, "_")
	if name == "" {
		name = "col_" + strings.ToLower(letter)
	}
	return name, unit, unitPart
}

// pgSystemColumns are names Postgres reserves in every table.
var pgSystemColumns = map[string]bool{
	"tableoid": true, "xmin": true, "cmin": true, "xmax": true, "cmax": true, "ctid": true,
}

// SystemColumn reports whether name is a Postgres system column name, which
// a table cannot use for its own column.
func SystemColumn(name string) bool { return pgSystemColumns[name] }

// MaxIdent is the longest identifier kempt emits: Postgres truncates
// identifiers at 63 bytes.
const MaxIdent = 63

// Truncate cuts s to at most n bytes without splitting a UTF-8 sequence,
// then drops trailing underscores.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], "_")
}

// Unique truncates name to max bytes and appends _2, _3, ... until it is
// not in seen, then records it.
func Unique(name string, seen map[string]bool, max int) string {
	name = Truncate(name, max)
	out := name
	for i := 2; seen[out]; i++ {
		suffix := "_" + strconv.Itoa(i)
		out = Truncate(name, max-len(suffix)) + suffix
	}
	seen[out] = true
	return out
}
