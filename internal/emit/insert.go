package emit

import (
	"strings"

	"github.com/FavioJasso/kempt/internal/plan"
)

const defaultBatchRows = 500

// InsertOptions control INSERT rendering.
type InsertOptions struct {
	Schema    string
	BatchRows int // rows per statement; 0 = 500
	MaxRows   int // 0 = all rows
}

// InsertSQL renders p's rows as multi-row INSERT statements with literal
// values, each ending in ";". It returns nil when there is nothing to insert.
func InsertSQL(p *plan.Plan, dialect string, opts InsertOptions) []string {
	d := ddlDialect(dialect)
	rows := p.Rows
	if opts.MaxRows > 0 && len(rows) > opts.MaxRows {
		rows = rows[:opts.MaxRows]
	}
	if len(rows) == 0 || len(p.Columns) == 0 {
		return nil
	}
	batch := opts.BatchRows
	if batch <= 0 {
		batch = defaultBatchRows
	}
	head := insertHead(p, d, opts.Schema)
	var out []string
	for start := 0; start < len(rows); start += batch {
		var b strings.Builder
		b.WriteString(head)
		b.WriteString("\n")
		for i, row := range rows[start:min(start+batch, len(rows))] {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString("  (")
			for j := range p.Columns {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(Literal(d, cell(row, j)))
			}
			b.WriteString(")")
		}
		b.WriteString(";")
		out = append(out, b.String())
	}
	return out
}

// insertHead renders `INSERT INTO table (cols) VALUES`.
func insertHead(p *plan.Plan, d, schema string) string {
	cols := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = QuoteIdent(d, c.Name)
	}
	return "INSERT INTO " + QualifiedTable(d, schema, p.Table) + " (" + strings.Join(cols, ", ") + ") VALUES"
}

// cell returns row[j], or nil when the row is short.
func cell(row []any, j int) any {
	if j < len(row) {
		return row[j]
	}
	return nil
}
