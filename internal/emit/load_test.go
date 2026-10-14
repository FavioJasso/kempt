package emit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// rtPlan holds one column per clean value type plus a row number "n" for
// ordering, with tricky strings and a NULL in every column.
func rtPlan(table string) *plan.Plan {
	date := func(y int, m time.Month, d int) normalize.Date {
		return normalize.NewDate(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	}
	ts := func(y int, m time.Month, d, h, mi, s, ns int) normalize.Timestamp {
		return normalize.NewTimestamp(time.Date(y, m, d, h, mi, s, ns, time.UTC))
	}
	return &plan.Plan{
		Sheet:  "Round Trip",
		Table:  table,
		Region: "A1:H6",
		Columns: []plan.Column{
			col("n", infer.Type{Base: infer.Int}, ""),
			col("flag", infer.Type{Base: infer.Bool}, "Flag?"),
			col("qty", infer.Type{Base: infer.Int}, "Qty"),
			col("big", infer.Type{Base: infer.BigInt}, "Big"),
			col("amount", infer.Type{Base: infer.Decimal, Precision: 12, Scale: 2}, "Amount ($)"),
			col("day", infer.Type{Base: infer.Date}, "Day"),
			col("at", infer.Type{Base: infer.Timestamp}, "At"),
			col("note", infer.Type{Base: infer.Text, MaxLen: 60}, "Owner's \"note\"\nsecond line"),
		},
		Rows: [][]any{
			{int64(1), true, int64(42), int64(9223372036854775807), normalize.Decimal("1234.50"), date(2026, 3, 15), ts(2026, 3, 15, 10, 30, 0, 0), "O'Reilly"},
			{int64(2), false, int64(-7), int64(-9223372036854775808), normalize.Decimal("-0.25"), date(1999, 12, 31), ts(2026, 3, 15, 10, 30, 0, 123456000), `C:\new\'path'`},
			{int64(3), nil, nil, nil, nil, nil, nil, nil},
			{int64(4), true, int64(0), int64(0), normalize.Decimal("0.00"), date(2000, 2, 29), ts(1999, 12, 31, 23, 59, 59, 0), "naïve café 日本語 🙂 \"dq\""},
			{int64(5), false, int64(2147483647), int64(1), normalize.Decimal("9999999999.99"), date(1000, 1, 1), ts(9999, 12, 31, 0, 0, 0, 1000), "x');\nDROP TABLE rt; -- \\"},
		},
	}
}

// sqliteRows reads every column as (typeof, CAST AS TEXT) ordered by n.
func sqliteRows(t *testing.T, db *sql.DB, p *plan.Plan) [][]*string {
	t.Helper()
	exprs := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		exprs[i] = "CAST(" + QuoteIdent(infer.SQLite, c.Name) + " AS TEXT)"
	}
	rows, err := db.Query("SELECT " + strings.Join(exprs, ", ") + " FROM " + QuoteIdent(infer.SQLite, p.Table) + ` ORDER BY "n"`)
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkSQLiteValues compares stored values to the plan's clean values.
func checkSQLiteValues(t *testing.T, p *plan.Plan, got [][]*string) {
	t.Helper()
	if len(got) != len(p.Rows) {
		t.Fatalf("got %d rows, want %d", len(got), len(p.Rows))
	}
	for i, row := range p.Rows {
		for j, v := range row {
			g := got[i][j]
			name := p.Columns[j].Name
			switch x := v.(type) {
			case nil:
				if g != nil {
					t.Errorf("row %d %s = %q, want NULL", i+1, name, *g)
				}
			case normalize.Decimal:
				f, err := strconv.ParseFloat(deref(g), 64)
				if err != nil || f != x.Float() {
					t.Errorf("row %d %s = %q, want %s", i+1, name, deref(g), x)
				}
			default:
				want := normalize.Format(v)
				if b, ok := v.(bool); ok {
					want = map[bool]string{true: "1", false: "0"}[b]
				}
				if g == nil || *g != want {
					t.Errorf("row %d %s = %q, want %q", i+1, name, deref(g), want)
				}
			}
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLoadSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rt.db")
	p := rtPlan("rt")
	res, err := Load(ctx, infer.SQLite, path, []*plan.Plan{p}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0] != (LoadResult{Table: "rt", Rows: 5}) {
		t.Fatalf("results = %+v", res)
	}
	db := openSQLite(t, path)
	loaded := sqliteRows(t, db, p)
	checkSQLiteValues(t, p, loaded)

	// The same rows rendered as literals must store identically.
	lit := rtPlan("rt_lit")
	if _, err := db.Exec(DDL(lit, infer.SQLite, DDLOptions{})); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range InsertSQL(lit, infer.SQLite, InsertOptions{BatchRows: 2}) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	if got := sqliteRows(t, db, lit); fmt.Sprint(derefAll(got)) != fmt.Sprint(derefAll(loaded)) {
		t.Errorf("literal insert differs from bound insert:\n%v\n%v", derefAll(got), derefAll(loaded))
	}

	// Storage classes: bools and ints as INTEGER, dates and text as TEXT.
	var flag, day, at, note string
	if err := db.QueryRow(`SELECT typeof("flag"), typeof("day"), typeof("at"), typeof("note") FROM "rt" WHERE "n" = 1`).Scan(&flag, &day, &at, &note); err != nil {
		t.Fatal(err)
	}
	if flag != "integer" || day != "text" || at != "text" || note != "text" {
		t.Errorf("storage classes = %s %s %s %s", flag, day, at, note)
	}

	// Comments survive in sqlite_master.
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'rt'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-- Loaded by kempt from Round Trip A1:H6", `-- Owner's "note" second line`, "-- Amount ($)"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("sqlite_master sql lacks %q:\n%s", want, ddl)
		}
	}
}

func derefAll(rows [][]*string) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		for _, s := range r {
			out[i] = append(out[i], deref(s))
		}
	}
	return out
}

func TestLoadSQLiteIfExists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	db := openSQLite(t, path)
	p := rtPlan("items")
	steps := []struct {
		mode      string
		wantErr   error
		wantCount int
	}{
		{"", nil, 5},
		{"fail", ErrTableExists, 5},
		{"append", nil, 10},
		{"Append", nil, 15},
		{"replace", nil, 5},
		{"fail", ErrTableExists, 5},
		{"bogus", errAny, 5},
	}
	for i, s := range steps {
		res, err := Load(ctx, infer.SQLite, path, []*plan.Plan{p}, LoadOptions{IfExists: s.mode})
		switch {
		case s.wantErr == nil && err != nil:
			t.Fatalf("step %d (%s): %v", i, s.mode, err)
		case s.wantErr == errAny && err == nil, s.wantErr != nil && s.wantErr != errAny && !errors.Is(err, s.wantErr):
			t.Fatalf("step %d (%s): err = %v, want %v", i, s.mode, err, s.wantErr)
		case s.wantErr == nil && (len(res) != 1 || res[0].Rows != 5):
			t.Fatalf("step %d (%s): results = %+v", i, s.mode, res)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM "items"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != s.wantCount {
			t.Errorf("step %d (%s): %d rows, want %d", i, s.mode, n, s.wantCount)
		}
	}
}

var errAny = errors.New("any error")

func TestLoadSQLiteAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	db := openSQLite(t, path)
	if _, err := db.Exec(`CREATE TABLE "B" (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("a"), rtPlan("b")}, LoadOptions{})
	if !errors.Is(err, ErrTableExists) {
		t.Fatalf("err = %v, want ErrTableExists (table names are case-insensitive)", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'a'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("table a exists after a failed load (n=%d, err=%v)", n, err)
	}
}

func TestLoadSQL(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		batch int
		rows  int
	}{
		{"remainder only", 10, 3},
		{"full batches and remainder", 2, 5},
		{"exact batches", 3, 6},
		{"default batch", 0, 1201},
		{"no rows", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openSQLite(t, filepath.Join(t.TempDir(), "x.db"))
			p := &plan.Plan{Sheet: "S", Table: "t", Columns: []plan.Column{
				col("i", infer.Type{Base: infer.Int}, ""),
				col("s", infer.Type{Base: infer.Text}, ""),
			}}
			for i := range tt.rows {
				p.Rows = append(p.Rows, []any{int64(i), fmt.Sprint("v", i)})
			}
			n, err := LoadSQL(ctx, db, infer.SQLite, p, LoadOptions{BatchRows: tt.batch})
			if err != nil {
				t.Fatal(err)
			}
			var count, sum int
			if err := db.QueryRow(`SELECT count(*), coalesce(sum("i"), 0) FROM "t"`).Scan(&count, &sum); err != nil {
				t.Fatal(err)
			}
			if n != int64(tt.rows) || count != tt.rows || sum != tt.rows*(tt.rows-1)/2 {
				t.Errorf("n=%d count=%d sum=%d, want %d rows", n, count, sum, tt.rows)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	db := openSQLite(t, path)
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{"unsupported dialect", func() error {
			_, err := Load(ctx, "oracle", path, nil, LoadOptions{})
			return err
		}, "unsupported dialect"},
		{"bad if-exists", func() error {
			_, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("t")}, LoadOptions{IfExists: "merge"})
			return err
		}, "if-exists must be"},
		{"LoadSQL postgres", func() error {
			_, err := LoadSQL(ctx, db, infer.Postgres, rtPlan("t"), LoadOptions{})
			return err
		}, "supports mysql and sqlite"},
		{"no columns", func() error {
			_, err := LoadSQL(ctx, db, infer.SQLite, &plan.Plan{Table: "t"}, LoadOptions{})
			return err
		}, "no columns"},
		{"no table name", func() error {
			_, err := LoadSQL(ctx, db, infer.SQLite, &plan.Plan{Columns: rtPlan("t").Columns}, LoadOptions{})
			return err
		}, "no table name"},
		{"nil plan", func() error {
			_, err := LoadSQL(ctx, db, infer.SQLite, nil, LoadOptions{})
			return err
		}, "nil plan"},
		{"unknown schema", func() error {
			_, err := LoadSQL(ctx, db, infer.SQLite, rtPlan("t"), LoadOptions{Schema: "nope"})
			return err
		}, "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadSQLiteSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	res, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("t")}, LoadOptions{Schema: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Table != "main.t" || res[0].Rows != 5 {
		t.Errorf("results = %+v", res)
	}
	if _, err := Load(ctx, infer.SQLite, path, []*plan.Plan{rtPlan("t")}, LoadOptions{Schema: "main"}); !errors.Is(err, ErrTableExists) {
		t.Errorf("second load: err = %v, want ErrTableExists", err)
	}
}

func TestCheckIdent(t *testing.T) {
	long63, long64 := strings.Repeat("a", 63), strings.Repeat("a", 64)
	wide := strings.Repeat("é", 40) // 40 characters, 80 bytes
	tests := []struct {
		d, name string
		ok      bool
	}{
		{infer.Postgres, long63, true},
		{infer.Postgres, long64, false},
		{infer.Postgres, wide, false},
		{infer.MySQL, long64, true},
		{infer.MySQL, long64 + "a", false},
		{infer.MySQL, wide, true},
		{infer.SQLite, strings.Repeat("a", 200), true},
	}
	for _, tt := range tests {
		if err := checkIdent(tt.d, tt.name); (err == nil) != tt.ok {
			t.Errorf("checkIdent(%s, %d bytes) = %v, want ok=%v", tt.d, len(tt.name), err, tt.ok)
		}
	}
	p := rtPlan(long64)
	if _, err := Load(context.Background(), infer.Postgres, "postgres://unused", []*plan.Plan{p}, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "63") {
		t.Errorf("Load with a 64-byte Postgres table name: %v", err)
	}
}

func TestCheckAppend(t *testing.T) {
	num := func(p, s int64) existingColumn {
		return existingColumn{dataType: "numeric", precision: sql.NullInt64{Int64: p, Valid: true}, scale: sql.NullInt64{Int64: s, Valid: true}}
	}
	plain := func(dt string) existingColumn { return existingColumn{dataType: dt} }
	plan1 := func(typ infer.Type) *plan.Plan {
		return &plan.Plan{Table: "t", Columns: []plan.Column{col("v", typ, "")}}
	}
	dec := func(p, s int) infer.Type { return infer.Type{Base: infer.Decimal, Precision: p, Scale: s} }
	tests := []struct {
		name string
		have existingColumn
		typ  infer.Type
		ok   bool
	}{
		{"int into integer", plain("integer"), infer.Type{Base: infer.Int}, true},
		{"int into bigint", plain("bigint"), infer.Type{Base: infer.Int}, true},
		{"decimal into numeric with room", num(12, 2), dec(10, 2), true},
		{"decimal into unbounded numeric", plain("numeric"), dec(30, 10), true},
		{"decimal into integer", plain("integer"), dec(10, 2), false},
		{"decimal into tinyint bool", plain("tinyint"), dec(4, 1), false},
		{"more scale", num(12, 2), dec(12, 4), false},
		{"more integer digits", num(12, 2), dec(14, 2), false},
		{"bigint into narrow numeric", num(10, 0), infer.Type{Base: infer.BigInt}, false},
		{"bigint into wide numeric", num(20, 0), infer.Type{Base: infer.BigInt}, true},
		{"timestamp into date", plain("date"), infer.Type{Base: infer.Timestamp}, false},
		{"timestamp into timestamp", plain("timestamp without time zone"), infer.Type{Base: infer.Timestamp}, true},
		{"date into timestamp", plain("timestamp without time zone"), infer.Type{Base: infer.Date}, true},
		{"text into text", plain("text"), infer.Type{Base: infer.Text}, true},
	}
	for _, tt := range tests {
		err := checkAppend(map[string]existingColumn{"v": tt.have}, plan1(tt.typ), "t")
		if (err == nil) != tt.ok {
			t.Errorf("%s: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	if err := checkAppend(map[string]existingColumn{"other": plain("text")}, plan1(infer.Type{Base: infer.Text}), "t"); err == nil || !strings.Contains(err.Error(), "no column v") {
		t.Errorf("missing column: %v", err)
	}
	if err := checkAppend(map[string]existingColumn{"v": plain("text")}, &plan.Plan{Table: "t", Columns: []plan.Column{col("V", infer.Type{Base: infer.Text}, "")}}, "t"); err != nil {
		t.Errorf("column names match case-insensitively: %v", err)
	}
}
