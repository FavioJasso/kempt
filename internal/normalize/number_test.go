package normalize

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestParseNumber(t *testing.T) {
	type flags struct {
		cur                       string
		pct, acct, trail, grouped bool
	}
	tests := []struct {
		in    string
		ok    bool
		text  string
		scale int
		f     flags
	}{
		// Plain forms.
		{"0", true, "0", 0, flags{}},
		{"12", true, "12", 0, flags{}},
		{"-5", true, "-5", 0, flags{}},
		{"+5", true, "5", 0, flags{}},
		{"\u22123", true, "-3", 0, flags{}},
		{"+1.0", true, "1.0", 1, flags{}},
		{"1.50", true, "1.50", 2, flags{}},
		{"0.5", true, "0.5", 1, flags{}},
		{"-0.25", true, "-0.25", 2, flags{}},
		{"0.000001", true, "0.000001", 6, flags{}},
		{".5", true, "0.5", 1, flags{}},
		{"-.5", true, "-0.5", 1, flags{}},
		{"5.", true, "5", 0, flags{}},
		{"-0", true, "0", 0, flags{}},
		{"-0.00", true, "0.00", 2, flags{}},
		{"\u00a0 42 \t", true, "42", 0, flags{}},
		{"123456789012345678901234567890", true, "123456789012345678901234567890", 0, flags{}},
		// Thousands separators.
		{"1,000", true, "1000", 0, flags{grouped: true}},
		{"1,234", true, "1234", 0, flags{grouped: true}},
		{"12,345.678", true, "12345.678", 3, flags{grouped: true}},
		{"1,234,567.5", true, "1234567.5", 1, flags{grouped: true}},
		// Accounting negatives and trailing minus.
		{"(1,234)", true, "-1234", 0, flags{acct: true, grouped: true}},
		{"( 1,234 )", true, "-1234", 0, flags{acct: true, grouped: true}},
		{"($1,234.00)", true, "-1234.00", 2, flags{cur: "$", acct: true, grouped: true}},
		{"$(1,234)", true, "-1234", 0, flags{cur: "$", acct: true, grouped: true}},
		{"500-", true, "-500", 0, flags{trail: true}},
		{"75.00-", true, "-75.00", 2, flags{trail: true}},
		{"$500-", true, "-500", 0, flags{cur: "$", trail: true}},
		// Currency.
		{"$1,200.50", true, "1200.50", 2, flags{cur: "$", grouped: true}},
		{"€5", true, "5", 0, flags{cur: "€"}},
		{"5 €", true, "5", 0, flags{cur: "€"}},
		{"5€", true, "5", 0, flags{cur: "€"}},
		{"£ 3.20", true, "3.20", 2, flags{cur: "£"}},
		{"¥1000", true, "1000", 0, flags{cur: "¥"}},
		{"-$5", true, "-5", 0, flags{cur: "$"}},
		{"$-5", true, "-5", 0, flags{cur: "$"}},
		{"$ -5", true, "-5", 0, flags{cur: "$"}},
		// Percent.
		{"12%", true, "12", 0, flags{pct: true}},
		{"12.5 %", true, "12.5", 1, flags{pct: true}},
		{"0%", true, "0", 0, flags{pct: true}},
		{"(5.2%)", true, "-5.2", 1, flags{pct: true, acct: true}},
		// Exponent notation (xlsx raw values).
		{"1.5E+03", true, "1500", 0, flags{}},
		{"1.5e-3", true, "0.0015", 4, flags{}},
		{"2E5", true, "200000", 0, flags{}},
		{"-1.25E1", true, "-12.5", 1, flags{}},
		{"1.2345678901234501E-2", true, "0.012345678901234501", 18, flags{}},
		{"0E5", true, "0", 0, flags{}},

		// Rejected.
		{"", false, "", 0, flags{}},
		{"   ", false, "", 0, flags{}},
		{"abc", false, "", 0, flags{}},
		{"12abc", false, "", 0, flags{}},
		{"1.2.3", false, "", 0, flags{}},
		{"1,23", false, "", 0, flags{}},
		{"12,34,567", false, "", 0, flags{}},
		{"1,2345", false, "", 0, flags{}},
		{",123", false, "", 0, flags{}},
		{"1,234,", false, "", 0, flags{}},
		{"1,234.5,6", false, "", 0, flags{}},
		{"007", false, "", 0, flags{}},
		{"02134", false, "", 0, flags{}},
		{"00", false, "", 0, flags{}},
		{"00.5", false, "", 0, flags{}},
		{"0,123", false, "", 0, flags{}},
		{"--5", false, "", 0, flags{}},
		{"+-5", false, "", 0, flags{}},
		{"-5-", false, "", 0, flags{}},
		{"(5", false, "", 0, flags{}},
		{"5)", false, "", 0, flags{}},
		{"(-5)", false, "", 0, flags{}},
		{"-(5)", false, "", 0, flags{}},
		{"(5)-", false, "", 0, flags{}},
		{"$$5", false, "", 0, flags{}},
		{"$5€", false, "", 0, flags{}},
		{"$12%", false, "", 0, flags{}},
		{"5%%", false, "", 0, flags{}},
		{"%", false, "", 0, flags{}},
		{"$", false, "", 0, flags{}},
		{"()", false, "", 0, flags{}},
		{"-", false, "", 0, flags{}},
		{"\u2212", false, "", 0, flags{}},
		{".", false, "", 0, flags{}},
		{"1 234", false, "", 0, flags{}},
		{"- 5", false, "", 0, flags{}},
		{"1,234E5", false, "", 0, flags{}},
		{"1E", false, "", 0, flags{}},
		{"1E+", false, "", 0, flags{}},
		{"E5", false, "", 0, flags{}},
		{"1e999", false, "", 0, flags{}},
		{"1e5e5", false, "", 0, flags{}},
		{"NaN", false, "", 0, flags{}},
		{"Inf", false, "", 0, flags{}},
		{"0x1F", false, "", 0, flags{}},
		{"1_000", false, "", 0, flags{}},
		{"N/A", false, "", 0, flags{}},
		{"#DIV/0!", false, "", 0, flags{}},
		{"3/15/2026", false, "", 0, flags{}},
		{"$-", false, "", 0, flags{}},
		{"$ -", false, "", 0, flags{}},
		{"\u20135", false, "", 0, flags{}},
		{"\u20145", false, "", 0, flags{}},
		{"(\u22125)", false, "", 0, flags{}},
		{"5\u2212", true, "-5", 0, flags{trail: true}},
		{"\u00a0$\u00a012\u00a0", true, "12", 0, flags{cur: "$"}},
		{"(1,23)", false, "", 0, flags{}},
		{"12345678901234567890123456789012345678901", true, "12345678901234567890123456789012345678901", 0, flags{}},
		{"USD 5", false, "", 0, flags{}},
	}
	for _, tc := range tests {
		got, ok := ParseNumber(tc.in)
		if ok != tc.ok {
			t.Errorf("ParseNumber(%q) ok = %v, want %v (got %+v)", tc.in, ok, tc.ok, got)
			continue
		}
		if !ok {
			if got != (Number{}) {
				t.Errorf("ParseNumber(%q) rejected but returned %+v", tc.in, got)
			}
			continue
		}
		gf := flags{got.Currency, got.Percent, got.Accounting, got.TrailingMinus, got.Grouped}
		if string(got.Text) != tc.text || got.Scale != tc.scale || gf != tc.f {
			t.Errorf("ParseNumber(%q) = {Text:%q Scale:%d %+v}, want {Text:%q Scale:%d %+v}",
				tc.in, got.Text, got.Scale, gf, tc.text, tc.scale, tc.f)
		}
		checkDerived(t, tc.in, got)
	}
}

// checkDerived verifies the fields that follow from Text.
func checkDerived(t *testing.T, in string, n Number) {
	t.Helper()
	ip, _, _ := strings.Cut(strings.TrimPrefix(string(n.Text), "-"), ".")
	if n.IntDigits != len(ip) {
		t.Errorf("%q: IntDigits = %d, want %d", in, n.IntDigits, len(ip))
	}
	want, _ := strconv.ParseFloat(string(n.Text), 64)
	if n.Float != want {
		t.Errorf("%q: Float = %v, want %v", in, n.Float, want)
	}
}

func TestParseNumberDigits(t *testing.T) {
	tests := []struct {
		in                string
		intDigits, scale  int
		wantInt64, fitInt bool
	}{
		{"0.25", 1, 2, false, false},
		{"1234.5", 4, 1, false, false},
		{".5", 1, 1, false, false},
		{"5.", 1, 0, true, true},
		{"-0", 1, 0, true, true},
		{"9223372036854775807", 19, 0, true, true},
		{"9223372036854775808", 19, 0, true, false},
		{"1E+400", 401, 0, true, false},
		{"1e-5", 1, 5, false, false},
		{"(1,234,567.890)", 7, 3, false, false},
	}
	for _, tc := range tests {
		n, ok := ParseNumber(tc.in)
		if !ok {
			t.Errorf("ParseNumber(%q) failed", tc.in)
			continue
		}
		_, fits := n.Int64()
		if n.IntDigits != tc.intDigits || n.Scale != tc.scale || n.IsInt() != tc.wantInt64 || fits != tc.fitInt {
			t.Errorf("ParseNumber(%q) = IntDigits %d, Scale %d, IsInt %v, Int64 ok %v; want %d, %d, %v, %v",
				tc.in, n.IntDigits, n.Scale, n.IsInt(), fits, tc.intDigits, tc.scale, tc.wantInt64, tc.fitInt)
		}
	}
}

func TestNumberFromFloat(t *testing.T) {
	tests := []struct {
		in    float64
		text  string
		scale int
	}{
		{0.1 + 0.2, "0.3", 1},
		{1e-7, "0.0000001", 7},
		{123456789012345678, "123456789012346000", 0},
		{math.Copysign(0, -1), "0", 0},
		{0, "0", 0},
		{1, "1", 0},
		{-1234.5, "-1234.5", 1},
		{46096, "46096", 0},
		{129.99, "129.99", 2},
		{1.0 / 3, "0.333333333333333", 15},
		{2.0 / 3, "0.666666666666667", 15},
		{-0.000123, "-0.000123", 6},
		{1e20, "100000000000000000000", 0},
		{999999999999999.9, "1000000000000000", 0},
		{2450, "2450", 0},
		{0.5, "0.5", 1},
	}
	for _, tc := range tests {
		got := NumberFromFloat(tc.in)
		if string(got.Text) != tc.text || got.Scale != tc.scale {
			t.Errorf("NumberFromFloat(%v) = {Text:%q Scale:%d}, want {Text:%q Scale:%d}", tc.in, got.Text, got.Scale, tc.text, tc.scale)
		}
		if got.Currency != "" || got.Percent || got.Accounting || got.TrailingMinus || got.Grouped {
			t.Errorf("NumberFromFloat(%v) set decoration flags: %+v", tc.in, got)
		}
		checkDerived(t, tc.text, got)
	}

	if n := NumberFromFloat(math.NaN()); n.Text != "0" || !math.IsNaN(n.Float) {
		t.Errorf("NumberFromFloat(NaN) = %+v", n)
	}
	if n := NumberFromFloat(math.Inf(-1)); n.Text != "0" || !math.IsInf(n.Float, -1) {
		t.Errorf("NumberFromFloat(-Inf) = %+v", n)
	}
}

func TestNumberInt64(t *testing.T) {
	tests := []struct {
		in    string
		isInt bool
		v     int64
		fits  bool
	}{
		{"12", true, 12, true},
		{"-1234", true, -1234, true},
		{"0", true, 0, true},
		{"1.0", false, 0, false},
		{"5.", true, 5, true},
		{"1.5E+03", true, 1500, true},
		{"9223372036854775807", true, math.MaxInt64, true},
		{"-9223372036854775808", true, math.MinInt64, true},
		{"9223372036854775808", true, 0, false},
		{"12%", true, 12, true},
		{"(1,234)", true, -1234, true},
		{"0.25", false, 0, false},
	}
	for _, tc := range tests {
		n, ok := ParseNumber(tc.in)
		if !ok {
			t.Fatalf("ParseNumber(%q) failed", tc.in)
		}
		v, fits := n.Int64()
		if n.IsInt() != tc.isInt || v != tc.v || fits != tc.fits {
			t.Errorf("%q: IsInt=%v Int64=(%d,%v), want IsInt=%v Int64=(%d,%v)", tc.in, n.IsInt(), v, fits, tc.isInt, tc.v, tc.fits)
		}
	}
}
