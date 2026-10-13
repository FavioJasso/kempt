package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/grid"
)

func rawOf(t *testing.T, g *grid.Grid, maxRows, maxCols int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Raw(&buf, g, maxRows, maxCols); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

const legend = `Legend: # number  @ date  ? bool  ƒ formula (cached value)  ! error  (text is unmarked)
        ⟵ merged into the cell to the left  ↑ merged into the cell above
`

func TestRaw(t *testing.T) {
	want := `Sheet "Budget Summary": A1:C6, 6 rows × 3 columns, source xlsx
` + legend + `
row | A                  | B              | C
----+--------------------+----------------+----------
  1 | City of Riverton…  | ⟵              | ⟵
  2 | Department         | FY2025 ($000s) | Date
  3 | Parks & Recreation | 2450 #         | 46096 @
  4 | ↑                  | (1,234)        | #DIV/0! !
  5 | Total              | 1216 ƒ         | TRUE ?
  6 | Source: Finance D… |                |

Merges (2): A1:C1, A3:A4
Formulas (1):
  B5  =SUM(B3:B4)
`
	out := rawOf(t, sampleGrid(), 0, 0)
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	checkLines(t, out)
}

func TestRawLimits(t *testing.T) {
	tests := []struct {
		rows, cols  int
		want, avoid []string
	}{
		{0, 0, []string{"  6 | Source"}, []string{"showing", "more"}},
		{-1, 99, []string{"  6 | Source", "| C\n"}, []string{"showing"}},
		{6, 3, []string{"A1:C6, 6 rows × 3 columns, source xlsx\n"}, []string{"showing"}},
		{3, 2, []string{"; showing A1:B3\n", "… 3 more rows (to row 6)\n", "… 1 more column (to column C)\n", "Merges (2)"},
			[]string{"  4 |", " | C\n", "Formulas"}},
		{5, 0, []string{"… 1 more row (to row 6)\n", "  B5  =SUM(B3:B4)"}, []string{"  6 |"}},
		{1, 1, []string{"row | A\n----+------------------\n  1 | City of Riverton…\n"}, []string{"  2 |"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%dx%d", tc.rows, tc.cols), func(t *testing.T) {
			out := rawOf(t, sampleGrid(), tc.rows, tc.cols)
			for _, s := range tc.want {
				if !strings.Contains(out, s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			for _, s := range tc.avoid {
				if strings.Contains(out, s) {
					t.Errorf("output contains %q:\n%s", s, out)
				}
			}
		})
	}
}

func TestRawEmptyAndCSV(t *testing.T) {
	empty := grid.New("Blank", 0, 0)
	empty.Source = "csv"
	empty.Delimiter = '\t'
	want := "Sheet \"Blank\": empty, source csv (delimiter '\\t')\n" + legend + "\n(no cells)\n\nMerges: none\n"
	if out := rawOf(t, empty, 0, 0); out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if out := rawOf(t, nil, 0, 0); out != "(no grid)\n" {
		t.Errorf("nil grid: %q", out)
	}

	g := grid.New("tidy", 2, 2)
	g.Source, g.Delimiter, g.Date1904 = "csv", ';', true
	g.Cells[0][0] = grid.Cell{Raw: "id", Kind: grid.String}
	g.Cells[1][1] = grid.Cell{Raw: " padded ", Kind: grid.String}
	out := rawOf(t, g, 0, 0)
	if !strings.HasPrefix(out, "Sheet \"tidy\": A1:B2, 2 rows × 2 columns, source csv (delimiter ';'), 1904 date system\n") {
		t.Errorf("header: %s", out)
	}
	if !strings.Contains(out, "\n  2 |    |  padded\n") {
		t.Errorf("rows: %s", out)
	}
}

func TestRawCell(t *testing.T) {
	g := grid.New("S", 3, 3)
	tests := []struct {
		cell grid.Cell
		want string
	}{
		{grid.Cell{}, ""},
		{grid.Cell{Raw: "   ", Kind: grid.String}, ""},
		{grid.Cell{Raw: "Revenue", Kind: grid.String}, "Revenue"},
		{grid.Cell{Raw: "2450", Kind: grid.Number, Num: num(2450)}, "2450 #"},
		{grid.Cell{Kind: grid.Number, Num: num(0.5)}, "0.5 #"},
		{grid.Cell{Raw: "46100", Kind: grid.Date, Num: num(46100), DateFmt: true}, "46100 @"},
		{grid.Cell{Raw: "2026-03-15", Kind: grid.Date, DateFmt: true}, "2026-03-15 @"},
		{grid.Cell{Raw: "FALSE", Kind: grid.Bool}, "FALSE ?"},
		{grid.Cell{Raw: "#N/A", Kind: grid.Error}, "#N/A !"},
		{grid.Cell{Raw: "3570", Kind: grid.Formula, Num: num(3570), Formula: "SUM(C6:C7)"}, "3570 ƒ"},
		{grid.Cell{Raw: "Grand total for all departments", Kind: grid.Formula, Formula: "A1&B1"}, "Grand total for a… ƒ"},
		{grid.Cell{Raw: "two\nlines", Kind: grid.String}, "two↵lines"},
		{grid.Cell{Raw: "12345678901234567890", Kind: grid.Number, Num: num(1)}, "12345678901234567… #"},
	}
	for _, tc := range tests {
		g.Cells[1][1] = tc.cell
		if got := rawCell(g, 1, 1); got != tc.want {
			t.Errorf("rawCell(%+v) = %q, want %q", tc.cell, got, tc.want)
		}
	}

	m := grid.New("M", 3, 3)
	m.Cells[0][0] = grid.Cell{Raw: "Block", Kind: grid.String}
	m.AddMerge(grid.Range{R1: 0, C1: 0, R2: 1, C2: 1})
	for _, tc := range []struct {
		r, c int
		want string
	}{{0, 0, "Block"}, {0, 1, "⟵"}, {1, 0, "↑"}, {1, 1, "↑"}, {2, 2, ""}} {
		if got := rawCell(m, tc.r, tc.c); got != tc.want {
			t.Errorf("merge cell %s = %q, want %q", grid.CellRef(tc.r, tc.c), got, tc.want)
		}
	}
}

func TestRawBlocks(t *testing.T) {
	g := grid.New("Wide", 2, 12)
	g.Source = "xlsx"
	for c := range 12 {
		g.Cells[0][c] = grid.Cell{Raw: fmt.Sprintf("Header number %02d long", c), Kind: grid.String}
		g.Cells[1][c] = grid.Cell{Raw: "1", Kind: grid.Number, Num: num(1)}
	}
	out := rawOf(t, g, 0, 0)
	checkLines(t, out)
	if n := strings.Count(out, "\nrow | "); n != 3 {
		t.Errorf("want 3 column blocks, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "\nrow | I                 | J                 | K                 | L\n") {
		t.Errorf("last block:\n%s", out)
	}
}

func TestRawFormulaCap(t *testing.T) {
	g := grid.New("F", rawFormulas+5, 1)
	for r := range g.Rows {
		g.Cells[r][0] = grid.Cell{Raw: "1", Kind: grid.Formula, Num: num(1), Formula: "1+0"}
	}
	out := rawOf(t, g, 0, 0)
	if !strings.Contains(out, fmt.Sprintf("Formulas (%d):\n", rawFormulas+5)) || !strings.HasSuffix(out, "  … and 5 more\n") {
		t.Errorf("formula list:\n%s", out)
	}
}
