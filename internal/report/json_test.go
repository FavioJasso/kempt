package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

const sheetName = "Budget Summary"

func num(f float64) *float64 { return &f }

// sampleGrid is a small messy sheet: merged title, header with a unit,
// a vertical merge, every cell kind, a totals row with a formula, a footer.
func sampleGrid() *grid.Grid {
	g := grid.New(sheetName, 6, 3)
	g.Source = "xlsx"
	set := func(ref string, c grid.Cell) {
		r, col, _ := grid.ParseRef(ref)
		g.Cells[r][col] = c
	}
	set("A1", grid.Cell{Raw: "City of Riverton — Departmental Budget", Kind: grid.String})
	set("A2", grid.Cell{Raw: "Department", Kind: grid.String})
	set("B2", grid.Cell{Raw: "FY2025 ($000s)", Kind: grid.String})
	set("C2", grid.Cell{Raw: "Date", Kind: grid.String})
	set("A3", grid.Cell{Raw: "Parks & Recreation", Kind: grid.String})
	set("B3", grid.Cell{Raw: "2450", Kind: grid.Number, Num: num(2450)})
	set("C3", grid.Cell{Raw: "46096", Kind: grid.Date, Num: num(46096), DateFmt: true})
	set("B4", grid.Cell{Raw: "(1,234)", Kind: grid.String})
	set("C4", grid.Cell{Raw: "#DIV/0!", Kind: grid.Error})
	set("A5", grid.Cell{Raw: "Total", Kind: grid.String})
	set("B5", grid.Cell{Raw: "1216", Kind: grid.Formula, Num: num(1216), Formula: "SUM(B3:B4)"})
	set("C5", grid.Cell{Raw: "TRUE", Kind: grid.Bool})
	set("A6", grid.Cell{Raw: "Source: Finance\tDept", Kind: grid.String})
	g.AddMerge(grid.Range{R1: 0, C1: 0, R2: 0, C2: 2})
	g.AddMerge(grid.Range{R1: 2, C1: 0, R2: 3, C2: 0})
	return g
}

func dec(kind string, rng grid.Range, conf float64, reason string) grid.Decision {
	return grid.NewDecision(kind, sheetName, rng, conf, reason)
}

func sampleSheet() *plan.Sheet {
	sqlTypes := func(t infer.Type) map[string]string {
		m := map[string]string{}
		for _, d := range infer.Dialects {
			m[d] = t.SQL(d)
		}
		return m
	}
	text := infer.Type{Base: infer.Text, MaxLen: 18}
	amount := infer.Type{Base: infer.Decimal, Precision: 6, Scale: 2}
	p := &plan.Plan{
		Sheet:  sheetName,
		Table:  "budget_summary",
		Region: "A1:C6",
		Columns: []plan.Column{
			{Name: "department", Header: "Department", Type: text, SQLType: sqlTypes(text), SourceCols: []string{"A"}, Comment: "Department"},
			{Name: "fy2025", Header: "FY2025 ($000s)", Type: amount, SQLType: sqlTypes(amount), Unit: "$000s",
				SourceCols: []string{"B"}, Comment: "FY2025 ($000s) [unit: $000s; values not rescaled]"},
			{Name: "date", Header: "Date", Type: infer.Type{Base: infer.Date}, SourceCols: []string{"C"}, Comment: "Date"},
		},
		Rows: [][]any{
			{"Parks & Recreation", normalize.Decimal("2450.00"), normalize.NewDate(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))},
			{"Parks & Recreation", normalize.Decimal("-1234.00"), nil},
		},
		SourceRows: []int{3, 4},
		Decisions: []grid.Decision{
			dec(grid.KDropTotalRow, grid.RowRange(4, 0, 2), 0.95, "Row 5 starts with \"Total\" and its numbers equal the sum of the rows above it in the group, "+
				"so it is a totals row; dropped (use --keep-totals to keep it)."),
			dec(grid.KFillDown, grid.Range{R1: 2, C1: 0, R2: 3, C2: 0}, 0.9, "Vertical merge A3:A4 copied down."),
			dec(grid.KSkipTitleRow, grid.RowRange(0, 0, 2), 0.95, "Row 1 is a single merged cell spanning A:C."),
		},
		CellRoles: map[string]string{"A1": "title", "A2": "header", "A3": "data", "C4": "null"},
	}
	return &plan.Sheet{
		Name:      sheetName,
		Grid:      sampleGrid(),
		Tables:    []*plan.Plan{p},
		Decisions: []grid.Decision{dec(grid.KSkipFooter, grid.RowRange(5, 0, 2), 0.9, "Row 6 starts with \"Source\".")},
		CellRoles: map[string]string{"A1": "title", "A10": "footer", "A2": "header", "A3": "data", "C4": "null"},
	}
}

func TestJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, "/some/dir/budget_report.xlsx", []*plan.Sheet{sampleSheet()}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "}\n") || !strings.HasPrefix(out, "{\n  \"file\": ") {
		t.Errorf("want 2-space indent and trailing newline, got:\n%s", out)
	}
	if !strings.Contains(out, `"Parks & Recreation"`) {
		t.Error("HTML characters must not be escaped")
	}

	var doc struct {
		File   string `json:"file"`
		Sheets []struct {
			Sheet      string            `json:"sheet"`
			Dimensions string            `json:"dimensions"`
			Decisions  []map[string]any  `json:"decisions"`
			CellRoles  map[string]string `json:"cellRoles"`
			Tables     []struct {
				Table     string           `json:"table"`
				Region    string           `json:"region"`
				RowCount  int              `json:"rowCount"`
				Columns   []map[string]any `json:"columns"`
				Rows      [][]any          `json:"rows"`
				Decisions []map[string]any `json:"decisions"`
			} `json:"tables"`
		} `json:"sheets"`
	}
	dc := json.NewDecoder(strings.NewReader(out))
	dc.UseNumber()
	if err := dc.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.File != "budget_report.xlsx" {
		t.Errorf("file = %q", doc.File)
	}
	if len(doc.Sheets) != 1 {
		t.Fatalf("sheets = %d", len(doc.Sheets))
	}
	sh := doc.Sheets[0]
	if sh.Sheet != sheetName || sh.Dimensions != "A1:C6" || len(sh.CellRoles) != 5 || len(sh.Decisions) != 1 {
		t.Errorf("sheet = %+v", sh)
	}
	if got := sh.Decisions[0]; got["kind"] != "skip_footer" || got["range"] != "'Budget Summary'!A6:C6" ||
		got["confidence"] != json.Number("0.9") || len(got) != 4 {
		t.Errorf("sheet decision = %v", got)
	}
	tb := sh.Tables[0]
	if tb.Table != "budget_summary" || tb.Region != "A1:C6" || tb.RowCount != 2 || len(tb.Rows) != 2 {
		t.Errorf("table = %+v", tb)
	}
	var kinds []string
	for _, d := range tb.Decisions {
		kinds = append(kinds, d["kind"].(string))
	}
	if got := strings.Join(kinds, ","); got != "skip_title_row,fill_down,drop_total_row" {
		t.Errorf("decision order = %s", got)
	}
	if got := tb.Rows[0]; got[0] != "Parks & Recreation" || got[1] != json.Number("2450.00") || got[2] != "2026-03-15" {
		t.Errorf("row 0 = %v", got)
	}
	if tb.Rows[1][2] != nil {
		t.Errorf("NULL = %v, want null", tb.Rows[1][2])
	}

	tests := []struct {
		col  int
		key  string
		want string // compact JSON; maps have sorted keys
	}{
		{0, "name", `"department"`},
		{0, "header", `"Department"`},
		{0, "unit", `""`},
		{1, "unit", `"$000s"`},
		{1, "comment", `"FY2025 ($000s) [unit: $000s; values not rescaled]"`},
		{1, "type", `{"base":"decimal","maxLen":0,"precision":6,"scale":2}`},
		{0, "type", `{"base":"text","maxLen":18,"precision":0,"scale":0}`},
		{1, "sqlType", `{"mysql":"DECIMAL(6,2)","postgres":"NUMERIC(6,2)","sqlite":"NUMERIC(6,2)"}`},
		{2, "sqlType", `{"mysql":"DATE","postgres":"DATE","sqlite":"DATE"}`}, // falls back to Type.SQL
		{2, "sourceCols", `["C"]`},
	}
	for _, tc := range tests {
		b, err := json.Marshal(tb.Columns[tc.col][tc.key])
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != tc.want {
			t.Errorf("columns[%d].%s = %s, want %s", tc.col, tc.key, got, tc.want)
		}
	}
	for _, c := range tb.Columns {
		if len(c) != 7 {
			t.Errorf("column keys = %v, want 7", c)
		}
	}
}

func TestJSONEmpty(t *testing.T) {
	tests := []struct {
		name   string
		sheets []*plan.Sheet
		want   string
	}{
		{"no sheets", nil, "{\n  \"file\": \"x.csv\",\n  \"sheets\": []\n}\n"},
		{"empty sheet", []*plan.Sheet{{Name: "x"}}, `{
  "file": "x.csv",
  "sheets": [
    {
      "sheet": "x",
      "dimensions": "",
      "decisions": [],
      "cellRoles": {},
      "tables": []
    }
  ]
}
`},
		{"empty table", []*plan.Sheet{{Name: "x", Grid: grid.New("x", 1, 1), Tables: []*plan.Plan{{Table: "x", Region: "A1",
			Columns: []plan.Column{{Name: "c", Type: infer.Type{Base: infer.Bool}}}, Rows: [][]any{nil}}}}}, `{
  "file": "x.csv",
  "sheets": [
    {
      "sheet": "x",
      "dimensions": "A1",
      "decisions": [],
      "cellRoles": {},
      "tables": [
        {
          "table": "x",
          "region": "A1",
          "rowCount": 1,
          "columns": [
            {
              "name": "c",
              "header": "",
              "type": {
                "base": "bool",
                "precision": 0,
                "scale": 0,
                "maxLen": 0
              },
              "sqlType": {
                "mysql": "BOOLEAN",
                "postgres": "BOOLEAN",
                "sqlite": "INTEGER"
              },
              "unit": "",
              "sourceCols": [],
              "comment": ""
            }
          ],
          "rows": [
            []
          ],
          "decisions": []
        }
      ]
    }
  ]
}
`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := JSON(&buf, `C:\Users\me\x.csv`, tc.sheets); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestDecisionsReport(t *testing.T) {
	empty := &plan.Sheet{Name: "Notes"}
	var buf bytes.Buffer
	if err := Decisions(&buf, "dir/budget_report.xlsx", []*plan.Sheet{sampleSheet(), empty}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		File   string `json:"file"`
		Tables []struct {
			Sheet, Table, Region string
			Decisions            []decisionDoc `json:"decisions"`
		} `json:"tables"`
		SheetDecisions []struct {
			Sheet     string        `json:"sheet"`
			Decisions []decisionDoc `json:"decisions"`
		} `json:"sheetDecisions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.File != "budget_report.xlsx" || len(doc.Tables) != 1 || len(doc.SheetDecisions) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	if tb := doc.Tables[0]; tb.Sheet != sheetName || tb.Table != "budget_summary" || tb.Region != "A1:C6" ||
		len(tb.Decisions) != 3 || tb.Decisions[0].Kind != grid.KSkipTitleRow {
		t.Errorf("table = %+v", tb)
	}
	if sd := doc.SheetDecisions[0]; sd.Sheet != sheetName || len(sd.Decisions) != 1 || sd.Decisions[0].Kind != grid.KSkipFooter {
		t.Errorf("sheet decisions = %+v", sd)
	}
	if !strings.Contains(buf.String(), "\"sheet\": \"Notes\",\n      \"decisions\": []") {
		t.Errorf("empty decisions must render as []:\n%s", buf.String())
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"a.xlsx", "a.xlsx"},
		{"/tmp/a.xlsx", "a.xlsx"},
		{"dir/sub/a.csv", "a.csv"},
		{`C:\data\a.csv`, "a.csv"},
		{"dir/", ""},
		{"../x/y z.xlsx", "y z.xlsx"},
		{`mixed/dir\a.tsv`, "a.tsv"},
		{"./a", "a"},
		{"no_ext", "no_ext"},
	}
	for _, tc := range tests {
		if got := baseName(tc.in); got != tc.want {
			t.Errorf("baseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
