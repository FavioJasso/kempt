package plan

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/detect"
	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
)

// mk builds a grid: "" is empty, strings are text, ints/floats are numbers.
func mk(t *testing.T, sheet string, rows [][]any, merges ...string) *grid.Grid {
	t.Helper()
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	g := grid.New(sheet, len(rows), cols)
	for r, row := range rows {
		for c, v := range row {
			cell := &g.Cells[r][c]
			switch x := v.(type) {
			case string:
				if x != "" {
					cell.Kind, cell.Raw = grid.String, x
				}
			case int:
				f := float64(x)
				cell.Kind, cell.Num, cell.Raw = grid.Number, &f, strconv.Itoa(x)
			case float64:
				f := x
				cell.Kind, cell.Num, cell.Raw = grid.Number, &f, strconv.FormatFloat(x, 'f', -1, 64)
			}
		}
	}
	for _, m := range merges {
		rng, err := grid.ParseRange(m)
		if err != nil {
			t.Fatal(err)
		}
		g.AddMerge(rng)
	}
	return g
}

func names(p *Plan) []string {
	var out []string
	for _, c := range p.Columns {
		out = append(out, c.Name)
	}
	return out
}

func hasDecision(ds []grid.Decision, kind string) bool {
	for _, d := range ds {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

func TestBuildNamesUnitsAndTypes(t *testing.T) {
	g := mk(t, "Budget Summary", [][]any{
		{"Budget report"},
		{"Dept", "FY2025 ($000s)", "", "Change (%)", "Dept", ""},
		{"", "Budget", "Actual", "", "", ""},
		{"Parks", 10, "(3)", 1.5, "x", "a"},
		{"Roads", 20, 21, 2.25, "y", "b"},
		{"Total", 30, 18, 3.75, "", ""},
	}, "A2:A3", "B2:C2", "D2:D3", "E2:E3")
	sh := Build(g, Options{})
	if len(sh.Tables) != 1 {
		t.Fatalf("tables = %d", len(sh.Tables))
	}
	p := sh.Tables[0]
	if p.Table != "budget_summary" || p.Region != "A1:F6" {
		t.Fatalf("table %q region %q", p.Table, p.Region)
	}
	want := []string{"dept", "fy2025_budget", "fy2025_actual", "change_pct", "dept_2", "col_f"}
	if got := names(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("names\n got %v\nwant %v", got, want)
	}
	if p.Columns[1].Unit != "$000s" || p.Columns[3].Unit != "%" {
		t.Fatalf("units %q %q", p.Columns[1].Unit, p.Columns[3].Unit)
	}
	if p.Columns[1].Type.Base != infer.Int || p.Columns[2].Type.Base != infer.Int {
		t.Fatalf("types %v %v", p.Columns[1].Type, p.Columns[2].Type)
	}
	if got := p.Columns[3].Type; got.Base != infer.Decimal || got.Precision != 3 || got.Scale != 2 {
		t.Fatalf("change type %v", got)
	}
	if p.Columns[1].SQLType["postgres"] != "INTEGER" || p.Columns[3].SQLType["mysql"] != "DECIMAL(3,2)" {
		t.Fatalf("sql types %v %v", p.Columns[1].SQLType, p.Columns[3].SQLType)
	}
	if !reflect.DeepEqual(p.Rows[0], []any{"Parks", int64(10), int64(-3), normalize.Decimal("1.5"), "x", "a"}) {
		t.Fatalf("row 0 %#v", p.Rows[0])
	}
	if len(p.Rows) != 2 || !reflect.DeepEqual(p.SourceRows, []int{4, 5}) {
		t.Fatalf("rows %d source %v", len(p.Rows), p.SourceRows)
	}
	for _, k := range []string{grid.KStripUnit, grid.KColumnName, grid.KDropTotalRow, grid.KHeaderMerge, grid.KAccountingNeg} {
		if !hasDecision(p.Decisions, k) {
			t.Errorf("missing %s decision", k)
		}
	}
	if p.Columns[1].Comment != "FY2025 ($000s) / Budget [unit: $000s; values not rescaled]" {
		t.Fatalf("comment %q", p.Columns[1].Comment)
	}
	if sh.CellRoles["A1"] != grid.RoleTitle || sh.CellRoles["B3"] != grid.RoleHeader || sh.CellRoles["A6"] != grid.RoleTotal {
		t.Fatalf("roles %v", sh.CellRoles)
	}
	if !reflect.DeepEqual(p.CellRoles, sh.CellRoles) {
		t.Fatal("a single table's roles equal the sheet's")
	}
}

func TestBuildNullRolesAndFillDown(t *testing.T) {
	g := mk(t, "Inventory", [][]any{
		{"Category", "Item", "Qty"},
		{"Tools", "Saw", 1},
		{"", "Drill", "N/A"},
		{"", "Hammer", 3},
	}, "A2:A4")
	sh := Build(g, Options{})
	p := sh.Tables[0]
	for i, want := range []string{"Tools", "Tools", "Tools"} {
		if p.Rows[i][0] != want {
			t.Fatalf("row %d category %v", i, p.Rows[i][0])
		}
	}
	if p.Rows[1][2] != nil || sh.CellRoles["C3"] != grid.RoleNull {
		t.Fatalf("N/A → %v role %q", p.Rows[1][2], sh.CellRoles["C3"])
	}
	if sh.CellRoles["A3"] != grid.RoleData {
		t.Fatalf("filled cell role %q", sh.CellRoles["A3"])
	}
	if !hasDecision(p.Decisions, grid.KFillDown) || !hasDecision(p.Decisions, grid.KNullToken) {
		t.Fatalf("decisions %v", p.Decisions)
	}
}

func TestBuildMultipleTablesAndOverrides(t *testing.T) {
	g := mk(t, "Summary", [][]any{
		{"Region", "Q1"},
		{"North", 1},
		{"South", 2},
		{},
		{},
		{"Rep", "Deals"},
		{"Ann", 3},
		{"Bob", 4},
		{},
		{},
		{"stray note"},
	})
	sh := Build(g, Options{})
	if len(sh.Tables) != 2 || sh.Tables[0].Table != "summary_1" || sh.Tables[1].Table != "summary_2" {
		t.Fatalf("tables %v", sh.Tables)
	}
	if !hasDecision(sh.Decisions, grid.KSkipRegion) || sh.CellRoles["A11"] != grid.RoleIgnored {
		t.Fatalf("sheet decisions %v role %q", sh.Decisions, sh.CellRoles["A11"])
	}
	all := sh.AllDecisions()
	if len(all) != len(sh.Decisions)+len(sh.Tables[0].Decisions)+len(sh.Tables[1].Decisions) {
		t.Fatal("AllDecisions must merge every decision")
	}
	for i := 1; i < len(all); i++ {
		if grid.CompareDecisions(all[i-1], all[i]) > 0 {
			t.Fatal("AllDecisions is not sorted")
		}
	}

	sh = Build(g, Options{Table: "deals", RoleRows: 3})
	if sh.Tables[0].Table != "deals_1" {
		t.Fatalf("override name %q", sh.Tables[0].Table)
	}
	for ref := range sh.CellRoles {
		if r, _, _ := grid.ParseRef(ref); r >= 3 {
			t.Fatalf("RoleRows=3 but got role for %s", ref)
		}
	}
}

func TestBuildEmptySheet(t *testing.T) {
	sh := Build(grid.New("Empty", 0, 0), Options{})
	if len(sh.Tables) != 0 || sh.Tables == nil || sh.Decisions == nil || sh.CellRoles == nil {
		t.Fatalf("empty sheet %+v", sh)
	}
}

func TestDedupeAndTruncate(t *testing.T) {
	seen := map[string]bool{}
	long := "a_very_long_column_name_that_keeps_going_and_going_beyond_sixty_three_bytes"
	first := dedupe(long, seen)
	second := dedupe(long, seen)
	if len(first) > maxIdent || len(second) > maxIdent || first == second || second[len(second)-2:] != "_2" {
		t.Fatalf("%q %q", first, second)
	}
	if got := dedupe("x", map[string]bool{"x": true, "x_2": true}); got != "x_3" {
		t.Fatalf("got %q", got)
	}
}

func TestUniqueTables(t *testing.T) {
	a := &Sheet{Tables: []*Plan{{Table: "data"}}}
	b := &Sheet{Tables: []*Plan{{Table: "data"}, {Table: "data_2"}}}
	UniqueTables([]*Sheet{a, b})
	got := []string{a.Tables[0].Table, b.Tables[0].Table, b.Tables[1].Table}
	if got[0] == got[1] || got[1] == got[2] || got[0] == got[2] {
		t.Fatalf("names still collide: %v", got)
	}
}

func TestFillDownOptionReachesRows(t *testing.T) {
	g := mk(t, "S", [][]any{
		{"Group", "Item", "Qty"},
		{"A", "x", 1},
		{"", "y", 2},
		{"B", "z", 3},
	})
	sh := Build(g, Options{Detect: detect.Options{FillDown: []string{"group"}}})
	if sh.Tables[0].Rows[1][0] != "A" {
		t.Fatalf("fill-down value %v", sh.Tables[0].Rows[1][0])
	}
}

func TestDecisionsUseFinalNames(t *testing.T) {
	g := mk(t, "S", [][]any{
		{"Discount", "Discount"},
		{"12%", "N/A"},
		{"5%", "x"},
		{"N/A", "y"},
	})
	p := Build(g, Options{}).Tables[0]
	if got := names(p); !reflect.DeepEqual(got, []string{"discount_pct", "discount"}) {
		t.Fatalf("names %v", got)
	}
	for _, d := range p.Decisions {
		if d.Kind == grid.KNullToken && d.Range == "S!A4" && !strings.Contains(d.Reason, "column discount_pct ") {
			t.Fatalf("reason uses a stale name: %s", d.Reason)
		}
	}
}
