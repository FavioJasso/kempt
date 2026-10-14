package emit

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// col builds a column whose SQLType map is rendered from t, as plan.Build does.
func col(name string, t infer.Type, comment string) plan.Column {
	c := plan.Column{Name: name, Type: t, Comment: comment, SQLType: map[string]string{}}
	for _, d := range infer.Dialects {
		c.SQLType[d] = t.SQL(d)
	}
	return c
}

func samplePlan() *plan.Plan {
	return &plan.Plan{
		Sheet:  "Budget Summary",
		Table:  "budget",
		Region: "A3:F9",
		Columns: []plan.Column{
			col("id", infer.Type{Base: infer.Int}, ""),
			col("amount", infer.Type{Base: infer.Decimal, Precision: 10, Scale: 2}, "Amount [unit: $000s; values not rescaled]"),
			col("name", infer.Type{Base: infer.Text, MaxLen: 20}, "Owner's name"),
			col("active", infer.Type{Base: infer.Bool}, `Active\?`),
			col("day", infer.Type{Base: infer.Date}, "Day"),
			col("at", infer.Type{Base: infer.Timestamp}, "Line1\nLine2"),
		},
	}
}

func TestDDL(t *testing.T) {
	tests := []struct {
		dialect string
		want    string
	}{
		{infer.Postgres, `CREATE TABLE "budget" (
  "id" INTEGER,
  "amount" NUMERIC(10,2),
  "name" TEXT,
  "active" BOOLEAN,
  "day" DATE,
  "at" TIMESTAMP
);
COMMENT ON COLUMN "budget"."amount" IS 'Amount [unit: $000s; values not rescaled]';
COMMENT ON COLUMN "budget"."name" IS 'Owner''s name';
COMMENT ON COLUMN "budget"."active" IS 'Active\?';
COMMENT ON COLUMN "budget"."day" IS 'Day';
COMMENT ON COLUMN "budget"."at" IS 'Line1
Line2';
COMMENT ON TABLE "budget" IS 'Loaded by kempt from Budget Summary A3:F9';
`},
		{infer.MySQL, "CREATE TABLE `budget` (\n" +
			"  `id` INT,\n" +
			"  `amount` DECIMAL(10,2) COMMENT 'Amount [unit: $000s; values not rescaled]',\n" +
			"  `name` VARCHAR(32) COMMENT 'Owner''s name',\n" +
			"  `active` BOOLEAN COMMENT 'Active\\\\?',\n" +
			"  `day` DATE COMMENT 'Day',\n" +
			"  `at` DATETIME(6) COMMENT 'Line1\nLine2'\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Loaded by kempt from Budget Summary A3:F9';\n"},
		{infer.SQLite, `CREATE TABLE "budget" ( -- Loaded by kempt from Budget Summary A3:F9
  "id" INTEGER,
  "amount" NUMERIC(10,2), -- Amount [unit: $000s; values not rescaled]
  "name" TEXT, -- Owner's name
  "active" INTEGER, -- Active\?
  "day" DATE, -- Day
  "at" TIMESTAMP -- Line1 Line2
);
`},
	}
	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			if got := DDL(samplePlan(), tt.dialect, DDLOptions{}); got != tt.want {
				t.Errorf("DDL mismatch\n got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestDDLOptions(t *testing.T) {
	tests := []struct {
		dialect string
		opts    DDLOptions
		prefix  string
		contain string
	}{
		{infer.Postgres, DDLOptions{Schema: "demo_ab12", IfNotExists: true},
			`CREATE TABLE IF NOT EXISTS "demo_ab12"."budget" (`, `COMMENT ON COLUMN "demo_ab12"."budget"."day" IS 'Day';`},
		{infer.Postgres, DDLOptions{Schema: `we"ird`}, `CREATE TABLE "we""ird"."budget" (`, `COMMENT ON TABLE "we""ird"."budget"`},
		{infer.MySQL, DDLOptions{Schema: "shop", IfNotExists: true}, "CREATE TABLE IF NOT EXISTS `shop`.`budget` (", "ENGINE=InnoDB"},
		{infer.SQLite, DDLOptions{Schema: "main", IfNotExists: true}, `CREATE TABLE IF NOT EXISTS "main"."budget" (`, `"day" DATE, -- Day`},
		{"POSTGRESQL", DDLOptions{}, `CREATE TABLE "budget" (`, `"at" TIMESTAMP`},
		{"oracle", DDLOptions{}, `CREATE TABLE "budget" (`, `"amount" NUMERIC(10,2)`}, // unknown → postgres
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.dialect, tt.opts.Schema), func(t *testing.T) {
			got := DDL(samplePlan(), tt.dialect, tt.opts)
			if !strings.HasPrefix(got, tt.prefix) || !strings.Contains(got, tt.contain) {
				t.Errorf("DDL = %s\nwant prefix %q and %q", got, tt.prefix, tt.contain)
			}
		})
	}
}

func TestDDLSQLTypeFallback(t *testing.T) {
	p := &plan.Plan{Sheet: "S", Table: "t", Columns: []plan.Column{
		{Name: "code", Type: infer.Type{Base: infer.Text, MaxLen: 5}, SQLType: map[string]string{infer.Postgres: "VARCHAR(5)"}},
		{Name: "qty", Type: infer.Type{Base: infer.BigInt}},
	}}
	tests := []struct{ dialect, want string }{
		{infer.Postgres, "CREATE TABLE \"t\" (\n  \"code\" VARCHAR(5),\n  \"qty\" BIGINT\n);\nCOMMENT ON TABLE \"t\" IS 'Loaded by kempt from S';\n"},
		{infer.MySQL, "CREATE TABLE `t` (\n  `code` VARCHAR(8),\n  `qty` BIGINT\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Loaded by kempt from S';\n"},
		{infer.SQLite, "CREATE TABLE \"t\" ( -- Loaded by kempt from S\n  \"code\" TEXT,\n  \"qty\" INTEGER\n);\n"},
	}
	for _, tt := range tests {
		if got := DDL(p, tt.dialect, DDLOptions{}); got != tt.want {
			t.Errorf("%s: DDL =\n%s\nwant\n%s", tt.dialect, got, tt.want)
		}
	}
}

func repeat(n, times int) []int {
	out := make([]int, times)
	for i := range out {
		out[i] = n
	}
	return out
}

func TestDDLMySQLRowSize(t *testing.T) {
	// A negative length makes a BIGINT column.
	text := func(n int) infer.Type {
		if n < 0 {
			return infer.Type{Base: infer.BigInt}
		}
		return infer.Type{Base: infer.Text, MaxLen: n}
	}
	tests := []struct {
		name      string
		lens      []int
		wantMoved []string
	}{
		{"fits", []int{1000, 1000, 500, 10}, nil},
		{"just under budget", []int{512, 16, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000}, nil},
		{"widest first, leftmost on ties", []int{200, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000},
			[]string{"c1", "c2"}},
		{"many wide", []int{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000},
			[]string{"c0", "c1", "c2", "c3", "c4"}},
		// Fixed-width columns count too: 58 VARCHAR(256) plus 800 BIGINTs overflow.
		{"fixed widths count", append(repeat(256, 58), repeat(-8, 800)...), []string{"c0"}},
		{"beyond 1024 already TEXT", []int{5000, 1000, 1000}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &plan.Plan{Sheet: "S", Table: "wide"}
			for i, n := range tt.lens {
				p.Columns = append(p.Columns, col(fmt.Sprintf("c%d", i), text(n), ""))
			}
			got := DDL(p, infer.MySQL, DDLOptions{})
			total := 0
			for i := range p.Columns {
				line := fmt.Sprintf("  `c%d` ", i)
				var typ string
				for _, l := range strings.Split(got, "\n") {
					if strings.HasPrefix(l, line) {
						typ = strings.TrimSuffix(strings.TrimPrefix(l, line), ",")
					}
				}
				total += mysqlRowBytes(typ)
			}
			if total > mysqlRowLimit {
				t.Errorf("VARCHAR bytes = %d, over budget:\n%s", total, got)
			}
			if tt.wantMoved == nil {
				if strings.HasPrefix(got, "--") {
					t.Errorf("unexpected note:\n%s", got)
				}
				return
			}
			note := "-- kempt: " + strings.Join(tt.wantMoved, ", ") + " changed from VARCHAR to TEXT"
			if !strings.HasPrefix(got, note) {
				t.Errorf("DDL =\n%s\nwant note %q", got, note)
			}
			for _, name := range tt.wantMoved {
				if !strings.Contains(got, "`"+name+"` TEXT") {
					t.Errorf("%s not switched to TEXT:\n%s", name, got)
				}
			}
		})
	}
}

func TestDDLMySQLLongComment(t *testing.T) {
	p := &plan.Plan{Sheet: strings.Repeat("s", 3000), Table: "t", Columns: []plan.Column{
		col("a", infer.Type{Base: infer.Int}, strings.Repeat("é", 1500)),
	}}
	got := DDL(p, infer.MySQL, DDLOptions{})
	if !strings.Contains(got, "COMMENT '"+strings.Repeat("é", 1024)+"'") {
		t.Errorf("column comment not truncated to 1024 characters")
	}
	if !strings.Contains(got, "COMMENT='Loaded by kempt from "+strings.Repeat("s", 2048-len("Loaded by kempt from "))+"';") {
		t.Errorf("table comment not truncated to 2048 characters")
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct{ dialect, name, want string }{
		{infer.Postgres, "order_id", `"order_id"`},
		{infer.Postgres, `a"b`, `"a""b"`},
		{infer.Postgres, "select", `"select"`},
		{infer.Postgres, "", `""`},
		{infer.SQLite, `x""y`, `"x""""y"`},
		{infer.SQLite, "naïve", `"naïve"`},
		{infer.MySQL, "order_id", "`order_id`"},
		{infer.MySQL, "a`b", "`a``b`"},
		{infer.MySQL, `a"b`, "`a\"b`"},
		{"MySQL", "x", "`x`"},
		{"unknown", "x", `"x"`},
	}
	for _, tt := range tests {
		if got := QuoteIdent(tt.dialect, tt.name); got != tt.want {
			t.Errorf("QuoteIdent(%q, %q) = %s, want %s", tt.dialect, tt.name, got, tt.want)
		}
	}
}

func TestQualifiedTable(t *testing.T) {
	tests := []struct{ dialect, schema, table, want string }{
		{infer.Postgres, "", "t", `"t"`},
		{infer.Postgres, "public", "t", `"public"."t"`},
		{infer.Postgres, "a.b", `c"d`, `"a.b"."c""d"`},
		{infer.MySQL, "", "t", "`t`"},
		{infer.MySQL, "db", "t", "`db`.`t`"},
		{infer.SQLite, "main", "t", `"main"."t"`},
	}
	for _, tt := range tests {
		if got := QualifiedTable(tt.dialect, tt.schema, tt.table); got != tt.want {
			t.Errorf("QualifiedTable(%q, %q, %q) = %s, want %s", tt.dialect, tt.schema, tt.table, got, tt.want)
		}
	}
}

func TestLiteral(t *testing.T) {
	day := normalize.NewDate(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	ts := normalize.NewTimestamp(time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC))
	tsFrac := normalize.NewTimestamp(time.Date(2026, 3, 15, 10, 30, 0, 123456000, time.UTC))
	tests := []struct {
		dialect string
		v       any
		want    string
	}{
		{infer.Postgres, nil, "NULL"},
		{infer.MySQL, nil, "NULL"},
		{infer.SQLite, nil, "NULL"},
		{infer.Postgres, true, "TRUE"},
		{infer.Postgres, false, "FALSE"},
		{infer.MySQL, true, "TRUE"},
		{infer.SQLite, true, "1"},
		{infer.SQLite, false, "0"},
		{infer.Postgres, int64(-9223372036854775808), "-9223372036854775808"},
		{infer.MySQL, int64(42), "42"},
		{infer.SQLite, 7, "7"},
		{infer.Postgres, normalize.Decimal("-1234.50"), "-1234.50"},
		{infer.MySQL, normalize.Decimal("0.5"), "0.5"},
		{infer.Postgres, normalize.Decimal("1; DROP TABLE t"), "'1; DROP TABLE t'"},
		{infer.Postgres, normalize.Decimal("1e5"), "'1e5'"},
		{infer.Postgres, normalize.Decimal("12345678901234567890.12"), "12345678901234567890.12"},
		{infer.MySQL, normalize.Decimal("-12345678901234567890.12"), "-12345678901234567890.12"},
		{infer.SQLite, normalize.Decimal("12345678901234567890.12"), "'12345678901234567890.12'"}, // bare would be REAL
		{infer.SQLite, normalize.Decimal("-0.25"), "'-0.25'"},
		{infer.MySQL, normalize.Decimal("x'; --"), `'x''; --'`},
		{infer.Postgres, normalize.Decimal(""), "''"},
		{infer.Postgres, day, "'2026-03-15'"},
		{infer.MySQL, day, "'2026-03-15'"},
		{infer.Postgres, ts, "'2026-03-15 10:30:00'"},
		{infer.SQLite, tsFrac, "'2026-03-15 10:30:00.123456'"},
		{infer.Postgres, "O'Reilly", "'O''Reilly'"},
		{infer.SQLite, "O'Reilly", "'O''Reilly'"},
		{infer.MySQL, "O'Reilly", "'O''Reilly'"},
		{infer.Postgres, `C:\new`, `'C:\new'`},
		{infer.SQLite, `C:\new`, `'C:\new'`},
		{infer.MySQL, `C:\new`, `'C:\\new'`},
		{infer.MySQL, `\'`, `'\\'''`},
		{infer.MySQL, "a\x00b\x1a", `'a\0b\Z'`},
		{infer.Postgres, "naïve 日本語 🙂", "'naïve 日本語 🙂'"},
		{infer.Postgres, "line1\nline2", "'line1\nline2'"},
		{infer.Postgres, "", "''"},
		{infer.Postgres, 1.5, "'1.5'"},
	}
	for _, tt := range tests {
		if got := Literal(tt.dialect, tt.v); got != tt.want {
			t.Errorf("Literal(%s, %#v) = %s, want %s", tt.dialect, tt.v, got, tt.want)
		}
	}
}

func TestMySQLComment(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"naïve café 日本語", "naïve café 日本語"},
		{"ok 🙂 done", "ok U+1F642 done"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := mysqlComment(tt.in); got != tt.want {
			t.Errorf("mysqlComment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDDLNoComments(t *testing.T) {
	p := samplePlan()
	for _, d := range infer.Dialects {
		with := DDL(p, d, DDLOptions{})
		without := DDL(p, d, DDLOptions{NoComments: true})
		if !strings.Contains(strings.ToUpper(with), "COMMENT") && d != infer.SQLite {
			t.Fatalf("%s: sample plan should have comments:\n%s", d, with)
		}
		for _, c := range p.Columns {
			if c.Comment != "" && strings.Contains(without, c.Comment) {
				t.Errorf("%s: comment %q still in\n%s", d, c.Comment, without)
			}
		}
		if strings.Contains(without, "COMMENT") || strings.Contains(without, "Loaded by kempt") {
			t.Errorf("%s: comments left in\n%s", d, without)
		}
	}
	if p.Columns[0].Comment == "" && samplePlan().Columns[0].Comment != "" {
		t.Error("NoComments changed the plan")
	}
}
