package report

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// TextOptions controls the human-readable report.
type TextOptions struct {
	PreviewRows int // rows previewed per table; 0 means 10, negative disables the preview
}

const (
	lineWidth   = 100 // target terminal width
	minLast     = 30  // the last table column never gets narrower than this
	previewCell = 24  // longest preview value or column name before truncation
	nullMark    = "∅"
	indent      = "  "
)

// Text writes the human-readable inspect output (`kempt inspect`): per sheet
// its decisions, and per table its columns, decisions, and a row preview.
// Lines stay within 100 columns.
func Text(w io.Writer, file string, sheets []*plan.Sheet, opts TextOptions) error {
	n := opts.PreviewRows
	if n == 0 {
		n = 10
	}
	bw := bufio.NewWriter(w)
	nSheets, nTables := 0, 0
	for _, sh := range sheets {
		if sh != nil {
			nSheets++
			nTables += len(sh.Tables)
		}
	}
	fmt.Fprintf(bw, "%s: %s, %s\n", baseName(file), plural(nSheets, "sheet"), plural(nTables, "table"))
	for _, sh := range sheets {
		if sh != nil {
			writeSheet(bw, sh, n)
		}
	}
	return bw.Flush()
}

func writeSheet(w io.Writer, sh *plan.Sheet, preview int) {
	info := plural(len(sh.Tables), "table")
	if sh.Grid != nil {
		info = cmp.Or(dimensions(sh.Grid), "empty") + ", " + info
	}
	fmt.Fprintf(w, "\n== Sheet %q (%s) ==\n", sh.Name, info)
	if len(sh.Decisions) > 0 {
		fmt.Fprintf(w, "\nSheet decisions (%d):\n", len(sh.Decisions))
		writeDecisions(w, sh.Name, sh.Decisions)
	}
	if len(sh.Tables) == 0 {
		fmt.Fprintln(w, "\nNo tables found on this sheet.")
	}
	for _, p := range sh.Tables {
		if p != nil {
			writeTablePlan(w, sh.Name, p, preview)
		}
	}
}

func writeTablePlan(w io.Writer, sheet string, p *plan.Plan, preview int) {
	fmt.Fprintf(w, "\n-- Table %s (region %s, %s, %s) --\n", p.Table, p.Region,
		plural(len(p.Rows), "row"), plural(len(p.Columns), "column"))

	fmt.Fprintf(w, "\nColumns (%d):\n", len(p.Columns))
	rows := [][]string{{"#", "NAME", "TYPE", "UNIT", "SOURCE", "HEADER"}}
	for i, c := range p.Columns {
		rows = append(rows, []string{strconv.Itoa(i + 1), c.Name, c.Type.String(), clean(c.Unit),
			strings.Join(c.SourceCols, ","), clean(c.Header)})
	}
	writeTable(w, rows, []bool{true}, false)

	if len(p.Decisions) == 0 {
		fmt.Fprintln(w, "\nDecisions: none")
	} else {
		fmt.Fprintf(w, "\nDecisions (%d):\n", len(p.Decisions))
		writeDecisions(w, sheet, p.Decisions)
	}
	if preview > 0 {
		writePreview(w, p, preview)
	}
}

// writeDecisions lists decisions in canonical order with ranges relative to
// the sheet and reasons wrapped to the line width.
func writeDecisions(w io.Writer, sheet string, ds []grid.Decision) {
	prefix := grid.QuoteSheet(sheet) + "!"
	rows := [][]string{{"RANGE", "KIND", "CONF", "REASON"}}
	for _, d := range decisionDocs(ds) {
		rows = append(rows, []string{strings.TrimPrefix(d.Range, prefix), d.Kind,
			strconv.FormatFloat(d.Confidence, 'f', 2, 64), clean(d.Reason)})
	}
	writeTable(w, rows, nil, true)
}

// writePreview prints the first n rows as aligned columns, split into
// blocks of columns that each fit the line width.
func writePreview(w io.Writer, p *plan.Plan, n int) {
	shown := min(n, len(p.Rows))
	what := fmt.Sprintf("first %d of %d rows", shown, len(p.Rows))
	if shown == len(p.Rows) {
		what = "all " + plural(shown, "row")
	}
	fmt.Fprintf(w, "\nPreview (%s; %s = NULL):\n", what, nullMark)
	if shown == 0 || len(p.Columns) == 0 {
		fmt.Fprintln(w, indent+"(no rows)")
		return
	}

	// Label rows by their sheet row when known, else by ordinal.
	hasSrc := len(p.SourceRows) == len(p.Rows)
	label := []string{"#"}
	if hasSrc {
		label[0] = "ROW"
	}
	for i := range shown {
		k := i + 1
		if hasSrc {
			k = p.SourceRows[i]
		}
		label = append(label, strconv.Itoa(k))
	}
	cols := make([][]string, len(p.Columns))
	for j, c := range p.Columns {
		cols[j] = []string{truncate(c.Name, previewCell)}
		for i := range shown {
			var v any
			if j < len(p.Rows[i]) {
				v = p.Rows[i][j]
			}
			cols[j] = append(cols[j], truncate(valueText(v), previewCell))
		}
	}

	for start := 0; start < len(cols); {
		width := len(indent) + colWidth(label)
		end := start
		for end < len(cols) && (end == start || width+2+colWidth(cols[end]) <= lineWidth) {
			width += 2 + colWidth(cols[end])
			end++
		}
		if start > 0 {
			fmt.Fprintln(w)
		}
		rows := make([][]string, shown+1)
		right := []bool{true}
		for i := range rows {
			rows[i] = []string{label[i]}
		}
		for j := start; j < end; j++ {
			right = append(right, numeric(p.Columns[j].Type.Base))
			for i := range rows {
				rows[i] = append(rows[i], cols[j][i])
			}
		}
		writeTable(w, rows, right, false)
		start = end
	}
}

func numeric(b infer.Base) bool {
	return b == infer.Int || b == infer.BigInt || b == infer.Decimal
}

// valueText renders a clean value for display; NULL becomes ∅.
func valueText(v any) string {
	switch v.(type) {
	case nil:
		return nullMark
	case bool, int64, normalize.Decimal, normalize.Date, normalize.Timestamp, string:
		return clean(normalize.Format(v))
	}
	return clean(fmt.Sprint(v))
}

// writeTable writes rows (the first is the header) as left-aligned columns
// separated by two spaces and indented. right marks right-aligned columns.
// The last column is wrapped (wrapLast) or truncated to keep each line
// within lineWidth.
func writeTable(w io.Writer, rows [][]string, right []bool, wrapLast bool) {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return
	}
	n := len(rows[0])
	widths := make([]int, n)
	for _, row := range rows {
		for i := 0; i < n && i < len(row); i++ {
			widths[i] = max(widths[i], runeLen(row[i]))
		}
	}
	prefix := len(indent)
	for _, wd := range widths[:n-1] {
		prefix += wd + 2
	}
	avail := max(lineWidth-prefix, minLast)
	widths[n-1] = min(widths[n-1], avail)

	var b strings.Builder
	for _, row := range rows {
		cell := func(i int) string {
			if i < len(row) {
				return row[i]
			}
			return ""
		}
		last := []string{truncate(cell(n-1), avail)}
		if wrapLast {
			last = wrap(cell(n-1), avail)
		}
		for k, part := range last {
			b.Reset()
			b.WriteString(indent)
			for i := range n {
				s := part
				if i < n-1 {
					s = ""
					if k == 0 {
						s = cell(i)
					}
				}
				pad := strings.Repeat(" ", max(widths[i]-runeLen(s), 0))
				if i < len(right) && right[i] {
					b.WriteString(pad + s)
				} else {
					b.WriteString(s + pad)
				}
				if i < n-1 {
					b.WriteString("  ")
				}
			}
			fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
		}
	}
}

// wrap splits s into lines of at most width runes at spaces, hard-breaking
// words that are longer than width.
func wrap(s string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		for runeLen(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			r := []rune(word)
			lines = append(lines, string(r[:width]))
			word = string(r[width:])
		}
		switch {
		case line == "":
			line = word
		case runeLen(line)+1+runeLen(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// truncate shortens s to at most n runes, marking the cut with "…".
func truncate(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	if n == 1 {
		return "…"
	}
	return strings.TrimRight(string([]rune(s)[:n-1]), " ") + "…"
}

var controlChars = strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵", "\t", " ")

// clean makes s safe for a single aligned line.
func clean(s string) string { return controlChars.Replace(s) }

func colWidth(col []string) int {
	w := 0
	for _, s := range col {
		w = max(w, runeLen(s))
	}
	return w
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// plural renders a count with its noun: "1 row", "3 rows".
func plural(n int, noun string) string { return strconv.Itoa(n) + " " + pluralNoun(n, noun) }

func pluralNoun(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}
