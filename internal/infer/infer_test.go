package infer

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/normalize"
)

func str(raw string) *grid.Cell { return &grid.Cell{Kind: grid.String, Raw: raw} }

func num(v float64) *grid.Cell {
	return &grid.Cell{Kind: grid.Number, Raw: strconv.FormatFloat(v, 'f', -1, 64), Num: &v}
}

func serial(v float64) *grid.Cell {
	c := num(v)
	c.Kind, c.DateFmt = grid.Date, true
	return c
}

func boolean(v bool) *grid.Cell {
	if v {
		return &grid.Cell{Kind: grid.Bool, Raw: "TRUE"}
	}
	return &grid.Cell{Kind: grid.Bool, Raw: "FALSE"}
}

func excelErr(raw string) *grid.Cell { return &grid.Cell{Kind: grid.Error, Raw: raw} }

// col builds an Input for sheet "Data", column C, starting at sheet row 2.
func col(header string, cells ...*grid.Cell) Input {
	in := Input{Sheet: "Data", Name: strings.ToLower(strings.ReplaceAll(header, " ", "_")), Header: header, Col: 2, Cells: cells}
	for i := range cells {
		in.Rows = append(in.Rows, i+1)
	}
	return in
}

func strs(ss ...string) []*grid.Cell {
	cells := make([]*grid.Cell, len(ss))
	for i, s := range ss {
		cells[i] = str(s)
	}
	return cells
}

func date(y int, m time.Month, d int) normalize.Date {
	return normalize.NewDate(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

func stamp(y int, m time.Month, d, hh, mm int) normalize.Timestamp {
	return normalize.NewTimestamp(time.Date(y, m, d, hh, mm, 0, 0, time.UTC))
}

type dec struct {
	kind string
	rng  string
	conf float64
	has  []string // substrings of the reason
}

func TestColumn(t *testing.T) {
	units := make([]*grid.Cell, 0, 12)
	for i := 1; i <= 12; i++ {
		if i == 9 {
			units = append(units, str("see note"))
		} else {
			units = append(units, num(float64(i)))
		}
	}
	unitsText := []any{"1", "2", "3", "4", "5", "6", "7", "8", "see note", "10", "11", "12"}
	unitsInt := []any{int64(1), int64(2), int64(3), int64(4), int64(5), int64(6), int64(7), int64(8), nil, int64(10), int64(11), int64(12)}
	mixedOrder := strs("03/15/2026", "03/16/2026", "15/03/2026", "03/17/2026", "03/18/2026")
	sparse := col("Code", str("N/A"), str("X1"), str("-"))
	sparse.Rows = []int{4, 9, 12}

	tests := []struct {
		name   string
		in     Input
		opts   Options
		typ    Type
		values []any
		unit   string
		pct    bool
		nulls  []int
		decs   []dec
	}{
		{
			name:   "all null column is text",
			in:     col("Empty", nil, str(""), str("   "), &grid.Cell{}),
			typ:    Type{Base: Text},
			values: []any{nil, nil, nil, nil},
		},
		{
			name:   "null tokens become NULL",
			in:     col("Region Code", str("NE"), str("NA"), str("SW"), str("—"), str(" n/a ")),
			typ:    Type{Base: Text, MaxLen: 2},
			values: []any{"NE", nil, "SW", nil, nil},
			nulls:  []int{1, 3, 4},
			decs: []dec{{grid.KNullToken, "Data!C3:C6", 0.95,
				[]string{"Read 3 values in column region_code as NULL", `C3 ("NA"), C5 ("—"), C6 (" n/a ")`}}},
		},
		{
			name:   "excel errors become NULL",
			in:     col("Margin", num(0.25), excelErr("#DIV/0!"), str("#N/A"), num(0.5)),
			typ:    Type{Base: Decimal, Precision: 3, Scale: 2},
			values: []any{normalize.Decimal("0.25"), nil, nil, normalize.Decimal("0.5")},
			nulls:  []int{1, 2},
			decs:   []dec{{grid.KExcelError, "Data!C3:C4", 0.9, []string{"Excel error values", `C3 ("#DIV/0!"), C4 ("#N/A")`}}},
		},
		{
			name:   "bool cells, words, and formula results",
			in:     col("Paid", boolean(true), str("no"), str(" Yes "), boolean(false), &grid.Cell{Kind: grid.Formula, Raw: "TRUE", Formula: "A1>0"}),
			typ:    Type{Base: Bool},
			values: []any{true, false, true, false, true},
		},
		{
			name:   "1 and 0 are ints, not bools",
			in:     col("Flag", strs("1", "0", "1")...),
			typ:    Type{Base: Int},
			values: []any{int64(1), int64(0), int64(1)},
		},
		{
			name:   "int from numbers and text",
			in:     col("Order ID", num(1001), str("1,234"), str("-5"), str(" 7 ")),
			typ:    Type{Base: Int},
			values: []any{int64(1001), int64(1234), int64(-5), int64(7)},
		},
		{
			name:   "beyond int32 promotes to bigint",
			in:     col("Big", num(1), str("3000000000")),
			typ:    Type{Base: BigInt},
			values: []any{int64(1), int64(3000000000)},
		},
		{
			name:   "beyond int64 promotes to decimal(p,0)",
			in:     col("Huge", str("1"), str("123456789012345678901234")),
			typ:    Type{Base: Decimal, Precision: 24, Scale: 0},
			values: []any{normalize.Decimal("1"), normalize.Decimal("123456789012345678901234")},
		},
		{
			name:   "decimal precision and scale from observed values",
			in:     col("Price", str("1.50"), str("-1234.5"), num(0.125), num(899)),
			typ:    Type{Base: Decimal, Precision: 7, Scale: 3},
			values: []any{normalize.Decimal("1.50"), normalize.Decimal("-1234.5"), normalize.Decimal("0.125"), normalize.Decimal("899")},
		},
		{
			name: "decimal precision capped at 38",
			in:   col("Wide", str(strings.Repeat("9", 30)+".5"), str("0."+strings.Repeat("1", 20))),
			typ:  Type{Base: Decimal, Precision: 38, Scale: 8},
		},
		{
			name:   "zip codes with leading zeros stay text",
			in:     col("Zip", strs("02134", "10001", "00501", "94105", "60601")...),
			typ:    Type{Base: Text, MaxLen: 5},
			values: []any{"02134", "10001", "00501", "94105", "60601"},
		},
		{
			name: "currency and accounting negatives",
			in:   col("Amount", strs("$1,200.50", "$89.99", "—", "75.00-", "(120.00)", "$8.75")...),
			typ:  Type{Base: Decimal, Precision: 6, Scale: 2},
			values: []any{normalize.Decimal("1200.50"), normalize.Decimal("89.99"), nil,
				normalize.Decimal("-75.00"), normalize.Decimal("-120.00"), normalize.Decimal("8.75")},
			unit:  "$",
			nulls: []int{2},
			decs: []dec{
				{grid.KStripCurrency, "Data!C2:C7", 0.95, []string{"symbol $ from 3 values in column amount", `C2 ("$1,200.50" → 1200.50)`}},
				{grid.KNullToken, "Data!C4", 0.95, []string{`C4 ("—")`}},
				{grid.KAccountingNeg, "Data!C5:C6", 0.95, []string{`C5 ("75.00-" → -75.00), C6 ("(120.00)" → -120.00)`}},
			},
		},
		{
			name:   "currency on fewer than half the values is not the unit",
			in:     col("Fee", strs("$5", "6", "7")...),
			typ:    Type{Base: Int},
			values: []any{int64(5), int64(6), int64(7)},
			decs:   []dec{{grid.KStripCurrency, "Data!C2", 0.95, []string{"1 value"}}},
		},
		{
			name:   "mixed currencies",
			in:     col("Cost", strs("$5", "€6")...),
			typ:    Type{Base: Int},
			values: []any{int64(5), int64(6)},
			decs:   []dec{{grid.KStripCurrency, "Data!C2:C3", 0.5, []string{"$ and €", "mixes currencies"}}},
		},
		{
			name:   "percent column",
			in:     col("Discount", strs("12%", "5 %", "N/A", "12.5%")...),
			typ:    Type{Base: Decimal, Precision: 3, Scale: 1},
			values: []any{normalize.Decimal("12"), normalize.Decimal("5"), nil, normalize.Decimal("12.5")},
			unit:   "%",
			pct:    true,
			nulls:  []int{2},
			decs: []dec{
				{grid.KPercent, "Data!C2:C5", 0.9, []string{"not divided by 100", "suffixed _pct", `C2 ("12%" → 12)`}},
				{grid.KNullToken, "Data!C4", 0.95, nil},
			},
		},
		{
			name:   "mixed percent and plain numbers",
			in:     col("Change", strs("5%", "6", "7.5")...),
			typ:    Type{Base: Decimal, Precision: 2, Scale: 1},
			values: []any{normalize.Decimal("5"), normalize.Decimal("6"), normalize.Decimal("7.5")},
			decs:   []dec{{grid.KPercent, "Data!C2", 0.5, []string{"Only 1 of 3 values", "not suffixed _pct"}}},
		},
		{
			name:   "serials by date number format ignore the header",
			in:     col("Shipped", serial(46096), serial(46097.5)),
			typ:    Type{Base: Timestamp},
			values: []any{stamp(2026, 3, 15, 0, 0), stamp(2026, 3, 16, 12, 0)},
			decs:   []dec{{grid.KExcelSerialDate, "Data!C2:C3", 0.95, []string{"date number format", `C2 ("46096" → 2026-03-15 00:00:00)`}}},
		},
		{
			name:   "serials by header hint",
			in:     col("Invoice Date", num(46100), num(46111)),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 19), date(2026, 3, 30)},
			decs:   []dec{{grid.KExcelSerialDate, "Data!C2:C3", 0.8, []string{`header "Invoice Date"`, `C3 ("46111" → 2026-03-30)`}}},
		},
		{
			name:   "no header hint keeps serial-range numbers as ints",
			in:     col("Invoice No", num(46100), num(46111)),
			typ:    Type{Base: Int},
			values: []any{int64(46100), int64(46111)},
		},
		{
			name:   "header hint needs every number to be a plausible serial",
			in:     col("Days", num(3), num(46100)),
			typ:    Type{Base: Int},
			values: []any{int64(3), int64(46100)},
		},
		{
			name:   "1904 date system",
			in:     col("Opened", serial(1)),
			opts:   Options{Date1904: true},
			typ:    Type{Base: Date},
			values: []any{date(1904, 1, 2)},
			decs:   []dec{{grid.KExcelSerialDate, "Data!C2", 0.95, nil}},
		},
		{
			name:   "text dates in mixed US formats",
			in:     col("Ship Date", strs("03/15/2026", "3/16/26", "Mar 17, 2026", "18-Mar-2026", "2026-03-19", "N/A")...),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 15), date(2026, 3, 16), date(2026, 3, 17), date(2026, 3, 18), date(2026, 3, 19), nil},
			nulls:  []int{5},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C3", 0.9, []string{"month/day (MM/DD)", `C2 ("03/15/2026")`}},
				{grid.KDateParse, "Data!C2:C6", 0.9, []string{"Parsed 5 text dates", "MM/DD/YYYY (1); M/D/YY (1); Mon D, YYYY (1); D-Mon-YYYY (1); YYYY-MM-DD (1)"}},
				{grid.KNullToken, "Data!C7", 0.95, nil},
			},
		},
		{
			name:   "ISO dates are already clean",
			in:     col("Signup Date", strs("2026-01-15", "2026-02-01")...),
			typ:    Type{Base: Date},
			values: []any{date(2026, 1, 15), date(2026, 2, 1)},
		},
		{
			name:   "xlsx ISO date cells",
			in:     col("When", &grid.Cell{Kind: grid.Date, Raw: "2026-03-15T00:00:00Z", DateFmt: true}),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 15)},
			decs:   []dec{{grid.KDateParse, "Data!C2", 0.9, []string{"YYYY-MM-DDTHH:MM:SSZ (1)"}}},
		},
		{
			name:   "a time of day makes timestamps",
			in:     col("Created", strs("2026-03-15 10:30", "2026-03-16")...),
			typ:    Type{Base: Timestamp},
			values: []any{stamp(2026, 3, 15, 10, 30), stamp(2026, 3, 16, 0, 0)},
			decs:   []dec{{grid.KDateParse, "Data!C2:C3", 0.9, []string{"into timestamp values", "YYYY-MM-DD HH:MM (1); YYYY-MM-DD (1)"}}},
		},
		{
			name:   "day-first evidence",
			in:     col("Event", strs("15/03/2026", "04/03/2026")...),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 15), date(2026, 3, 4)},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C3", 0.9, []string{"day/month (DD/MM)", `C2 ("15/03/2026")`}},
				{grid.KDateParse, "Data!C2:C3", 0.9, []string{"DD/MM/YYYY (2)"}},
			},
		},
		{
			name:   "ambiguous order defaults to month first",
			in:     col("Event Date", strs("03/04/2026", "05/06/2026")...),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 4), date(2026, 5, 6)},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C3", 0.5, []string{"--date-order dmy", `C2 ("03/04/2026")`}},
				{grid.KDateParse, "Data!C2:C3", 0.9, []string{"MM/DD/YYYY (2)"}},
			},
		},
		{
			name:   "equal day and month are not ambiguous",
			in:     col("Day", strs("03/03/2026", "2026-01-01")...),
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 3), date(2026, 1, 1)},
			decs:   []dec{{grid.KDateParse, "Data!C2:C3", 0.9, nil}},
		},
		{
			name:   "forced day-first order",
			in:     col("Event", strs("03/04/2026", "13/04/2026")...),
			opts:   Options{DateOrder: "dmy"},
			typ:    Type{Base: Date},
			values: []any{date(2026, 4, 3), date(2026, 4, 13)},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C3", 1, []string{"--date-order dmy was given"}},
				{grid.KDateParse, "Data!C2:C3", 0.9, nil},
			},
		},
		{
			name:   "mixed orders keep text",
			in:     col("Event", mixedOrder...),
			typ:    Type{Base: Text, MaxLen: 10},
			values: []any{"03/15/2026", "03/16/2026", "15/03/2026", "03/17/2026", "03/18/2026"},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C6", 0.5, []string{"mixes month/day", `1 value in the other order does not parse as a date: C4 ("15/03/2026")`}},
				{grid.KTypeWidened, "Data!C4", 0.6, []string{"4 of 5 values (80%) parse as date", "Use --lenient 0.8 to cast."}},
			},
		},
		{
			name:   "mixed orders under lenient",
			in:     col("Event", mixedOrder...),
			opts:   Options{Lenient: 0.8},
			typ:    Type{Base: Date},
			values: []any{date(2026, 3, 15), date(2026, 3, 16), nil, date(2026, 3, 17), date(2026, 3, 18)},
			nulls:  []int{2},
			decs: []dec{
				{grid.KDateOrder, "Data!C2:C6", 0.5, nil},
				{grid.KDateParse, "Data!C2:C6", 0.9, []string{"Parsed 4 text dates"}},
				{grid.KCastFailNull, "Data!C4", 0.8, nil},
			},
		},
		{
			name:   "stray string widens to text",
			in:     col("Units", units...),
			typ:    Type{Base: Text, MaxLen: 8},
			values: unitsText,
			decs: []dec{{grid.KTypeWidened, "Data!C10", 0.6,
				[]string{`In column units, 11 of 12 values (92%) parse as int; kept TEXT because of C10 ("see note"). Use --lenient 0.91 to cast.`}}},
		},
		{
			name:   "lenient casts and nulls failures",
			in:     col("Units", units...),
			opts:   Options{Lenient: 0.9},
			typ:    Type{Base: Int},
			values: unitsInt,
			nulls:  []int{8},
			decs: []dec{{grid.KCastFailNull, "Data!C10", 0.92,
				[]string{"Cast column units to int because 11 of 12 values (92%) parse, meeting --lenient 0.9", `the 1 value that did not parse became NULL: C10 ("see note")`}}},
		},
		{
			name:   "lenient below the threshold stays strict",
			in:     col("Units", units...),
			opts:   Options{Lenient: 0.95},
			typ:    Type{Base: Text, MaxLen: 8},
			values: unitsText,
			decs:   []dec{{grid.KTypeWidened, "Data!C10", 0.6, nil}},
		},
		{
			name:   "lenient of 1 is strict",
			in:     col("Units", units...),
			opts:   Options{Lenient: 1},
			typ:    Type{Base: Text, MaxLen: 8},
			values: unitsText,
			decs:   []dec{{grid.KTypeWidened, "Data!C10", 0.6, nil}},
		},
		{
			name:   "no widening note for fewer than 5 values",
			in:     col("Qty", num(1), num(2), str("x"), num(3)),
			typ:    Type{Base: Text, MaxLen: 1},
			values: []any{"1", "2", "x", "3"},
		},
		{
			name:   "no widening note below 80 percent",
			in:     col("Qty", num(1), num(2), str("x"), num(3), str("y")),
			typ:    Type{Base: Text, MaxLen: 1},
			values: []any{"1", "2", "x", "3", "y"},
		},
		{
			name:   "text column renders every cell kind",
			in:     col("Mixed", str("  Acme "), num(42), serial(46096), boolean(true), str("x")),
			typ:    Type{Base: Text, MaxLen: 10},
			values: []any{"Acme", "42", "2026-03-15", "TRUE", "x"},
			decs:   []dec{{grid.KTrimWhitespace, "Data!C2", 1, []string{`Trimmed leading or trailing whitespace from 1 value in column mixed: C2 ("  Acme ")`}}},
		},
		{
			name:   "max length counts runes",
			in:     col("Name", strs("Zoë", "日本語テキスト")...),
			typ:    Type{Base: Text, MaxLen: 7},
			values: []any{"Zoë", "日本語テキスト"},
		},
		{
			name:   "examples are capped at five",
			in:     col("Notes", strs("-", "-", "-", "-", "-", "-", "-")...),
			typ:    Type{Base: Text},
			values: make([]any, 7),
			nulls:  []int{0, 1, 2, 3, 4, 5, 6},
			decs:   []dec{{grid.KNullToken, "Data!C2:C8", 0.95, []string{`C6 ("-") and 2 more.`}}},
		},
		{
			name:   "ranges follow the source rows",
			in:     sparse,
			typ:    Type{Base: Text, MaxLen: 2},
			values: []any{nil, "X1", nil},
			nulls:  []int{0, 2},
			decs:   []dec{{grid.KNullToken, "Data!C5:C13", 0.95, []string{`C5 ("N/A"), C13 ("-")`}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Column(tc.in, tc.opts)
			if got.Type != tc.typ {
				t.Errorf("Type = %+v, want %+v", got.Type, tc.typ)
			}
			if tc.values != nil && !reflect.DeepEqual(got.Values, tc.values) {
				t.Errorf("Values = %#v\nwant     %#v", got.Values, tc.values)
			}
			if got.Unit != tc.unit || got.Pct != tc.pct {
				t.Errorf("Unit, Pct = %q, %v; want %q, %v", got.Unit, got.Pct, tc.unit, tc.pct)
			}
			var nulls []int
			for i, n := range got.Nulls {
				if n {
					nulls = append(nulls, i)
				}
			}
			if !reflect.DeepEqual(nulls, tc.nulls) {
				t.Errorf("Nulls at %v, want %v", nulls, tc.nulls)
			}
			if len(got.Values) != len(tc.in.Cells) || len(got.Nulls) != len(tc.in.Cells) {
				t.Errorf("len(Values), len(Nulls) = %d, %d; want %d", len(got.Values), len(got.Nulls), len(tc.in.Cells))
			}
			if len(got.Decisions) != len(tc.decs) {
				t.Fatalf("got %d decisions, want %d:\n%s", len(got.Decisions), len(tc.decs), dump(got.Decisions))
			}
			for i, w := range tc.decs {
				d := got.Decisions[i]
				if d.Kind != w.kind || d.Range != w.rng || d.Confidence != w.conf {
					t.Errorf("decision %d = %s %s %.2f, want %s %s %.2f", i, d.Kind, d.Range, d.Confidence, w.kind, w.rng, w.conf)
				}
				for _, s := range w.has {
					if !strings.Contains(d.Reason, s) {
						t.Errorf("decision %d (%s) reason %q lacks %q", i, d.Kind, d.Reason, s)
					}
				}
				if !strings.HasSuffix(d.Reason, ".") {
					t.Errorf("decision %d (%s) reason %q is not a sentence", i, d.Kind, d.Reason)
				}
			}
		})
	}
}

func dump(ds []grid.Decision) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.Kind + " " + d.Range + " " + strconv.FormatFloat(d.Confidence, 'f', 2, 64) + " " + d.Reason + "\n")
	}
	return b.String()
}

func TestColumnDeterministic(t *testing.T) {
	in := col("Ship Date", strs("03/15/2026", "3/16/26", "N/A", "Mar 17, 2026", "#REF!", "18-Mar-2026", "$5", "  x ")...)
	first := Column(in, Options{Lenient: 0.5})
	for range 20 {
		if got := Column(in, Options{Lenient: 0.5}); !reflect.DeepEqual(got, first) {
			t.Fatalf("Column is not deterministic:\n%+v\n%+v", got, first)
		}
	}
}

func TestColumnSQLTypes(t *testing.T) {
	tests := []struct {
		in   Input
		want map[string]string
	}{
		{col("Price", strs("1.50", "-1234.5")...), map[string]string{Postgres: "NUMERIC(6,2)", MySQL: "DECIMAL(6,2)", SQLite: "NUMERIC(6,2)"}},
		{col("Name", strs("Avery Chen", "Sam")...), map[string]string{Postgres: "TEXT", MySQL: "VARCHAR(16)", SQLite: "TEXT"}},
		{col("Paid", strs("yes", "no")...), map[string]string{Postgres: "BOOLEAN", MySQL: "BOOLEAN", SQLite: "INTEGER"}},
		{col("Big", strs("3000000000")...), map[string]string{Postgres: "BIGINT", MySQL: "BIGINT", SQLite: "INTEGER"}},
	}
	for _, tc := range tests {
		typ := Column(tc.in, Options{}).Type
		for dialect, want := range tc.want {
			if got := typ.SQL(dialect); got != want {
				t.Errorf("%s: %s.SQL(%s) = %q, want %q", tc.in.Header, typ, dialect, got, want)
			}
		}
	}
}
