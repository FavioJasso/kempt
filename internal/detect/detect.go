// Package detect finds the real tables inside a worksheet: it splits the
// sheet into regions, then for each region picks the header (including
// multi-row headers), skips titles, footers, and totals rows, and plans
// fill-down for merged cells. Every choice is logged as a grid.Decision.
//
// The detector is conservative: when it cannot tell whether a row is data,
// it keeps the row and logs a low-confidence decision.
package detect

import (
	"fmt"

	"github.com/FavioJasso/kempt/internal/grid"
)

// maxScanRows bounds how far into a region kempt looks for the header,
// counted from the region's first row with two or more values.
const maxScanRows = 30

// Options override automatic detection.
type Options struct {
	// SkipRows forces this many leading sheet rows to be skipped; the header
	// starts on the next non-blank row. 0 = auto unless SkipRowsSet.
	SkipRows int
	// SkipRowsSet marks SkipRows as given explicitly, so --skip-rows 0 forces
	// the header onto the first row.
	SkipRowsSet bool
	HeaderRows  int      // force the header height; 0 = auto
	KeepTotals  bool     // keep totals rows as data
	FillDown    []string // column letters, names, or header texts to fill down
}

// HeaderCol is one source column kept in a table.
type HeaderCol struct {
	Col   int      // 0-based source column
	Parts []string // header labels top to bottom (group, sub, unit); empty for headerless tables
}

// Region is one table-shaped block of the sheet.
type Region struct {
	Index     int
	Bounds    grid.Range        // every row and column the region covers, titles and footers included
	Header    []int             // header rows, top to bottom (0-based); empty when the table has no header
	Cols      []HeaderCol       // kept columns, left to right
	Data      []int             // rows that become table rows, in order
	Totals    []int             // rows detected as totals (dropped unless KeepTotals)
	Skipped   bool              // too small to be a table; no Plan is built
	Fill      map[[2]int][2]int // (row, col) → (row, col) whose value fills it
	Decisions []grid.Decision

	group   int // header row holding group labels, or -1
	rowRole map[int]string
	dropped map[int]bool // dropped (entirely empty) columns
}

// Result is everything found on one sheet.
type Result struct {
	Regions   []Region
	Decisions []grid.Decision // sheet-level decisions (forced skips)
	Warnings  []string        // problems with user options, e.g. an unmatched --fill-down name

	forced int // rows skipped by --skip-rows
}

// CellRole returns the role of cell (r, c) for UI coloring, or "" when the
// cell is empty and outside every table.
func (res *Result) CellRole(g *grid.Grid, r, c int) string {
	for i := range res.Regions {
		rg := &res.Regions[i]
		if rg.Bounds.Contains(r, c) {
			return rg.CellRole(g, r, c)
		}
	}
	cell := g.At(r, c)
	if cell == nil || (cell.IsEmpty() && cell.Merge == nil) {
		return ""
	}
	if r < res.forced {
		return grid.RoleTitle
	}
	return grid.RoleIgnored
}

// CellRole returns the role of cell (r, c) inside the region.
func (rg *Region) CellRole(g *grid.Grid, r, c int) string {
	if !rg.Bounds.Contains(r, c) {
		return ""
	}
	role := rg.rowRole[r]
	if role == "" {
		role = grid.RoleBlank
	}
	if rg.Skipped {
		if hasValue(g, r, c) {
			return grid.RoleIgnored
		}
		return grid.RoleBlank
	}
	if _, ok := rg.Fill[[2]int{r, c}]; ok {
		return grid.RoleData
	}
	if rg.dropped[c] || !hasValue(g, r, c) {
		return grid.RoleBlank
	}
	return role
}

// hasValue reports whether (r, c) shows a value, directly or through a merge.
func hasValue(g *grid.Grid, r, c int) bool {
	v := g.Value(r, c)
	return v != nil && !v.IsEmpty()
}

// Detect analyzes one sheet.
func Detect(g *grid.Grid, opts Options) Result {
	s := newSheet(g)
	res := Result{}
	start := 0
	if opts.SkipRows > 0 {
		start = min(opts.SkipRows, g.Rows)
		res.forced = start
		res.Decisions = append(res.Decisions, grid.NewDecision(grid.KSkipRowsForced, g.Sheet,
			grid.Range{R1: 0, C1: 0, R2: start - 1, C2: max(g.Cols-1, 0)}, 1,
			fmt.Sprintf("--skip-rows %d skipped rows 1–%d.", opts.SkipRows, start)))
	}
	forced := opts.SkipRows > 0 || opts.SkipRowsSet

	groups := s.splitColumns(s.groupBlocks(s.blocks(start)))
	for i, grp := range groups {
		rg := s.region(grp, opts, i == 0 && forced)
		rg.Index = len(res.Regions)
		if grp.why != "" {
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSplitRegion, g.Sheet,
				grid.RowRange(grp.first, grp.c1, grp.c2), grp.conf, grp.why))
		}
		res.Regions = append(res.Regions, rg)
	}
	res.Warnings = s.applyFillDown(&res, opts)
	for i := range res.Regions {
		grid.SortDecisions(res.Regions[i].Decisions)
	}
	grid.SortDecisions(res.Decisions)
	return res
}

// ---------------------------------------------------------------------------
// Blocks and region grouping

// block is a maximal run of non-blank rows.
type block struct{ first, last int }

// group is the rows and columns of one candidate region.
type group struct {
	first, last int
	c1, c2      int
	blocks      []block
	attached    bool    // a title-only group was folded into this one
	why         string  // why this group starts a new region (empty for the first)
	conf        float64 // confidence of that split
}

func (s *sheet) blocks(start int) []block {
	var out []block
	in := false
	for r := start; r < s.g.Rows; r++ {
		if s.blank(r) {
			in = false
			continue
		}
		if !in {
			out = append(out, block{first: r, last: r})
			in = true
		} else {
			out[len(out)-1].last = r
		}
	}
	return out
}

// groupBlocks applies the region-split rule: two or more blank rows, or one
// blank row followed by a row that starts a new table, begin a new region.
// A block after blank rows that has no header of its own and sits under the
// same columns continues the table above. Short title-only groups are folded
// into the table below them.
func (s *sheet) groupBlocks(blocks []block) []group {
	var groups []group
	for _, b := range blocks {
		bc1, bc2 := s.colSpan(b.first, b.last)
		if len(groups) == 0 {
			groups = append(groups, group{first: b.first, last: b.last, c1: bc1, c2: bc2, blocks: []block{b}})
			continue
		}
		cur := &groups[len(groups)-1]
		gap := b.first - cur.last - 1
		var why string
		var conf float64
		switch {
		case gap >= 2 && !s.continues(*cur, b, bc1, bc2):
			why, conf = fmt.Sprintf("rows %d–%d are blank", cur.last+2, b.first), 0.9
		case gap == 1 && s.hasTable(*cur) && s.startsTable(b, bc1, bc2):
			why, conf = fmt.Sprintf("row %d is blank and row %d looks like a new header", cur.last+2, b.first+1), 0.75
		}
		if why != "" {
			groups = append(groups, group{first: b.first, last: b.last, c1: bc1, c2: bc2, blocks: []block{b},
				why: fmt.Sprintf("A new region starts at row %d: %s after the table ending at row %d.", b.first+1, why, cur.last+1), conf: conf})
			continue
		}
		cur.last = b.last
		cur.c1, cur.c2 = min(cur.c1, bc1), max(cur.c2, bc2)
		cur.blocks = append(cur.blocks, b)
	}

	// Fold short title-only groups (a title separated from its table by
	// blank rows) into the next group. Longer single-column runs are lists
	// and stay tables of their own.
	var out []group
	for i := 0; i < len(groups); i++ {
		grp := groups[i]
		if i+1 < len(groups) && s.sparseOnly(grp) && len(s.nonBlankRows(grp.first, grp.last)) <= 2 &&
			groups[i+1].first-grp.last-1 <= 3 && !s.sparseOnly(groups[i+1]) {
			next := groups[i+1]
			next.first = grp.first
			next.c1, next.c2 = min(next.c1, grp.c1), max(next.c2, grp.c2)
			next.blocks = append(append([]block(nil), grp.blocks...), next.blocks...)
			next.attached = true
			next.why, next.conf = grp.why, grp.conf
			groups[i+1] = next
			continue
		}
		out = append(out, grp)
	}
	return out
}

// continues reports whether block b, found after two or more blank rows,
// is more of the table in cur: cur already holds a table, b lies under
// cur's columns, and b has no header of its own.
func (s *sheet) continues(cur group, b block, bc1, bc2 int) bool {
	if !s.hasTable(cur) || bc1 < cur.c1 || bc2 > cur.c2 || s.sparseOnly(group{first: b.first, last: b.last}) {
		return false
	}
	rows := s.nonBlankRows(b.first, b.last)
	if len(rows) == 1 {
		return s.effFilled(rows[0], bc1, bc2) >= 2
	}
	_, ok := s.pickHeader(rows, cur.c1, cur.c2)
	return !ok
}

// startsTable reports whether a block after a single blank row begins a new
// table: its first row is clearly a header (its labels sit over values of a
// different type) and fills most of the block's width.
func (s *sheet) startsTable(b block, c1, c2 int) bool {
	rows := s.nonBlankRows(b.first, b.last)
	if len(rows) < 2 {
		return false
	}
	h, ok := s.pickHeader(rows, c1, c2)
	if !ok || h.row != rows[0] {
		return false
	}
	width := c2 - c1 + 1
	return h.contrast > 0 && float64(h.eff) >= 0.8*float64(width) && h.textFrac() >= 0.7 && h.uniqFrac() >= 0.99
}

// hasTable reports whether a group already contains a header and data: at
// least three rows with two or more values.
func (s *sheet) hasTable(grp group) bool {
	n := 0
	for r := grp.first; r <= grp.last; r++ {
		if s.effFilled(r, 0, s.g.Cols-1) >= 2 {
			n++
		}
	}
	return n >= 3
}

// sparseOnly reports whether every row in the group has at most one value.
func (s *sheet) sparseOnly(grp group) bool {
	for r := grp.first; r <= grp.last; r++ {
		if s.distinct(r, 0, s.g.Cols-1) > 1 {
			return false
		}
	}
	return true
}

// splitColumns splits a group whose columns fall into blocks separated by
// two or more fully blank columns (side-by-side tables).
func (s *sheet) splitColumns(groups []group) []group {
	var out []group
	for _, grp := range groups {
		// Runs of used columns; a single blank column does not split a table.
		var runs [][2]int
		for c := grp.c1; c <= grp.c2; c++ {
			if !s.colUsed(c, grp.first, grp.last) {
				continue
			}
			if n := len(runs); n > 0 && c-runs[n-1][1] <= 2 {
				runs[n-1][1] = c
			} else {
				runs = append(runs, [2]int{c, c})
			}
		}
		if len(runs) <= 1 {
			out = append(out, grp)
			continue
		}
		for i, run := range runs {
			sub := grp
			sub.c1, sub.c2 = run[0], run[1]
			rows := s.nonBlankRowsIn(grp.first, grp.last, run[0], run[1])
			sub.first, sub.last = rows[0], rows[len(rows)-1]
			if i > 0 {
				sub.why = fmt.Sprintf("A new region starts at column %s: columns %s are blank, so the tables side by side are read separately.",
					grid.ColName(run[0]), colSpanText(runs[i-1][1]+1, run[0]-1))
				sub.conf = 0.8
			}
			out = append(out, sub)
		}
	}
	return out
}

// colUsed reports whether column c shows a value in rows first..last.
func (s *sheet) colUsed(c, first, last int) bool {
	for r := first; r <= last; r++ {
		if hasValue(s.g, r, c) {
			return true
		}
	}
	return false
}
