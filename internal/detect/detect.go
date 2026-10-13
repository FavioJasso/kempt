// Package detect finds the real tables inside a worksheet: it splits the
// sheet into regions, then for each region picks the header (including
// multi-row headers), skips titles, footers, and totals rows, and plans
// fill-down for merged cells. Every choice is logged as a grid.Decision.
package detect

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
)

// maxScanRows bounds how far into a region kempt looks for the header.
const maxScanRows = 30

// Options override automatic detection.
type Options struct {
	SkipRows   int      // force this many leading sheet rows to be skipped (the header starts right after); 0 = auto
	HeaderRows int      // force the header height; 0 = auto
	KeepTotals bool     // keep totals rows as data
	FillDown   []string // column letters, names, or header texts to fill down
}

// HeaderCol is one source column kept in a table.
type HeaderCol struct {
	Col   int      // 0-based source column
	Parts []string // header labels top to bottom (group, then sub); may be empty
}

// Region is one table-shaped block of the sheet.
type Region struct {
	Index     int
	Bounds    grid.Range        // every row and column the region covers, titles and footers included
	Header    []int             // header rows, top to bottom (0-based)
	Cols      []HeaderCol       // kept columns, left to right
	Data      []int             // rows that become table rows, in order
	Totals    []int             // rows detected as totals (dropped unless KeepTotals)
	Skipped   bool              // too small to be a table; no Plan is built
	Fill      map[[2]int][2]int // (row, col) → (row, col) whose value fills it
	Decisions []grid.Decision

	rowRole map[int]string
	dropped map[int]bool // dropped (entirely empty) columns
}

// Result is everything found on one sheet.
type Result struct {
	Regions   []Region
	Decisions []grid.Decision // sheet-level decisions (forced skips, region splits)
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

	blocks := s.blocks(start)
	groups := s.groupBlocks(blocks)
	for i, grp := range groups {
		rg := s.region(grp, opts, i == 0 && opts.SkipRows > 0)
		rg.Index = len(res.Regions)
		if i > 0 {
			prev := groups[i-1]
			gap := grp.first - prev.last - 1
			blanks := fmt.Sprintf("rows %d–%d are blank", prev.last+2, prev.last+1+gap)
			conf, why := 0.9, "two or more blank rows separate tables"
			if gap == 1 {
				blanks = fmt.Sprintf("row %d is blank", prev.last+2)
				conf, why = 0.75, "a blank row followed by a header-like row starts a new table"
			}
			if grp.attached {
				conf = 0.8
				why += "; the title row(s) above it belong to the new table"
			}
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSplitRegion, g.Sheet,
				grid.Range{R1: grp.first, C1: rg.Bounds.C1, R2: grp.first, C2: rg.Bounds.C2}, conf,
				fmt.Sprintf("A new region starts at row %d: %s after the table ending at row %d, and %s.",
					grp.first+1, blanks, prev.last+1, strings.ReplaceAll(why, "new table", "new region"))))
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

// group is a set of consecutive blocks that form one region.
type group struct {
	first, last int
	blocks      []block
	attached    bool // a title-only group was folded into this one
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
// blank row followed by a header-like row (once the current region already
// holds a table), start a new region. Groups that hold only sparse rows are
// folded into the following region as titles, or left as their own (skipped)
// region.
func (s *sheet) groupBlocks(blocks []block) []group {
	var groups []group
	for _, b := range blocks {
		if len(groups) == 0 {
			groups = append(groups, group{first: b.first, last: b.last, blocks: []block{b}})
			continue
		}
		cur := &groups[len(groups)-1]
		gap := b.first - cur.last - 1
		split := gap >= 2 || (gap == 1 && s.hasTable(*cur) && s.headerLike(b))
		if split {
			groups = append(groups, group{first: b.first, last: b.last, blocks: []block{b}})
			continue
		}
		cur.last = b.last
		cur.blocks = append(cur.blocks, b)
	}
	// Fold sparse-only groups (titles separated from their table by blank
	// rows) into the next group.
	var out []group
	for i := 0; i < len(groups); i++ {
		grp := groups[i]
		if i+1 < len(groups) && s.sparseOnly(grp) && groups[i+1].first-grp.last-1 <= 3 && !s.sparseOnly(groups[i+1]) {
			next := groups[i+1]
			next.first = grp.first
			next.blocks = append(append([]block(nil), grp.blocks...), next.blocks...)
			next.attached = true
			groups[i+1] = next
			continue
		}
		out = append(out, grp)
	}
	return out
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

// headerLike reports whether the first row of a block looks like a header:
// mostly unique text, two or more labels, and clearly more numeric rows below.
func (s *sheet) headerLike(b block) bool {
	r := b.first
	c1, c2 := s.colSpan(b.first, b.last)
	st := s.stats(r, c1, c2)
	if st.eff < 2 || st.textFrac() < 0.7 || st.uniqFrac() < 0.99 {
		return false
	}
	return s.gain(r, b.last, c1, c2, st) >= 0.15
}

// ---------------------------------------------------------------------------
// Region analysis

var (
	totalRe     = regexp.MustCompile(`(?i)^\s*(grand\s+)?(sub)?total\b|^\s*sum\b|^\s*all\s+`)
	totalTailRe = regexp.MustCompile(`(?i)\b(sub)?totals?\s*:?\s*$`)
	footerRe    = regexp.MustCompile(`(?i)^\s*(source|sources|note|notes|prepared\s+by)\b|^\s*[*†‡§¹²³⁴⁵⁶⁷⁸⁹]`)
)

func (s *sheet) region(grp group, opts Options, forced bool) Region {
	g := s.g
	c1, c2 := s.colSpan(grp.first, grp.last)
	rg := Region{
		Bounds:  grid.Range{R1: grp.first, C1: c1, R2: grp.last, C2: c2},
		Fill:    map[[2]int][2]int{},
		rowRole: map[int]string{},
		dropped: map[int]bool{},
	}
	width := c2 - c1 + 1
	rows := s.nonBlankRows(grp.first, grp.last)
	if len(rows) < 2 {
		s.skipRegion(&rg, "it has a single row")
		return rg
	}

	// Header.
	header, conf, reason := s.findHeader(rows, c1, c2, opts, forced)
	if header == nil {
		s.skipRegion(&rg, "no row looks like a header")
		return rg
	}
	rg.Header = header
	top, leaf := header[0], header[len(header)-1]
	rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderRow, g.Sheet,
		grid.RowRange(leaf, c1, c2), conf, reason))
	for _, h := range header {
		rg.rowRole[h] = grid.RoleHeader
	}

	// Rows above the header: titles (or, conservatively logged, other rows).
	for _, r := range rows {
		if r >= top {
			break
		}
		st := s.stats(r, c1, c2)
		own := s.ownFilled(r, c1, c2)
		switch {
		case st.distinct <= 1:
			conf, why := 0.9, fmt.Sprintf("Row %d has a single value", r+1)
			if m := s.rowMerge(r, c1, c2); m != nil {
				conf, why = 0.95, fmt.Sprintf("Row %d has a single value merged across %s", r+1, colSpanText(m.C1, m.C2))
			}
			rg.rowRole[r] = grid.RoleTitle
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipTitleRow, g.Sheet,
				grid.RowRange(r, c1, c2), conf, fmt.Sprintf("%s above the header (row %d); skipped as a title.", why, top+1)))
		case float64(own) < 0.4*float64(width):
			rg.rowRole[r] = grid.RoleTitle
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipTitleRow, g.Sheet,
				grid.RowRange(r, c1, c2), 0.8, fmt.Sprintf("Row %d fills %d of %d columns (under 40%%) above the header (row %d); skipped as a title.", r+1, own, width, top+1)))
		default:
			rg.rowRole[r] = grid.RoleIgnored
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipLeadingRow, g.Sheet,
				grid.RowRange(r, c1, c2), 0.5, fmt.Sprintf("Row %d sits above the detected header (row %d) but does not look like a title; it was skipped. Use --skip-rows or --header-rows to override.", r+1, top+1)))
		}
	}

	// Body: footers, totals, data.
	var body []int
	for _, r := range rows {
		if r > leaf {
			body = append(body, r)
		}
	}
	body = s.footers(&rg, body, c1, c2)
	s.blankRuns(&rg, leaf+1, lastOr(body, leaf), c1, c2)
	s.totals(&rg, body, c1, c2, opts.KeepTotals)

	if len(rg.Data) < 2 {
		s.skipRegion(&rg, fmt.Sprintf("it has %d data row(s) below the header", len(rg.Data)))
		return rg
	}

	// Columns and fill-down from vertical merges.
	s.columns(&rg, c1, c2)
	s.mergeFill(&rg)
	return rg
}

func lastOr(xs []int, def int) int {
	if len(xs) == 0 {
		return def
	}
	return xs[len(xs)-1]
}

func (s *sheet) skipRegion(rg *Region, why string) {
	rg.Skipped = true
	rg.Data = nil
	rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipRegion, s.g.Sheet, rg.Bounds, 0.7,
		fmt.Sprintf("Region %s was not loaded as a table because %s (a table needs a header and at least 2 data rows).", rg.Bounds.A1(), why)))
}

// findHeader picks the header rows. It returns nil when no candidate exists.
func (s *sheet) findHeader(rows []int, c1, c2 int, opts Options, forced bool) ([]int, float64, string) {
	width := c2 - c1 + 1
	if forced || opts.HeaderRows > 0 {
		// With --skip-rows the header starts at the first row after the skip;
		// --header-rows alone keeps the detected top.
		var top int
		if forced {
			top = rows[0]
		} else {
			scores := s.scoreHeader(rows, c1, c2)
			if len(scores) == 0 {
				return nil, 0, ""
			}
			best := scores[0]
			for _, sc := range scores[1:] {
				if sc.total > best.total+1e-9 {
					best = sc
				}
			}
			h := best.row
			top = h
			if a := h - 1; a >= rows[0] && s.groupRow(a, h, c1, c2) != nil {
				top = a
			}
		}
		n := opts.HeaderRows
		if n <= 0 {
			n = 1
			if i := slices.Index(rows, top); i+1 < len(rows) && rows[i+1] == top+1 && s.groupRow(top, top+1, c1, c2) != nil {
				n = 2
			}
		}
		i := slices.Index(rows, top)
		if i < 0 || i+n >= len(rows) {
			n = max(len(rows)-1-i, 1)
		}
		header := rows[i : i+n]
		why := fmt.Sprintf("Header forced to %s by --skip-rows/--header-rows.", rowsText(header))
		return header, 1, why
	}

	scores := s.scoreHeader(rows, c1, c2)
	if len(scores) == 0 {
		return nil, 0, ""
	}
	best := scores[0]
	for _, sc := range scores[1:] {
		if sc.total > best.total+1e-9 {
			best = sc
		}
	}
	h := best.row
	header := []int{h}
	extra := ""
	if a := h - 1; a >= rows[0] && !s.blank(a) && s.groupRow(a, h, c1, c2) != nil {
		header = []int{a, h}
	} else if i := slices.Index(rows, h); i+2 < len(rows) && rows[i+1] == h+1 &&
		s.stats(h+1, c1, c2).textFrac() >= 0.8 && s.groupRow(h, h+1, c1, c2) != nil {
		// The group row outscored its sub-header row; keep both as the header.
		header = []int{h, h + 1}
		extra = fmt.Sprintf("; row %d below holds its sub-headers", h+2)
	}

	st := s.stats(h, c1, c2)
	reason := fmt.Sprintf("Row %d scored highest as the header (%.2f: %.0f%% filled, %.0f%% text, %.0f%% unique, next rows %.0f%% more numeric)",
		h+1, best.total, 100*float64(st.eff)/float64(width), 100*st.textFrac(), 100*st.uniqFrac(), 100*best.gain)
	// Confidence comes from the margin over the best row outside the header.
	conf := 0.9
	runner := headerScore{row: -1}
	for _, sc := range scores {
		if !slices.Contains(header, sc.row) && (runner.row < 0 || sc.total > runner.total+1e-9) {
			runner = sc
		}
	}
	if runner.row >= 0 {
		conf = 0.55 + min(best.total-runner.total, 0.4)
		reason += fmt.Sprintf("; the next best row (row %d) scored %.2f", runner.row+1, runner.total)
	}
	return header, conf, reason + extra + "."
}

type headerScore struct {
	row   int
	total float64
	gain  float64
}

// scoreHeader scores each candidate in the first maxScanRows rows of the
// region: fraction filled + fraction text + fraction unique + how much more
// numeric the next five rows are. Scores come back in row order, so taking
// the first maximum breaks ties toward the earliest row.
func (s *sheet) scoreHeader(rows []int, c1, c2 int) []headerScore {
	width := c2 - c1 + 1
	var out []headerScore
	limit := rows[0] + maxScanRows
	for i, r := range rows {
		if r >= limit || i == len(rows)-1 {
			break
		}
		st := s.stats(r, c1, c2)
		if st.eff == 0 || (width > 1 && st.distinct <= 1) {
			continue
		}
		g := s.gain(r, rows[len(rows)-1], c1, c2, st)
		out = append(out, headerScore{row: r, gain: g,
			total: float64(st.eff)/float64(width) + st.textFrac() + st.uniqFrac() + g})
	}
	return out
}

// gain is how much more numeric the next (up to) five non-blank rows are
// than row r, floored at zero.
func (s *sheet) gain(r, last, c1, c2 int, st rowStats) float64 {
	sum, n := 0.0, 0
	for rr := r + 1; rr <= last && n < 5; rr++ {
		if s.blank(rr) {
			continue
		}
		ns := s.stats(rr, c1, c2)
		if ns.eff == 0 {
			continue
		}
		sum += ns.numFrac()
		n++
	}
	if n == 0 {
		return 0
	}
	return max(0, sum/float64(n)-st.numFrac())
}

// groupLabels is the per-column group label of a group header row.
type groupLabels struct {
	labels map[int]string
	merged bool
	spans  []grid.Range
}

// groupRow decides whether row a, directly above header row h, holds group
// labels for h: horizontal merges over header columns, or sparse labels that
// each sit above two or more sub-headers. It returns nil when a is not a
// group row (for example a title that spans the table).
func (s *sheet) groupRow(a, h, c1, c2 int) *groupLabels {
	width := c2 - c1 + 1
	st := s.stats(a, c1, c2)
	if st.eff == 0 {
		return nil
	}
	gl := &groupLabels{labels: map[int]string{}}
	// Horizontal merges anchored in row a.
	hasGroupMerge := false
	for c := c1; c <= c2; c++ {
		cell := s.g.At(a, c)
		if cell.Merge == nil || cell.Merge.R1 != a || cell.Merge.C1 != c || cell.Merge.C2 == cell.Merge.C1 {
			continue
		}
		m := *cell.Merge
		if m.Cols() >= width && st.distinct <= 1 {
			return nil // a title merged across the whole table
		}
		if m.R2 >= h {
			continue // spans into the header row: a label, not a group
		}
		if cell.IsEmpty() {
			continue
		}
		hasGroupMerge = true
		gl.spans = append(gl.spans, m)
		for cc := max(m.C1, c1); cc <= min(m.C2, c2); cc++ {
			gl.labels[cc] = labelText(s.g, cell)
		}
	}
	if st.distinct <= 1 && s.firstOwn(a, c1, c2) == c1 {
		return nil // a single label starting at the table's left edge is a title
	}
	if hasGroupMerge {
		gl.merged = true
		s.ownLabels(gl, a, h, c1, c2)
		return gl
	}

	// Sparse labels without merges: each label extends right over sub-headers
	// whose own cell in row a is empty; the last label reuses the previous
	// span width.
	if st.distinct <= 1 && st.eff >= width {
		return nil
	}
	var anchors []int
	for c := c1; c <= c2; c++ {
		if !s.g.At(a, c).IsEmpty() && s.g.At(a, c).Merge == nil {
			anchors = append(anchors, c)
		}
	}
	if len(anchors) == 0 || len(anchors) == width {
		return nil
	}
	prevWidth := 0
	grouped := false
	for i, c := range anchors {
		if !hasValue(s.g, h, c) {
			continue // a standalone label above an empty sub-header
		}
		end := c
		limit := c2
		if i+1 < len(anchors) {
			limit = anchors[i+1] - 1
		} else if prevWidth > 0 {
			limit = min(c2, c+prevWidth-1)
		}
		for end+1 <= limit && s.g.At(a, end+1).IsEmpty() && hasValue(s.g, h, end+1) {
			end++
		}
		if end > c {
			grouped = true
			prevWidth = end - c + 1
			gl.spans = append(gl.spans, grid.Range{R1: a, C1: c, R2: a, C2: end})
			for cc := c; cc <= end; cc++ {
				gl.labels[cc] = labelText(s.g, s.g.At(a, c))
			}
		}
	}
	if !grouped {
		return nil
	}
	s.ownLabels(gl, a, h, c1, c2)
	return gl
}

// ownLabels adds row a's remaining labels (cells not already in a group span).
func (s *sheet) ownLabels(gl *groupLabels, a, h, c1, c2 int) {
	for c := c1; c <= c2; c++ {
		if _, ok := gl.labels[c]; ok {
			continue
		}
		v := s.g.Value(a, c)
		if v == nil || v.IsEmpty() {
			continue
		}
		gl.labels[c] = labelText(s.g, v)
	}
}

// footers removes trailing footnote rows from body and returns the rest.
// Only single-value text rows at the end qualify; those starting with
// Source/Note/*/†/¹/Prepared by are dropped, and rows that follow one of them
// are dropped with lower confidence. Other trailing single-value rows are
// kept as data with a low-confidence decision.
func (s *sheet) footers(rg *Region, body []int, c1, c2 int) []int {
	i := len(body)
	for i > 0 && s.ownFilled(body[i-1], c1, c2) == 1 && s.firstClass(body[i-1], c1, c2) == clText {
		i--
	}
	tail := body[i:]
	firstMatch := -1
	for j, r := range tail {
		if footerRe.MatchString(s.firstText(r, c1, c2)) {
			firstMatch = j
			break
		}
	}
	var keep []int
	for j, r := range tail {
		text := s.firstText(r, c1, c2)
		switch {
		case footerRe.MatchString(text):
			rg.rowRole[r] = grid.RoleFooter
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipFooter, s.g.Sheet,
				grid.RowRange(r, c1, c2), 0.9, fmt.Sprintf("Row %d follows the last data row and holds a single note (%s); dropped as a footer.", r+1, quote(text, 40))))
		case firstMatch >= 0 && j > firstMatch:
			rg.rowRole[r] = grid.RoleFooter
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipFooter, s.g.Sheet,
				grid.RowRange(r, c1, c2), 0.6, fmt.Sprintf("Row %d holds a single value (%s) below a footnote; dropped as a footer.", r+1, quote(text, 40))))
		default:
			if len(tail) < len(body) { // single-value rows in a multi-column table
				rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KPossibleFooter, s.g.Sheet,
					grid.RowRange(r, c1, c2), 0.4, fmt.Sprintf("Row %d at the end of the table holds a single value (%s); it may be a note but was kept as data.", r+1, quote(text, 40))))
			}
			keep = append(keep, r)
		}
	}
	return append(body[:i:i], keep...)
}

// blankRuns logs blank rows between the header and the last body row.
func (s *sheet) blankRuns(rg *Region, from, to, c1, c2 int) {
	for r := from; r <= to; r++ {
		if !s.blank(r) {
			continue
		}
		end := r
		for end+1 <= to && s.blank(end+1) {
			end++
		}
		for rr := r; rr <= end; rr++ {
			rg.rowRole[rr] = grid.RoleBlank
		}
		what := fmt.Sprintf("Row %d is blank", r+1)
		if end > r {
			what = fmt.Sprintf("Rows %d–%d are blank", r+1, end+1)
		}
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KBlankRow, s.g.Sheet,
			grid.Range{R1: r, C1: c1, R2: end, C2: c2}, 1, what+" inside the table; skipped."))
		r = end
	}
}

// totals classifies body rows as totals or data.
func (s *sheet) totals(rg *Region, body []int, c1, c2 int, keep bool) {
	var sinceLast, all, subtotals []int
	for _, r := range body {
		if rg.rowRole[r] == grid.RoleFooter {
			continue
		}
		label, col := s.firstTextCell(r, c1, c2)
		strong := totalRe.MatchString(label)
		weak := !strong && totalTailRe.MatchString(label)
		if !(strong || weak) || !s.hasNumber(r, c1, c2, col) {
			if strong || weak {
				rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KKeepTotalRow, s.g.Sheet,
					grid.RowRange(r, c1, c2), 0.3, fmt.Sprintf("Row %d starts with %s but has no numbers; kept as data.", r+1, quote(label, 40))))
			}
			rg.rowRole[r] = grid.RoleData
			rg.Data = append(rg.Data, r)
			sinceLast = append(sinceLast, r)
			all = append(all, r)
			continue
		}
		conf := 0.8
		if weak {
			conf = 0.6
		}
		why := fmt.Sprintf("Row %d is labeled %s", r+1, quote(label, 40))
		if proof := s.sumProof(r, c1, c2, col, sinceLast, all, subtotals); proof != "" {
			conf = 0.97
			why += "; " + proof
		}
		rg.Totals = append(rg.Totals, r)
		rg.rowRole[r] = grid.RoleTotal
		if keep {
			rg.Data = append(rg.Data, r)
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KKeepTotalRow, s.g.Sheet,
				grid.RowRange(r, c1, c2), conf, why+". Kept because of --keep-totals."))
		} else {
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KDropTotalRow, s.g.Sheet,
				grid.RowRange(r, c1, c2), conf, why+". Dropped as a totals row (use --keep-totals to keep it)."))
		}
		subtotals = append(subtotals, r)
		sinceLast = nil
	}
}

// sumProof checks whether a numeric cell of a totals row equals the sum of
// the rows in its group, of all data rows above, or of the earlier totals.
func (s *sheet) sumProof(r, c1, c2, labelCol int, group, all, subtotals []int) string {
	for c := c1; c <= c2; c++ {
		if c == labelCol {
			continue
		}
		v, ok := s.number(r, c)
		if !ok {
			continue
		}
		for _, cand := range []struct {
			rows []int
			what string
		}{
			{group, "its group"},
			{all, "every data row above"},
			{subtotals, "the subtotals above"},
		} {
			if len(cand.rows) < 2 && cand.what != "its group" || len(cand.rows) == 0 {
				continue
			}
			sum, n := 0.0, 0
			for _, rr := range cand.rows {
				if x, ok := s.number(rr, c); ok {
					sum += x
					n++
				}
			}
			if n > 0 && abs(sum-v) <= 0.01 {
				return fmt.Sprintf("%s equals the sum of %s (%s)", grid.CellRef(r, c), cand.what, trimFloat(sum))
			}
		}
	}
	return ""
}

// columns decides which source columns become table columns and records
// their header labels. Columns with no header and no data are dropped.
func (s *sheet) columns(rg *Region, c1, c2 int) {
	var gl *groupLabels
	if len(rg.Header) >= 2 {
		gl = s.groupRow(rg.Header[len(rg.Header)-2], rg.Header[len(rg.Header)-1], c1, c2)
	}
	var groupSpans []string
	for c := c1; c <= c2; c++ {
		var parts []string
		var prev *grid.Cell
		for i, h := range rg.Header {
			var text string
			cell := s.g.Value(h, c)
			if gl != nil && i == len(rg.Header)-2 {
				text = gl.labels[c]
			} else if cell != nil && !cell.IsEmpty() {
				text = labelText(s.g, cell)
			}
			if text == "" {
				continue
			}
			// The same merged cell seen again in a lower header row is not a new part.
			if prev != nil && cell == prev {
				continue
			}
			prev = cell
			parts = append(parts, text)
		}
		hasData := false
		for _, r := range rg.Data {
			if hasValue(s.g, r, c) {
				hasData = true
				break
			}
		}
		if len(parts) == 0 && !hasData {
			rg.dropped[c] = true
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KDropEmptyColumn, s.g.Sheet,
				grid.Range{R1: rg.Bounds.R1, C1: c, R2: rg.Bounds.R2, C2: c}, 1,
				fmt.Sprintf("Column %s has no header and no data; dropped.", grid.ColName(c))))
			continue
		}
		rg.Cols = append(rg.Cols, HeaderCol{Col: c, Parts: parts})
	}
	if gl != nil {
		for _, sp := range gl.spans {
			groupSpans = append(groupSpans, fmt.Sprintf("%s over %s", quote(gl.labels[sp.C1], 30), colSpanText(sp.C1, sp.C2)))
		}
		a, h := rg.Header[len(rg.Header)-2], rg.Header[len(rg.Header)-1]
		conf, how := 0.9, "merged group labels"
		if !gl.merged {
			conf, how = 0.7, "sparse group labels"
		}
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderMerge, s.g.Sheet,
			grid.Range{R1: a, C1: c1, R2: h, C2: c2}, conf,
			fmt.Sprintf("Rows %d–%d form a two-row header: %s in row %d (%s) are combined with the labels below as group_sub.",
				a+1, h+1, how, a+1, strings.Join(groupSpans, ", "))))
	} else if len(rg.Header) > 2 {
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderMerge, s.g.Sheet,
			grid.Range{R1: rg.Header[0], C1: c1, R2: rg.Header[len(rg.Header)-1], C2: c2}, 1,
			fmt.Sprintf("Header rows %s are combined top to bottom into each column name.", rowsText(rg.Header))))
	}
}

// mergeFill copies vertically merged values down into the data rows they cover.
func (s *sheet) mergeFill(rg *Region) {
	data := map[int]bool{}
	for _, r := range rg.Data {
		data[r] = true
	}
	for _, m := range s.g.Merges {
		if m.R2 == m.R1 || !rg.Bounds.Contains(m.R1, m.C1) || rg.dropped[m.C1] {
			continue
		}
		anchor := s.g.At(m.R1, m.C1)
		if anchor.IsEmpty() {
			continue
		}
		n := 0
		for r := m.R1 + 1; r <= m.R2; r++ {
			if data[r] {
				rg.Fill[[2]int{r, m.C1}] = [2]int{m.R1, m.C1}
				n++
			}
		}
		if n == 0 {
			continue
		}
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KFillDown, s.g.Sheet, m, 0.95,
			fmt.Sprintf("Merged cell %s (%s) spans %d rows; its value was copied into the %d data row(s) below the first.",
				m.A1(), quote(labelText(s.g, anchor), 30), m.Rows(), n)))
	}
}

// applyFillDown handles --fill-down: matching columns copy the last
// non-empty value down into blank data cells.
func (s *sheet) applyFillDown(res *Result, opts Options) []string {
	var warnings []string
	for _, want := range opts.FillDown {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		matched := false
		for i := range res.Regions {
			rg := &res.Regions[i]
			if rg.Skipped {
				continue
			}
			for _, hc := range rg.Cols {
				if !matchColumn(want, hc) {
					continue
				}
				matched = true
				var src *[2]int
				n, first, last := 0, -1, -1
				for _, r := range rg.Data {
					key := [2]int{r, hc.Col}
					if from, ok := rg.Fill[key]; ok {
						src = &from
						continue
					}
					if hasValue(s.g, r, hc.Col) {
						p := key
						if m := s.g.At(r, hc.Col).Merge; m != nil {
							p = [2]int{m.R1, m.C1}
						}
						src = &p
						continue
					}
					if src != nil {
						rg.Fill[key] = *src
						n++
						if first < 0 {
							first = r
						}
						last = r
					}
				}
				if n > 0 {
					rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KFillDown, s.g.Sheet,
						grid.Range{R1: first, C1: hc.Col, R2: last, C2: hc.Col}, 1,
						fmt.Sprintf("--fill-down %s: %d blank cell(s) in column %s were filled with the value above.", want, n, grid.ColName(hc.Col))))
				}
			}
		}
		if !matched {
			warnings = append(warnings, fmt.Sprintf("--fill-down %q matched no column", want))
		}
	}
	return warnings
}

// matchColumn matches a --fill-down argument against a column letter, its
// header text, or its snake_case name.
func matchColumn(want string, hc HeaderCol) bool {
	if strings.EqualFold(want, grid.ColName(hc.Col)) {
		return true
	}
	text := strings.Join(hc.Parts, " ")
	if strings.EqualFold(want, text) || (len(hc.Parts) > 0 && strings.EqualFold(want, hc.Parts[len(hc.Parts)-1])) {
		return true
	}
	return snake(want) != "" && snake(want) == snake(text)
}

// ---------------------------------------------------------------------------
// helpers

func colSpanText(c1, c2 int) string {
	if c1 == c2 {
		return grid.ColName(c1)
	}
	return grid.ColName(c1) + ":" + grid.ColName(c2)
}

func rowsText(rows []int) string {
	if len(rows) == 1 {
		return fmt.Sprintf("row %d", rows[0]+1)
	}
	return fmt.Sprintf("rows %d–%d", rows[0]+1, rows[len(rows)-1]+1)
}

func quote(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return fmt.Sprintf("%q", s)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
