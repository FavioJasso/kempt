// Command gen writes kempt's fixture workbooks and CSV files.
//
// Usage (from anywhere inside the module):
//
//	go run ./testdata/gen [-out DIR]
//
// Output is byte-for-byte deterministic: workbook document properties are
// fixed and excelize writes zip entries in sorted order without timestamps.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// fixedTime stamps every workbook's core properties.
const fixedTime = "2026-01-01T00:00:00Z"

// fixture is one generated file.
type fixture struct {
	name  string
	build func() ([]byte, error)
}

var fixtures = []fixture{
	{"budget_report.xlsx", budgetReport},
	{"two_tables.xlsx", twoTables},
	{"merged_categories.xlsx", mergedCategories},
	{"messy_values.xlsx", messyValues},
	{"ambiguous_dates.csv", ambiguousDates},
	{"tidy.csv", tidy},
}

func main() {
	log.SetFlags(0)
	out := flag.String("out", "", "output directory (default: testdata/fixtures under the module root)")
	flag.Parse()
	dir := *out
	if dir == "" {
		root, err := moduleRoot()
		if err != nil {
			log.Fatal(err)
		}
		dir = filepath.Join(root, "testdata", "fixtures")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, fx := range fixtures {
		data, err := fx.build()
		if err != nil {
			log.Fatalf("%s: %v", fx.name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, fx.name), data, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", filepath.Join(dir, fx.name))
	}
}

// moduleRoot walks up from the working directory to the directory holding go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found; pass -out")
		}
		dir = parent
	}
}

// newBook returns a workbook whose only sheet is named sheet.
func newBook(sheet string) (*excelize.File, error) {
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	return f, nil
}

// save fixes the document properties and serializes the workbook.
func save(f *excelize.File) ([]byte, error) {
	defer f.Close()
	if err := f.SetDocProps(&excelize.DocProperties{
		Creator:        "kempt testdata/gen",
		LastModifiedBy: "kempt testdata/gen",
		Created:        fixedTime,
		Modified:       fixedTime,
	}); err != nil {
		return nil, err
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// setRows writes rows starting at row top, column A. nil values are skipped.
func setRows(f *excelize.File, sheet string, top int, rows [][]any) error {
	for i, row := range rows {
		for j, v := range row {
			if v == nil {
				continue
			}
			ref, err := excelize.CoordinatesToCellName(j+1, top+i)
			if err != nil {
				return err
			}
			if err := f.SetCellValue(sheet, ref, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// numFmt registers a style with a custom number format.
func numFmt(f *excelize.File, code string) (int, error) {
	return f.NewStyle(&excelize.Style{CustomNumFmt: &code})
}

// budgetReport is a report-style sheet: titles, a two-row grouped header,
// accounting text negatives, subtotals with cached formulas and footnotes.
// It is written with the stream writer because that is the only excelize
// path that stores a formula together with a numeric cached value.
func budgetReport() ([]byte, error) {
	const sheet = "Budget Summary"
	f, err := newBook(sheet)
	if err != nil {
		return nil, err
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	money, err := numFmt(f, "#,##0;(#,##0)")
	if err != nil {
		return nil, err
	}
	pct, err := numFmt(f, "0.0")
	if err != nil {
		return nil, err
	}
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return nil, err
	}

	// cell converts a spec value: int → money, float64 → change %, string →
	// text, formula → formula with cached money value.
	type formula struct {
		expr   string
		cached int
	}
	cell := func(v any) any {
		switch x := v.(type) {
		case int:
			return excelize.Cell{StyleID: money, Value: x}
		case float64:
			return excelize.Cell{StyleID: pct, Value: x}
		case formula:
			return excelize.Cell{StyleID: money, Formula: x.expr, Value: x.cached}
		}
		return v
	}
	totals := func(refs string, cached ...int) []any {
		out := make([]any, len(cached))
		for i, c := range cached {
			col := string(rune('C' + i))
			out[i] = formula{strings.ReplaceAll(refs, "#", col), c}
		}
		return out
	}
	line := func(dept, prog any, nums []any, change float64, note any) []any {
		row := []any{dept, prog}
		for _, n := range nums {
			row = append(row, cell(n))
		}
		return append(row, cell(change), note)
	}
	b := func(s string) any { return excelize.Cell{StyleID: bold, Value: s} }

	rows := map[int][]any{
		1:  {b("City of Riverton — Departmental Budget Report")},
		2:  {"Fiscal years 2025 and 2026, all funds"},
		4:  {b("Department"), b("Program"), b("FY2025 ($000s)"), nil, b("FY2026 ($000s)"), nil, b("Change (%)"), b("Notes")},
		5:  {nil, nil, b("Budget"), b("Actual"), b("Budget"), b("Actual")},
		6:  line("Public Works", "Streets", []any{2450, 2391, 2600, 1212}, 6.1, "Paving backlog"),
		7:  line("Public Works", "Fleet", []any{1120, 1165, 1180, 604}, 5.4, nil),
		8:  line("Total Public Works", nil, totals("SUM(#6:#7)", 3570, 3556, 3780, 1816), 5.9, nil),
		9:  line("Parks & Recreation", "Parks Maintenance", []any{1800, 1742, 1850, 930}, 2.8, nil),
		10: line("Parks & Recreation", "Recreation Programs", []any{960, 1010, 1020, 488}, 6.3, "Summer camp expansion"),
		11: line("Parks & Recreation", "Fee revenue offset", []any{"(1,234)", "(1,180)", "(1,300)", "(655)"}, 5.3, "—"),
		// Excel's + coerces the accounting text "(1,234)" to -1234, so the
		// cached totals match what Excel would compute.
		12: line("Total Parks & Recreation", nil, totals("#9+#10+#11", 1526, 1572, 1570, 763), 2.9, nil),
		13: line("Public Safety", "Police", []any{5400, 5512, 5650, 2840}, 4.6, nil),
		14: line("Public Safety", "Fire", []any{3900, 3860, 4010, "N/A"}, 2.8, "New station opens July*"),
		// SUM skips the "N/A" text in F14.
		15: line("Total Public Safety", nil, totals("SUM(#13:#14)", 9300, 9372, 9660, 2840), 3.9, nil),
		16: line("Grand Total", nil, totals("#8+#12+#15", 14396, 14500, 15010, 5419), 4.3, nil),
		18: {"Source: Riverton Finance Department, FY2026 Adopted Budget."},
		19: {"* FY2026 actuals are year-to-date through March."},
	}
	for r := 1; r <= 19; r++ {
		row, ok := rows[r]
		if !ok {
			continue
		}
		if err := sw.SetRow(fmt.Sprintf("A%d", r), row); err != nil {
			return nil, err
		}
	}
	for _, m := range [][2]string{
		{"A1", "H1"}, {"A4", "A5"}, {"B4", "B5"}, {"C4", "D4"}, {"E4", "F4"}, {"G4", "G5"}, {"H4", "H5"},
	} {
		if err := sw.MergeCell(m[0], m[1]); err != nil {
			return nil, err
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}
	return save(f)
}

// twoTables holds two tables of different widths and a stray note.
func twoTables() ([]byte, error) {
	const sheet = "Summary"
	f, err := newBook(sheet)
	if err != nil {
		return nil, err
	}
	blocks := []struct {
		top  int
		rows [][]any
	}{
		{1, [][]any{
			{"Region", "Q1 Sales", "Q2 Sales", "Q3 Sales", "Q4 Sales"},
			{"North", 120500, 131200, 128900, 140100},
			{"South", 98000, 101500, 99750, 110250},
			{"East", 143250, 150000, 147800, 162300},
			{"West", 87600, 90100, 93450, 97800},
		}},
		{8, [][]any{
			{"Top sales reps — 2026"},
			{"Rep", "Region", "Deals Closed"},
			{"Avery Chen", "East", 42},
			{"Jordan Patel", "North", 38},
			{"Sam Rivera", "West", 35},
		}},
		{15, [][]any{{"Last refreshed 2026-03-31"}}},
	}
	for _, bl := range blocks {
		if err := setRows(f, sheet, bl.top, bl.rows); err != nil {
			return nil, err
		}
	}
	return save(f)
}

// mergedCategories has a vertically merged category column.
func mergedCategories() ([]byte, error) {
	const sheet = "Inventory"
	f, err := newBook(sheet)
	if err != nil {
		return nil, err
	}
	price, err := f.NewStyle(&excelize.Style{NumFmt: 2}) // built-in "0.00"
	if err != nil {
		return nil, err
	}
	groups := []struct {
		category string
		items    [][]any // item, SKU, qty, unit price
	}{
		{"Electronics", [][]any{
			{"Wireless Mouse", "EL-1001", 45, 24.5},
			{"Mechanical Keyboard", "EL-1002", 20, 129.99},
			{"27-inch Monitor", "EL-1003", 12, 349.0},
			{"USB-C Hub", "EL-1004", 30, 39.99},
		}},
		{"Furniture", [][]any{
			{"Standing Desk", "FU-2001", 5, 899.0},
			{"Ergonomic Chair", "FU-2002", 8, 459.5},
			{"Bookshelf", "FU-2003", 6, 189.99},
		}},
		{"Office Supplies", [][]any{
			{"Stapler", "OS-3001", 60, 12.75},
			{"Printer Paper (case)", "00731", 100, 42.0},
			{"Gel Pens (12-pack)", "OS-3003", 80, 9.99},
			{"Sticky Notes", "OS-3004", 120, 6.5},
			{"Desk Organizer", "OS-3005", 25, 18.25},
		}},
	}
	if err := setRows(f, sheet, 1, [][]any{{"Category", "Item", "SKU", "Qty", "Unit Price"}}); err != nil {
		return nil, err
	}
	r := 2
	for _, g := range groups {
		rows := make([][]any, len(g.items))
		for i, it := range g.items {
			rows[i] = append([]any{nil}, it...)
		}
		if err := setRows(f, sheet, r, rows); err != nil {
			return nil, err
		}
		end := r + len(g.items) - 1
		if err := f.SetCellValue(sheet, fmt.Sprintf("A%d", r), g.category); err != nil {
			return nil, err
		}
		if err := f.MergeCell(sheet, fmt.Sprintf("A%d", r), fmt.Sprintf("A%d", end)); err != nil {
			return nil, err
		}
		if err := f.SetCellStyle(sheet, fmt.Sprintf("E%d", r), fmt.Sprintf("E%d", end), price); err != nil {
			return nil, err
		}
		r = end + 1
	}
	return save(f)
}

// messyValues mixes currency text, percentages, null tokens, Excel error
// text, serial dates with and without a date format, and text dates.
func messyValues() ([]byte, error) {
	const sheet = "Orders"
	f, err := newBook(sheet)
	if err != nil {
		return nil, err
	}
	date, err := numFmt(f, "yyyy-mm-dd")
	if err != nil {
		return nil, err
	}
	const march1 = 46082 // Excel serial of 2026-03-01
	rows := [][]any{
		{"Order ID", "Order Date", "Ship Date", "Customer", "Amount", "Discount", "Margin", "Units", "Invoice Date", "Paid", "Region Code"},
		{1001, march1 + 1, "03/15/2026", "Acme Corp", "$1,200.50", "12%", 0.32, 10, 46100, true, "NE"},
		{1002, march1 + 2, "3/16/26", "Birch & Co", "$89.99", "5%", 0.18, 2, 46101, true, "SW"},
		{1003, march1 + 3, "Mar 17, 2026", "Cobalt Labs", "$15,000.00", "0%", 0.41, 150, 46102, false, "SE"},
		{1004, march1 + 4, "18-Mar-2026", "Delta Freight", "—", "N/A", "#DIV/0!", 4, 46103, false, "NW"},
		{1005, march1 + 5, "2026-03-19", "Evergreen Foods", "$350.25", "15%", 0.27, 7, 46104, true, "NA"},
		{1006, march1 + 6, "03/20/2026", "Foxglove Design", "$42.10", "10%", 0.09, 1, 46105, true, "MW"},
		{1007, march1 + 7, "3/21/2026", "Granite Supply", "75.00-", "5%", -0.05, "see note", 46106, false, "NE"},
		{1008, march1 + 8, "Mar 22, 2026", "Harbor Marine", "$1,999.99", "20%", 0.36, 25, 46107, true, "SW"},
		{1009, march1 + 9, "23-Mar-2026", "Ivy Analytics", "(120.00)", "0%", -0.12, 3, 46108, false, "SE"},
		{1010, march1 + 10, "03/24/2026", "Juniper Tech", "$64.00", "12.5%", 0.22, 8, 46109, true, "NW"},
		{1011, march1 + 11, "N/A", "Kestrel Outfitters", "$8.75", "5%", 0.15, 1, 46110, false, "MW"},
		{1012, march1 + 12, "3/26/26", "Lumen Health", "$2,300.40", "10%", 0.29, 40, 46111, true, "NE"},
	}
	if err := setRows(f, sheet, 1, rows); err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(sheet, "B2", fmt.Sprintf("B%d", len(rows)), date); err != nil {
		return nil, err
	}
	return save(f)
}

// ambiguousDates is a semicolon CSV with a BOM, CRLF line endings and dates
// whose day and month are both ≤ 12.
func ambiguousDates() ([]byte, error) {
	return csvBytes([][]string{
		{"id", "event", "event_date", "amount"},
		{"1", "Kickoff meeting", "03/04/2026", "12.50"},
		{"2", "Design review", "05/06/2026", "7.25"},
		{"3", "Budget sign-off", "01/02/2026", "30.00"},
		{"4", "Vendor demo", "11/12/2026", "18.75"},
		{"5", "Team offsite", "07/08/2026", "145.20"},
		{"6", "Quarterly report", "02/03/2026", "9.99"},
		{"7", "Security audit", "09/10/2026", "60.00"},
		{"8", "Holiday party", "12/11/2026", "220.40"},
	}, ';', "\r\n", true), nil
}

// tidy is an already-clean comma CSV that must round-trip unchanged.
func tidy() ([]byte, error) {
	return csvBytes([][]string{
		{"ID", "Name", "Email", "City", "Signup Date", "Plan", "Monthly Spend", "Active"},
		{"1", "Ada Lovelace", "ada@example.com", "London", "2026-01-15", "Pro", "49", "true"},
		{"2", "Grace Hopper", "grace@example.com", "Arlington", "2026-01-22", "Team", "120.75", "true"},
		{"3", "Alan Turing", "alan@example.com", "Manchester", "2026-02-03", "Free", "0", "false"},
		{"4", "Katherine Johnson", "katherine@example.com", "Hampton", "2026-02-11", "Pro", "19.5", "true"},
		{"5", "Edsger Dijkstra", "edsger@example.com", "Austin", "2026-02-27", "Team", "240", "false"},
		{"6", "Barbara Liskov", "barbara@example.com", "Boston", "2026-03-04", "Enterprise", "1250.25", "true"},
		{"7", "Donald Knuth", "donald@example.com", "Stanford", "2026-03-18", "Pro", "49", "true"},
		{"8", "Margaret Hamilton", "margaret@example.com", "Cambridge", "2026-04-01", "Free", "0", "false"},
		{"9", "Dennis Ritchie", "dennis@example.com", "Murray Hill", "2026-04-09", "Team", "99.9", "true"},
		{"10", "Frances Allen", "frances@example.com", "Albany", "2026-04-23", "Enterprise", "880.4", "false"},
	}, ',', "\n", false), nil
}

// csvBytes renders records with the given delimiter and line ending,
// quoting only fields that need it.
func csvBytes(records [][]string, sep rune, eol string, bom bool) []byte {
	var sb strings.Builder
	if bom {
		sb.WriteString("\uFEFF")
	}
	for _, rec := range records {
		for i, field := range rec {
			if i > 0 {
				sb.WriteRune(sep)
			}
			if strings.ContainsAny(field, string(sep)+"\"\r\n") {
				field = `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
			}
			sb.WriteString(field)
		}
		sb.WriteString(eol)
	}
	return []byte(sb.String())
}
