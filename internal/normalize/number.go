package normalize

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Number is a parsed numeric value plus the decorations that were stripped
// to get it.
type Number struct {
	Text          Decimal // canonical: optional "-", digits, optional "." + digits
	Float         float64
	IntDigits     int    // digits before the point ("0.25" → 1, "1234.5" → 4)
	Scale         int    // digits after the point as written ("1.50" → 2, "12" → 0)
	Currency      string // "$", "€", "£", or "¥" when one was stripped
	Percent       bool   // had a trailing %
	Accounting    bool   // parenthesized negative
	TrailingMinus bool   // "500-"
	Grouped       bool   // had thousands separators
}

// IsInt reports whether the number has no fractional digits.
func (n Number) IsInt() bool { return n.Scale == 0 }

// Int64 returns the value as an int64 when it is an integer that fits.
func (n Number) Int64() (int64, bool) {
	if !n.IsInt() {
		return 0, false
	}
	v, err := strconv.ParseInt(string(n.Text), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// maxExponent bounds exponent notation so a hostile "1E999999" cannot
// expand into a huge digit string.
const maxExponent = 400

func isCurrency(r rune) bool { return r == '$' || r == '€' || r == '£' || r == '¥' }

func isMinus(r rune) bool { return r == '-' || r == '\u2212' }

// ParseNumber parses spreadsheet-style numeric text: signs (including
// U+2212), one currency symbol, accounting parentheses, a trailing minus, a
// trailing percent, comma thousands separators in 3-digit groups, and
// exponent notation. Integers written with leading zeros ("02134") are
// rejected so that ZIP codes and IDs stay text.
func ParseNumber(s string) (Number, bool) {
	var (
		n                     Number
		sign, neg             bool
		openParen, closeParen bool
	)
	rest := trim(s)

prefix:
	for rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		switch {
		case r == '+' || isMinus(r):
			if sign {
				return Number{}, false
			}
			sign, neg = true, r != '+'
			rest = rest[size:]
		case isCurrency(r):
			if n.Currency != "" {
				return Number{}, false
			}
			n.Currency = string(r)
			rest = strings.TrimLeftFunc(rest[size:], isSpace)
		case r == '(':
			if openParen {
				return Number{}, false
			}
			openParen = true
			rest = strings.TrimLeftFunc(rest[size:], isSpace)
		default:
			break prefix
		}
	}

suffix:
	for rest != "" {
		r, size := utf8.DecodeLastRuneInString(rest)
		head := rest[:len(rest)-size]
		switch {
		case r == '%':
			if n.Percent {
				return Number{}, false
			}
			n.Percent = true
			rest = strings.TrimRightFunc(head, isSpace)
		case isCurrency(r):
			if n.Currency != "" {
				return Number{}, false
			}
			n.Currency = string(r)
			rest = strings.TrimRightFunc(head, isSpace)
		case r == ')':
			if closeParen {
				return Number{}, false
			}
			closeParen = true
			rest = strings.TrimRightFunc(head, isSpace)
		case isMinus(r):
			if n.TrailingMinus {
				return Number{}, false
			}
			n.TrailingMinus = true
			rest = head
		default:
			break suffix
		}
	}

	if openParen != closeParen || (n.Currency != "" && n.Percent) {
		return Number{}, false
	}
	if b2i(sign)+b2i(openParen)+b2i(n.TrailingMinus) > 1 {
		return Number{}, false
	}
	n.Accounting = openParen
	neg = neg || openParen || n.TrailingMinus

	mant, exp := rest, 0
	hasExp := false
	if i := strings.IndexAny(rest, "eE"); i >= 0 {
		e := rest[i+1:]
		if len(e) == 0 || len(e) > 4 || !isDigit(e[len(e)-1]) {
			return Number{}, false
		}
		v, err := strconv.Atoi(e)
		if err != nil || v > maxExponent || v < -maxExponent {
			return Number{}, false
		}
		mant, exp, hasExp = rest[:i], v, true
	}

	intPart, frac, _ := strings.Cut(mant, ".")
	if intPart == "" && frac == "" {
		return Number{}, false
	}
	if strings.Contains(intPart, ",") {
		if hasExp {
			return Number{}, false
		}
		groups := strings.Split(intPart, ",")
		for i, g := range groups {
			if (i == 0 && (len(g) < 1 || len(g) > 3)) || (i > 0 && len(g) != 3) {
				return Number{}, false
			}
		}
		intPart = strings.Join(groups, "")
		n.Grouped = true
	}
	if !allDigits(intPart) || !allDigits(frac) {
		return Number{}, false
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		return Number{}, false
	}

	fixed(&n, neg, intPart+frac, len(intPart)+exp, hasExp)
	return n, true
}

// NumberFromFloat renders an xlsx numeric value the way Excel displays it:
// 15 significant digits, no exponent, trailing zeros trimmed. NaN and
// infinities have no decimal form; they yield Text "0" with Float set to f.
func NumberFromFloat(f float64) Number {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Number{Text: "0", Float: f, IntDigits: 1}
	}
	s := strconv.FormatFloat(f, 'e', 14, 64) // d.dddddddddddddde±XX
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	mant, e, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(e)
	var n Number
	fixed(&n, neg, strings.Replace(mant, ".", "", 1), 1+exp, true)
	return n
}

// fixed fills n's Text, Float, IntDigits, and Scale from a digit string
// with the decimal point after point digits (point may be out of range).
func fixed(n *Number, neg bool, digits string, point int, trimFrac bool) {
	if point < 0 {
		digits = strings.Repeat("0", -point) + digits
		point = 0
	}
	if point > len(digits) {
		digits += strings.Repeat("0", point-len(digits))
	}
	ip, fp := strings.TrimLeft(digits[:point], "0"), digits[point:]
	if ip == "" {
		ip = "0"
	}
	if trimFrac {
		fp = strings.TrimRight(fp, "0")
	}
	if strings.Trim(ip+fp, "0") == "" {
		neg = false
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(ip)
	if fp != "" {
		b.WriteByte('.')
		b.WriteString(fp)
	}
	n.Text = Decimal(b.String())
	n.Float, _ = strconv.ParseFloat(b.String(), 64)
	n.IntDigits, n.Scale = len(ip), len(fp)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
