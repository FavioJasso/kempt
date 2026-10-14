package detect

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
)

var (
	totalRe     = regexp.MustCompile(`(?i)^\s*(grand\s+)?(sub)?total\b|^\s*sum\b|^\s*all\s+`)
	totalTailRe = regexp.MustCompile(`(?i)\b(sub)?totals?\s*:?\s*$`)
	footerRe    = regexp.MustCompile(`(?i)^\s*(sources?|notes?|footnotes?|prepared\s+by|last\s+updated|data\s+as\s+of|copyright)\b|^\s*[*†‡§©¹²³⁴⁵⁶⁷⁸⁹]`)
)

// region analyzes one group of rows and columns.
func (s *sheet) region(grp group, opts Options, forced bool) Region {
	g := s.g
	c1, c2 := grp.c1, grp.c2
	rg := Region{
		Bounds:  grid.Range{R1: grp.first, C1: c1, R2: grp.last, C2: c2},
		Fill:    map[[2]int][2]int{},
		group:   -1,
		rowRole: map[int]string{},
		dropped: map[int]bool{},
	}
	rows := s.nonBlankRowsIn(grp.first, grp.last, c1, c2)
	if len(rows) < 2 {
		s.skipRegion(&rg, "it has a single row")
		return rg
	}

	hd := s.findHeader(rows, c1, c2, opts, forced)
	var lead, body []int
	switch {
	case hd.none:
		// Leading single-value rows are titles; everything else is data.
		top := len(rows)
		for i, r := range rows {
			if s.effFilled(r, c1, c2) >= 2 || c1 == c2 {
				top = i
				break
			}
		}
		if top == len(rows) {
			s.skipRegion(&rg, "no row looks like a header")
			return rg
		}
		s.leadingRows(&rg, rows[:top], rows[top], c1, c2, false)
		body = rows[top:]
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KNoHeader, g.Sheet,
			grid.Range{R1: rows[top], C1: c1, R2: rows[len(rows)-1], C2: c2}, hd.conf, hd.reason))
	case hd.rows == nil:
		s.skipRegion(&rg, "no row looks like a header")
		return rg
	default:
		rg.Header, rg.group = hd.rows, hd.group
		top, leaf := hd.rows[0], hd.rows[len(hd.rows)-1]
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderRow, g.Sheet,
			grid.RowRange(leaf, c1, c2), hd.conf, hd.reason))
		if hd.units >= 0 {
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderMerge, g.Sheet,
				grid.RowRange(hd.units, c1, c2), 0.85,
				fmt.Sprintf("Row %d holds only units under the header; it joins the header so the units move into the column comments.", hd.units+1)))
		}
		for _, h := range hd.rows {
			rg.rowRole[h] = grid.RoleHeader
		}
		var above []int
		for _, r := range rows {
			if r < top {
				above = append(above, r)
			} else if r > leaf {
				body = append(body, r)
			}
		}
		lead = s.leadingRows(&rg, above, top, c1, c2, true)
	}

	if len(body) == 0 {
		s.skipRegion(&rg, "it has no rows below the header")
		return rg
	}
	first := body[0]
	body = s.footers(&rg, body, c1, c2)
	s.blankRuns(&rg, first, lastOr(body, first), c1, c2)
	s.note(&rg, body, c1, c2)
	s.totals(&rg, append(lead, body...), c1, c2, opts.KeepTotals)

	if len(rg.Data) < 2 {
		s.skipRegion(&rg, fmt.Sprintf("it has %d data row(s)", len(rg.Data)))
		return rg
	}
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
		fmt.Sprintf("Region %s was not loaded as a table because %s (a table needs at least 2 data rows).", rg.Bounds.A1(), why)))
}

// leadingRows classifies rows above the header (or above the first data row
// of a headerless table). Single-value and sparse rows are titles. Rows
// whose values have the same kinds as the data below are kept as data, and
// anything else is skipped with a low-confidence decision. It returns the
// rows kept as data.
func (s *sheet) leadingRows(rg *Region, above []int, top, c1, c2 int, hasHeader bool) []int {
	g := s.g
	width := c2 - c1 + 1
	where := fmt.Sprintf("above the header (row %d)", top+1)
	if !hasHeader {
		where = fmt.Sprintf("above the first data row (row %d)", top+1)
	}
	var kept []int
	under := s.belowRows(top, c1, c2, 5)
	for _, r := range above {
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
				grid.RowRange(r, c1, c2), conf, fmt.Sprintf("%s %s; skipped as a title.", why, where)))
		case float64(own) < 0.4*float64(width):
			rg.rowRole[r] = grid.RoleTitle
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipTitleRow, g.Sheet,
				grid.RowRange(r, c1, c2), 0.8, fmt.Sprintf("Row %d fills %d of %d columns (under 40%%) %s; skipped as a title.", r+1, own, width, where)))
		case s.dataLike(r, under, c1, c2):
			kept = append(kept, r)
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KLeadingData, g.Sheet,
				grid.RowRange(r, c1, c2), 0.4, fmt.Sprintf("Row %d sits %s but its values have the same kinds as the data below; it was kept as data. Use --skip-rows or --header-rows if the header is wrong.", r+1, where)))
		default:
			rg.rowRole[r] = grid.RoleIgnored
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipLeadingRow, g.Sheet,
				grid.RowRange(r, c1, c2), 0.5, fmt.Sprintf("Row %d sits %s but does not look like a title or like data; it was skipped. Use --skip-rows or --header-rows to override.", r+1, where)))
		}
	}
	return kept
}

// dataLike reports whether row r has two or more values whose kinds match
// the columns below it, including at least one number, date, or boolean.
func (s *sheet) dataLike(r int, under []int, c1, c2 int) bool {
	n, typed := 0, false
	for c := c1; c <= c2; c++ {
		if !hasValue(s.g, r, c) {
			continue
		}
		k := s.classOf(r, c)
		if k == clNull {
			continue
		}
		if k != s.colClass(under, c) {
			return false
		}
		n++
		typed = typed || k != clText
	}
	return n >= 2 && typed
}

// footers removes the trailing run of footnote rows: single text values that
// start with Source, Note, Prepared by, *, †, ¹ and the like. Other single
// values at the end of a multi-column table are kept as data with a
// low-confidence decision.
func (s *sheet) footers(rg *Region, body []int, c1, c2 int) []int {
	single := func(r int) bool {
		return s.ownFilled(r, c1, c2) == 1 && s.firstClass(r, c1, c2) == clText
	}
	i := len(body)
	for i > 0 && single(body[i-1]) && footerRe.MatchString(s.firstText(body[i-1], c1, c2)) {
		i--
	}
	for _, r := range body[i:] {
		rg.rowRole[r] = grid.RoleFooter
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KSkipFooter, s.g.Sheet,
			grid.RowRange(r, c1, c2), 0.9, fmt.Sprintf("Row %d follows the last data row and holds a single note (%s); dropped as a footer.", r+1, quote(s.firstText(r, c1, c2), 40))))
	}
	if c2 > c1 {
		j := i
		for j > 0 && single(body[j-1]) {
			j--
		}
		if j > 0 {
			for _, r := range body[j:i] {
				rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KPossibleFooter, s.g.Sheet,
					grid.RowRange(r, c1, c2), 0.4, fmt.Sprintf("Row %d at the end of the table holds a single value (%s); it may be a note but was kept as data.", r+1, quote(s.firstText(r, c1, c2), 40))))
			}
		}
	}
	return body[:i:i]
}

// note logs a single-value row directly under the header of a multi-column
// table ("Amounts in USD"), which is kept as data.
func (s *sheet) note(rg *Region, body []int, c1, c2 int) {
	if len(rg.Header) == 0 || len(body) < 2 || c2 == c1 {
		return
	}
	r := body[0]
	if r != rg.Header[len(rg.Header)-1]+1 || s.ownFilled(r, c1, c2) != 1 || s.firstClass(r, c1, c2) != clText ||
		s.effFilled(body[1], c1, c2) < 2 {
		return
	}
	rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KPossibleNote, s.g.Sheet,
		grid.RowRange(r, c1, c2), 0.4, fmt.Sprintf("Row %d directly under the header holds a single value (%s); it may be a note but was kept as data.", r+1, quote(s.firstText(r, c1, c2), 40))))
}

// blankRuns logs blank rows between the first and the last body row.
func (s *sheet) blankRuns(rg *Region, from, to, c1, c2 int) {
	for r := from; r <= to; r++ {
		if !s.blankIn(r, c1, c2) {
			continue
		}
		end := r
		for end+1 <= to && s.blankIn(end+1, c1, c2) {
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

// totals classifies body rows as totals or data. A row is a totals
// candidate when one of its leading text cells (those before its first
// number) matches the totals pattern. It is dropped when a number in it
// equals a sum of the rows above, or when the label is its only leading
// text; otherwise ("Total Wine & More | Catering | 340") it is kept and
// logged.
func (s *sheet) totals(rg *Region, body []int, c1, c2 int, keep bool) {
	var sinceLast, all, subtotals []int
	data := func(r int) {
		rg.rowRole[r] = grid.RoleData
		rg.Data = append(rg.Data, r)
		sinceLast = append(sinceLast, r)
		all = append(all, r)
	}
	for _, r := range body {
		lead := s.leadingText(r, c1, c2)
		label, col, strong := "", -1, false
		for _, c := range lead {
			t := labelText(s.g, s.g.At(r, c))
			if totalRe.MatchString(t) {
				label, col, strong = t, c, true
				break
			}
			if totalTailRe.MatchString(t) {
				label, col = t, c
				break
			}
		}
		if col < 0 {
			data(r)
			continue
		}
		if !s.hasNumber(r, c1, c2, col) {
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KKeepTotalRow, s.g.Sheet,
				grid.RowRange(r, c1, c2), 0.3, fmt.Sprintf("Row %d starts with %s but has no numbers; kept as data.", r+1, quote(label, 40))))
			data(r)
			continue
		}
		proof := s.sumProof(r, c1, c2, col, sinceLast, all, subtotals)
		if proof == "" && len(lead) > 1 {
			var others []string
			for _, c := range lead {
				if c != col {
					others = append(others, fmt.Sprintf("%s %s", grid.CellRef(r, c), quote(labelText(s.g, s.g.At(r, c)), 30)))
				}
			}
			rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KKeepTotalRow, s.g.Sheet,
				grid.RowRange(r, c1, c2), 0.3, fmt.Sprintf("Row %d has %s, which looks like a total label, but other cells hold text (%s) and no number equals a sum of the rows above; kept as data.",
					r+1, quote(label, 40), strings.Join(others, ", "))))
			data(r)
			continue
		}
		conf := 0.8
		if !strong {
			conf = 0.6
		}
		why := fmt.Sprintf("Row %d is labeled %s", r+1, quote(label, 40))
		if proof != "" {
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

// leadingText returns the columns of the row's own text cells that come
// before its first number (the row's labels).
func (s *sheet) leadingText(r, c1, c2 int) []int {
	var out []int
	for c := c1; c <= c2; c++ {
		if s.g.At(r, c).IsEmpty() {
			continue
		}
		switch s.classOf(r, c) {
		case clText:
			out = append(out, c)
		case clNum, clDate, clBool:
			return out
		}
	}
	return out
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
			if len(cand.rows) == 0 || (len(cand.rows) < 2 && cand.what != "its group") {
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
	if rg.group >= 0 {
		gl = s.groupRow(rg.group, rg.group+1, c1, c2)
	}
	for c := c1; c <= c2; c++ {
		var parts []string
		var prev *grid.Cell
		for _, h := range rg.Header {
			var text string
			cell := s.g.Value(h, c)
			if gl != nil && h == rg.group {
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
	switch {
	case gl != nil:
		var spans []string
		for _, sp := range gl.spans {
			spans = append(spans, fmt.Sprintf("%s over %s", quote(gl.labels[max(sp.C1, c1)], 30), colSpanText(sp.C1, sp.C2)))
		}
		a := rg.group
		conf, how := 0.9, "merged group labels"
		if !gl.merged {
			conf, how = 0.7, "sparse group labels"
		}
		rg.Decisions = append(rg.Decisions, grid.NewDecision(grid.KHeaderMerge, s.g.Sheet,
			grid.Range{R1: a, C1: c1, R2: a + 1, C2: c2}, conf,
			fmt.Sprintf("Rows %d–%d form a two-row header: %s in row %d (%s) are combined with the labels below as group_sub.",
				a+1, a+2, how, a+1, strings.Join(spans, ", "))))
	case len(rg.Header) > 2:
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
