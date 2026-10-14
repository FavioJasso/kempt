package emit

import (
	"testing"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

func TestInsertSQL(t *testing.T) {
	p := &plan.Plan{Sheet: "S", Table: "t", Columns: []plan.Column{
		col("id", infer.Type{Base: infer.Int}, ""),
		col("note", infer.Type{Base: infer.Text, MaxLen: 10}, ""),
		col("ok", infer.Type{Base: infer.Bool}, ""),
	}}
	p.Rows = [][]any{
		{int64(1), "it's", true},
		{int64(2), `a\b`, false},
		{int64(3), nil, nil},
		{int64(4)}, // short row: padded with NULL
		{int64(5), "e", true},
	}
	tests := []struct {
		name    string
		dialect string
		opts    InsertOptions
		want    []string
	}{
		{"postgres one batch", infer.Postgres, InsertOptions{}, []string{
			"INSERT INTO \"t\" (\"id\", \"note\", \"ok\") VALUES\n" +
				"  (1, 'it''s', TRUE),\n  (2, 'a\\b', FALSE),\n  (3, NULL, NULL),\n  (4, NULL, NULL),\n  (5, 'e', TRUE);",
		}},
		{"mysql batches of 2", infer.MySQL, InsertOptions{BatchRows: 2, Schema: "db"}, []string{
			"INSERT INTO `db`.`t` (`id`, `note`, `ok`) VALUES\n  (1, 'it''s', TRUE),\n  (2, 'a\\\\b', FALSE);",
			"INSERT INTO `db`.`t` (`id`, `note`, `ok`) VALUES\n  (3, NULL, NULL),\n  (4, NULL, NULL);",
			"INSERT INTO `db`.`t` (`id`, `note`, `ok`) VALUES\n  (5, 'e', TRUE);",
		}},
		{"sqlite max rows", infer.SQLite, InsertOptions{MaxRows: 2, BatchRows: 1}, []string{
			"INSERT INTO \"t\" (\"id\", \"note\", \"ok\") VALUES\n  (1, 'it''s', 1);",
			"INSERT INTO \"t\" (\"id\", \"note\", \"ok\") VALUES\n  (2, 'a\\b', 0);",
		}},
		{"max rows above count", infer.SQLite, InsertOptions{MaxRows: 99, BatchRows: 4}, []string{
			"INSERT INTO \"t\" (\"id\", \"note\", \"ok\") VALUES\n  (1, 'it''s', 1),\n  (2, 'a\\b', 0),\n  (3, NULL, NULL),\n  (4, NULL, NULL);",
			"INSERT INTO \"t\" (\"id\", \"note\", \"ok\") VALUES\n  (5, 'e', 1);",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InsertSQL(p, tt.dialect, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statements, want %d:\n%q", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("statement %d:\n got %s\nwant %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestInsertSQLEmpty(t *testing.T) {
	p := &plan.Plan{Table: "t", Columns: []plan.Column{col("a", infer.Type{Base: infer.Int}, "")}}
	if got := InsertSQL(p, infer.Postgres, InsertOptions{}); got != nil {
		t.Errorf("no rows: got %q, want nil", got)
	}
	p.Rows = [][]any{{normalize.Decimal("1")}}
	p.Columns = nil
	if got := InsertSQL(p, infer.Postgres, InsertOptions{}); got != nil {
		t.Errorf("no columns: got %q, want nil", got)
	}
}

func TestInsertSQLDefaultBatch(t *testing.T) {
	p := &plan.Plan{Table: "t", Columns: []plan.Column{col("a", infer.Type{Base: infer.Int}, "")}}
	for i := range 1201 {
		p.Rows = append(p.Rows, []any{int64(i)})
	}
	if got := InsertSQL(p, infer.Postgres, InsertOptions{}); len(got) != 3 {
		t.Errorf("1201 rows: got %d statements, want 3 (batches of 500)", len(got))
	}
}
