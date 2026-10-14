package infer

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/normalize"
)

// Options tune type inference.
type Options struct {
	Lenient   float64 // fraction of values that must parse for a non-text type; 0 or >= 1 means all of them
	DateOrder string  // "" detects day/month order (month first when ambiguous); "mdy" or "dmy" forces it
	Date1904  bool
	// DecimalComma parses text numbers with "," as the decimal point and "."
	// as the thousands separator (set by the caller from sheet-wide evidence).
	DecimalComma bool // the workbook uses the 1904 date system
}

// Input is one source column to type.
type Input struct {
	Sheet  string
	Name   string       // final column name, used in reasons
	Header string       // original header text; drives the date hint
	Col    int          // 0-based source column
	Rows   []int        // 0-based sheet row of each cell
	Cells  []*grid.Cell // effective cell per row (after fill-down); nil or empty means NULL
}

// Result is a column's inferred type, its cast values, and the decisions
// that explain them.
type Result struct {
	Type      Type
	Values    []any  // parallel to Input.Cells: nil, bool, int64, normalize.Decimal, normalize.Date, normalize.Timestamp, or string
	Unit      string // "%" for percent columns, or the one currency symbol at least half the values carried
	Pct       bool   // every value carried "%" and the type is numeric; the caller suffixes the name with _pct
	Nulls     []bool // parallel to Input.Cells: a non-empty source cell became NULL
	Decisions []grid.Decision
}

// maxExamples caps the example cells quoted in a reason.
const maxExamples = 5

type source uint8

const (
	srcText   source = iota // string, bool, formula text, or ISO date cell: parsed from its text
	srcNumber               // numeric cell without a date number format
	srcSerial               // numeric cell with a date number format
)

// entry is one non-null cell and every base it parses as.
type entry struct {
	i       int // index into Input.Cells
	cell    *grid.Cell
	text    string // trimmed Raw
	src     source
	ok      [Text]bool
	b       bool
	num     normalize.Number
	t       time.Time
	date    normalize.ParsedDate // text dates only
	hint    bool                 // numeric cell read as a serial date because of the header
	textInt *float64             // text that is a plain integer (CSV), a serial candidate under the header hint
	clock   bool                 // numeric cell with a time-only format: stored as HH:MM:SS text

	tried  [2]bool // ParseDate ran for each normalize.DateOrder
	parsed [2]normalize.ParsedDate
	valid  [2]bool
}

// parseDate parses e as a text date in order, caching the result.
func (e *entry) parseDate(order normalize.DateOrder) (normalize.ParsedDate, bool) {
	if !e.tried[order] {
		e.tried[order] = true
		if strings.ContainsAny(e.text, "0123456789") {
			e.parsed[order], e.valid[order] = normalize.ParseDate(e.text, order)
		}
	}
	return e.parsed[order], e.valid[order]
}

// dateOrder records how numeric text dates such as "03/04/2026" are read.
type dateOrder struct {
	order    normalize.DateOrder
	forced   bool
	numeric  []int // numeric text dates
	mdy, dmy []int // values that only work month-first / day-first
	varied   bool  // some value has different day and month components
}

type column struct {
	in     Input
	opts   Options
	res    Result
	es     []*entry
	tokens []int // null placeholders
	errs   []int // Excel error values
	blanks []int // non-empty cells holding only invisible characters
	hint   bool  // numeric cells are serial dates because of the header
	ord    dateOrder
}

// Column infers the narrowest SQL type that fits a column's values, casts
// them, and logs every transformation as a decision.
func Column(in Input, opts Options) Result {
	c := &column{in: in, opts: opts}
	c.res.Values = make([]any, len(in.Cells))
	c.res.Nulls = make([]bool, len(in.Cells))
	c.classify()
	c.resolveOrder()
	for _, e := range c.es {
		c.parse(e)
	}
	c.choose()
	grid.SortDecisions(c.res.Decisions)
	return c.res
}

// classify splits cells into NULLs and entries to parse.
func (c *column) classify() {
	numbers, plausible := 0, true
	for i, cell := range c.in.Cells {
		if cell == nil || cell.IsEmpty() {
			continue
		}
		e := &entry{i: i, cell: cell, text: trim(cell.Raw)}
		if cell.Num != nil && !math.IsNaN(*cell.Num) && !math.IsInf(*cell.Num, 0) {
			e.src = srcSerial
			if !cell.DateFmt {
				e.src = srcNumber
				numbers++
				plausible = plausible && normalize.PlausibleSerial(*cell.Num)
			}
			if e.text == "" {
				e.text = string(normalize.NumberFromFloat(*cell.Num).Text)
			}
			c.es = append(c.es, e)
			continue
		}
		switch nk := normalize.ClassifyNull(cell.Raw); {
		case cell.Kind == grid.Error || nk == normalize.NullExcelError:
			c.errs = append(c.errs, i)
			c.res.Nulls[i] = true
		case nk == normalize.NullToken:
			c.tokens = append(c.tokens, i)
			c.res.Nulls[i] = true
		case nk == normalize.NotNull:
			if v, ok := plainInt(e.text); ok && !cell.DateFmt {
				e.textInt = &v
				numbers++
				plausible = plausible && normalize.PlausibleSerial(v)
			}
			c.es = append(c.es, e)
		default: // zero-width spaces or byte-order marks only
			c.blanks = append(c.blanks, i)
			c.res.Nulls[i] = true
		}
	}
	c.hint = numbers > 0 && plausible && normalize.DateHint(c.in.Header)
}

// resolveOrder picks month/day or day/month for numeric text dates.
func (c *column) resolveOrder() {
	o := &c.ord
	switch strings.ToLower(c.opts.DateOrder) {
	case "mdy":
		o.order, o.forced = normalize.MDY, true
	case "dmy":
		o.order, o.forced = normalize.DMY, true
	}
	for _, e := range c.es {
		if e.src != srcText {
			continue
		}
		pd, ok := e.parseDate(normalize.MDY)
		if !ok && e.text[0] >= '0' && e.text[0] <= '9' { // numeric dates start with a digit
			pd, ok = e.parseDate(normalize.DMY)
		}
		if !ok || !pd.Numeric {
			continue
		}
		o.numeric = append(o.numeric, e.i)
		switch {
		case pd.P1 > 12:
			o.dmy = append(o.dmy, e.i)
		case pd.P2 > 12:
			o.mdy = append(o.mdy, e.i)
		}
		o.varied = o.varied || pd.P1 != pd.P2
	}
	if !o.forced && len(o.dmy) > len(o.mdy) {
		o.order = normalize.DMY
	}
}

// parse records every base e parses as.
func (c *column) parse(e *entry) {
	switch e.src {
	case srcSerial:
		c.serial(e)
	case srcNumber:
		if e.cell.TimeFmt != "" {
			e.clock = true // a time of day or a duration is not a quantity to type as decimal
			return
		}
		e.num = normalize.NumberFromFloat(*e.cell.Num)
		if n, ok := normalize.ParseNumber(e.cell.Raw); ok && n.IsInt() && !strings.ContainsAny(e.cell.Raw, "eE") {
			e.num = n // integers keep every digit the file stores
		}
		if e.cell.PctFmt {
			// A percent number format shows 0.12 as 12%; store what the sheet shows.
			e.num = e.num.Times100()
			e.num.Percent = true
		}
		if c.hint {
			e.hint = true
			c.serial(e)
		} else {
			e.numeric()
		}
	default:
		if e.cell.Kind == grid.Bool {
			e.b, e.ok[Bool] = boolCell(e.text)
			return
		}
		if c.hint && e.textInt != nil {
			// Integer text under a date header, as CSV exports write Excel serials.
			e.hint = true
			c.serialAt(e, *e.textInt)
			return
		}
		if e.cell.Mask != "" {
			return // zero-padded by its number format: an identifier, kept as text
		}
		e.b, e.ok[Bool] = normalize.ParseBool(e.text)
		if !e.cell.DateFmt && !bareExponent(e.text) {
			parse := normalize.ParseNumber
			if c.opts.DecimalComma {
				parse = normalize.ParseNumberDecimalComma
			}
			if n, ok := parse(e.text); ok {
				e.num = n
				e.numeric()
			}
		}
		pd, ok := e.parseDate(normalize.MDY)
		if c.ord.order == normalize.DMY && (!ok || pd.Numeric) {
			pd, ok = e.parseDate(normalize.DMY)
		}
		if ok {
			e.date, e.t = pd, pd.T
			e.ok[Date], e.ok[Timestamp] = !pd.HasTime, true
		}
	}
}

func (c *column) serial(e *entry) { c.serialAt(e, *e.cell.Num) }

func (c *column) serialAt(e *entry, v float64) {
	if t, hasTime, ok := normalize.ExcelSerial(v, c.opts.Date1904); ok {
		e.t = t
		e.ok[Date], e.ok[Timestamp] = !hasTime, true
	}
}

func (e *entry) numeric() {
	if v, ok := e.num.Int64(); ok {
		e.ok[BigInt] = true
		e.ok[Int] = v >= math.MinInt32 && v <= math.MaxInt32
	}
	e.ok[Decimal] = e.num.IntDigits+e.num.Scale <= MaxPrecision
}

// plainInt parses text written as a bare integer ("46100"): no sign
// variations beyond "-", no separators, currency, or percent.
func plainInt(s string) (float64, bool) {
	n, ok := normalize.ParseNumber(s)
	if !ok || !n.IsInt() || n.Grouped || n.Currency != "" || n.Percent || n.Accounting || n.TrailingMinus {
		return 0, false
	}
	return n.Float, true
}

// bareExponent reports whether text uses exponent notation with neither a
// decimal point nor an exponent sign ("1E5", "12e3"). Spreadsheets and
// programs always write one of the two ("1E+05", "1.5e3"), so such text
// reads as a code and stays text instead of silently becoming 100000.
func bareExponent(s string) bool {
	i := strings.IndexAny(s, "eE")
	if i < 0 || strings.Contains(s[:i], ".") {
		return false
	}
	exp := s[i+1:]
	return exp == "" || (exp[0] != '+' && exp[0] != '-')
}

func boolCell(s string) (v, ok bool) {
	if v, ok = normalize.ParseBool(s); ok {
		return v, ok
	}
	return s == "1", s == "1" || s == "0"
}

// choose picks the column type, casts the values, and logs decisions.
func (c *column) choose() {
	total := len(c.es)
	var cnt [Text]int
	for _, e := range c.es {
		for b := range Text {
			if e.ok[b] {
				cnt[b]++
			}
		}
	}
	if cnt[Bool] > 0 && !c.realBools() {
		cnt[Bool] = 0
	}
	best, bestBase := 0, Text
	for b := range Text {
		if cnt[b] > best {
			best, bestBase = cnt[b], b
		}
	}

	base, lenient, widened := Text, false, false
	switch {
	case total == 0:
	case best == total:
		base = bestBase
	case c.opts.Lenient > 0 && c.opts.Lenient < 1 && best > 0 && float64(best)/float64(total) >= c.opts.Lenient:
		base, lenient = bestBase, true
	default:
		widened = best > 0 && total >= 5 && 5*best >= 4*total
	}

	var rounded []int
	maxInt, maxScale := 0, 0
	if base == Decimal {
		if rounded, maxInt, maxScale = c.capRounds(); len(rounded) > 0 {
			base, lenient = Text, false
		}
	}

	var cast, failed []*entry
	for _, e := range c.es {
		if base != Text && !e.ok[base] {
			failed = append(failed, e)
			c.res.Nulls[e.i] = true
			continue
		}
		c.res.Values[e.i] = value(e, base)
		cast = append(cast, e)
	}
	c.res.Type = typeOf(base, cast, c.res.Values)

	if len(c.tokens) > 0 {
		var codes, others []int
		for _, i := range c.tokens {
			if trim(c.in.Cells[i].Raw) == "NA" && c.codeColumn() {
				codes = append(codes, i)
			} else {
				others = append(others, i)
			}
		}
		if n := len(others); n > 0 {
			c.add(grid.KNullToken, others, 0.95, fmt.Sprintf("Read %s in column %s as NULL because %s: %s.",
				count(n, "value"), c.name(), plural(n, "it is a missing-value placeholder", "they are missing-value placeholders"), c.examples(others, false)))
		}
		if n := len(codes); n > 0 {
			c.add(grid.KNullToken, codes, 0.5, fmt.Sprintf("Read %s in column %s as NULL because NA is a missing-value placeholder, but the column holds short codes, so NA may be a real code (North America, Namibia): %s.",
				count(n, "value"), c.name(), c.examples(codes, false)))
		}
	}
	if len(c.errs) > 0 {
		n := len(c.errs)
		c.add(grid.KExcelError, c.errs, 0.9, fmt.Sprintf("Read %s in column %s as NULL because %s: %s.",
			count(n, "value"), c.name(), plural(n, "it is an Excel error value", "they are Excel error values"), c.examples(c.errs, false)))
	}
	if len(c.blanks) > 0 {
		n := len(c.blanks)
		c.add(grid.KTrimWhitespace, c.blanks, 1, fmt.Sprintf("Read %s in column %s as NULL because %s only invisible characters (zero-width spaces or byte-order marks): %s.",
			count(n, "value"), c.name(), plural(n, "it holds", "they hold"), c.examples(c.blanks, false)))
	}
	if lenient {
		idx := indices(failed)
		c.add(grid.KCastFailNull, idx, float64(best)/float64(total), fmt.Sprintf(
			"Cast column %s to %s because %d of %d values (%s) parse, meeting --lenient %s; %s that did not parse became NULL: %s.",
			c.name(), base, best, total, percent(best, total), strconv.FormatFloat(c.opts.Lenient, 'f', -1, 64),
			plural(len(idx), "the 1 value", fmt.Sprintf("the %d values", len(idx))), c.examples(idx, false)))
	}
	if len(rounded) > 0 {
		c.add(grid.KTypeWidened, rounded, 0.6, fmt.Sprintf(
			"Kept column %s as TEXT because its values need up to %d integer digits and up to %d decimal places, more than the %d digits a NUMERIC column holds; NUMERIC(%d,%d) would round %s.",
			c.name(), maxInt, maxScale, MaxPrecision, MaxPrecision, MaxPrecision-maxInt, c.examples(rounded, false)))
	}
	if widened {
		var idx []int
		for _, e := range c.es {
			if !e.ok[bestBase] {
				idx = append(idx, e.i)
			}
		}
		c.add(grid.KTypeWidened, idx, 0.6, fmt.Sprintf(
			"In column %s, %d of %d values (%s) parse as %s; kept TEXT because of %s. Use --lenient %s to cast.",
			c.name(), best, total, percent(best, total), bestBase, c.examples(idx, false),
			strconv.FormatFloat(float64(100*best/total)/100, 'f', -1, 64)))
	}

	switch base {
	case Bool:
		c.boolDecision(cast)
	case Int, BigInt, Decimal:
		c.numberDecisions(cast)
		c.exponentDecision(cast)
	case Date, Timestamp:
		c.dateDecisions(cast, base)
		c.orderDecision()
	case Text:
		if widened && len(c.ord.mdy) > 0 && len(c.ord.dmy) > 0 && (bestBase == Date || bestBase == Timestamp) {
			c.orderDecision()
		}
		c.trimDecision(cast)
		c.textDecisions(cast)
	}
}

// realBools reports whether the column's boolean candidates are trustworthy:
// single letters (Y, N, T, F) count only when both a true and a false value
// appear, so a column of "F" (female) or "N" (north) stays text.
func (c *column) realBools() bool {
	var t, f, words bool
	for _, e := range c.es {
		if !e.ok[Bool] {
			continue
		}
		if e.cell.Kind == grid.Bool || len(e.text) > 1 {
			words = true
		}
		if e.b {
			t = true
		} else {
			f = true
		}
	}
	return words || (t && f)
}

// codeColumn reports whether the column's other values are short upper-case
// codes (EU, APAC), where "NA" may be a code rather than a placeholder.
func (c *column) codeColumn() bool {
	n := 0
	for _, e := range c.es {
		if len(e.text) < 2 || len(e.text) > 5 || strings.ToUpper(e.text) != e.text || strings.IndexFunc(e.text, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return false
		}
		n++
	}
	return n > 0
}

// boolDecision logs booleans read from text tokens.
func (c *column) boolDecision(cast []*entry) {
	var idx []int
	seen := map[string]bool{}
	var pairs []string
	for _, e := range cast {
		if l := strings.ToLower(e.text); e.cell.Kind == grid.Bool || l == "true" || l == "false" {
			continue // already a boolean; nothing to explain
		}
		idx = append(idx, e.i)
		if key := strings.ToLower(e.text); !seen[key] {
			seen[key] = true
			pairs = append(pairs, fmt.Sprintf("%s → %v", e.text, e.b))
		}
	}
	if len(idx) == 0 {
		return
	}
	sort.Strings(pairs)
	c.add(grid.KBoolText, idx, 0.85, fmt.Sprintf("Read %s in column %s as booleans (%s): %s.",
		count(len(idx), "text value"), c.name(), strings.Join(pairs, ", "), c.examples(idx, false)))
}

// exponentDecision flags long integers expanded from exponent notation
// (4.11111E+15 → 4111110000000000): a spreadsheet that saved an ID or card
// number that way already lost its trailing digits, and the zeros kempt
// fills in are not the original value. Ordinary scientific values pass.
func (c *column) exponentDecision(cast []*entry) {
	var padded []int
	for _, e := range cast {
		if e.src != srcText || !strings.ContainsAny(e.text, "eE") {
			continue
		}
		mant, _, _ := strings.Cut(strings.ToLower(e.text), "e")
		sig := len(strings.TrimLeft(strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, mant), "0"))
		if e.num.IntDigits >= 12 && e.num.IntDigits > sig {
			padded = append(padded, e.i)
		}
	}
	if len(padded) > 0 {
		c.add(grid.KExponent, padded, 0.4, fmt.Sprintf("Expanded %s in column %s from exponent notation; the digits after the mantissa were filled with zeros and may not be the original value (long IDs lose digits this way when a spreadsheet saves them): %s.",
			count(len(padded), "value"), c.name(), c.examples(padded, true)))
	}
}

// textDecisions logs values a TEXT column renders differently from the file:
// time-only cells, zero-padded numbers, and date-formatted serials.
func (c *column) textDecisions(cast []*entry) {
	var clock, padded, dates []int
	masks := map[string]bool{}
	var maskList []string
	for _, e := range cast {
		switch {
		case e.clock:
			clock = append(clock, e.i)
		case e.cell.Mask != "":
			padded = append(padded, e.i)
			if !masks[e.cell.Mask] {
				masks[e.cell.Mask] = true
				maskList = append(maskList, strconv.Quote(e.cell.Mask))
			}
		case e.src == srcSerial && (e.ok[Date] || e.ok[Timestamp]):
			dates = append(dates, e.i)
		}
	}
	if len(clock) > 0 {
		c.add(grid.KTimeText, clock, 0.9, fmt.Sprintf("Column %s uses a time-only number format, so %s stored as HH:MM:SS text rather than as the fraction of a day Excel keeps: %s.",
			c.name(), plural(len(clock), "its 1 value is", fmt.Sprintf("its %d values are", len(clock))), c.clockExamples(clock)))
	}
	if len(padded) > 0 {
		c.add(grid.KZeroPad, padded, 0.9, fmt.Sprintf("Column %s keeps %s as text with the leading zeros its number format %s shows: %s.",
			c.name(), count(len(padded), "value"), strings.Join(maskList, ", "), c.examples(padded, false)))
	}
	if len(dates) > 0 {
		c.add(grid.KExcelSerialDate, dates, 0.9, fmt.Sprintf("Rendered %s in column %s from Excel serial numbers as ISO dates because the cells use a date number format; the column stays TEXT because of its other values: %s.",
			count(len(dates), "value"), c.name(), c.examples(dates, true)))
	}
}

// clockExamples lists time cells with their serial and rendered text.
func (c *column) clockExamples(idx []int) string {
	parts := make([]string, 0, maxExamples)
	for _, i := range idx[:min(len(idx), maxExamples)] {
		cell := c.in.Cells[i]
		parts = append(parts, fmt.Sprintf("%s (%s → %s)", grid.CellRef(c.row(i), c.in.Col), cell.Raw, normalize.Format(c.res.Values[i])))
	}
	out := strings.Join(parts, ", ")
	if n := len(idx) - maxExamples; n > 0 {
		out += fmt.Sprintf(" and %d more", n)
	}
	return out
}

// clockText renders a time serial as HH:MM:SS. A time of day ignores whole
// days; an elapsed duration counts them into the hours (36:30:00).
func clockText(v float64, elapsed bool) string {
	neg := v < 0
	ms := int64(math.Round(math.Abs(v) * 86400000))
	if !elapsed {
		ms %= 86400000
	}
	h, m, sec, frac := ms/3600000, ms/60000%60, ms/1000%60, ms%1000
	out := fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	if frac != 0 {
		out += fmt.Sprintf(".%03d", frac)
	}
	if neg && elapsed {
		out = "-" + out
	}
	return out
}

// capRounds reports the decimal values that a precision-capped NUMERIC(38,s)
// column would round, with the widest integer and fractional parts seen.
// Digits beyond s that are all zeros are not a loss.
func (c *column) capRounds() (idx []int, maxInt, maxScale int) {
	for _, e := range c.es {
		if e.ok[Decimal] {
			maxInt, maxScale = max(maxInt, e.num.IntDigits), max(maxScale, e.num.Scale)
		}
	}
	if maxInt+maxScale <= MaxPrecision {
		return nil, maxInt, maxScale
	}
	scale := MaxPrecision - maxInt
	for _, e := range c.es {
		if !e.ok[Decimal] {
			continue
		}
		if _, frac, _ := strings.Cut(string(e.num.Text), "."); len(frac) > scale && strings.Trim(frac[scale:], "0") != "" {
			idx = append(idx, e.i)
		}
	}
	return idx, maxInt, maxScale
}

// value casts e to base.
func value(e *entry, base Base) any {
	switch base {
	case Bool:
		return e.b
	case Int, BigInt:
		v, _ := e.num.Int64()
		return v
	case Decimal:
		return e.num.Text
	case Date:
		return normalize.NewDate(e.t)
	case Timestamp:
		return normalize.NewTimestamp(e.t)
	}
	switch {
	case e.clock:
		return clockText(*e.cell.Num, e.cell.TimeFmt == "elapsed")
	case e.cell.Kind == grid.Bool && e.ok[Bool]:
		if e.b {
			return "TRUE"
		}
		return "FALSE"
	case (e.src == srcSerial || e.cell.DateFmt) && e.ok[Date]:
		return normalize.NewDate(e.t).String()
	case (e.src == srcSerial || e.cell.DateFmt) && e.ok[Timestamp]:
		return normalize.NewTimestamp(e.t).String()
	}
	return e.text
}

// typeOf sizes base from the cast values.
func typeOf(base Base, cast []*entry, values []any) Type {
	t := Type{Base: base}
	switch base {
	case Decimal:
		maxInt, maxScale := 0, 0
		for _, e := range cast {
			maxInt, maxScale = max(maxInt, e.num.IntDigits), max(maxScale, e.num.Scale)
		}
		t.Precision, t.Scale = max(maxInt+maxScale, 1), maxScale
		if t.Precision > MaxPrecision {
			t.Precision, t.Scale = MaxPrecision, MaxPrecision-maxInt
		}
	case Text:
		for _, e := range cast {
			t.MaxLen = max(t.MaxLen, utf8.RuneCountInString(values[e.i].(string)))
		}
	}
	return t
}

// numberDecisions sets Unit and Pct and logs stripped decorations.
func (c *column) numberDecisions(cast []*entry) {
	var cur, neg, pct []int
	var symbols []string
	for _, e := range cast {
		if s := e.num.Currency; s != "" {
			cur = append(cur, e.i)
			if !slices.Contains(symbols, s) {
				symbols = append(symbols, s)
			}
		}
		if e.num.Accounting || e.num.TrailingMinus {
			neg = append(neg, e.i)
		}
		if e.num.Percent {
			pct = append(pct, e.i)
		}
	}
	if len(cur) > 0 {
		conf, extra := 0.95, ""
		if len(symbols) > 1 {
			conf, extra = 0.5, " The column mixes currencies; amounts are not converted."
		} else if 2*len(cur) >= len(cast) {
			c.res.Unit = symbols[0]
		}
		c.add(grid.KStripCurrency, cur, conf, fmt.Sprintf("Removed the currency %s %s from %s in column %s: %s.%s",
			plural(len(symbols), "symbol", "symbols"), strings.Join(symbols, " and "), count(len(cur), "value"), c.name(), c.examples(cur, true), extra))
	}
	if len(neg) > 0 {
		c.add(grid.KAccountingNeg, neg, 0.95, fmt.Sprintf(
			"Read %s in column %s as negative because %s accounting style (parentheses or a trailing minus): %s.",
			count(len(neg), "value"), c.name(), plural(len(neg), "it is written in", "they are written in"), c.examples(neg, true)))
	}
	fromFmt := 0
	for _, i := range pct {
		if c.in.Cells[i].PctFmt {
			fromFmt++
		}
	}
	fmtNote := ""
	if fromFmt > 0 {
		fmtNote = fmt.Sprintf(" %s a percent number format, so the stored fraction is multiplied by 100 to match what the sheet shows (0.12 displays as 12%%).",
			plural(fromFmt, "1 value uses", fmt.Sprintf("%d values use", fromFmt)))
	}
	switch {
	case len(pct) == 0:
	case len(pct) == len(cast):
		c.res.Pct, c.res.Unit = true, "%"
		c.add(grid.KPercent, pct, 0.9, fmt.Sprintf(
			"Every value in column %s is a percentage; values are stored as shown (12%% is stored as 12, not divided by 100) and the column is suffixed _pct: %s.%s",
			c.name(), c.examples(pct, true), fmtNote))
	default:
		c.add(grid.KPercent, pct, 0.5, fmt.Sprintf(
			"Only %d of %d values in column %s are percentages; they are stored as shown (12%% is stored as 12, not divided by 100), and the column is not suffixed _pct because the values are mixed: %s.%s",
			len(pct), len(cast), c.name(), c.examples(pct, true), fmtNote))
	}
}

// dateDecisions logs serial conversions and parsed text dates.
func (c *column) dateDecisions(cast []*entry, base Base) {
	var byFmt, byHint, text []int
	var layouts []string
	seen := map[string]int{}
	changed := false
	for _, e := range cast {
		switch {
		case e.src == srcSerial:
			byFmt = append(byFmt, e.i)
		case e.hint:
			byHint = append(byHint, e.i)
		default:
			text = append(text, e.i)
			if _, ok := seen[e.date.Layout]; !ok {
				layouts = append(layouts, e.date.Layout)
			}
			seen[e.date.Layout]++
			changed = changed || normalize.Format(c.res.Values[e.i]) != e.text
		}
	}
	kind := "dates"
	if base == Timestamp {
		kind = "timestamps"
	}
	if len(byFmt) > 0 {
		c.add(grid.KExcelSerialDate, byFmt, 0.95, fmt.Sprintf(
			"Converted %s in column %s from Excel serial numbers to %s because the cells use a date number format: %s.",
			count(len(byFmt), "value"), c.name(), kind, c.examples(byFmt, true)))
	}
	if len(byHint) > 0 {
		c.add(grid.KExcelSerialDate, byHint, 0.8, fmt.Sprintf(
			"Converted %s in column %s from Excel serial numbers to %s because the header %q suggests dates and every number is a plausible serial (20000 to 80000): %s.",
			count(len(byHint), "value"), c.name(), kind, c.in.Header, c.examples(byHint, true)))
	}
	if changed {
		parts := make([]string, len(layouts))
		for k, l := range layouts {
			parts[k] = fmt.Sprintf("%s (%d)", l, seen[l])
		}
		window := ""
		for _, l := range layouts {
			if strings.Contains(strings.ReplaceAll(l, "YYYY", ""), "YY") {
				window = " Two-digit years follow Excel's rule: 00–29 are 2000–2029 and 30–99 are 1930–1999."
				break
			}
		}
		c.add(grid.KDateParse, text, 0.9, fmt.Sprintf("Parsed %s in column %s into %s values; layouts seen: %s. Examples: %s.%s",
			count(len(text), "text date"), c.name(), base, strings.Join(parts, "; "), c.examples(text, true), window))
	}
}

// orderDecision explains how numeric text dates were read.
func (c *column) orderDecision() {
	o := c.ord
	if len(o.numeric) == 0 {
		return
	}
	as := "month/day (MM/DD)"
	if o.order == normalize.DMY {
		as = "day/month (DD/MM)"
	}
	switch {
	case o.forced:
		flag := "mdy"
		if o.order == normalize.DMY {
			flag = "dmy"
		}
		c.add(grid.KDateOrder, o.numeric, 1, fmt.Sprintf("Read the numeric dates in column %s as %s because --date-order %s was given.",
			c.name(), as, flag))
	case len(o.mdy) > 0 && len(o.dmy) > 0:
		minority := o.dmy
		if o.order == normalize.DMY {
			minority = o.mdy
		}
		c.add(grid.KDateOrder, o.numeric, 0.5, fmt.Sprintf(
			"Column %s mixes month/day dates such as %s with day/month dates such as %s; read them as %s, the more common order, so %s in the other order %s: %s.",
			c.name(), c.examples(o.mdy[:1], false), c.examples(o.dmy[:1], false), as,
			count(len(minority), "value"), plural(len(minority), "does not parse as a date", "do not parse as dates"), c.examples(minority, false)))
	case len(o.mdy) > 0 || len(o.dmy) > 0:
		decider := o.mdy
		if len(o.dmy) > 0 {
			decider = o.dmy
		}
		c.add(grid.KDateOrder, o.numeric, 0.9, fmt.Sprintf("Read the numeric dates in column %s as %s because %s only makes sense that way.",
			c.name(), as, c.examples(decider[:1], false)))
	case o.varied:
		c.add(grid.KDateOrder, o.numeric, 0.5, fmt.Sprintf(
			"Every numeric date in column %s reads as either month/day or day/month, for example %s; read them as month/day (MM/DD). Use --date-order dmy if they are day/month.",
			c.name(), c.examples(o.numeric[:1], false)))
	}
}

// trimDecision logs text values that lost surrounding whitespace.
func (c *column) trimDecision(cast []*entry) {
	var idx []int
	for _, e := range cast {
		if e.src == srcText && e.cell.Raw != e.text {
			idx = append(idx, e.i)
		}
	}
	if len(idx) > 0 {
		c.add(grid.KTrimWhitespace, idx, 1, fmt.Sprintf("Trimmed leading or trailing whitespace from %s in column %s: %s.",
			count(len(idx), "value"), c.name(), c.examples(idx, false)))
	}
}

// add logs a decision covering the rows of the given cells in this column.
func (c *column) add(kind string, idx []int, conf float64, reason string) {
	rng := grid.Range{R1: math.MaxInt, C1: c.in.Col, R2: -1, C2: c.in.Col}
	for _, i := range idx {
		r := c.row(i)
		rng.R1, rng.R2 = min(rng.R1, r), max(rng.R2, r)
	}
	c.res.Decisions = append(c.res.Decisions, grid.NewDecision(kind, c.in.Sheet, rng, conf, reason))
}

func (c *column) row(i int) int {
	if i < len(c.in.Rows) {
		return c.in.Rows[i]
	}
	return i
}

func (c *column) name() string {
	if c.in.Name != "" {
		return c.in.Name
	}
	return grid.ColName(c.in.Col)
}

// examples lists up to maxExamples cells as `C7 ("N/A")`, adding the clean
// value (`C7 ("$5" → 5)`) when withValue is set.
func (c *column) examples(idx []int, withValue bool) string {
	parts := make([]string, 0, maxExamples)
	for _, i := range idx[:min(len(idx), maxExamples)] {
		s := fmt.Sprintf("%s (%q", grid.CellRef(c.row(i), c.in.Col), clip(c.in.Cells[i].Raw))
		if withValue {
			s += " → " + normalize.Format(c.res.Values[i])
		}
		parts = append(parts, s+")")
	}
	out := strings.Join(parts, ", ")
	if n := len(idx) - maxExamples; n > 0 {
		out += fmt.Sprintf(" and %d more", n)
	}
	return out
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= 40 {
		return s
	}
	return string([]rune(s)[:39]) + "…"
}

func indices(es []*entry) []int {
	idx := make([]int, len(es))
	for k, e := range es {
		idx[k] = e.i
	}
	return idx
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// percent renders part/total as a whole percentage that never rounds up to
// 100% for a partial share.
func percent(part, total int) string {
	p := int(math.Round(100 * float64(part) / float64(total)))
	if p == 100 && part < total {
		p = 99
	}
	return strconv.Itoa(p) + "%"
}

func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\u200b' || r == '\ufeff' })
}
