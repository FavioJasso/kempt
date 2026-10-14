package emit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" for checkWeird

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// serverFailPlan has a text value in an INT column, which the database
// rejects after the table has been created.
func serverFailPlan(table string) *plan.Plan {
	p := rtPlan(table)
	p.Rows = append(p.Rows, []any{int64(6), true, "not a number"})
	return p
}

// mismatchPlan is rtPlan with column i retyped (or, for i < 0, an extra
// column), so appending it to an rtPlan table would change values.
func mismatchPlan(table string, i int, typ infer.Type) *plan.Plan {
	p := rtPlan(table)
	if i < 0 {
		p.Columns = append(p.Columns, col("extra", typ, "Extra"))
		for r := range p.Rows {
			p.Rows[r] = append(p.Rows[r], nil)
		}
		return p
	}
	p.Columns[i].Type = typ
	p.Columns[i].SQLType = nil
	return p
}

// failureCases are loads that must fail without changing a database that
// holds one loaded table "existing".
func failureCases(prefix string) []struct {
	name  string
	plans []*plan.Plan
	mode  string
} {
	return []struct {
		name  string
		plans []*plan.Plan
		mode  string
	}{
		{"failed fresh load", []*plan.Plan{serverFailPlan(prefix + "fresh")}, IfExistsFail},
		{"failed fresh append", []*plan.Plan{serverFailPlan(prefix + "fresh")}, IfExistsAppend},
		{"failed replace", []*plan.Plan{serverFailPlan(prefix + "existing")}, IfExistsReplace},
		{"failed append", []*plan.Plan{serverFailPlan(prefix + "existing")}, IfExistsAppend},
		{"fail mode conflict", []*plan.Plan{rtPlan(prefix + "fresh"), rtPlan(prefix + "existing")}, IfExistsFail},
		{"append missing column", []*plan.Plan{mismatchPlan(prefix+"existing", -1, infer.Type{Base: infer.Text, MaxLen: 5})}, IfExistsAppend},
		{"append decimals into int", []*plan.Plan{mismatchPlan(prefix+"existing", 2, infer.Type{Base: infer.Decimal, Precision: 12, Scale: 2})}, IfExistsAppend},
		{"append wider decimals", []*plan.Plan{mismatchPlan(prefix+"existing", 4, infer.Type{Base: infer.Decimal, Precision: 14, Scale: 4})}, IfExistsAppend},
		{"append timestamps into date", []*plan.Plan{mismatchPlan(prefix+"existing", 5, infer.Type{Base: infer.Timestamp})}, IfExistsAppend},
	}
}

func TestPostgresSafety(t *testing.T) {
	url := os.Getenv("KEMPT_TEST_PG")
	if url == "" {
		t.Skip("KEMPT_TEST_PG not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	schema := fmt.Sprintf(`kempt "safety" \ ü %d`, time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+QuoteIdent(infer.Postgres, schema)+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	tables := func() []string {
		t.Helper()
		rows, err := conn.Query(ctx, "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 ORDER BY 1", schema)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}

	// A failed load into a new schema leaves no schema behind.
	if _, err := Load(ctx, infer.Postgres, url, []*plan.Plan{serverFailPlan("x")}, LoadOptions{Schema: schema}); err == nil {
		t.Fatal("load succeeded, want an error")
	}
	var have bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&have); err != nil || have {
		t.Fatalf("schema exists after a failed load (err=%v)", err)
	}

	if _, err := Load(ctx, infer.Postgres, url, []*plan.Plan{rtPlan("existing")}, LoadOptions{Schema: schema}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range failureCases("") {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(ctx, infer.Postgres, url, tt.plans, LoadOptions{Schema: schema, IfExists: tt.mode})
			if err == nil {
				t.Fatal("load succeeded, want an error")
			}
			if strings.HasPrefix(tt.name, "append ") && !strings.Contains(err.Error(), "append to") {
				t.Errorf("err = %v, want the append check to refuse it", err)
			}
			if got := tables(); !slices.Equal(got, []string{"existing"}) {
				t.Errorf("tables = %v, want [existing]", got)
			}
			checkPGValues(t, conn, schema, rtPlan("existing"))
		})
	}
	// LoadPostgres (the web server's path) is atomic too.
	if _, err := LoadPostgres(ctx, conn, serverFailPlan("direct"), LoadOptions{Schema: schema}); err == nil {
		t.Error("LoadPostgres succeeded, want an error")
	}
	if got := tables(); !slices.Equal(got, []string{"existing"}) {
		t.Errorf("tables after LoadPostgres failure = %v", got)
	}

	// Identifiers and values with quoting hazards, loaded and as literals.
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tb := range weirdTables {
		p := weirdPlan(tb)
		for _, mode := range []string{IfExistsFail, IfExistsReplace} {
			if _, err := Load(ctx, infer.Postgres, url, []*plan.Plan{p}, LoadOptions{Schema: schema, IfExists: mode}); err != nil {
				t.Fatalf("%s %s: %v", tb, mode, err)
			}
		}
		lit := weirdPlan(tb + "_lit")
		for _, q := range append([]string{DDL(lit, infer.Postgres, DDLOptions{Schema: schema})}, InsertSQL(lit, infer.Postgres, InsertOptions{Schema: schema})...) {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
		for _, name := range []string{tb, tb + "_lit"} {
			checkWeird(t, db, infer.Postgres, QualifiedTable(infer.Postgres, schema, name), 0)
		}
		var comment string
		q := `SELECT col_description(a.attrelid, a.attnum) FROM pg_attribute a WHERE a.attrelid = to_regclass($1) AND a.attname = 'c` + "`" + `d'`
		if err := conn.QueryRow(ctx, q, QualifiedTable(infer.Postgres, schema, tb)).Scan(&comment); err != nil || comment != p.Columns[1].Comment {
			t.Errorf("comment = %q (%v), want %q", comment, err, p.Columns[1].Comment)
		}
	}
}

func TestMySQLSafety(t *testing.T) {
	dsn := os.Getenv("KEMPT_TEST_MYSQL")
	if dsn == "" {
		t.Skip("KEMPT_TEST_MYSQL not set")
	}
	ctx := context.Background()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() }) // runs after the table cleanup below
	prefix := fmt.Sprintf("kempt_safety_%d_", time.Now().UnixNano())
	tables := func() []string {
		t.Helper()
		rows, err := db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()
			AND (table_name LIKE ? OR table_name LIKE 'kempt\_new\_%' OR table_name LIKE 'kempt\_old\_%') ORDER BY 1`,
			strings.ReplaceAll(prefix, "_", `\_`)+"%")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.TrimPrefix(s, prefix))
		}
		return out
	}
	t.Cleanup(func() {
		for _, name := range tables() {
			if !strings.HasPrefix(name, "kempt_") {
				name = prefix + name
			}
			db.Exec("DROP TABLE IF EXISTS " + QuoteIdent(infer.MySQL, name))
		}
	})
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + QuoteIdent(infer.MySQL, prefix+table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if _, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{rtPlan(prefix + "existing")}, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range failureCases(prefix) {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(ctx, infer.MySQL, dsn, tt.plans, LoadOptions{IfExists: tt.mode, BatchRows: 2})
			if err == nil {
				t.Fatal("load succeeded, want an error")
			}
			if strings.HasPrefix(tt.name, "append ") && !strings.Contains(err.Error(), "append to") {
				t.Errorf("err = %v, want the append check to refuse it", err)
			}
			if got := tables(); !slices.Equal(got, []string{"existing"}) {
				t.Errorf("tables = %v, want [existing]", got)
			}
			if n := count("existing"); n != 5 {
				t.Errorf("existing has %d rows, want 5", n)
			}
		})
	}

	// A successful replace swaps the table and leaves no staging tables.
	p := rtPlan(prefix + "existing")
	p.Rows = p.Rows[:2]
	if res, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{p}, LoadOptions{IfExists: IfExistsReplace}); err != nil || res[0].Rows != 2 {
		t.Fatalf("replace = %+v, %v", res, err)
	}
	if got := tables(); !slices.Equal(got, []string{"existing"}) || count("existing") != 2 {
		t.Errorf("after replace: tables = %v, rows = %d", got, count("existing"))
	}

	// A table too wide for innodb_strict_mode, and one that needs several
	// batches to stay under the 65535 placeholder limit.
	wide := &plan.Plan{Sheet: "S", Table: prefix + "wide"}
	many := &plan.Plan{Sheet: "S", Table: prefix + "many"}
	wideRow, manyRow := []any{}, []any{}
	for j := range 600 {
		wide.Columns = append(wide.Columns, col(fmt.Sprintf("c%d", j), infer.Type{Base: infer.Text, MaxLen: 300}, ""))
		wideRow = append(wideRow, "abc")
	}
	for j := range 1000 {
		many.Columns = append(many.Columns, col(fmt.Sprintf("c%d", j), infer.Type{Base: infer.Int}, ""))
		manyRow = append(manyRow, int64(j))
	}
	wide.Rows = [][]any{wideRow, wideRow}
	for range 200 {
		many.Rows = append(many.Rows, manyRow)
	}
	res, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{wide, many}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Rows != 2 || res[1].Rows != 200 || count("wide") != 2 || count("many") != 200 {
		t.Errorf("results = %+v", res)
	}
	var strict int
	if err := db.QueryRow("SELECT @@SESSION.innodb_strict_mode").Scan(&strict); err != nil || strict != 1 {
		t.Errorf("innodb_strict_mode = %d (%v), want it left on", strict, err)
	}
	// Rows that really do not fit fail loudly and leave no table behind.
	big := *wide
	big.Table = prefix + "wide_big"
	big.Rows = [][]any{slices.Repeat([]any{strings.Repeat("x", 30)}, 600)}
	if _, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{&big}, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "Row size too large") {
		t.Errorf("oversized row: err = %v", err)
	}
	if got := tables(); slices.Contains(got, "wide_big") {
		t.Errorf("wide_big left behind: %v", got)
	}

	// Decimals with 38 digits round-trip exactly.
	dp := &plan.Plan{Sheet: "S", Table: prefix + "dec", Columns: []plan.Column{
		col("d", infer.Type{Base: infer.Decimal, Precision: 38, Scale: 2}, ""),
	}, Rows: [][]any{{normalize.Decimal("123456789012345678901234567890123456.78")}, {normalize.Decimal("-0.01")}}}
	if _, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{dp}, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	rows, err := db.Query("SELECT CAST(d AS CHAR) FROM " + QuoteIdent(infer.MySQL, prefix+"dec") + " ORDER BY d DESC")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		got = append(got, s)
	}
	rows.Close()
	if !slices.Equal(got, []string{"123456789012345678901234567890123456.78", "-0.01"}) {
		t.Errorf("decimals = %v", got)
	}

	// Identifiers and values with quoting hazards.
	for i, tb := range weirdTables {
		name := fmt.Sprintf("%sw%d %s", prefix, i, tb)
		p := weirdPlan(name)
		for _, mode := range []string{IfExistsFail, IfExistsReplace} {
			if _, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{p}, LoadOptions{IfExists: mode}); err != nil {
				t.Fatalf("%s %s: %v", name, mode, err)
			}
		}
		lit := weirdPlan(name + "_lit")
		for _, q := range append([]string{DDL(lit, infer.MySQL, DDLOptions{})}, InsertSQL(lit, infer.MySQL, InsertOptions{})...) {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
		for _, n := range []string{name, name + "_lit"} {
			checkWeird(t, db, infer.MySQL, QuoteIdent(infer.MySQL, n), 0)
		}
		var comment string
		if err := db.QueryRow("SELECT column_comment FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
			name, `e\f`).Scan(&comment); err != nil || comment != p.Columns[2].Comment {
			t.Errorf("comment = %q (%v), want %q", comment, err, p.Columns[2].Comment)
		}
	}
}
