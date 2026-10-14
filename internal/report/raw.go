package report

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
)

const (
	rawCellMax  = 18 // longest raw value shown before truncation (kind mark excluded)
	rawFormulas = 50 // formulas listed after the dump
)

// kindMarks are the suffixes Raw appends to non-text cells.
var kindMarks = map[grid.Kind]string{
	grid.Number:  "#",
	grid.Date:    "@",
	grid.Bool:    "?",
	grid.Formula: "ƒ",
	grid.Error:   "!",
}

// Raw writes an aligned dump of the raw grid (`kempt inspect --raw`): a
// header line with the sheet name, dimensions, and source; a legend; one line
// per row under A1 column letters with non-text cells marked by kind (columns
// are split into blocks that fit 100 characters); then the merged ranges and
// formulas. maxRows and maxCols limit the dump; zero or
// negative means no limit.
func Raw(w io.Writer, g *grid.Grid, maxRows, maxCols int) error {
	bw := bufio.NewWriter(w)
	if g == nil {
		fmt.Fprintln(bw, "(no grid)")
		return bw.Flush()
	}
	rows, cols := g.Rows, g.Cols
	if maxRows > 0 {
		rows = min(rows, maxRows)
	}
	if maxCols > 0 {
		cols = min(cols, maxCols)
	}

	fmt.Fprintf(bw, "Sheet %q: %s\n", g.Sheet, rawSummary(g, rows, cols))
	fmt.Fprintln(bw, "Legend: # number  @ date  ? bool  ƒ formula (cached value)  ! error  (text is unmarked)")
	fmt.Fprintln(bw, "        ⟵ merged into the cell to the left  ↑ merged into the cell above")
	fmt.Fprintln(bw)

	if rows == 0 || cols == 0 {
		fmt.Fprintln(bw, "(no cells)")
	} else {
		writeRawGrid(bw, g, rows, cols)
	}

	fmt.Fprintln(bw)
	if len(g.Merges) == 0 {
		fmt.Fprintln(bw, "Merges: none")
	} else {
		refs := make([]string, len(g.Merges))
		for i, m := range g.Merges {
			refs[i] = m.A1()
		}
		lines := wrap(fmt.Sprintf("Merges (%d): %s", len(refs), strings.Join(refs, ", ")), lineWidth-len(indent))
		for i, l := range lines {
			if i > 0 {
				l = indent + l
			}
			fmt.Fprintln(bw, l)
		}
	}
	writeRawFormulas(bw, g, rows, cols)
	return bw.Flush()
}

func rawSummary(g *grid.Grid, rows, cols int) string {
	var b strings.Builder
	if g.Rows == 0 || g.Cols == 0 {
		b.WriteString("empty")
	} else {
		fmt.Fprintf(&b, "%s, %s × %s", dimensions(g), plural(g.Rows, "row"), plural(g.Cols, "column"))
	}
	b.WriteString(", source ")
	b.WriteString(cmp.Or(g.Source, "unknown"))
	if g.Delimiter != 0 {
		fmt.Fprintf(&b, " (delimiter %s)", strconv.QuoteRune(g.Delimiter))
	}
	if g.Date1904 {
		b.WriteString(", 1904 date system")
	}
	if (rows < g.Rows || cols < g.Cols) && rows > 0 && cols > 0 {
		fmt.Fprintf(&b, "; showing %s", grid.Range{R2: rows - 1, C2: cols - 1}.A1())
	}
	return b.String()
}

func writeRawGrid(w io.Writer, g *grid.Grid, rows, cols int) {
	rowW := max(len("row"), len(strconv.Itoa(rows)))
	widths := make([]int, cols)
	for c := range cols {
		widths[c] = len(grid.ColName(c))
		for r := range rows {
			widths[c] = max(widths[c], runeLen(rawCell(g, r, c)))
		}
	}

	// Split the columns into blocks that each fit the line width.
	for start := 0; start < cols; {
		end, width := start, rowW
		for end < cols && (end == start || width+3+widths[end] <= lineWidth) {
			width += 3 + widths[end]
			end++
		}
		if start > 0 {
			fmt.Fprintln(w)
		}
		var b strings.Builder
		line := func(first string, cell func(c int) string, sep string) {
			b.Reset()
			b.WriteString(strings.Repeat(" ", rowW-runeLen(first)) + first)
			for c := start; c < end; c++ {
				s := cell(c)
				b.WriteString(sep + s + strings.Repeat(" ", widths[c]-runeLen(s)))
			}
			fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
		}
		line("row", grid.ColName, " | ")
		line(strings.Repeat("-", rowW), func(c int) string { return strings.Repeat("-", widths[c]) }, "-+-")
		for r := range rows {
			line(strconv.Itoa(r+1), func(c int) string { return rawCell(g, r, c) }, " | ")
		}
		start = end
	}
	if n := g.Rows - rows; n > 0 {
		fmt.Fprintf(w, "… %d more %s (to row %d)\n", n, pluralNoun(n, "row"), g.Rows)
	}
	if n := g.Cols - cols; n > 0 {
		fmt.Fprintf(w, "… %d more %s (to column %s)\n", n, pluralNoun(n, "column"), grid.ColName(g.Cols-1))
	}
}

// rawCell renders one cell for the dump.
func rawCell(g *grid.Grid, r, c int) string {
	if r >= len(g.Cells) || c >= len(g.Cells[r]) {
		return "" // a ragged grid built by hand
	}
	cell := &g.Cells[r][c]
	if m := cell.Merge; m != nil && !cell.IsMergeAnchor(r, c) && cell.IsEmpty() {
		if r == m.R1 {
			return "⟵"
		}
		return "↑"
	}
	if cell.IsEmpty() {
		return ""
	}
	v := cell.Raw
	if strings.TrimSpace(v) == "" && cell.Num != nil {
		v = strconv.FormatFloat(*cell.Num, 'f', -1, 64)
	}
	v = truncate(clean(v), rawCellMax)
	if mark := kindMarks[cell.Kind]; mark != "" {
		v += " " + mark
	}
	return v
}

// writeRawFormulas lists the formulas inside the dumped area in row order.
func writeRawFormulas(w io.Writer, g *grid.Grid, rows, cols int) {
	var refs, texts []string
	total := 0
	for r := range min(rows, len(g.Cells)) {
		for c := range min(cols, len(g.Cells[r])) {
			if f := g.Cells[r][c].Formula; f != "" {
				total++
				if len(refs) < rawFormulas {
					refs = append(refs, grid.CellRef(r, c))
					texts = append(texts, "="+clean(f))
				}
			}
		}
	}
	if total == 0 {
		return
	}
	fmt.Fprintf(w, "Formulas (%d):\n", total)
	rw := colWidth(refs)
	for i, ref := range refs {
		fmt.Fprintf(w, "%s%-*s  %s\n", indent, rw, ref, truncate(texts[i], lineWidth-len(indent)-rw-2))
	}
	if total > len(refs) {
		fmt.Fprintf(w, "%s… and %d more\n", indent, total-len(refs))
	}
}
