// Package plan turns a detected region into a clean, typed table: column
// names, SQL types, cast rows, the decisions that explain them, and the role
// of every source cell.
package plan

import (
	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
)

// Column is one output column.
type Column struct {
	Name       string            `json:"name"`    // snake_case SQL name
	Header     string            `json:"header"`  // original header text, groups joined with " / "
	Type       infer.Type        `json:"type"`    // dialect-neutral type
	SQLType    map[string]string `json:"sqlType"` // dialect → rendered type
	Unit       string            `json:"unit,omitempty"`
	SourceCols []string          `json:"sourceCols"` // source column letters, e.g. ["C"]
	Comment    string            `json:"comment,omitempty"`
}

// Plan is one clean table.
type Plan struct {
	Sheet      string            `json:"sheet"`
	Table      string            `json:"table"`
	Region     string            `json:"region"` // A1 range of the whole region, e.g. "A1:H19"
	Columns    []Column          `json:"columns"`
	Rows       [][]any           `json:"rows"`
	SourceRows []int             `json:"-"` // 1-based sheet row for each output row
	Decisions  []grid.Decision   `json:"decisions"`
	CellRoles  map[string]string `json:"cellRoles"` // "A1" → role, for cells in this region
}

// Sheet is everything kempt found on one worksheet.
type Sheet struct {
	Name      string            `json:"sheet"`
	Grid      *grid.Grid        `json:"-"`
	Tables    []*Plan           `json:"tables"`
	Decisions []grid.Decision   `json:"decisions"`          // sheet-level only: region splits, ignored regions, forced skips
	CellRoles map[string]string `json:"cellRoles"`          // every role on the sheet: union of table roles plus ignored cells
	Warnings  []string          `json:"warnings,omitempty"` // problems with user options (e.g. an unmatched --fill-down)
}

// AllDecisions returns the sheet-level decisions merged with every table's
// decisions, in canonical order.
func (s *Sheet) AllDecisions() []grid.Decision {
	all := append([]grid.Decision(nil), s.Decisions...)
	for _, t := range s.Tables {
		all = append(all, t.Decisions...)
	}
	grid.SortDecisions(all)
	return all
}
