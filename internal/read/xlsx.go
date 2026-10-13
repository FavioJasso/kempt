package read

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
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
	x := &xlsxReader{f: f, maxRows: opts.MaxRows, dateStyles: map[int]bool{}}
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
	f          *excelize.File
	maxRows    int
	dateStyles map[int]bool // style id → number format is a date/time
}

type placedCell struct {
	r, c int
	cell grid.Cell
}

func (x *xlsxReader) sheet(name string) (*grid.Grid, error) {
	rows, err := x.rawRows(name)
	if err != nil {
		return nil, err
	}
	merges, err := x.merges(name)
	if err != nil {
		return nil, err
	}

	var cells []placedCell
	maxR, maxC := -1, -1
	var active []grid.Range // merges overlapping the current row
	next := 0
	for r, row := range rows {
		active = activeMerges(active, merges, &next, r)
		for c, raw := range row {
			if coveredByMerge(active, r, c) {
				continue // Excel only shows the anchor's value
			}
			cell, err := x.cell(name, r, c, raw)
			if err != nil {
				return nil, err
			}
			if cell.Kind == grid.Empty {
				continue
			}
			cells = append(cells, placedCell{r, c, cell})
			if !cell.IsEmpty() {
				maxR, maxC = max(maxR, r), max(maxC, c)
			}
		}
	}
	for _, m := range merges {
		maxR, maxC = max(maxR, m.R2), max(maxC, m.C2)
	}
	if x.maxRows > 0 && maxR+1 > x.maxRows {
		return nil, tooManyRows(name, x.maxRows)
	}

	g := grid.New(name, maxR+1, maxC+1)
	g.Source = "xlsx"
	for _, p := range cells {
		if p.r <= maxR && p.c <= maxC {
			g.Cells[p.r][p.c] = p.cell
		}
	}
	for _, m := range merges {
		g.AddMerge(m)
	}
	return g, nil
}

// rawRows returns raw cell values (what GetRows returns with RawCellValue),
// stopping early once the sheet exceeds maxRows.
func (x *xlsxReader) rawRows(sheet string) ([][]string, error) {
	it, err := x.f.Rows(sheet)
	if err != nil {
		return nil, fmt.Errorf("read: sheet %q: %w", sheet, err)
	}
	defer it.Close()
	var out [][]string
	for n := 1; it.Next(); n++ {
		row, err := it.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return nil, fmt.Errorf("read: sheet %q row %d: %w", sheet, n, err)
		}
		if len(row) == 0 {
			continue
		}
		if x.maxRows > 0 && n > x.maxRows && hasValue(row) {
			return nil, tooManyRows(sheet, x.maxRows)
		}
		for len(out) < n-1 {
			out = append(out, nil)
		}
		out = append(out, row)
	}
	if err := it.Error(); err != nil {
		return nil, fmt.Errorf("read: sheet %q: %w", sheet, err)
	}
	return out, nil
}

func hasValue(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// merges returns the sheet's merged ranges sorted by position. A chart sheet
// yields errNotWorksheet.
func (x *xlsxReader) merges(sheet string) ([]grid.Range, error) {
	mcs, err := x.f.GetMergeCells(sheet, true)
	if err != nil {
		if strings.HasSuffix(err.Error(), "is not a worksheet") {
			return nil, fmt.Errorf("%w: sheet %q is a chart or dialog sheet", errNotWorksheet, sheet)
		}
		return nil, fmt.Errorf("read: sheet %q: %w", sheet, err)
	}
	if len(mcs) > 0 {
		// excelize resolves every per-cell lookup through the merge list; drop
		// it from the in-memory workbook (never saved) so lookups stay O(1) in
		// the number of merges. Covered cells are skipped by the caller.
		if err := x.f.UnmergeCell(sheet, "A1", "XFD1048576"); err != nil {
			return nil, fmt.Errorf("read: sheet %q: %w", sheet, err)
		}
	}
	out := make([]grid.Range, 0, len(mcs))
	for _, mc := range mcs {
		rg, err := grid.ParseRange(mc.GetStartAxis() + ":" + mc.GetEndAxis())
		if err != nil {
			continue
		}
		out = append(out, rg)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.R1 != b.R1 {
			return a.R1 < b.R1
		}
		if a.C1 != b.C1 {
			return a.C1 < b.C1
		}
		if a.R2 != b.R2 {
			return a.R2 < b.R2
		}
		return a.C2 < b.C2
	})
	return out, nil
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

// cell builds the grid cell at (r, c) from its raw value plus the cell's
// stored type, formula, and number format.
func (x *xlsxReader) cell(sheet string, r, c int, raw string) (grid.Cell, error) {
	ref := grid.CellRef(r, c)
	formula, err := x.f.GetCellFormula(sheet, ref)
	if err != nil {
		return grid.Cell{}, cellError(sheet, ref, err)
	}
	formula = strings.TrimPrefix(formula, "=")
	if raw == "" {
		if formula == "" {
			return grid.Cell{}, nil
		}
		return grid.Cell{Kind: grid.Formula, Formula: formula}, nil // no cached value
	}
	typ, err := x.f.GetCellType(sheet, ref)
	if err != nil {
		return grid.Cell{}, cellError(sheet, ref, err)
	}

	var cell grid.Cell
	switch typ {
	case excelize.CellTypeSharedString, excelize.CellTypeInlineString:
		cell = grid.Cell{Kind: grid.String, Raw: raw}
	case excelize.CellTypeBool:
		cell = boolCell(raw)
	case excelize.CellTypeError:
		return grid.Cell{Kind: grid.Error, Raw: raw}, nil // cached errors stay errors
	case excelize.CellTypeDate:
		cell = grid.Cell{Kind: grid.Date, Raw: raw, DateFmt: true}
	case excelize.CellTypeFormula: // t="str": a string formula result
		cell = grid.Cell{Kind: grid.String, Raw: raw}
		if formula != "" && plainNumber.MatchString(raw) {
			// Some writers (excelize included) store numeric results as text.
			if cell, err = x.number(sheet, ref, raw); err != nil {
				return grid.Cell{}, err
			}
		}
	default: // "n" or no type
		if cell, err = x.number(sheet, ref, raw); err != nil {
			return grid.Cell{}, err
		}
	}
	if formula != "" {
		cell.Kind = grid.Formula
		cell.Formula = formula
	}
	return cell, nil
}

func cellError(sheet, ref string, err error) error {
	return fmt.Errorf("read: %s!%s: %w", grid.QuoteSheet(sheet), ref, err)
}

// plainNumber matches a number written without exponent, leading zeros, or
// trailing fractional zeros, i.e. exactly as a float would be printed.
var plainNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)

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
func (x *xlsxReader) number(sheet, ref, raw string) (grid.Cell, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return grid.Cell{Kind: grid.String, Raw: raw}, nil
	}
	cell := grid.Cell{Kind: grid.Number, Raw: formatNumber(v), Num: &v}
	isDate, err := x.dateStyle(sheet, ref)
	if err != nil {
		return grid.Cell{}, err
	}
	if isDate {
		cell.Kind, cell.DateFmt = grid.Date, true
	}
	return cell, nil
}

// dateStyle reports whether the cell's number format is a date/time format.
// Results are cached per style id.
func (x *xlsxReader) dateStyle(sheet, ref string) (bool, error) {
	id, err := x.f.GetCellStyle(sheet, ref)
	if err != nil {
		return false, cellError(sheet, ref, err)
	}
	if v, ok := x.dateStyles[id]; ok {
		return v, nil
	}
	v := false
	if st, err := x.f.GetStyle(id); err == nil && st != nil {
		if st.CustomNumFmt != nil {
			v = isDateFormat(*st.CustomNumFmt)
		} else {
			v = isBuiltinDateFormat(st.NumFmt)
		}
	}
	x.dateStyles[id] = v
	return v, nil
}

// isBuiltinDateFormat reports whether a built-in number format id is a date or
// time format (including the locale-specific CJK and Thai ids).
func isBuiltinDateFormat(id int) bool {
	return 14 <= id && id <= 22 || 27 <= id && id <= 36 || 45 <= id && id <= 47 ||
		50 <= id && id <= 58 || 71 <= id && id <= 81
}

// isDateFormat reports whether a custom number format code displays a date or
// time. It looks for y/m/d/h/s tokens, AM/PM, or an elapsed-time bracket such
// as [h] outside quoted literals, escaped or padding characters, and other
// bracketed sections ([Red], [$-409], [>=100]).
func isDateFormat(code string) bool {
	s := strings.ToLower(code)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return false
			}
			i += j + 1
		case '\\', '_', '*':
			i++ // escaped literal, padding width, or fill character
		case '[':
			j := strings.IndexByte(s[i+1:], ']')
			if j < 0 {
				return false
			}
			if isElapsed(s[i+1 : i+1+j]) {
				return true
			}
			i += j + 1
		case 'y', 'm', 'd', 'h', 's':
			return true
		case 'a':
			if strings.HasPrefix(s[i:], "am/pm") || strings.HasPrefix(s[i:], "a/p") {
				return true
			}
		}
	}
	return false
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
