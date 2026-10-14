package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/FavioJasso/kempt/internal/normalize"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.json from current output")

var (
	fixtures = filepath.Join("..", "..", "testdata", "fixtures")
	goldens  = filepath.Join("..", "..", "testdata", "golden")
)

// goldenCases maps each golden file to the inspect invocation that produces it.
var goldenCases = []struct {
	name string
	file string
	args []string
}{
	{"budget_report", "budget_report.xlsx", nil},
	{"budget_report.keep_totals", "budget_report.xlsx", []string{"--keep-totals"}},
	{"two_tables", "two_tables.xlsx", nil},
	{"merged_categories", "merged_categories.xlsx", nil},
	{"messy_values", "messy_values.xlsx", nil},
	{"messy_values.lenient", "messy_values.xlsx", []string{"--lenient", "0.9"}},
	{"ambiguous_dates", "ambiguous_dates.csv", nil},
	{"ambiguous_dates.dmy", "ambiguous_dates.csv", []string{"--date-order", "dmy"}},
	{"tidy", "tidy.csv", nil},
}

// run executes the CLI in-process and returns stdout.
func run(t *testing.T, args ...string) []byte {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("kempt %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.Bytes()
}

func TestGolden(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"inspect", "--json", filepath.Join(fixtures, c.file)}, c.args...)
			got := run(t, args...)
			path := filepath.Join(goldens, c.name+".json")
			if *update {
				if err := os.MkdirAll(goldens, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./cmd/kempt -run TestGolden -update)", err)
			}
			if !bytes.Equal(got, want) {
				gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
				for i := 0; i < max(len(gl), len(wl)); i++ {
					var g, w string
					if i < len(gl) {
						g = gl[i]
					}
					if i < len(wl) {
						w = wl[i]
					}
					if g != w {
						t.Fatalf("output differs from %s at line %d:\n got: %s\nwant: %s", path, i+1, g, w)
					}
				}
			}
		})
	}
}

// golden is the subset of inspect --json the load tests compare against.
type golden struct {
	Sheets []struct {
		Tables []struct {
			Table    string `json:"table"`
			RowCount int    `json:"rowCount"`
			Columns  []struct {
				Name    string            `json:"name"`
				SQLType map[string]string `json:"sqlType"`
			} `json:"columns"`
		} `json:"tables"`
	} `json:"sheets"`
}

// TestLoadFixturesSQLite loads every fixture into SQLite and checks row
// counts and declared column types against the goldens.
func TestLoadFixturesSQLite(t *testing.T) {
	for _, c := range goldenCases {
		if len(c.args) > 0 {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(goldens, c.name+".json"))
			if err != nil {
				t.Skip("golden missing:", err)
			}
			var want golden
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(t.TempDir(), "k.db")
			run(t, "load", "--dsn", "sqlite://"+db, filepath.Join(fixtures, c.file))

			conn, err := sql.Open("sqlite", db)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			for _, sh := range want.Sheets {
				for _, tb := range sh.Tables {
					var n int
					if err := conn.QueryRow(`SELECT count(*) FROM "` + tb.Table + `"`).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != tb.RowCount {
						t.Errorf("%s: %d rows, golden says %d", tb.Table, n, tb.RowCount)
					}
					rows, err := conn.Query(`SELECT name, type FROM pragma_table_info('` + tb.Table + `') ORDER BY cid`)
					if err != nil {
						t.Fatal(err)
					}
					i := 0
					for rows.Next() {
						var name, typ string
						if err := rows.Scan(&name, &typ); err != nil {
							t.Fatal(err)
						}
						if i >= len(tb.Columns) {
							t.Fatalf("%s: extra column %s", tb.Table, name)
						}
						if name != tb.Columns[i].Name || typ != tb.Columns[i].SQLType["sqlite"] {
							t.Errorf("%s column %d: %s %s, golden %s %s", tb.Table, i, name, typ, tb.Columns[i].Name, tb.Columns[i].SQLType["sqlite"])
						}
						i++
					}
					rows.Close()
					if i != len(tb.Columns) {
						t.Errorf("%s: %d columns, golden says %d", tb.Table, i, len(tb.Columns))
					}
				}
			}
		})
	}
}

// TestTidyRoundTrip guards against kempt changing an already-clean file:
// every value loaded into SQLite must read back exactly as written in the
// CSV; only the column names may change.
func TestTidyRoundTrip(t *testing.T) {
	f, err := os.Open(filepath.Join(fixtures, "tidy.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	db := filepath.Join(t.TempDir(), "tidy.db")
	run(t, "load", "--dsn", db, filepath.Join(fixtures, "tidy.csv"))
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rows, err := conn.Query(`SELECT * FROM "tidy" ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for i, h := range records[0] {
		if cols[i] != normalize.SnakeCase(h) {
			t.Errorf("column %d: %s, want %s", i, cols[i], normalize.SnakeCase(h))
		}
	}
	var g struct {
		Sheets []struct {
			Tables []struct {
				Columns []struct {
					Type struct {
						Base string `json:"base"`
					} `json:"type"`
				} `json:"columns"`
			} `json:"tables"`
		} `json:"sheets"`
	}
	if err := json.Unmarshal(run(t, "inspect", "--json", filepath.Join(fixtures, "tidy.csv")), &g); err != nil {
		t.Fatal(err)
	}
	r := 1
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			got := sqliteText(v, g.Sheets[0].Tables[0].Columns[i].Type.Base)
			if want := records[r][i]; got != want {
				t.Errorf("row %d %s: got %q, want %q", r, cols[i], got, want)
			}
		}
		r++
	}
	if r != len(records) {
		t.Fatalf("read back %d rows, want %d", r-1, len(records)-1)
	}
}

// sqliteText renders a value read from SQLite the way it appears in a CSV,
// given the column's neutral type.
func sqliteText(v any, base string) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		if base == "bool" {
			return normalize.Format(x != 0)
		}
		return normalize.Format(x)
	case float64:
		return string(normalize.NumberFromFloat(x).Text)
	case time.Time: // the driver parses text stored in DATE/TIMESTAMP columns
		if base == "date" {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05")
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprintf("%T(%v)", v, v)
	}
}

func TestTableFlagTooLong(t *testing.T) {
	root := newRoot()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"ddl", "--table", strings.Repeat("t", 64), filepath.Join(fixtures, "tidy.csv")})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--table") {
		t.Fatalf("err = %v, want a --table length error", err)
	}
}
