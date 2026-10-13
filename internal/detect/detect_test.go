package detect

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/grid"
)

// mk builds a grid from rows of values: "" is empty, other strings are text,
// int and float64 are numbers, bool is a boolean. merges are A1 ranges.
func mk(t *testing.T, rows [][]any, merges ...string) *grid.Grid {
	t.Helper()
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	g := grid.New("Sheet1", len(rows), cols)
	for r, row := range rows {
		for c, v := range row {
			cell := &g.Cells[r][c]
			switch x := v.(type) {
			case nil:
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
			case bool:
				cell.Kind, cell.Raw = grid.Bool, strings.ToUpper(strconv.FormatBool(x))
			default:
				t.Fatalf("unsupported value %T", v)
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

func kinds(ds []grid.Decision) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Kind)
	}
	return out
}

func find(ds []grid.Decision, kind string) *grid.Decision {
	for i := range ds {
		if ds[i].Kind == kind {
			return &ds[i]
		}
	}
	return nil
}

func plus1(rows []int) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r + 1
	}
	return out
}

func parts(rg Region) []string {
	var out []string
	for _, c := range rg.Cols {
		out = append(out, strings.Join(c.Parts, "|"))
	}
	return out
}

func TestSimpleTable(t *testing.T) {
	g := mk(t, [][]any{
		{"Name", "City", "Score"},
		{"Ann", "Oslo", 3},
		{"Bob", "Rome", 5},
		{"Cy", "Lima", 8},
	})
	res := Detect(g, Options{})
	if len(res.Regions) != 1 {
		t.Fatalf("regions = %d", len(res.Regions))
	}
	rg := res.Regions[0]
	if !reflect.DeepEqual(rg.Header, []int{0}) || !reflect.DeepEqual(plus1(rg.Data), []int{2, 3, 4}) {
		t.Fatalf("header %v data %v", rg.Header, plus1(rg.Data))
	}
	if got := parts(rg); !reflect.DeepEqual(got, []string{"Name", "City", "Score"}) {
		t.Fatalf("parts %v", got)
	}
	if !reflect.DeepEqual(kinds(rg.Decisions), []string{grid.KHeaderRow}) {
		t.Fatalf("decisions %v", kinds(rg.Decisions))
	}
	if r := res.CellRole(g, 0, 0); r != grid.RoleHeader {
		t.Fatalf("A1 role %q", r)
	}
	if r := res.CellRole(g, 3, 2); r != grid.RoleData {
		t.Fatalf("C4 role %q", r)
	}
}

func TestTitlesAndTwoRowHeader(t *testing.T) {
	g := mk(t, [][]any{
		{"City Budget Report", "", "", "", "", ""},
		{"All funds", "", "", "", "", ""},
		{},
		{"Dept", "FY2025 ($000s)", "", "FY2026 ($000s)", "", "Notes"},
		{"", "Budget", "Actual", "Budget", "Actual", ""},
		{"Parks", 10, 11, 12, 13, "x"},
		{"Roads", 20, 21, 22, 23, ""},
		{"Police", 30, 31, 32, 33, "y"},
	}, "A1:F1", "A4:A5", "B4:C4", "D4:E4", "F4:F5")
	res := Detect(g, Options{})
	if len(res.Regions) != 1 {
		t.Fatalf("regions = %d", len(res.Regions))
	}
	rg := res.Regions[0]
	if !reflect.DeepEqual(plus1(rg.Header), []int{4, 5}) {
		t.Fatalf("header rows %v", plus1(rg.Header))
	}
	want := []string{"Dept", "FY2025 ($000s)|Budget", "FY2025 ($000s)|Actual", "FY2026 ($000s)|Budget", "FY2026 ($000s)|Actual", "Notes"}
	if got := parts(rg); !reflect.DeepEqual(got, want) {
		t.Fatalf("parts\n got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(plus1(rg.Data), []int{6, 7, 8}) {
		t.Fatalf("data %v", plus1(rg.Data))
	}
	titles := 0
	for _, d := range rg.Decisions {
		if d.Kind == grid.KSkipTitleRow {
			titles++
		}
	}
	if titles != 2 || find(rg.Decisions, grid.KHeaderMerge) == nil {
		t.Fatalf("decisions %v", kinds(rg.Decisions))
	}
	if d := find(rg.Decisions, grid.KSkipTitleRow); d.Confidence != 0.95 || !strings.Contains(d.Reason, "merged across A:F") {
		t.Fatalf("title decision %+v", d)
	}
	if r := res.CellRole(g, 0, 3); r != grid.RoleTitle {
		t.Fatalf("D1 role %q", r)
	}
	if r := res.CellRole(g, 2, 0); r != grid.RoleBlank {
		t.Fatalf("A3 role %q", r)
	}
}

func TestSparseGroupRow(t *testing.T) {
	g := mk(t, [][]any{
		{"", "Q1", "", "Q2", "", ""},
		{"Region", "Revenue", "Cost", "Revenue", "Cost", "Owner"},
		{"North", 1, 2, 3, 4, "Ann"},
		{"South", 5, 6, 7, 8, "Bob"},
	})
	rg := Detect(g, Options{}).Regions[0]
	want := []string{"Region", "Q1|Revenue", "Q1|Cost", "Q2|Revenue", "Q2|Cost", "Owner"}
	if got := parts(rg); !reflect.DeepEqual(got, want) {
		t.Fatalf("parts\n got %v\nwant %v", got, want)
	}
	if d := find(rg.Decisions, grid.KHeaderMerge); d == nil || d.Confidence != 0.7 {
		t.Fatalf("header_merge %+v", d)
	}
}

func TestTitleAboveHeaderIsNotAGroup(t *testing.T) {
	g := mk(t, [][]any{
		{"Inventory list", "", ""},
		{"Item", "Qty", "Price"},
		{"Pen", 3, 1.5},
		{"Cup", 4, 2.25},
	})
	rg := Detect(g, Options{}).Regions[0]
	if got := parts(rg); !reflect.DeepEqual(got, []string{"Item", "Qty", "Price"}) {
		t.Fatalf("parts %v", got)
	}
	if find(rg.Decisions, grid.KSkipTitleRow) == nil {
		t.Fatalf("decisions %v", kinds(rg.Decisions))
	}
}

func TestTotalsAndFooters(t *testing.T) {
	rows := [][]any{
		{"Dept", "Program", "Amount"},
		{"Works", "Streets", 10},
		{"Works", "Fleet", 5},
		{"Total Works", "", 15},
		{"Parks", "Trails", "(3)"},
		{"Parks", "Pools", 8},
		{"Subtotal", "", 5},
		{"Grand Total", "", 20},
		{},
		{"Source: Finance"},
		{"* preliminary"},
	}
	g := mk(t, rows)
	res := Detect(g, Options{})
	rg := res.Regions[0]
	if !reflect.DeepEqual(plus1(rg.Data), []int{2, 3, 5, 6}) {
		t.Fatalf("data %v", plus1(rg.Data))
	}
	if !reflect.DeepEqual(plus1(rg.Totals), []int{4, 7, 8}) {
		t.Fatalf("totals %v", plus1(rg.Totals))
	}
	n := 0
	for _, d := range rg.Decisions {
		if d.Kind == grid.KDropTotalRow {
			n++
			if d.Confidence < 0.95 {
				t.Errorf("total %s confidence %v: %s", d.Range, d.Confidence, d.Reason)
			}
		}
	}
	if n != 3 {
		t.Fatalf("drop_total_row = %d (%v)", n, kinds(rg.Decisions))
	}
	footers := 0
	for _, d := range rg.Decisions {
		if d.Kind == grid.KSkipFooter {
			footers++
		}
	}
	if footers != 2 {
		t.Fatalf("footers = %d", footers)
	}
	if r := res.CellRole(g, 9, 0); r != grid.RoleFooter {
		t.Fatalf("A10 role %q", r)
	}
	if r := res.CellRole(g, 3, 2); r != grid.RoleTotal {
		t.Fatalf("C4 role %q", r)
	}

	kept := Detect(g, Options{KeepTotals: true}).Regions[0]
	if !reflect.DeepEqual(plus1(kept.Data), []int{2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("keep-totals data %v", plus1(kept.Data))
	}
	if find(kept.Decisions, grid.KKeepTotalRow) == nil || find(kept.Decisions, grid.KDropTotalRow) != nil {
		t.Fatalf("keep-totals decisions %v", kinds(kept.Decisions))
	}
}

func TestTotalWithoutNumbersIsKept(t *testing.T) {
	g := mk(t, [][]any{
		{"Course", "Teacher"},
		{"Algebra", "Ann"},
		{"Total Quality Management", "Bob"},
		{"Biology", "Cy"},
	})
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Data) != 3 || len(rg.Totals) != 0 {
		t.Fatalf("data %v totals %v", rg.Data, rg.Totals)
	}
	if d := find(rg.Decisions, grid.KKeepTotalRow); d == nil || d.Confidence > 0.5 {
		t.Fatalf("decision %+v", d)
	}
}

func TestTrailingNoteKeptAsPossibleFooter(t *testing.T) {
	g := mk(t, [][]any{
		{"Name", "Score"},
		{"Ann", 1},
		{"Bob", 2},
		{"Last updated March"},
	})
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Data) != 3 {
		t.Fatalf("data %v", plus1(rg.Data))
	}
	if d := find(rg.Decisions, grid.KPossibleFooter); d == nil || d.Confidence != 0.4 {
		t.Fatalf("decision %+v", d)
	}
}

func TestTwoTablesAndStrayNote(t *testing.T) {
	g := mk(t, [][]any{
		{"Region", "Q1", "Q2", "Q3"},
		{"North", 1, 2, 3},
		{"South", 4, 5, 6},
		{},
		{},
		{"Top reps"},
		{"Rep", "Region", "Deals"},
		{"Ann", "North", 4},
		{"Bob", "South", 6},
		{},
		{},
		{"Last refreshed 2026-03-31"},
	})
	res := Detect(g, Options{})
	if len(res.Regions) != 3 {
		t.Fatalf("regions = %d", len(res.Regions))
	}
	a, b, c := res.Regions[0], res.Regions[1], res.Regions[2]
	if a.Bounds.A1() != "A1:D3" || b.Bounds.A1() != "A6:C9" || c.Bounds.A1() != "A12" {
		t.Fatalf("bounds %s %s %s", a.Bounds.A1(), b.Bounds.A1(), c.Bounds.A1())
	}
	if a.Skipped || b.Skipped || !c.Skipped {
		t.Fatalf("skipped %v %v %v", a.Skipped, b.Skipped, c.Skipped)
	}
	if !reflect.DeepEqual(plus1(b.Header), []int{7}) || find(b.Decisions, grid.KSkipTitleRow) == nil {
		t.Fatalf("table 2 header %v decisions %v", plus1(b.Header), kinds(b.Decisions))
	}
	if find(b.Decisions, grid.KSplitRegion) == nil || find(c.Decisions, grid.KSkipRegion) == nil {
		t.Fatalf("decisions %v / %v", kinds(b.Decisions), kinds(c.Decisions))
	}
	if r := res.CellRole(g, 11, 0); r != grid.RoleIgnored {
		t.Fatalf("A12 role %q", r)
	}
}

func TestSingleBlankRowSplit(t *testing.T) {
	g := mk(t, [][]any{
		{"Item", "Qty"},
		{"Pen", 1},
		{"Cup", 2},
		{},
		{"Name", "Age"},
		{"Ann", 30},
		{"Bob", 40},
	})
	if n := len(Detect(g, Options{}).Regions); n != 2 {
		t.Fatalf("regions = %d, want 2", n)
	}
	// A blank row inside an all-text table must not split it.
	g = mk(t, [][]any{
		{"Name", "City"},
		{"Ann", "Oslo"},
		{"Bob", "Rome"},
		{},
		{"Cy", "Lima"},
		{"Di", "Kiev"},
	})
	res := Detect(g, Options{})
	if len(res.Regions) != 1 || len(res.Regions[0].Data) != 4 {
		t.Fatalf("regions = %d data %v", len(res.Regions), res.Regions[0].Data)
	}
	if find(res.Regions[0].Decisions, grid.KBlankRow) == nil {
		t.Fatalf("decisions %v", kinds(res.Regions[0].Decisions))
	}
}

func TestMergedFillDown(t *testing.T) {
	g := mk(t, [][]any{
		{"Category", "Item", "Qty"},
		{"Tools", "Saw", 1},
		{"", "Drill", 2},
		{"", "Hammer", 3},
		{"Toys", "Ball", 4},
		{"", "Kite", 5},
	}, "A2:A4", "A5:A6")
	rg := Detect(g, Options{}).Regions[0]
	want := map[[2]int][2]int{{2, 0}: {1, 0}, {3, 0}: {1, 0}, {5, 0}: {4, 0}}
	if !reflect.DeepEqual(rg.Fill, want) {
		t.Fatalf("fill %v", rg.Fill)
	}
	n := 0
	for _, d := range rg.Decisions {
		if d.Kind == grid.KFillDown {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("fill_down decisions = %d", n)
	}
}

func TestFillDownOption(t *testing.T) {
	g := mk(t, [][]any{
		{"Group Name", "Item", "Qty"},
		{"Tools", "Saw", 1},
		{"", "Drill", 2},
		{"Toys", "Ball", 4},
		{"", "Kite", 5},
	})
	for _, name := range []string{"A", "group_name", "Group Name", "group name"} {
		res := Detect(g, Options{FillDown: []string{name}})
		rg := res.Regions[0]
		if len(rg.Fill) != 2 || len(res.Warnings) != 0 {
			t.Fatalf("%q: fill %v warnings %v", name, rg.Fill, res.Warnings)
		}
	}
	res := Detect(g, Options{FillDown: []string{"nope"}})
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if len(Detect(g, Options{}).Regions[0].Fill) != 0 {
		t.Fatal("blank cells must stay blank without --fill-down")
	}
}

func TestForcedSkipAndHeaderRows(t *testing.T) {
	g := mk(t, [][]any{
		{"junk", "junk", "junk"},
		{"more", "junk", "here"},
		{"A", "B", "C"},
		{"x", "y", "z"},
		{1, 2, 3},
		{4, 5, 6},
	})
	res := Detect(g, Options{SkipRows: 2})
	rg := res.Regions[0]
	if !reflect.DeepEqual(plus1(rg.Header), []int{3}) || !reflect.DeepEqual(plus1(rg.Data), []int{4, 5, 6}) {
		t.Fatalf("header %v data %v", plus1(rg.Header), plus1(rg.Data))
	}
	if find(res.Decisions, grid.KSkipRowsForced) == nil {
		t.Fatalf("decisions %v", kinds(res.Decisions))
	}
	if r := res.CellRole(g, 0, 0); r != grid.RoleTitle {
		t.Fatalf("A1 role %q", r)
	}
	res = Detect(g, Options{SkipRows: 2, HeaderRows: 2})
	rg = res.Regions[0]
	if !reflect.DeepEqual(plus1(rg.Header), []int{3, 4}) || !reflect.DeepEqual(parts(rg), []string{"A|x", "B|y", "C|z"}) {
		t.Fatalf("header %v parts %v", plus1(rg.Header), parts(rg))
	}
}

func TestEmptyColumnDropped(t *testing.T) {
	g := mk(t, [][]any{
		{"Name", "", "Score"},
		{"Ann", "", 1},
		{"Bob", "", 2},
	})
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Cols) != 2 || rg.Cols[1].Col != 2 || find(rg.Decisions, grid.KDropEmptyColumn) == nil {
		t.Fatalf("cols %+v decisions %v", rg.Cols, kinds(rg.Decisions))
	}
}

func TestNumericHeaderRow(t *testing.T) {
	g := mk(t, [][]any{
		{"Region", 2023, 2024, 2025},
		{"North", 10, 11, 12},
		{"South", 20, 21, 22},
	})
	rg := Detect(g, Options{}).Regions[0]
	if !reflect.DeepEqual(rg.Header, []int{0}) || len(rg.Data) != 2 {
		t.Fatalf("header %v data %v", rg.Header, rg.Data)
	}
}

func TestDegenerateSheets(t *testing.T) {
	if res := Detect(grid.New("Empty", 0, 0), Options{}); len(res.Regions) != 0 {
		t.Fatalf("empty sheet regions %d", len(res.Regions))
	}
	g := mk(t, [][]any{{"only a title"}})
	res := Detect(g, Options{})
	if len(res.Regions) != 1 || !res.Regions[0].Skipped {
		t.Fatalf("regions %+v", res.Regions)
	}
	g = mk(t, [][]any{{"A", "B"}, {1, 2}})
	if res := Detect(g, Options{}); !res.Regions[0].Skipped {
		t.Fatal("a header with one data row must be skipped")
	}
}

func TestDeterministic(t *testing.T) {
	g := mk(t, [][]any{
		{"Report"},
		{"Dept", "A", "B"},
		{"x", 1, 2},
		{"Total", 1, 2},
		{"y", 3, 4},
		{"Source: me"},
	})
	first := Detect(g, Options{})
	for i := 0; i < 20; i++ {
		if again := Detect(g, Options{}); !reflect.DeepEqual(first.Regions[0].Decisions, again.Regions[0].Decisions) {
			t.Fatal("decisions differ between runs")
		}
	}
}
