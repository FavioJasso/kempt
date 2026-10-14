package emit

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

func TestSQLiteExact(t *testing.T) {
	tests := []struct {
		d    string
		want bool
	}{
		{"0", true},
		{"1234.50", true},
		{"-0.25", true},
		{"0.000000000000001", true},
		{"123456789012345", true},
		{"12345678901234.5", true},
		{"9223372036854775807", true},  // fits int64: stored as INTEGER
		{"-9223372036854775808", true}, // likewise
		{"9223372036854775807.00", true},
		{"100000000000000000000", true}, // one significant digit
		{"1234567890123456", true},      // 16 digits, but an int64
		{"1234567890123456.5", false},   // 17 digits
		{"9223372036854775808", false},  // just past int64
		{"12345678901234567890.12", false},
		{"0.1234567890123456", false},
		{"99999999999999999999999999999999999999", false},
		{"1e5", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := sqliteExact(tt.d); got != tt.want {
			t.Errorf("sqliteExact(%q) = %v, want %v", tt.d, got, tt.want)
		}
	}
}

// TestSQLiteDecimalPrecision checks that decimals SQLite cannot hold exactly
// in a NUMERIC column are stored as TEXT, by Load and by DDL + InsertSQL.
func TestSQLiteDecimalPrecision(t *testing.T) {
	tests := []struct {
		name     string
		prec     int
		vals     []string
		wantType string
	}{
		{"short values stay numeric", 14, []string{"1234.50", "-0.25", "123456789012.34"}, "NUMERIC(14,2)"},
		{"precision above 15 becomes text", 19, []string{"9223372036854775807", "-9223372036854775808"}, "TEXT"},
		{"twenty digits become text", 22, []string{"1.00", "12345678901234567890.12"}, "TEXT"},
		{"38 digits become text", 38, []string{"999999999999999999999999999999999999.99"}, "TEXT"},
		{"big integer becomes text", 30, []string{"123456789012345678901234567890"}, "TEXT"},
		// A hand-built plan whose declared precision understates its values
		// still gets TEXT from the per-value check.
		{"long value under a short type becomes text", 15, []string{"1.00", "12345678901234567890.12"}, "TEXT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := infer.Type{Base: infer.Decimal, Precision: tt.prec, Scale: 2}
			p := &plan.Plan{Sheet: "S", Table: "d", Columns: []plan.Column{col("v", dec, "Value")}}
			for _, v := range tt.vals {
				p.Rows = append(p.Rows, []any{normalize.Decimal(v)})
			}
			p.Rows = append(p.Rows, []any{nil})
			ddl := DDL(p, infer.SQLite, DDLOptions{})
			if !strings.Contains(ddl, `"v" `+tt.wantType+" -- Value") {
				t.Errorf("DDL =\n%s\nwant column type %s", ddl, tt.wantType)
			}
			if note := strings.HasPrefix(ddl, "-- kempt: v stored as TEXT"); note != (tt.wantType == "TEXT") {
				t.Errorf("note = %v in\n%s", note, ddl)
			}
			for _, how := range []string{"load", "literal"} {
				path := filepath.Join(t.TempDir(), "x.db")
				db := openSQLite(t, path)
				if how == "load" {
					if _, err := Load(context.Background(), infer.SQLite, path, []*plan.Plan{p}, LoadOptions{}); err != nil {
						t.Fatal(err)
					}
				} else {
					for _, q := range append([]string{ddl}, InsertSQL(p, infer.SQLite, InsertOptions{})...) {
						if _, err := db.Exec(q); err != nil {
							t.Fatalf("%v\n%s", err, q)
						}
					}
				}
				rows, err := db.Query(`SELECT CAST("v" AS TEXT) FROM "d" ORDER BY rowid`)
				if err != nil {
					t.Fatal(err)
				}
				var got []*string
				for rows.Next() {
					var s *string
					if err := rows.Scan(&s); err != nil {
						t.Fatal(err)
					}
					got = append(got, s)
				}
				rows.Close()
				for i, v := range tt.vals {
					want := v
					if tt.wantType != "TEXT" {
						want = strings.TrimSuffix(strings.TrimRight(v, "0"), ".") // REAL drops trailing zeros
						if !strings.Contains(v, ".") {
							want = v
						}
					}
					if g := deref(got[i]); g != want {
						t.Errorf("%s: row %d = %s, want %s", how, i, g, want)
					}
				}
				if got[len(got)-1] != nil {
					t.Errorf("%s: NULL stored as %q", how, *got[len(got)-1])
				}
			}
		})
	}
}

func TestInnodbRecordSize(t *testing.T) {
	rep := func(n int, typ string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = typ
		}
		return out
	}
	// The ok flags were checked against MySQL 8.4 with innodb_strict_mode=ON.
	tests := []struct {
		name  string
		types []string
		ok    bool
	}{
		{"196 TEXT", rep(196, "TEXT"), true},
		{"197 TEXT", rep(197, "TEXT"), false},
		{"244 VARCHAR(8)", rep(244, "VARCHAR(8)"), true},
		{"245 VARCHAR(8)", rep(245, "VARCHAR(8)"), false},
		{"400 VARCHAR(8)", rep(400, "VARCHAR(8)"), false},
		{"190 TEXT + 9 DECIMAL", append(rep(190, "TEXT"), rep(9, "DECIMAL(38,2)")...), true},
		{"190 TEXT + 20 DECIMAL", append(rep(190, "TEXT"), rep(20, "DECIMAL(38,2)")...), false},
		{"1000 INT", rep(1000, "INT"), true},
		{"990 BIGINT", rep(990, "BIGINT"), true},
		{"150 TEXT + 120 DATETIME(6)", append(rep(150, "TEXT"), rep(120, "DATETIME(6)")...), true},
		{"600 VARCHAR(512)", rep(600, "VARCHAR(512)"), false},
	}
	for _, tt := range tests {
		if got := innodbRecordSize(tt.types) < innodbRecordLimit; got != tt.ok {
			t.Errorf("%s: size %d fits = %v, want %v", tt.name, innodbRecordSize(tt.types), got, tt.ok)
		}
	}
	if got := decimalBytes(38) + decimalBytes(0); got != 17 {
		t.Errorf("DECIMAL(38,0) = %d bytes, want 17", got)
	}
}

func TestDDLMySQLInnoDBNote(t *testing.T) {
	for _, n := range []int{244, 245} {
		p := &plan.Plan{Sheet: "S", Table: "wide"}
		for i := range n {
			p.Columns = append(p.Columns, col(fmt.Sprintf("c%d", i), infer.Type{Base: infer.Text, MaxLen: 8}, ""))
		}
		got := strings.Contains(DDL(p, infer.MySQL, DDLOptions{}), "SET SESSION innodb_strict_mode = OFF")
		if got != (n == 245) {
			t.Errorf("%d columns: strict-mode note = %v", n, got)
		}
	}
}

// failingPlan has a value the driver cannot convert, so its inserts fail
// after the table has been created.
func failingPlan(table string) *plan.Plan {
	p := rtPlan(table)
	p.Rows = append(p.Rows, []any{int64(6), struct{}{}})
	return p
}

func TestLoadSQLiteFailureLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		plans []*plan.Plan
		mode  string
	}{
		{"failed fresh load", []*plan.Plan{failingPlan("items")}, IfExistsFail},
		{"failed second table", []*plan.Plan{rtPlan("other"), failingPlan("third")}, IfExistsFail},
		{"failed replace", []*plan.Plan{failingPlan("existing")}, IfExistsReplace},
		{"failed append", []*plan.Plan{failingPlan("existing")}, IfExistsAppend},
		{"fail mode conflict", []*plan.Plan{rtPlan("new_one"), rtPlan("Existing")}, IfExistsFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.db")
			db := openSQLite(t, path)
			if _, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("existing")}, LoadOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(ctx, infer.SQLite, path, tt.plans, LoadOptions{IfExists: tt.mode}); err == nil {
				t.Fatal("load succeeded, want an error")
			}
			var tables string
			if err := db.QueryRow(`SELECT group_concat(name) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
				t.Fatal(err)
			}
			if tables != "existing" {
				t.Errorf("tables after failed load = %s, want existing", tables)
			}
			checkSQLiteValues(t, rtPlan("existing"), sqliteRows(t, db, rtPlan("existing")))
		})
	}
}

func TestLoadDuplicateTables(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		dialect string
		names   []string
		wantErr bool
	}{
		{infer.SQLite, []string{"a", "b"}, false},
		{infer.SQLite, []string{"a", "a"}, true},
		{infer.SQLite, []string{"Sales", "sales"}, true},
		{infer.MySQL, []string{"t", "T"}, true},
		{infer.Postgres, []string{"x", "y", "x"}, true},
	}
	for _, tt := range tests {
		var plans []*plan.Plan
		for _, n := range tt.names {
			plans = append(plans, rtPlan(n))
		}
		path := filepath.Join(t.TempDir(), "x.db") // never reached for the other dialects on error
		_, err := Load(ctx, tt.dialect, path, plans, LoadOptions{IfExists: IfExistsReplace})
		if tt.wantErr != (err != nil && strings.Contains(err.Error(), "two tables are named")) {
			t.Errorf("%s %v: err = %v, want duplicate error %v", tt.dialect, tt.names, err, tt.wantErr)
		}
	}
	// A nil plan anywhere is rejected before anything is written.
	path := filepath.Join(t.TempDir(), "y.db")
	if _, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("a"), nil}, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "nil plan") {
		t.Errorf("nil plan: err = %v", err)
	}
}

// TestLoadSQLiteWide checks that batches respect SQLite's bound-parameter
// limit for the widest table SQLite allows.
func TestLoadSQLiteWide(t *testing.T) {
	const cols, rows = 2000, 40
	p := &plan.Plan{Sheet: "S", Table: "wide"}
	for j := range cols {
		p.Columns = append(p.Columns, col(fmt.Sprintf("c%d", j), infer.Type{Base: infer.Int}, ""))
	}
	for i := range rows {
		row := make([]any, cols)
		for j := range row {
			row[j] = int64(i)
		}
		p.Rows = append(p.Rows, row)
	}
	path := filepath.Join(t.TempDir(), "w.db")
	res, err := Load(context.Background(), infer.SQLite, path, []*plan.Plan{p}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var n, sum int
	if err := openSQLite(t, path).QueryRow(`SELECT count(*), sum("c1999") FROM "wide"`).Scan(&n, &sum); err != nil {
		t.Fatal(err)
	}
	if res[0].Rows != rows || n != rows || sum != rows*(rows-1)/2 {
		t.Errorf("rows = %d/%d, sum = %d", res[0].Rows, n, sum)
	}
}

// weirdPlan has identifiers and values with every quoting hazard.
func weirdPlan(table string) *plan.Plan {
	names := []string{`a"b`, "c`d", `e\f`, "naïve 日本", "x; DROP TABLE y; --", "it's", "-- z", "/* w */"}
	p := &plan.Plan{Sheet: "We'ird \\ Sheet`\"", Table: table, Region: "A1:H2"}
	for i, n := range names {
		p.Columns = append(p.Columns, col(n, infer.Type{Base: infer.Text, MaxLen: 40},
			fmt.Sprintf("hdr %d: '\"`\\ -- */ /* \n\r end ;", i)))
	}
	row := make([]any, len(names))
	for i := range row {
		row[i] = fmt.Sprintf("v%d '\"`\\ \\' \\\\ -- ; \x1a ü 🙂", i)
	}
	p.Rows = [][]any{row}
	return p
}

var weirdTables = []string{`we"ird`, "we`ird", `back\slash`, "ünï 表"}

func TestSQLiteWeirdIdentifiers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "w.db")
	db := openSQLite(t, path)
	for _, tb := range weirdTables {
		p := weirdPlan(tb)
		for _, mode := range []string{IfExistsFail, IfExistsReplace, IfExistsAppend} {
			if _, err := Load(ctx, infer.SQLite, path, []*plan.Plan{p}, LoadOptions{IfExists: mode}); err != nil {
				t.Fatalf("%s %s: %v\n%s", tb, mode, err, DDL(p, infer.SQLite, DDLOptions{}))
			}
		}
		lit := weirdPlan(tb + "_lit")
		for _, q := range append([]string{DDL(lit, infer.SQLite, DDLOptions{})}, InsertSQL(lit, infer.SQLite, InsertOptions{})...) {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
		for _, name := range []string{tb, tb + "_lit"} {
			checkWeird(t, db, infer.SQLite, QuoteIdent(infer.SQLite, name), 0)
		}
	}
}

// checkWeird reads every column of a weird table back and compares it with
// the plan's values; extra counts rows beyond the first that must match too.
func checkWeird(t *testing.T, db *sql.DB, dialect, table string, extra int) {
	t.Helper()
	p := weirdPlan("x")
	for j, c := range p.Columns {
		rows, err := db.Query("SELECT " + QuoteIdent(dialect, c.Name) + " FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			if s != p.Rows[0][j] {
				t.Errorf("%s.%s = %q, want %q", table, c.Name, s, p.Rows[0][j])
			}
			n++
		}
		rows.Close()
		if n < 1 {
			t.Errorf("%s: no rows", table)
		}
	}
}
