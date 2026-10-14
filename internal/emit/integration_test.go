package emit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// TestPostgres runs against the database in $KEMPT_TEST_PG (a postgres://
// URL) inside a throwaway schema.
func TestPostgres(t *testing.T) {
	url := os.Getenv("KEMPT_TEST_PG")
	if url == "" {
		t.Skip("KEMPT_TEST_PG not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) }) // runs after the schema drop below
	schema := fmt.Sprintf("kempt_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+QuoteIdent(infer.Postgres, schema)+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	count := func(table string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+QualifiedTable(infer.Postgres, schema, table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Load creates the schema and copies every value type.
	p := rtPlan("rt")
	opts := LoadOptions{Schema: schema}
	res, err := Load(ctx, infer.Postgres, url, []*plan.Plan{p}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0] != (LoadResult{Table: schema + ".rt", Rows: 5}) {
		t.Fatalf("results = %+v", res)
	}
	checkPGValues(t, conn, schema, p)

	// Comments.
	var colComment, tableComment string
	q := `SELECT col_description(c.oid, a.attnum), obj_description(c.oid, 'pg_class')
		FROM pg_class c JOIN pg_namespace s ON s.oid = c.relnamespace JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE s.nspname = $1 AND c.relname = 'rt' AND a.attname = 'note'`
	if err := conn.QueryRow(ctx, q, schema).Scan(&colComment, &tableComment); err != nil {
		t.Fatal(err)
	}
	if colComment != p.Columns[7].Comment || tableComment != "Loaded by kempt from Round Trip A1:H6" {
		t.Errorf("comments = %q, %q", colComment, tableComment)
	}

	// If-exists modes.
	for _, s := range []struct {
		mode    string
		wantErr error
		want    int
	}{
		{"fail", ErrTableExists, 5},
		{"append", nil, 10},
		{"replace", nil, 5},
	} {
		opts.IfExists = s.mode
		_, err := Load(ctx, infer.Postgres, url, []*plan.Plan{p}, opts)
		if (s.wantErr == nil) != (err == nil) || (s.wantErr != nil && !errors.Is(err, s.wantErr)) {
			t.Fatalf("%s: err = %v, want %v", s.mode, err, s.wantErr)
		}
		if n := count("rt"); n != s.want {
			t.Errorf("%s: %d rows, want %d", s.mode, n, s.want)
		}
	}

	// A failed multi-table load leaves nothing behind.
	if _, err := Load(ctx, infer.Postgres, url, []*plan.Plan{rtPlan("fresh"), rtPlan("rt")}, LoadOptions{Schema: schema}); !errors.Is(err, ErrTableExists) {
		t.Fatalf("err = %v, want ErrTableExists", err)
	}
	var fresh bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+".fresh").Scan(&fresh); err != nil || fresh {
		t.Errorf("table fresh exists after a failed load (err=%v)", err)
	}

	// LoadPostgres on an existing connection, into an existing schema.
	direct := rtPlan("direct")
	if n, err := LoadPostgres(ctx, conn, direct, LoadOptions{Schema: schema}); err != nil || n != 5 {
		t.Fatalf("LoadPostgres = %d, %v", n, err)
	}
	checkPGValues(t, conn, schema, direct)

	// DDL and literal INSERTs execute and store the same values.
	lit := rtPlan("lit")
	if _, err := conn.Exec(ctx, DDL(lit, infer.Postgres, DDLOptions{Schema: schema})); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range InsertSQL(lit, infer.Postgres, InsertOptions{Schema: schema, BatchRows: 2}) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	checkPGValues(t, conn, schema, lit)
}

// checkPGValues reads every column as text and compares it to the clean
// value's display form (Postgres renders each type the way kempt does).
func checkPGValues(t *testing.T, conn *pgx.Conn, schema string, p *plan.Plan) {
	t.Helper()
	exprs := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		exprs[i] = QuoteIdent(infer.Postgres, c.Name) + "::text"
	}
	rows, err := conn.Query(context.Background(), "SELECT "+strings.Join(exprs, ", ")+" FROM "+
		QualifiedTable(infer.Postgres, schema, p.Table)+` ORDER BY "n"`)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]*string
	for rows.Next() {
		vals := make([]*string, len(p.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		got = append(got, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	compareText(t, p, got, normalize.Format)
}

// compareText checks stored text against want(v) for every non-NULL value.
func compareText(t *testing.T, p *plan.Plan, got [][]*string, want func(any) string) {
	t.Helper()
	if len(got) != len(p.Rows) {
		t.Fatalf("%s: got %d rows, want %d", p.Table, len(got), len(p.Rows))
	}
	for i, row := range p.Rows {
		for j, v := range row {
			g := got[i][j]
			switch {
			case v == nil && g != nil:
				t.Errorf("%s row %d %s = %q, want NULL", p.Table, i+1, p.Columns[j].Name, *g)
			case v != nil && (g == nil || *g != want(v)):
				t.Errorf("%s row %d %s = %q, want %q", p.Table, i+1, p.Columns[j].Name, deref(g), want(v))
			}
		}
	}
}

// TestMySQL runs against the database in $KEMPT_TEST_MYSQL (a go-sql-driver
// DSN such as user:pass@tcp(localhost:3306)/kempt).
func TestMySQL(t *testing.T) {
	dsn := os.Getenv("KEMPT_TEST_MYSQL")
	if dsn == "" {
		t.Skip("KEMPT_TEST_MYSQL not set")
	}
	ctx := context.Background()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() }) // registered first, so it runs after the drops below
	table := fmt.Sprintf("kempt_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, name := range []string{table, table + "_lit", table + "_lax"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + QuoteIdent(infer.MySQL, name)); err != nil {
				t.Errorf("drop %s: %v", name, err)
			}
		}
	})
	want := func(v any) string {
		switch x := v.(type) {
		case bool:
			return map[bool]string{true: "1", false: "0"}[x]
		case normalize.Timestamp:
			return x.Format("2006-01-02 15:04:05.000000")
		}
		return normalize.Format(v)
	}
	read := func(p *plan.Plan) [][]*string {
		exprs := make([]string, len(p.Columns))
		for i, c := range p.Columns {
			exprs[i] = "CAST(" + QuoteIdent(infer.MySQL, c.Name) + " AS CHAR)"
		}
		rows, err := db.Query("SELECT " + strings.Join(exprs, ", ") + " FROM " + QuoteIdent(infer.MySQL, p.Table) + " ORDER BY `n`")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][]*string
		for rows.Next() {
			vals := make([]*string, len(p.Columns))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, vals)
		}
		return out
	}

	p := rtPlan(table)
	for _, s := range []struct {
		mode    string
		wantErr error
		rows    int
	}{
		{"fail", nil, 5},
		{"fail", ErrTableExists, 5},
		{"append", nil, 10},
		{"replace", nil, 5},
	} {
		_, err := Load(ctx, infer.MySQL, dsn, []*plan.Plan{p}, LoadOptions{IfExists: s.mode, BatchRows: 2})
		if (s.wantErr == nil) != (err == nil) || (s.wantErr != nil && !errors.Is(err, s.wantErr)) {
			t.Fatalf("%s: err = %v, want %v", s.mode, err, s.wantErr)
		}
		if got := len(read(p)); got != s.rows {
			t.Errorf("%s: %d rows, want %d", s.mode, got, s.rows)
		}
	}
	compareText(t, p, read(p), want)

	// Loads set their own sql_mode: a server without strict mode, or with
	// NO_BACKSLASH_ESCAPES, must neither change values nor accept bad ones.
	lax := dsn + "?sql_mode=%27NO_BACKSLASH_ESCAPES%27"
	if strings.Contains(dsn, "?") {
		lax = dsn + "&sql_mode=%27NO_BACKSLASH_ESCAPES%27"
	}
	if _, err := Load(ctx, infer.MySQL, lax, []*plan.Plan{p}, LoadOptions{IfExists: "replace", BatchRows: 2}); err != nil {
		t.Fatal(err)
	}
	compareText(t, p, read(p), want)
	if _, err := Load(ctx, infer.MySQL, lax, []*plan.Plan{serverFailPlan(table + "_lax")}, LoadOptions{}); err == nil {
		t.Error("a non-strict session accepted text in an INT column")
	}

	lit := rtPlan(table + "_lit")
	if _, err := db.Exec(DDL(lit, infer.MySQL, DDLOptions{})); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range InsertSQL(lit, infer.MySQL, InsertOptions{}) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	compareText(t, lit, read(lit), want)
}
