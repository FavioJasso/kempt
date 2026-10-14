package detect

import (
	"reflect"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/grid"
)

// csvGrid builds a grid from CSV-like lines where every field is text, the
// way the CSV reader delivers it.
func csvGrid(t *testing.T, lines ...string) *grid.Grid {
	t.Helper()
	var rows [][]any
	for _, ln := range lines {
		var row []any
		for _, f := range strings.Split(ln, ",") {
			row = append(row, f)
		}
		rows = append(rows, row)
	}
	return mk(t, rows)
}

func dataCount(res Result) (tables, rows int) {
	for _, rg := range res.Regions {
		if !rg.Skipped {
			tables++
			rows += len(rg.Data)
		}
	}
	return
}

// Year headers must beat data rows that hold a null marker.
func TestYearHeaderBeatsNullMarkers(t *testing.T) {
	g := csvGrid(t,
		"County,2019,2020,2021",
		"Adams,1200,1300,1250",
		"Brown,800,-,820",
		"Clark,450,470,480",
		"Dane,3000,3100,3200",
	)
	rg := Detect(g, Options{}).Regions[0]
	if !reflect.DeepEqual(rg.Header, []int{0}) || len(rg.Data) != 4 {
		t.Fatalf("header %v data %v", plus1(rg.Header), plus1(rg.Data))
	}
	// A crosstab with a blank corner.
	g = csvGrid(t,
		",2021,2022,2023",
		"North,100,200,300",
		"South,110,210,310",
		"East,120,220,320",
	)
	rg = Detect(g, Options{}).Regions[0]
	if !reflect.DeepEqual(rg.Header, []int{0}) || len(rg.Data) != 3 {
		t.Fatalf("crosstab header %v data %v", plus1(rg.Header), plus1(rg.Data))
	}
}

// All-text tables: a header with a blank or duplicate label still wins, and
// no data row is swallowed into a two-row header.
func TestAllTextHeaderWithGaps(t *testing.T) {
	for _, lines := range [][]string{
		{"Name,,Department,Email", "Alice Moore,Mgr,Finance,alice@city.gov", "Bob Chan,Analyst,Finance,bob@city.gov", "Cara Diaz,Clerk,Parks,cara@city.gov"},
		{"Name,Phone,Phone,Email", "Ann,555-0101,555-0201,ann@x.org", "Bo,555-0102,555-0202,bo@x.org", "Cy,555-0103,555-0203,cy@x.org"},
	} {
		rg := Detect(csvGrid(t, lines...), Options{}).Regions[0]
		if !reflect.DeepEqual(rg.Header, []int{0}) || len(rg.Data) != 3 {
			t.Fatalf("%q: header %v data %v", lines[0], plus1(rg.Header), plus1(rg.Data))
		}
	}
}

// Blank rows inside a table do not cost its continuation a row.
func TestBlankRowsInsideTable(t *testing.T) {
	g := csvGrid(t,
		"Date,Store,Sales",
		"2026-01-01,Downtown,1200",
		"2026-01-02,Downtown,1300",
		"2026-01-03,Downtown,1250",
		",,",
		",,",
		"2026-01-04,Uptown,900",
		"2026-01-05,Uptown,950",
	)
	res := Detect(g, Options{})
	if tables, rows := dataCount(res); tables != 1 || rows != 5 {
		t.Fatalf("tables %d rows %d", tables, rows)
	}
	g = csvGrid(t,
		"Name,Department,City,Salary,Bonus",
		"Ann Lee,Finance,Austin,70000,5000",
		"Bo Kim,Parks,Dallas,65000,4000",
		"Cy Ray,Works,Waco,60000,3000",
		",,,,",
		"Di Park,Library,Tyler,,",
		"Ed Wu,Finance,Austin,72000,5100",
	)
	if tables, rows := dataCount(Detect(g, Options{})); tables != 1 || rows != 5 {
		t.Fatalf("single blank: tables %d rows %d", tables, rows)
	}
}

// A table with no header row keeps every row and gets generated names.
func TestHeaderlessTable(t *testing.T) {
	g := csvGrid(t, "1,Ann,30", "2,Bob,40", "3,Cy,50")
	rg := Detect(g, Options{}).Regions[0]
	if rg.Skipped || len(rg.Header) != 0 || len(rg.Data) != 3 || len(rg.Cols) != 3 {
		t.Fatalf("skipped %v header %v data %v cols %v", rg.Skipped, rg.Header, rg.Data, rg.Cols)
	}
	if d := find(rg.Decisions, grid.KNoHeader); d == nil {
		t.Fatalf("decisions %v", kinds(rg.Decisions))
	}
}

// Total-like words are only dropped with evidence.
func TestTotalsNeedEvidence(t *testing.T) {
	g := csvGrid(t,
		"Vendor,Category,Amount",
		"Acme Supply,Office,120.50",
		"Total Wine & More,Catering,340.00",
		"All Saints Hospital,Medical,1200.00",
		"Sum Kee Restaurant,Catering,95.00",
		"Beta Corp,Office,60.00",
	)
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Data) != 5 || len(rg.Totals) != 0 {
		t.Fatalf("data %v totals %v", plus1(rg.Data), plus1(rg.Totals))
	}
	// Status text in a later column is not a row label.
	g = csvGrid(t, "Order,Amount,Status", "1001,50,Shipped", "1002,99,All items backordered", "1004,70,Total refund issued")
	if rg := Detect(g, Options{}).Regions[0]; len(rg.Data) != 3 {
		t.Fatalf("status rows dropped: data %v", plus1(rg.Data))
	}
	// A subtotal labeled in the second column is found by its sum.
	g = csvGrid(t,
		"Department,Program,Amount",
		"Public Works,Roads,100",
		"Public Works,Bridges,50",
		"Public Works,Subtotal,150",
		"Parks,Trails,30",
		"Parks,Pools,20",
		"Parks,Subtotal,50",
	)
	if rg := Detect(g, Options{}).Regions[0]; len(rg.Data) != 4 || len(rg.Totals) != 2 {
		t.Fatalf("subtotals: data %v totals %v", plus1(rg.Data), plus1(rg.Totals))
	}
}

// Only a trailing run of footnote-like rows is dropped.
func TestFootersOnlyTrailingRun(t *testing.T) {
	g := csvGrid(t, "Vendor", "Acme Supply", "Beta Corp", "Source One Distributors", "Gamma LLC", "Delta Inc")
	if rg := Detect(g, Options{}).Regions[0]; len(rg.Data) != 5 {
		t.Fatalf("single-column list: data %v", plus1(rg.Data))
	}
	g = csvGrid(t, "Task,Owner,Due", "Pay,Ann,2026-01-02", "File,Bo,2026-01-03", "* rush order for Acme,,", "Call vendor back,,")
	if rg := Detect(g, Options{}).Regions[0]; len(rg.Data) != 4 {
		t.Fatalf("trailing notes: data %v", plus1(rg.Data))
	}
}

// A single-column list above a table is its own table, not five titles.
func TestListAboveTable(t *testing.T) {
	g := csvGrid(t,
		"Approved Vendors,,",
		"Acme Supply,,",
		"Beta Corp,,",
		"Gamma LLC,,",
		",,",
		",,",
		"Item,Qty,Price",
		"Pens,10,1.5",
		"Paper,5,4.25",
	)
	if tables, rows := dataCount(Detect(g, Options{})); tables != 2 || rows != 5 {
		t.Fatalf("tables %d rows %d", tables, rows)
	}
}

// --skip-rows 0 given explicitly forces the header onto the first row.
func TestExplicitSkipZero(t *testing.T) {
	g := csvGrid(t, "a,b,c", "x,y,z", "p,q,r")
	rg := Detect(g, Options{SkipRowsSet: true}).Regions[0]
	if !reflect.DeepEqual(rg.Header, []int{0}) {
		t.Fatalf("header %v", plus1(rg.Header))
	}
}

// The 30-row header window starts after single-value preamble rows.
func TestLongPreamble(t *testing.T) {
	var lines []string
	for i := 0; i < 32; i++ {
		lines = append(lines, "Instruction line,,")
	}
	lines = append(lines, "Code,Name,Value", "A1,Alpha,1", "B2,Beta,2", "C3,Gamma,3")
	if tables, rows := dataCount(Detect(csvGrid(t, lines...), Options{})); tables != 1 || rows != 3 {
		t.Fatalf("tables %d rows %d", tables, rows)
	}
}

// Two or more blank columns separate side-by-side tables.
func TestSideBySide(t *testing.T) {
	g := csvGrid(t,
		"Item,Qty,Price,,,Region,Manager",
		"Pens,10,1.5,,,North,Ann",
		"Paper,5,4.25,,,South,Bo",
		"Ink,2,20,,,,",
	)
	res := Detect(g, Options{})
	if tables, rows := dataCount(res); tables != 2 || rows != 5 {
		t.Fatalf("tables %d rows %d", tables, rows)
	}
}

// Title-like rows above the header are not group labels.
func TestTitleIsNotGroup(t *testing.T) {
	g := csvGrid(t, "Monthly Report,,Oct 2026", "Item,Qty,Price", "Pens,10,1.5", "Paper,5,4.25", "Ink,2,20")
	rg := Detect(g, Options{}).Regions[0]
	if got := parts(rg); !reflect.DeepEqual(got, []string{"Item", "Qty", "Price"}) {
		t.Fatalf("parts %v", got)
	}
}

// A units row under the header joins the header.
func TestUnitsRowUnderHeader(t *testing.T) {
	g := csvGrid(t, "Program,Budget,Actual,Variance", ",($000s),($000s),(%)", "Roads,100,90,-10", "Parks,50,45,-10", "Library,30,29,-3.3")
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Data) != 3 || !reflect.DeepEqual(parts(rg), []string{"Program", "Budget|($000s)", "Actual|($000s)", "Variance|(%)"}) {
		t.Fatalf("data %v parts %v", plus1(rg.Data), parts(rg))
	}
}

// A note directly under the header is kept but logged.
func TestNoteUnderHeader(t *testing.T) {
	g := csvGrid(t, "Program,Budget,Actual", "Amounts in USD; preliminary,,", "Roads,100,90", "Parks,50,45")
	rg := Detect(g, Options{}).Regions[0]
	if len(rg.Data) != 3 || find(rg.Decisions, grid.KPossibleNote) == nil {
		t.Fatalf("data %v decisions %v", plus1(rg.Data), kinds(rg.Decisions))
	}
}
