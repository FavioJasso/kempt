package read

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/FavioJasso/kempt/internal/grid"
)

// maxUnzip caps the uncompressed workbook size. The XML limit matches it so
// excelize keeps every part in memory instead of spilling to temp files.
const maxUnzip = 1 << 30

var errNotWorksheet = errors.New("read: not a worksheet")

// XLSX reads an .xlsx workbook: every visible worksheet in workbook order, or
// only the sheet named in opts.Sheet (which may be hidden). Chart sheets are
// skipped.
func XLSX(r io.Reader, opts Options) ([]*grid.Grid, error) {
	f, err := excelize.OpenReader(r, excelize.Options{UnzipSizeLimit: maxUnzip, UnzipXMLSizeLimit: maxUnzip})
	if err != nil {
		return nil, fmt.Errorf("read: open xlsx: %w", err)
	}
	defer f.Close()

	names, err := sheetsToRead(f, opts.Sheet)
	if err != nil {
		return nil, err
	}
	date1904 := false
	if p, err := f.GetWorkbookProps(); err == nil && p.Date1904 != nil {
		date1904 = *p.Date1904
	}
	parts, err := packageParts(f)
	if err != nil {
		return nil, err
	}
	x := &xlsxReader{f: f, parts: parts, maxRows: opts.MaxRows, maxCells: opts.MaxCells, calcBytes: opts.CalcBytes, styles: map[int]cellStyle{}}
	grids := make([]*grid.Grid, 0, len(names))
	for _, name := range names {
		g, err := x.sheet(name)
		if errors.Is(err, errNotWorksheet) && opts.Sheet == "" {
			continue
		}
		if err != nil {
			return nil, err
		}
		g.Date1904 = date1904
		grids = append(grids, g)
	}
	if len(grids) == 0 {
		return nil, noVisibleSheets(f)
	}
	return grids, nil
}

// sheetsToRead resolves opts.Sheet (exact name first, then case-insensitive,
// as Excel does) or lists the visible sheets.
func sheetsToRead(f *excelize.File, want string) ([]string, error) {
	all := f.GetSheetList()
	if want != "" {
		for _, n := range all {
			if n == want {
				return []string{n}, nil
			}
		}
		for _, n := range all {
			if strings.EqualFold(n, want) {
				return []string{n}, nil
			}
		}
		return nil, fmt.Errorf("%w: %q (workbook has: %s)", ErrSheetNotFound, want, strings.Join(all, ", "))
	}
	var visible []string
	for _, n := range all {
		if ok, err := f.GetSheetVisible(n); err == nil && ok {
			visible = append(visible, n)
		}
	}
	return visible, nil
}

func noVisibleSheets(f *excelize.File) error {
	var hidden []string
	for _, n := range f.GetSheetList() {
		if ok, _ := f.GetSheetVisible(n); !ok {
			hidden = append(hidden, n)
		}
	}
	if len(hidden) == 0 {
		return errors.New("read: workbook has no worksheets")
	}
	return fmt.Errorf("read: workbook has no visible worksheets; name a hidden one to read it (%s)", strings.Join(hidden, ", "))
}

type xlsxReader struct {
	f         *excelize.File
	parts     pkgParts
	sst       []string
	sstLoaded bool
	maxRows   int
	maxCells  int64
	calcBytes int64
	calc      calcState
	styles    map[int]cellStyle // style id → classified number format
}

// calcState tracks whether formulas without a cached value can be evaluated.
type calcState uint8

const (
	calcUnknown calcState = iota
	calcReady
	calcOff
)

// canCalc prepares excelize to evaluate formulas, once per workbook. Merges
// must go first: excelize reads a cell under a merge as the merge's anchor,
// so =SUM(B3:C3) over a merged B3:C3 would count B3 twice, where Excel sees
// an empty C3.
func (x *xlsxReader) canCalc() bool {
	if x.calc != calcUnknown {
		return x.calc == calcReady
	}
	x.calc = calcOff
	var total int64
	var merged []string
	for name, part := range x.parts.sheets {
		if !isWorksheet(part.kind) {
			continue
		}
		data, _ := pkgPart(x.f, part.path)
		total += int64(len(data))
		if bytes.Contains(data, []byte("mergeCell")) {
			merged = append(merged, name)
		}
	}
	if x.calcBytes > 0 && total > x.calcBytes {
		return false
	}
	for _, name := range merged {
		if err := x.f.UnmergeCell(name, "A1", "XFD1048576"); err != nil {
			return false
		}
	}
	x.calc = calcReady
	return true
}

// sharedStrings loads the shared string table once per workbook.
func (x *xlsxReader) sharedStrings() error {
	if x.sstLoaded {
		return nil
	}
	x.sstLoaded = true
	data, ok := pkgPart(x.f, x.parts.sst)
	if !ok {
		return nil // a workbook without text has no table
	}
	sst, err := readSST(newDecoder(x.f, data))
	x.sst = sst
	return err
}

func (x *xlsxReader) sheet(name string) (*grid.Grid, error) {
	part, ok := x.parts.sheets[name]
	if !ok {
		return nil, fmt.Errorf("read: sheet %q: its part is missing from the workbook", name)
	}
	if !isWorksheet(part.kind) {
		return nil, fmt.Errorf("%w: sheet %q is a chart or dialog sheet", errNotWorksheet, name)
	}
	data, ok := pkgPart(x.f, part.path)
	if !ok {
		return nil, fmt.Errorf("read: sheet %q: part %s is missing", name, part.path)
	}
	if err := x.sharedStrings(); err != nil {
		return nil, err
	}
	sd, err := x.scan(name, data)
	if err != nil {
		return nil, err
	}
	rows, merges := sd.rows, sd.merges

	// The grid ends at the last visible value or merge extent.
	maxR, maxC, stored := bounds(rows, merges)
	for _, m := range merges {
		maxR, maxC = max(maxR, m.R2), max(maxC, m.C2)
	}
	if err := checkDensity(name, maxR+1, maxC+1, stored); err != nil {
		return nil, err
	}
	if err := checkCells(name, maxR+1, maxC+1, x.maxCells); err != nil {
		return nil, err
	}
	calc := x.uncached(name, rows, merges)
	for k := range calc {
		maxR, maxC = max(maxR, k[0]), max(maxC, k[1])
		stored++
	}
	if x.maxRows > 0 && maxR+1 > x.maxRows {
		return nil, tooManyRows(name, x.maxRows)
	}
	if err := checkDensity(name, maxR+1, maxC+1, stored); err != nil {
		return nil, err
	}
	if err := checkCells(name, maxR+1, maxC+1, x.maxCells); err != nil {
		return nil, err
	}

	g := grid.New(name, maxR+1, maxC+1)
	g.Source = "xlsx"
	var active []grid.Range // merges overlapping the current row
	next := 0
	for r := 0; r <= maxR && r < len(rows); r++ {
		active = activeMerges(active, merges, &next, r)
		for _, rv := range rows[r].vals {
			if rv.c > maxC || coveredByMerge(active, r, rv.c) {
				continue // Excel only shows the anchor's value
			}
			var cell grid.Cell
			if res, ok := calc[[2]int{r, rv.c}]; ok {
				cell = x.calcCell(rv, res)
			} else {
				cell = x.cell(rv)
			}
			if cell.Kind != grid.Empty {
				g.Cells[r][rv.c] = cell
			}
		}
	}
	for _, m := range merges {
		g.AddMerge(m)
	}
	return g, nil
}

// bounds returns the last row and column holding a visible value (not blank
// and not hidden under one of merges) and the number of such values. Exactly
// these raw values become non-empty cells.
func bounds(rows []rawRow, merges []grid.Range) (maxR, maxC, values int) {
	maxR, maxC = -1, -1
	var active []grid.Range
	next := 0
	for r, row := range rows {
		active = activeMerges(active, merges, &next, r)
		for _, v := range row.vals {
			if strings.TrimSpace(v.v) != "" && !coveredByMerge(active, r, v.c) {
				maxR, maxC = max(maxR, r), max(maxC, v.c)
				values++
			}
		}
	}
	return maxR, maxC, values
}

func hasValue(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// activeMerges updates the set of merges that overlap row r. merges is sorted
// by R1 and *next indexes the first merge not yet activated.
func activeMerges(active, merges []grid.Range, next *int, r int) []grid.Range {
	kept := active[:0]
	for _, m := range active {
		if m.R2 >= r {
			kept = append(kept, m)
		}
	}
	for ; *next < len(merges) && merges[*next].R1 <= r; *next++ {
		if merges[*next].R2 >= r {
			kept = append(kept, merges[*next])
		}
	}
	return kept
}

func coveredByMerge(active []grid.Range, r, c int) bool {
	for _, m := range active {
		if m.Contains(r, c) && (m.R1 != r || m.C1 != c) {
			return true
		}
	}
	return false
}

// cell builds a grid cell from a stored cell's raw value, type, formula and
// number format.
func (x *xlsxReader) cell(rv rawVal) grid.Cell {
	raw := rv.v
	if raw == "" {
		if rv.formula == "" {
			return grid.Cell{}
		}
		return grid.Cell{Kind: grid.Formula, Formula: rv.formula} // no cached value
	}
	var cell grid.Cell
	switch rv.t {
	case "s", "inlineStr":
		cell = grid.Cell{Kind: grid.String, Raw: raw}
	case "b":
		cell = boolCell(raw)
	case "e":
		return grid.Cell{Kind: grid.Error, Raw: raw} // cached errors stay errors
	case "d":
		cell = grid.Cell{Kind: grid.Date, Raw: raw, DateFmt: true}
	case "str": // a string formula result
		cell = grid.Cell{Kind: grid.String, Raw: raw}
		if rv.formula != "" && plainNumber.MatchString(raw) {
			// Some writers (excelize included) store numeric results as
			// text. Only text a float reproduces exactly becomes a number:
			// "4111111111111111" (16 digits) and "-0" stay text.
			if n := x.number(raw, rv.style); n.Num != nil && formatNumber(*n.Num) == raw {
				cell = n
			}
		}
	default: // "n" or no type
		cell = x.number(raw, rv.style)
	}
	if rv.formula != "" {
		cell.Kind = grid.Formula
		cell.Formula = rv.formula
	}
	return cell
}

// maxCalc bounds how many formulas without a cached value kempt evaluates
// per sheet; the rest read as #CALC! errors (NULL, with a decision).
const maxCalc = 20_000

// calcResult is a formula that had no cached value in the file.
type calcResult struct {
	formula string
	value   string
	failed  bool
}

// uncached evaluates formulas that the file stores without a cached result
// (workbooks written by scripts are often never recalculated). Without this
// their cells would read as empty and whole rows could vanish. Evaluating
// makes excelize load whole sheets, so workbooks over calcBytes are not
// evaluated and their uncached formulas read as #CALC!.
func (x *xlsxReader) uncached(sheet string, rows []rawRow, merges []grid.Range) map[[2]int]calcResult {
	out := map[[2]int]calcResult{}
	var active []grid.Range
	next, n := 0, 0
	for r, row := range rows {
		active = activeMerges(active, merges, &next, r)
		for _, rv := range row.vals {
			if rv.v != "" || rv.formula == "" || coveredByMerge(active, r, rv.c) {
				continue
			}
			res := calcResult{formula: rv.formula, failed: true}
			if n < maxCalc && x.canCalc() {
				v, err := x.f.CalcCellValue(sheet, grid.CellRef(r, rv.c), excelize.Options{RawCellValue: true})
				if err == nil && strings.TrimSpace(v) == "" {
					n++
					continue // evaluates to an empty string, as in =IF(A1="","",...)
				}
				if err == nil {
					res.value, res.failed = v, false
				}
			}
			n++
			out[[2]int{r, rv.c}] = res
		}
	}
	return out
}

// calcCell builds the cell for an evaluated formula. A formula kempt cannot
// evaluate becomes the error value #CALC!, so it is visible and logged.
func (x *xlsxReader) calcCell(rv rawVal, res calcResult) grid.Cell {
	if res.failed {
		return grid.Cell{Kind: grid.Error, Raw: "#CALC!", Formula: res.formula}
	}
	cell := x.number(res.value, rv.style)
	if cell.Kind == grid.Date {
		cell.DateFmt = true
	}
	cell.Kind, cell.Formula = grid.Formula, res.formula
	return cell
}

func cellError(sheet, ref string, err error) error {
	return fmt.Errorf("read: %s!%s: %w", grid.QuoteSheet(sheet), ref, err)
}

// plainNumber matches a number written without exponent, leading zeros, or
// trailing fractional zeros, i.e. exactly as a float would be printed.
var plainNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)

// plainInteger matches an integer written without sign tricks or leading zeros.
var plainInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// zeroMask matches number formats that only pad with zeros, such as the
// ZIP Code ("00000"), ZIP+4 ("00000-0000") and fixed-width ID formats.
var zeroMask = regexp.MustCompile(`^0{2,}([-. ]0+)*$`)

// applyMask renders a non-negative integer through a zero-padding mask:
// ("00000", 2134) → "02134", ("00000-0000", 21341234) → "02134-1234".
func applyMask(mask string, v float64) (string, bool) {
	if v < 0 || v != float64(int64(v)) {
		return "", false
	}
	digits := strings.Count(mask, "0")
	s := strconv.FormatInt(int64(v), 10)
	if len(s) > digits {
		return "", false
	}
	s = strings.Repeat("0", digits-len(s)) + s
	var b strings.Builder
	i := 0
	for _, r := range mask {
		if r == '0' {
			b.WriteByte(s[i])
			i++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String(), true
}

func boolCell(raw string) grid.Cell {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true":
		return grid.Cell{Kind: grid.Bool, Raw: "TRUE"}
	case "0", "false":
		return grid.Cell{Kind: grid.Bool, Raw: "FALSE"}
	}
	return grid.Cell{Kind: grid.String, Raw: raw}
}

// number builds a numeric cell (Kind Date when its number format is a date or
// time format). Non-numeric raw text becomes a String cell.
func (x *xlsxReader) number(raw string, style int) grid.Cell {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return grid.Cell{Kind: grid.String, Raw: raw}
	}
	text := formatNumber(v)
	if t := strings.TrimSpace(raw); plainInteger.MatchString(t) {
		text = t // integers keep every digit (card numbers, long IDs); Excel shows only 15
	}
	cell := grid.Cell{Kind: grid.Number, Raw: text, Num: &v}
	st := x.style(style)
	switch st.kind {
	case fmtDate:
		cell.Kind, cell.DateFmt = grid.Date, true
	case fmtPct:
		cell.PctFmt = true
	case fmtClock:
		cell.TimeFmt = "clock"
	case fmtElapsed:
		cell.TimeFmt = "elapsed"
	case fmtZeroPad:
		if padded, ok := applyMask(st.code, v); ok {
			return grid.Cell{Kind: grid.String, Raw: padded, Mask: st.code}
		}
	}
	return cell
}

// numFmt is the kind of a number format.
type numFmt uint8

const (
	fmtPlain   numFmt = iota
	fmtDate           // shows a calendar date (possibly with a time)
	fmtPct            // shows a percentage
	fmtClock          // shows only a time of day (h:mm)
	fmtElapsed        // shows elapsed time ([h]:mm)
	fmtZeroPad        // pads integers with zeros (00000, 00000-0000)
)

// cellStyle is a classified number format.
type cellStyle struct {
	kind numFmt
	code string // the custom format code, if any
}

// style classifies a style id's number format. Results are cached per id.
func (x *xlsxReader) style(id int) cellStyle {
	if v, ok := x.styles[id]; ok {
		return v
	}
	var v cellStyle
	if st, err := x.f.GetStyle(id); err == nil && st != nil {
		if st.CustomNumFmt != nil {
			v = cellStyle{formatKind(*st.CustomNumFmt), *st.CustomNumFmt}
		} else {
			v = cellStyle{kind: builtinKind(st.NumFmt)}
		}
	}
	x.styles[id] = v
	return v
}

// builtinKind classifies a built-in number format id. Time-only ids
// (h:mm, mm:ss, [h]:mm:ss and their CJK and Thai variants) are not dates.
func builtinKind(id int) numFmt {
	switch {
	case id == 9 || id == 10:
		return fmtPct
	case 14 <= id && id <= 17, id == 22, 27 <= id && id <= 31, id == 36,
		50 <= id && id <= 54, 57 <= id && id <= 58, 71 <= id && id <= 74, id == 77, id == 81:
		return fmtDate
	case id == 46 || id == 79:
		return fmtElapsed
	case 18 <= id && id <= 21, 32 <= id && id <= 35, id == 45, id == 47, id == 55, id == 56, id == 75, id == 76, id == 78, id == 80:
		return fmtClock
	}
	return fmtPlain
}

// isBuiltinDateFormat reports whether a built-in number format id shows a date.
func isBuiltinDateFormat(id int) bool { return builtinKind(id) == fmtDate }

// isDateFormat reports whether a custom number format code shows a date.
func isDateFormat(code string) bool { return formatKind(code) == fmtDate }

// formatKind classifies a custom number format code. It reads the y/m/d/h/s
// tokens outside quoted literals, escapes, padding, and bracketed sections
// ([Red], [$-409], [>=100]). A format is a date when it has a year, a day,
// or a month ("m" not next to an hour or second, where it means minutes);
// time-only and elapsed formats (h:mm AM/PM, [h]:mm) are not. A format with
// an unquoted "%" and no date tokens is a percentage.
func formatKind(code string) numFmt {
	s := strings.ToLower(code)
	if first, _, _ := strings.Cut(s, ";"); zeroMask.MatchString(first) {
		return fmtZeroPad
	}
	var toks []byte
	pct, elapsed := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return fmtPlain
			}
			i += j + 1
		case '\\', '_', '*':
			i++ // escaped literal, padding width, or fill character
		case '[':
			j := strings.IndexByte(s[i+1:], ']')
			if j < 0 {
				return fmtPlain
			}
			if isElapsed(s[i+1 : i+1+j]) {
				toks = append(toks, 'h')
				elapsed = true
			}
			i += j + 1
		case '%':
			pct = true
		case 'a':
			switch {
			case strings.HasPrefix(s[i:], "am/pm"):
				i += 4
			case strings.HasPrefix(s[i:], "a/p"):
				i += 2
			}
		case 'y', 'm', 'd', 'h', 's':
			for i+1 < len(s) && s[i+1] == c {
				i++
			}
			toks = append(toks, c)
		case 'b':
			if i+1 < len(s) && s[i+1] == 'b' { // Buddhist-era year
				toks = append(toks, 'y')
				i++
			}
		}
	}
	for i, t := range toks {
		switch t {
		case 'y', 'd':
			return fmtDate
		case 'm':
			minutes := (i > 0 && toks[i-1] == 'h') || (i+1 < len(toks) && toks[i+1] == 's')
			if !minutes {
				return fmtDate
			}
		}
	}
	switch {
	case elapsed:
		return fmtElapsed
	case len(toks) > 0:
		return fmtClock
	case pct:
		return fmtPct
	}
	return fmtPlain
}

// isElapsed reports whether bracket content is an elapsed-time token: [h],
// [mm], [ss], ...
func isElapsed(b string) bool {
	if b == "" {
		return false
	}
	switch b[0] {
	case 'h', 'm', 's':
		return strings.Count(b, b[:1]) == len(b)
	}
	return false
}

// formatNumber renders v with 15 significant digits and no exponent, the way
// Excel displays stored numbers: 0.1+0.2 → "0.3", 1e-7 → "0.0000001".
func formatNumber(v float64) string {
	if v == 0 {
		return "0"
	}
	s := strconv.FormatFloat(v, 'e', 14, 64) // d.dddddddddddddde±XX
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	mant, expStr, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	point := exp + 1 // digits before the decimal point

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case point <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -point))
		b.WriteString(digits)
	case point >= len(digits):
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", point-len(digits)))
	default:
		b.WriteString(digits[:point])
		b.WriteByte('.')
		b.WriteString(digits[point:])
	}
	return b.String()
}
