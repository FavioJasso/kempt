package detect

import (
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/normalize"
)

// class is what a cell's value looks like to the detector.
type class uint8

const (
	clEmpty class = iota
	clNull        // placeholder or Excel error
	clNum         // number (also dates and booleans count as "numeric" in scores)
	clDate
	clBool
	clText
)

// sheet wraps a grid with memoized per-cell classes and per-row blankness.
type sheet struct {
	g      *grid.Grid
	cls    []class // class+1 per cell; 0 = not computed yet
	blanks []int8  // per row: 0 unknown, 1 blank, 2 not blank
}

func newSheet(g *grid.Grid) *sheet {
	return &sheet{g: g, cls: make([]class, g.Rows*g.Cols), blanks: make([]int8, g.Rows)}
}

// classOf classifies the cell that is visible at (r, c) (the merge anchor
// for covered cells).
func (s *sheet) classOf(r, c int) class {
	cell := s.g.At(r, c)
	if cell == nil {
		return clEmpty
	}
	if cell.Merge != nil {
		r, c = cell.Merge.R1, cell.Merge.C1
		cell = s.g.At(r, c)
	}
	i := r*s.g.Cols + c
	if v := s.cls[i]; v != 0 {
		return v - 1
	}
	k := classify(cell)
	s.cls[i] = k + 1
	return k
}

func classify(c *grid.Cell) class {
	if c == nil || c.IsEmpty() {
		return clEmpty
	}
	switch c.Kind {
	case grid.Error:
		return clNull
	case grid.Bool:
		return clBool
	case grid.Date:
		return clDate
	}
	if c.Num != nil {
		if c.DateFmt {
			return clDate
		}
		return clNum
	}
	s := strings.TrimSpace(c.Raw)
	if normalize.ClassifyNull(s) != normalize.NotNull {
		return clNull
	}
	if _, ok := normalize.ParseNumber(s); ok {
		return clNum
	}
	if _, ok := normalize.ParseDate(s, normalize.MDY); ok {
		return clDate
	}
	if _, ok := normalize.ParseDate(s, normalize.DMY); ok {
		return clDate
	}
	if _, ok := normalize.ParseBool(s); ok {
		return clBool
	}
	return clText
}

// blank reports whether row r shows no value at all (merges included).
func (s *sheet) blank(r int) bool {
	if r < 0 || r >= s.g.Rows {
		return true
	}
	switch s.blanks[r] {
	case 1:
		return true
	case 2:
		return false
	}
	b := true
	for c := 0; c < s.g.Cols; c++ {
		if hasValue(s.g, r, c) {
			b = false
			break
		}
	}
	s.blanks[r] = 2
	if b {
		s.blanks[r] = 1
	}
	return b
}

func (s *sheet) nonBlankRows(first, last int) []int {
	var out []int
	for r := first; r <= last; r++ {
		if !s.blank(r) {
			out = append(out, r)
		}
	}
	return out
}

// colSpan returns the leftmost and rightmost columns that show a value in
// rows first..last.
func (s *sheet) colSpan(first, last int) (int, int) {
	c1, c2 := s.g.Cols, -1
	for r := first; r <= last; r++ {
		for c := 0; c < s.g.Cols; c++ {
			if hasValue(s.g, r, c) {
				c1 = min(c1, c)
				c2 = max(c2, c)
			}
		}
	}
	if c2 < 0 {
		return 0, 0
	}
	return c1, c2
}

// rowStats summarizes the visible values of one row over [c1, c2].
type rowStats struct {
	eff      int // cells showing a value (merges included)
	text     int
	numeric  int // numbers, dates, booleans
	distinct int // distinct visible labels
}

func (st rowStats) frac(n int) float64 {
	if st.eff == 0 {
		return 0
	}
	return float64(n) / float64(st.eff)
}

func (st rowStats) textFrac() float64 { return st.frac(st.text) }
func (st rowStats) uniqFrac() float64 { return st.frac(st.distinct) }
func (st rowStats) numFrac() float64  { return st.frac(st.numeric) }

func (s *sheet) stats(r, c1, c2 int) rowStats {
	var st rowStats
	seen := map[string]bool{}
	for c := c1; c <= c2; c++ {
		v := s.g.Value(r, c)
		if v == nil || v.IsEmpty() {
			continue
		}
		st.eff++
		switch s.classOf(r, c) {
		case clText:
			st.text++
		case clNum, clDate, clBool:
			st.numeric++
		}
		label := labelText(s.g, v)
		if !seen[label] {
			seen[label] = true
			st.distinct++
		}
	}
	return st
}

func (s *sheet) effFilled(r, c1, c2 int) int {
	n := 0
	for c := c1; c <= c2; c++ {
		if hasValue(s.g, r, c) {
			n++
		}
	}
	return n
}

func (s *sheet) distinct(r, c1, c2 int) int { return s.stats(r, c1, c2).distinct }

// ownFilled counts cells in the row that hold their own value.
func (s *sheet) ownFilled(r, c1, c2 int) int {
	n := 0
	for c := c1; c <= c2; c++ {
		if !s.g.At(r, c).IsEmpty() {
			n++
		}
	}
	return n
}

// rowMerge returns the first horizontal merge anchored in row r.
func (s *sheet) rowMerge(r, c1, c2 int) *grid.Range {
	for c := c1; c <= c2; c++ {
		cell := s.g.At(r, c)
		if cell.IsMergeAnchor(r, c) && cell.Merge.C2 > cell.Merge.C1 {
			return cell.Merge
		}
	}
	return nil
}

// firstOwn returns the column of the first cell in the row holding its own value.
func (s *sheet) firstOwn(r, c1, c2 int) int {
	for c := c1; c <= c2; c++ {
		if !s.g.At(r, c).IsEmpty() {
			return c
		}
	}
	return -1
}

func (s *sheet) firstClass(r, c1, c2 int) class {
	c := s.firstOwn(r, c1, c2)
	if c < 0 {
		return clEmpty
	}
	return s.classOf(r, c)
}

func (s *sheet) firstText(r, c1, c2 int) string {
	c := s.firstOwn(r, c1, c2)
	if c < 0 {
		return ""
	}
	return labelText(s.g, s.g.At(r, c))
}

// firstTextCell returns the first own text cell in the row.
func (s *sheet) firstTextCell(r, c1, c2 int) (string, int) {
	for c := c1; c <= c2; c++ {
		cell := s.g.At(r, c)
		if cell.IsEmpty() {
			continue
		}
		if s.classOf(r, c) == clText {
			return labelText(s.g, cell), c
		}
	}
	return "", -1
}

// hasNumber reports whether the row holds a number outside column skip.
func (s *sheet) hasNumber(r, c1, c2, skip int) bool {
	for c := c1; c <= c2; c++ {
		if c != skip && !s.g.At(r, c).IsEmpty() && s.classOf(r, c) == clNum {
			return true
		}
	}
	return false
}

// number returns the numeric value of the cell at (r, c), if it has one.
func (s *sheet) number(r, c int) (float64, bool) {
	cell := s.g.At(r, c)
	if cell.IsEmpty() || s.classOf(r, c) != clNum {
		return 0, false
	}
	if cell.Num != nil {
		return *cell.Num, true
	}
	n, ok := normalize.ParseNumber(strings.TrimSpace(cell.Raw))
	return n.Float, ok
}

// labelText renders a cell as a header label: whitespace collapsed, dates
// as ISO text.
func labelText(g *grid.Grid, c *grid.Cell) string {
	if c == nil {
		return ""
	}
	if c.Num != nil && (c.Kind == grid.Date || c.DateFmt) {
		if t, hasTime, ok := normalize.ExcelSerial(*c.Num, g.Date1904); ok {
			if hasTime {
				return normalize.NewTimestamp(t).String()
			}
			return normalize.NewDate(t).String()
		}
	}
	return strings.Join(strings.Fields(c.Raw), " ")
}

func snake(s string) string { return normalize.SnakeCase(s) }

// blankIn reports whether row r shows no value in columns c1..c2.
func (s *sheet) blankIn(r, c1, c2 int) bool {
	for c := c1; c <= c2; c++ {
		if hasValue(s.g, r, c) {
			return false
		}
	}
	return true
}

// nonBlankRowsIn lists the rows in first..last that show a value in c1..c2.
func (s *sheet) nonBlankRowsIn(first, last, c1, c2 int) []int {
	var out []int
	for r := first; r <= last; r++ {
		if !s.blankIn(r, c1, c2) {
			out = append(out, r)
		}
	}
	return out
}

// belowRows returns up to n non-blank rows after r (within c1..c2),
// stopping at two consecutive blank rows.
func (s *sheet) belowRows(r, c1, c2, n int) []int {
	var out []int
	blanks := 0
	for rr := r + 1; rr < s.g.Rows && len(out) < n && blanks < 2; rr++ {
		if s.blankIn(rr, c1, c2) {
			blanks++
			continue
		}
		blanks = 0
		out = append(out, rr)
	}
	return out
}

// colClass returns the most common kind of value in column c across rows,
// ignoring empty cells and placeholders. Ties go to text, the kind that
// claims the least. It returns clEmpty when the column has no values.
func (s *sheet) colClass(rows []int, c int) class {
	var n [clText + 1]int
	for _, r := range rows {
		if !hasValue(s.g, r, c) {
			continue
		}
		n[s.classOf(r, c)]++
	}
	best := clEmpty
	for _, k := range []class{clText, clNum, clDate, clBool} {
		if n[k] > 0 && (best == clEmpty || n[k] > n[best]) {
			best = k
		}
	}
	return best
}

// colYearLike reports whether every number in column c across rows looks
// like a year.
func (s *sheet) colYearLike(rows []int, c int) bool {
	seen := false
	for _, r := range rows {
		if !hasValue(s.g, r, c) || s.classOf(r, c) != clNum {
			continue
		}
		if !yearLike(s.g, s.g.Value(r, c)) {
			return false
		}
		seen = true
	}
	return seen
}

// yearLike reports whether a numeric cell holds a whole number between 1800
// and 2200.
func yearLike(g *grid.Grid, c *grid.Cell) bool {
	var v float64
	switch {
	case c.Num != nil:
		v = *c.Num
	default:
		n, ok := normalize.ParseNumber(strings.TrimSpace(c.Raw))
		if !ok || !n.IsInt() || n.Grouped || n.Currency != "" || n.Percent || n.Accounting {
			return false
		}
		v = n.Float
	}
	return v == float64(int64(v)) && v >= 1800 && v <= 2200
}
