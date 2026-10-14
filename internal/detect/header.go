package detect

import (
	"fmt"
	"slices"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/normalize"
)

// pickTolerance lets an earlier, equally header-like row beat a slightly
// higher-scoring later one. Without it, a data row in an all-text table
// outscores a header that has one blank or repeated label.
const pickTolerance = 0.3

// candidate is a scored header candidate.
type candidate struct {
	row      int
	total    float64
	gain     float64
	eff      int // cells showing a value
	text     int // text cells, plus years and dates that sit over other kinds of values
	distinct int
	contrast int // cells whose kind differs from the values below them
}

func (c candidate) textFrac() float64 { return ratio(c.text, c.eff) }
func (c candidate) uniqFrac() float64 { return ratio(c.distinct, c.eff) }

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// headerResult is what findHeader decided for a region.
type headerResult struct {
	rows   []int // header rows, top to bottom; nil when there is none
	group  int   // header row holding group labels, or -1
	units  int   // header row holding only units, or -1
	none   bool  // the region has no header row: every row is data
	conf   float64
	reason string
}

// findHeader picks the header rows of a region.
func (s *sheet) findHeader(rows []int, c1, c2 int, opts Options, forced bool) headerResult {
	if forced || opts.HeaderRows > 0 {
		return s.forcedHeader(rows, c1, c2, opts, forced)
	}
	pick, ok := s.pickHeader(rows, c1, c2)
	if pick.row < 0 {
		return headerResult{group: -1, units: -1}
	}
	if !ok {
		return headerResult{none: true, group: -1, units: -1, conf: 0.6, reason: fmt.Sprintf(
			"No row looks like a header: the best candidate, row %d, holds the same kinds of values as the rows below it. Every row is kept as data and the columns are named col_<letter>; use --skip-rows or --header-rows if the table does have a header.",
			pick.row+1)}
	}

	h := pick.row
	hr := headerResult{rows: []int{h}, group: -1, units: -1}
	extra := ""
	if a := h - 1; slices.Contains(rows, a) && s.groupRow(a, h, c1, c2) != nil {
		hr.rows, hr.group = []int{a, h}, a
	} else if i := slices.Index(rows, h); i+2 < len(rows) && rows[i+1] == h+1 &&
		s.stats(h+1, c1, c2).textFrac() >= 0.8 && s.groupRow(h, h+1, c1, c2) != nil {
		// The group row outscored its sub-header row; keep both as the header.
		hr.rows, hr.group = []int{h, h + 1}, h
		extra = fmt.Sprintf("; row %d below holds its sub-headers", h+2)
	}
	leaf := hr.rows[len(hr.rows)-1]
	if i := slices.Index(rows, leaf); i+2 < len(rows) && rows[i+1] == leaf+1 && s.unitsRow(leaf+1, c1, c2) {
		hr.rows, hr.units = append(hr.rows, leaf+1), leaf+1
	}

	width := c2 - c1 + 1
	cands := s.scoreHeader(rows, c1, c2)
	best := bestOf(cands)
	describe := pick
	if best.row != h && slices.Contains(hr.rows, best.row) {
		describe = best // the top scorer is the sub-header row under the chosen group row
		extra = fmt.Sprintf("; row %d above holds its group labels", h+1)
	}
	st := s.stats(describe.row, c1, c2)
	hr.reason = fmt.Sprintf("Row %d scored highest as the header (%.2f: %.0f%% filled, %.0f%% labels, %.0f%% unique, next rows %.0f%% more numeric)",
		describe.row+1, describe.total, 100*float64(st.eff)/float64(width), 100*describe.textFrac(), 100*describe.uniqFrac(), 100*describe.gain)
	if describe.row != best.row {
		hr.reason = fmt.Sprintf("Row %d was chosen as the header (%.2f: %.0f%% filled, %.0f%% labels, %.0f%% unique); row %d scored %.2f, but row %d comes first and is just as header-like",
			h+1, pick.total, 100*float64(st.eff)/float64(width), 100*pick.textFrac(), 100*pick.uniqFrac(), best.row+1, best.total, h+1)
	}
	// Confidence comes from the margin over the best row outside the header.
	hr.conf = 0.9
	runner := candidate{row: -1}
	for _, c := range cands {
		if !slices.Contains(hr.rows, c.row) && (runner.row < 0 || c.total > runner.total+1e-9) {
			runner = c
		}
	}
	if runner.row >= 0 {
		hr.conf = 0.55 + min(max(describe.total-runner.total, 0), 0.4)
		hr.reason += fmt.Sprintf("; the next best row (row %d) scored %.2f", runner.row+1, runner.total)
	}
	hr.reason += extra + "."
	return hr
}

// forcedHeader applies --skip-rows / --header-rows.
func (s *sheet) forcedHeader(rows []int, c1, c2 int, opts Options, forced bool) headerResult {
	var top int
	var why string
	if forced {
		top = rows[0]
		why = fmt.Sprintf("--skip-rows %d puts the header at row %d", opts.SkipRows, top+1)
	} else {
		pick, ok := s.pickHeader(rows, c1, c2)
		if pick.row < 0 {
			return headerResult{group: -1, units: -1}
		}
		top = pick.row
		if a := top - 1; ok && slices.Contains(rows, a) && s.groupRow(a, top, c1, c2) != nil {
			top = a
		}
		why = fmt.Sprintf("the header starts at row %d, where detection found it", top+1)
	}
	i := slices.Index(rows, top)
	n := opts.HeaderRows
	if n > 0 {
		why += fmt.Sprintf("; --header-rows %d sets its height", n)
	} else {
		n = 1
		if i+1 < len(rows) && rows[i+1] == top+1 && s.groupRow(top, top+1, c1, c2) != nil {
			n = 2
		}
		why += "; its height was detected"
	}
	if i+n >= len(rows) {
		n = max(len(rows)-1-i, 1)
	}
	hr := headerResult{rows: rows[i : i+n], group: -1, units: -1, conf: 1}
	if len(hr.rows) >= 2 && s.groupRow(hr.rows[len(hr.rows)-2], hr.rows[len(hr.rows)-1], c1, c2) != nil {
		hr.group = hr.rows[len(hr.rows)-2]
	}
	hr.reason = fmt.Sprintf("Header is %s: %s.", rowsText(hr.rows), why)
	return hr
}

// scoreHeader scores each candidate in the first maxScanRows rows of the
// region (counted from its first row with two or more values): fraction
// filled + fraction labels + fraction unique + how much more numeric the
// next five rows are. Scores come back in row order.
func (s *sheet) scoreHeader(rows []int, c1, c2 int) []candidate {
	width := c2 - c1 + 1
	start := rows[0]
	for _, r := range rows {
		if width == 1 || s.effFilled(r, c1, c2) >= 2 {
			start = r
			break
		}
	}
	var out []candidate
	for i, r := range rows {
		if r >= start+maxScanRows || i == len(rows)-1 {
			break
		}
		below := rows[i+1 : min(i+6, len(rows))]
		c := s.headerStats(r, below, c1, c2)
		// A single value (often merged across the table) is a title, not a header.
		if c.eff == 0 || (width > 1 && c.distinct <= 1 && s.ownFilled(r, c1, c2) <= 1) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// headerStats scores row r as a header over the rows below it. Years and
// dates count as labels when the values under them are not years or dates
// (a "2019 | 2020" header over amounts); every label whose kind differs
// from the values below it adds to the contrast.
func (s *sheet) headerStats(r int, below []int, c1, c2 int) candidate {
	c := candidate{row: r}
	numeric := 0
	seen := map[string]bool{}
	// Numbers read as year labels only when every number in the row looks
	// like a year ("2019 | 2020 | 2021"), not when one amount happens to be 1850.
	years := true
	for col := c1; col <= c2; col++ {
		if v := s.g.Value(r, col); v != nil && !v.IsEmpty() && s.classOf(r, col) == clNum && !yearLike(s.g, v) {
			years = false
			break
		}
	}
	for col := c1; col <= c2; col++ {
		v := s.g.Value(r, col)
		if v == nil || v.IsEmpty() {
			continue
		}
		c.eff++
		k := s.classOf(r, col)
		under := s.colClass(below, col)
		label := false
		switch k {
		case clText:
			c.text++
			label = under == clNum || under == clDate || under == clBool
		case clNum:
			label = years && under == clNum && yearLike(s.g, v) && !s.colYearLike(below, col)
		case clDate:
			label = under == clNum || under == clBool
		}
		if label {
			c.contrast++
			if k != clText {
				c.text++
			}
		} else if k == clNum || k == clDate || k == clBool {
			numeric++
		}
		if t := labelText(s.g, v); !seen[t] {
			seen[t] = true
			c.distinct++
		}
	}
	if c.eff == 0 {
		return c
	}
	sum, n := 0.0, 0
	for _, rr := range below {
		if st := s.stats(rr, c1, c2); st.eff > 0 {
			sum += st.numFrac()
			n++
		}
	}
	if n > 0 {
		c.gain = max(0, sum/float64(n)-ratio(numeric, c.eff))
	}
	width := c2 - c1 + 1
	c.total = float64(c.eff)/float64(width) + c.textFrac() + c.uniqFrac() + c.gain
	return c
}

// pickHeader chooses the header row. The highest score wins, except that an
// earlier row within pickTolerance that is at least as header-like (mostly
// labels, mostly filled, as much contrast) wins over a later one. ok is
// false when the region has no header: the chosen row's values have the
// same kinds as the rows below and at least one column is typed. A region
// with no candidate at all returns row -1.
func (s *sheet) pickHeader(rows []int, c1, c2 int) (candidate, bool) {
	cands := s.scoreHeader(rows, c1, c2)
	if len(cands) == 0 {
		return candidate{row: -1}, false
	}
	best := bestOf(cands)
	width := c2 - c1 + 1
	pick := best
	for _, c := range cands {
		if c.row >= best.row {
			break
		}
		if c.total >= best.total-pickTolerance && c.textFrac() >= 0.8 &&
			float64(c.eff) >= 0.7*float64(width) && c.contrast >= best.contrast {
			pick = c
			break
		}
	}
	if pick.contrast == 0 {
		i := slices.Index(rows, pick.row)
		if s.typedColumns(rows[i+1:min(i+6, len(rows))], c1, c2) {
			return pick, false
		}
	}
	return pick, true
}

func bestOf(cands []candidate) candidate {
	best := candidate{row: -1, total: -1}
	for _, c := range cands {
		if best.row < 0 || c.total > best.total+1e-9 {
			best = c
		}
	}
	return best
}

// typedColumns reports whether any column holds mostly numbers, dates, or
// booleans in the given rows.
func (s *sheet) typedColumns(rows []int, c1, c2 int) bool {
	for c := c1; c <= c2; c++ {
		if k := s.colClass(rows, c); k == clNum || k == clDate || k == clBool {
			return true
		}
	}
	return false
}

// groupLabels is the per-column group label of a group header row.
type groupLabels struct {
	labels map[int]string
	merged bool
	spans  []grid.Range
}

// groupRow decides whether row a, directly above header row h, holds group
// labels for h: horizontal merges, or two or more sparse labels that each
// sit above two or more sub-headers. Group spans must sit over typed data
// columns and not over the table's first column, so titles ("Monthly
// Report ... Oct 2026") are never mistaken for groups. It returns nil when
// a is not a group row.
func (s *sheet) groupRow(a, h, c1, c2 int) *groupLabels {
	width := c2 - c1 + 1
	st := s.stats(a, c1, c2)
	if st.eff == 0 {
		return nil
	}
	if st.distinct <= 1 && s.firstOwn(a, c1, c2) == c1 {
		return nil // a single label starting at the table's left edge is a title
	}
	under := s.belowRows(h, c1, c2, 5)
	validSpan := func(lo, hi int) bool {
		if lo <= c1 || hi <= lo {
			return false
		}
		typed := false
		for c := lo; c <= hi; c++ {
			switch s.colClass(under, c) {
			case clNum, clDate, clBool:
				typed = true
			case clText:
				return false
			}
		}
		return typed
	}
	gl := &groupLabels{labels: map[int]string{}}

	// Horizontal merges anchored in row a.
	for c := c1; c <= c2; c++ {
		cell := s.g.At(a, c)
		if cell.Merge == nil || cell.Merge.R1 != a || cell.Merge.C1 != c || cell.Merge.C2 == cell.Merge.C1 {
			continue
		}
		m := *cell.Merge
		if m.Cols() >= width && st.distinct <= 1 {
			return nil // a title merged across the whole table
		}
		if m.R2 >= h || cell.IsEmpty() {
			continue // spans into the header row: a label, not a group
		}
		if !validSpan(max(m.C1, c1), min(m.C2, c2)) {
			return nil
		}
		gl.merged = true
		gl.spans = append(gl.spans, m)
		for cc := max(m.C1, c1); cc <= min(m.C2, c2); cc++ {
			gl.labels[cc] = labelText(s.g, cell)
		}
	}
	if gl.merged {
		s.ownLabels(gl, a, c1, c2)
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
	if len(anchors) < 2 || len(anchors) == width {
		return nil
	}
	prevWidth := 0
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
		if end == c {
			continue
		}
		if !validSpan(c, end) {
			return nil
		}
		prevWidth = end - c + 1
		gl.spans = append(gl.spans, grid.Range{R1: a, C1: c, R2: a, C2: end})
		for cc := c; cc <= end; cc++ {
			gl.labels[cc] = labelText(s.g, s.g.At(a, c))
		}
	}
	if len(gl.spans) == 0 {
		return nil
	}
	s.ownLabels(gl, a, c1, c2)
	return gl
}

// ownLabels adds row a's remaining labels (cells not already in a group span).
func (s *sheet) ownLabels(gl *groupLabels, a, c1, c2 int) {
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

// unitsRow reports whether row u holds nothing but units such as "($000s)"
// or "(%)", which belong to the header above it.
func (s *sheet) unitsRow(u, c1, c2 int) bool {
	n := 0
	for c := c1; c <= c2; c++ {
		cell := s.g.At(u, c)
		if cell.IsEmpty() {
			continue
		}
		if s.classOf(u, c) != clText {
			return false
		}
		rest, unit := normalize.ExtractUnit(labelText(s.g, cell))
		if unit == "" || strings.TrimSpace(rest) != "" {
			return false
		}
		n++
	}
	return n > 0
}
