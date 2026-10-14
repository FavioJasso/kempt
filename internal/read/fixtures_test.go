package read_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/read"
)

const fixtureDir = "../../testdata/fixtures"

// fixtureCell is an expected cell; a NaN num means Num must be nil.
type fixtureCell struct {
	ref     string
	kind    grid.Kind
	raw     string
	num     float64
	dateFmt bool
	formula string
}

var noNum = math.NaN()

func str(ref, raw string) fixtureCell { return fixtureCell{ref, grid.String, raw, noNum, false, ""} }

func num(ref string, v float64, raw string) fixtureCell {
	return fixtureCell{ref, grid.Number, raw, v, false, ""}
}

func sum(ref string, v float64, raw, formula string) fixtureCell {
	return fixtureCell{ref, grid.Formula, raw, v, false, formula}
}

var fixtures = map[string]struct {
	sheet      string
	rows, cols int
	delim      rune
	merges     []string
	blankRows  []int // 1-based
	cells      []fixtureCell
}{
	"budget_report.xlsx": {
		sheet: "Budget Summary", rows: 19, cols: 8,
		merges:    []string{"A1:H1", "A4:A5", "B4:B5", "C4:D4", "E4:F4", "G4:G5", "H4:H5"},
		blankRows: []int{3, 17},
		cells: []fixtureCell{
			str("A1", "City of Riverton — Departmental Budget Report"),
			{"B1", grid.Empty, "", noNum, false, ""},
			str("A2", "Fiscal years 2025 and 2026, all funds"),
			str("A4", "Department"), str("B4", "Program"), str("C4", "FY2025 ($000s)"),
			str("E4", "FY2026 ($000s)"), str("G4", "Change (%)"), str("H4", "Notes"),
			str("C5", "Budget"), str("D5", "Actual"), str("E5", "Budget"), str("F5", "Actual"),
			str("A6", "Public Works"), num("C6", 2450, "2450"), num("F6", 1212, "1212"), num("G6", 6.1, "6.1"),
			str("H6", "Paving backlog"),
			sum("C8", 3570, "3570", "SUM(C6:C7)"), sum("F8", 1816, "1816", "SUM(F6:F7)"), num("G8", 5.9, "5.9"),
			str("C11", "(1,234)"), str("D11", "(1,180)"), str("E11", "(1,300)"), str("F11", "(655)"), str("H11", "—"),
			str("F14", "N/A"), str("H14", "New station opens July*"),
			sum("C16", 14396, "14396", "C8+C12+C15"), sum("F16", 5419, "5419", "F8+F12+F15"),
			str("A18", "Source: Riverton Finance Department, FY2026 Adopted Budget."),
			str("A19", "* FY2026 actuals are year-to-date through March."),
		},
	},
	"two_tables.xlsx": {
		sheet: "Summary", rows: 15, cols: 5,
		blankRows: []int{6, 7, 13, 14},
		cells: []fixtureCell{
			str("A1", "Region"), str("E1", "Q4 Sales"), str("A2", "North"), num("B2", 120500, "120500"),
			num("E5", 97800, "97800"), str("A8", "Top sales reps — 2026"), str("C9", "Deals Closed"),
			str("A10", "Avery Chen"), num("C10", 42, "42"), num("C12", 35, "35"),
			str("A15", "Last refreshed 2026-03-31"),
		},
	},
	"merged_categories.xlsx": {
		sheet: "Inventory", rows: 13, cols: 5,
		merges: []string{"A2:A5", "A6:A8", "A9:A13"},
		cells: []fixtureCell{
			str("A1", "Category"), str("E1", "Unit Price"),
			str("A2", "Electronics"), {"A3", grid.Empty, "", noNum, false, ""}, str("A6", "Furniture"), str("A9", "Office Supplies"),
			str("C2", "EL-1001"), str("C10", "00731"), num("D2", 45, "45"),
			num("E2", 24.5, "24.5"), num("E3", 129.99, "129.99"), num("E6", 899, "899"),
		},
	},
	"messy_values.xlsx": {
		sheet: "Orders", rows: 13, cols: 11,
		cells: []fixtureCell{
			str("A1", "Order ID"), str("I1", "Invoice Date"), str("K1", "Region Code"),
			num("A2", 1001, "1001"),
			{"B2", grid.Date, "46083", 46083, true, ""},
			{"B13", grid.Date, "46094", 46094, true, ""},
			str("C2", "03/15/2026"), str("C3", "3/16/26"), str("C4", "Mar 17, 2026"), str("C5", "18-Mar-2026"), str("C12", "N/A"),
			str("E2", "$1,200.50"), str("E5", "—"), str("E8", "75.00-"), str("E10", "(120.00)"),
			str("F2", "12%"), str("F5", "N/A"), str("F11", "12.5%"),
			num("G2", 0.32, "0.32"), str("G5", "#DIV/0!"), num("G8", -0.05, "-0.05"),
			num("H2", 10, "10"), str("H8", "see note"),
			num("I2", 46100, "46100"), num("I13", 46111, "46111"),
			{"J2", grid.Bool, "TRUE", noNum, false, ""}, {"J4", grid.Bool, "FALSE", noNum, false, ""},
			str("K6", "NA"),
		},
	},
	"ambiguous_dates.csv": {
		sheet: "ambiguous_dates", rows: 9, cols: 4, delim: ';',
		cells: []fixtureCell{
			str("A1", "id"), str("B1", "event"), str("C1", "event_date"), str("D1", "amount"),
			str("C2", "03/04/2026"), str("D2", "12.50"), str("C9", "12/11/2026"), str("D9", "220.40"),
		},
	},
	"tidy.csv": {
		sheet: "tidy", rows: 11, cols: 8, delim: ',',
		cells: []fixtureCell{
			str("A1", "ID"), str("E1", "Signup Date"), str("G1", "Monthly Spend"), str("H1", "Active"),
			str("A2", "1"), str("E2", "2026-01-15"), str("G2", "49"), str("G5", "19.5"), str("H2", "true"),
			str("A11", "10"), str("H11", "false"),
		},
	},
}

func TestFixtures(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		seen[name] = true
		want, ok := fixtures[name]
		if !ok {
			t.Errorf("%s: no expectations; add it to the fixtures table", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			gs, err := read.File(filepath.Join(fixtureDir, name), read.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(gs) != 1 {
				t.Fatalf("got %d grids, want 1", len(gs))
			}
			g := gs[0]
			source := strings.TrimPrefix(filepath.Ext(name), ".")
			if g.Sheet != want.sheet || g.Rows != want.rows || g.Cols != want.cols || g.Source != source ||
				g.Delimiter != want.delim || g.Date1904 {
				t.Errorf("grid = %q %dx%d source %q delim %q 1904=%v, want %q %dx%d source %q delim %q",
					g.Sheet, g.Rows, g.Cols, g.Source, g.Delimiter, g.Date1904,
					want.sheet, want.rows, want.cols, source, want.delim)
			}
			var merges []string
			for _, m := range g.Merges {
				merges = append(merges, m.A1())
			}
			if fmt.Sprint(merges) != fmt.Sprint(want.merges) {
				t.Errorf("merges = %v, want %v", merges, want.merges)
			}
			for _, m := range g.Merges {
				for r := m.R1; r <= m.R2; r++ {
					for c := m.C1; c <= m.C2; c++ {
						if got := g.At(r, c).Merge; got == nil || *got != m {
							t.Errorf("%s merge = %v, want %v", grid.CellRef(r, c), got, m)
						}
					}
				}
			}
			for _, r := range want.blankRows {
				if r > g.Rows || !g.RowEmpty(r-1) {
					t.Errorf("row %d is not blank", r)
				}
			}
			for _, wc := range want.cells {
				checkFixtureCell(t, g, wc)
			}
			if g.Source == "csv" {
				checkCSVCells(t, g)
			}
		})
	}
	var missing []string
	for name := range fixtures {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("fixtures missing from %s: %v (run go run ./testdata/gen)", fixtureDir, missing)
	}
}

func checkFixtureCell(t *testing.T, g *grid.Grid, wc fixtureCell) {
	t.Helper()
	r, c, err := grid.ParseRef(wc.ref)
	if err != nil {
		t.Fatal(err)
	}
	cell := g.At(r, c)
	if cell == nil {
		t.Errorf("%s is outside the %dx%d grid", wc.ref, g.Rows, g.Cols)
		return
	}
	if cell.Kind != wc.kind || cell.Raw != wc.raw || cell.DateFmt != wc.dateFmt || cell.Formula != wc.formula {
		t.Errorf("%s = {%v %q dateFmt=%v formula=%q}, want {%v %q dateFmt=%v formula=%q}",
			wc.ref, cell.Kind, cell.Raw, cell.DateFmt, cell.Formula, wc.kind, wc.raw, wc.dateFmt, wc.formula)
	}
	switch {
	case math.IsNaN(wc.num) && cell.Num != nil:
		t.Errorf("%s Num = %v, want nil", wc.ref, *cell.Num)
	case !math.IsNaN(wc.num) && (cell.Num == nil || *cell.Num != wc.num):
		t.Errorf("%s Num = %v, want %v", wc.ref, cell.Num, wc.num)
	}
}

// checkCSVCells asserts the CSV reader's invariants on a whole grid: every
// field is a String (or Empty) without BOM or carriage-return residue.
func checkCSVCells(t *testing.T, g *grid.Grid) {
	t.Helper()
	for r := range g.Rows {
		for c := range g.Cols {
			cell := g.Cells[r][c]
			if cell.Kind != grid.String || cell.Num != nil || cell.DateFmt || cell.Formula != "" {
				t.Errorf("%s = %+v, want a plain String cell", grid.CellRef(r, c), cell)
			}
			if strings.ContainsAny(cell.Raw, "\ufeff\r\n") || strings.TrimSpace(cell.Raw) != cell.Raw {
				t.Errorf("%s raw %q has BOM, line-ending, or padding residue", grid.CellRef(r, c), cell.Raw)
			}
		}
	}
}

// TestFixtureColumns pins whole-column kinds the downstream inference relies
// on: messy_values B is all date-formatted serials and I is all plain numbers.
func TestFixtureColumns(t *testing.T) {
	gs, err := read.File(filepath.Join(fixtureDir, "messy_values.xlsx"), read.Options{})
	if err != nil {
		t.Fatal(err)
	}
	g := gs[0]
	tests := []struct {
		col     int
		kind    grid.Kind
		dateFmt bool
	}{
		{0, grid.Number, false}, // Order ID
		{1, grid.Date, true},    // Order Date
		{8, grid.Number, false}, // Invoice Date: serials without a date format
		{9, grid.Bool, false},   // Paid
	}
	for _, tt := range tests {
		for r := 1; r < g.Rows; r++ {
			cell := g.Cells[r][tt.col]
			if cell.Kind != tt.kind || cell.DateFmt != tt.dateFmt || (tt.kind != grid.Bool) != (cell.Num != nil) {
				t.Errorf("%s = %v dateFmt=%v num=%v, want %v dateFmt=%v",
					grid.CellRef(r, tt.col), cell.Kind, cell.DateFmt, cell.Num, tt.kind, tt.dateFmt)
			}
		}
	}
}
