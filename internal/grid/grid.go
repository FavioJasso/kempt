// Package grid is kempt's in-memory model of a single worksheet: a
// rectangular matrix of cells plus the merged ranges that cover them.
// Readers (xlsx, csv) produce a Grid; every later stage reads from it.
package grid

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind is the stored type of a cell as reported by its source file.
type Kind uint8

const (
	Empty   Kind = iota // no value
	String              // text (shared, inline, or any CSV field)
	Number              // numeric value
	Bool                // TRUE/FALSE
	Date                // numeric serial with a date/time number format, or an ISO "d" cell
	Formula             // formula cell; Raw holds the cached result
	Error               // Excel error value such as #DIV/0!
)

var kindNames = [...]string{"empty", "string", "number", "bool", "date", "formula", "error"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// MarshalText makes Kind render as its name in JSON.
func (k Kind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// Cell is one grid position.
//
// For merged ranges, the top-left cell holds the value and every cell in the
// range (top-left included) points at the same Range through Merge.
type Cell struct {
	Raw     string   // raw text; for formulas the cached value; for numbers the stored number
	Kind    Kind     // stored kind
	Num     *float64 // set when the stored (or cached) value is numeric
	Merge   *Range   // non-nil when the cell is part of a merged range
	DateFmt bool     // the cell's number format shows a date (time-only formats do not count)
	PctFmt  bool     // the cell's number format shows a percentage (0.12 displays as 12%)
	TimeFmt string   // "clock" (h:mm) or "elapsed" ([h]:mm) when the number format shows only a time
	Mask    string   // zero-padding number format ("00000") the value was rendered through; Raw holds the padded text
	Formula string   // formula text without the leading "=", when Kind == Formula
}

// IsEmpty reports whether the cell holds no value of its own. Cells covered
// by a merge (but not its anchor) are empty.
func (c *Cell) IsEmpty() bool {
	return c == nil || (c.Kind == Empty && strings.TrimSpace(c.Raw) == "") ||
		(c.Kind != Empty && c.Num == nil && strings.TrimSpace(c.Raw) == "")
}

// IsMergeAnchor reports whether the cell is the top-left cell of a merge.
func (c *Cell) IsMergeAnchor(r, col int) bool {
	return c != nil && c.Merge != nil && c.Merge.R1 == r && c.Merge.C1 == col
}

// Range is an inclusive, 0-based rectangle of cells.
type Range struct {
	R1, C1, R2, C2 int
}

// Rows returns the number of rows the range spans.
func (r Range) Rows() int { return r.R2 - r.R1 + 1 }

// Cols returns the number of columns the range spans.
func (r Range) Cols() int { return r.C2 - r.C1 + 1 }

// Contains reports whether (row, col) lies inside the range.
func (r Range) Contains(row, col int) bool {
	return row >= r.R1 && row <= r.R2 && col >= r.C1 && col <= r.C2
}

// A1 renders the range in A1 notation, e.g. "A1:F2". Single cells render
// without a colon ("B7").
func (r Range) A1() string {
	if r.R1 == r.R2 && r.C1 == r.C2 {
		return CellRef(r.R1, r.C1)
	}
	return CellRef(r.R1, r.C1) + ":" + CellRef(r.R2, r.C2)
}

func (r Range) String() string { return r.A1() }

// RowRange is the range covering columns c1..c2 of a single row.
func RowRange(row, c1, c2 int) Range { return Range{R1: row, C1: c1, R2: row, C2: c2} }

// Grid is a rectangular sheet. Cells[r][c] is always addressable for
// 0 <= r < Rows and 0 <= c < Cols.
type Grid struct {
	Sheet  string
	Rows   int
	Cols   int
	Cells  [][]Cell
	Merges []Range // sorted by (R1, C1)
	// Source is "xlsx" or "csv".
	Source string
	// Delimiter is the sniffed CSV delimiter (0 for xlsx).
	Delimiter rune
	// Date1904 is set when the workbook uses the 1904 date system.
	Date1904 bool
}

// New allocates an empty rows x cols grid.
func New(sheet string, rows, cols int) *Grid {
	g := &Grid{Sheet: sheet, Rows: rows, Cols: cols, Cells: make([][]Cell, rows)}
	for r := range g.Cells {
		g.Cells[r] = make([]Cell, cols)
	}
	return g
}

// At returns the cell at (r, c), or nil when out of bounds.
func (g *Grid) At(r, c int) *Cell {
	if r < 0 || c < 0 || r >= g.Rows || c >= g.Cols {
		return nil
	}
	return &g.Cells[r][c]
}

// Value returns the value visible at (r, c): the cell's own value, or the
// merge anchor's value when the cell is covered by a merge.
func (g *Grid) Value(r, c int) *Cell {
	cell := g.At(r, c)
	if cell == nil {
		return nil
	}
	if cell.Merge != nil && (cell.Merge.R1 != r || cell.Merge.C1 != c) {
		return g.At(cell.Merge.R1, cell.Merge.C1)
	}
	return cell
}

// RowEmpty reports whether every cell in row r is empty (merge-covered cells
// count as empty unless they are the anchor).
func (g *Grid) RowEmpty(r int) bool {
	for c := 0; c < g.Cols; c++ {
		if !g.Cells[r][c].IsEmpty() {
			return false
		}
	}
	return true
}

// AddMerge records a merged range and points every covered cell at it.
func (g *Grid) AddMerge(m Range) {
	if m.R1 < 0 || m.C1 < 0 || m.R2 >= g.Rows || m.C2 >= g.Cols || m.R2 < m.R1 || m.C2 < m.C1 {
		return
	}
	g.Merges = append(g.Merges, m)
	shared := m // one copy shared by every covered cell
	for r := m.R1; r <= m.R2; r++ {
		for c := m.C1; c <= m.C2; c++ {
			g.Cells[r][c].Merge = &shared
		}
	}
}

// ColName converts a 0-based column index to letters: 0 → "A", 26 → "AA".
func ColName(c int) string {
	if c < 0 {
		return "?"
	}
	var b [8]byte
	i := len(b)
	for c >= 0 {
		i--
		b[i] = byte('A' + c%26)
		c = c/26 - 1
	}
	return string(b[i:])
}

// ColIndex converts column letters to a 0-based index ("A" → 0). It returns
// -1 for invalid input.
func ColIndex(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, ch := range strings.ToUpper(s) {
		if ch < 'A' || ch > 'Z' {
			return -1
		}
		n = n*26 + int(ch-'A'+1)
	}
	return n - 1
}

// CellRef renders 0-based (row, col) as A1 notation: (0,0) → "A1".
func CellRef(r, c int) string { return ColName(c) + strconv.Itoa(r+1) }

// ParseRef parses "B7" into 0-based (row, col).
func ParseRef(s string) (row, col int, err error) {
	i := 0
	for i < len(s) && ((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
		i++
	}
	if i == 0 || i == len(s) {
		return 0, 0, fmt.Errorf("invalid cell reference %q", s)
	}
	col = ColIndex(s[:i])
	n, err := strconv.Atoi(s[i:])
	if err != nil || n < 1 || col < 0 {
		return 0, 0, fmt.Errorf("invalid cell reference %q", s)
	}
	return n - 1, col, nil
}

// ParseRange parses "A1:F2" (or a single "B7") into a Range.
func ParseRange(s string) (Range, error) {
	if i := strings.LastIndexByte(s, '!'); i >= 0 {
		s = s[i+1:]
	}
	a, b, ok := strings.Cut(s, ":")
	r1, c1, err := ParseRef(a)
	if err != nil {
		return Range{}, err
	}
	if !ok {
		return Range{R1: r1, C1: c1, R2: r1, C2: c1}, nil
	}
	r2, c2, err := ParseRef(b)
	if err != nil {
		return Range{}, err
	}
	if r2 < r1 {
		r1, r2 = r2, r1
	}
	if c2 < c1 {
		c1, c2 = c2, c1
	}
	return Range{R1: r1, C1: c1, R2: r2, C2: c2}, nil
}

// QualifiedRange renders "Sheet!A1:F2", quoting the sheet name when needed.
func QualifiedRange(sheet string, r Range) string {
	return QuoteSheet(sheet) + "!" + r.A1()
}

// QuoteSheet quotes a sheet name for A1 references when it contains anything
// other than letters, digits, and underscores.
func QuoteSheet(sheet string) string {
	plain := sheet != ""
	for _, ch := range sheet {
		if !(ch == '_' || ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z') {
			plain = false
			break
		}
	}
	if plain {
		return sheet
	}
	return "'" + strings.ReplaceAll(sheet, "'", "''") + "'"
}
