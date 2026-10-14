// Package report renders kempt's results: the human-readable inspect
// output, the JSON plan (golden format), the decision report, and a raw
// dump of the source grid.
package report

import (
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// The doc types below pin the JSON format independently of the json tags on
// plan types, so golden files only change when this file does.

type fileDoc struct {
	File   string     `json:"file"`
	Sheets []sheetDoc `json:"sheets"`
}

type sheetDoc struct {
	Sheet      string            `json:"sheet"`
	Dimensions string            `json:"dimensions"`
	Decisions  []decisionDoc     `json:"decisions"`
	CellRoles  map[string]string `json:"cellRoles"`
	Tables     []tableDoc        `json:"tables"`
}

type tableDoc struct {
	Table     string        `json:"table"`
	Region    string        `json:"region"`
	RowCount  int           `json:"rowCount"`
	Columns   []columnDoc   `json:"columns"`
	Rows      [][]any       `json:"rows"`
	Decisions []decisionDoc `json:"decisions"`
}

type columnDoc struct {
	Name       string     `json:"name"`
	Header     string     `json:"header"`
	Type       typeDoc    `json:"type"`
	SQLType    sqlTypeDoc `json:"sqlType"`
	Unit       string     `json:"unit"`
	SourceCols []string   `json:"sourceCols"`
	Comment    string     `json:"comment"`
}

type typeDoc struct {
	Base      string `json:"base"`
	Precision int    `json:"precision"`
	Scale     int    `json:"scale"`
	MaxLen    int    `json:"maxLen"`
}

type sqlTypeDoc struct {
	MySQL    string `json:"mysql"`
	Postgres string `json:"postgres"`
	SQLite   string `json:"sqlite"`
}

type decisionDoc struct {
	Kind       string  `json:"kind"`
	Range      string  `json:"range"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

type decisionsReport struct {
	File           string           `json:"file"`
	Tables         []tableDecisions `json:"tables"`
	SheetDecisions []sheetDecisions `json:"sheetDecisions"`
}

type tableDecisions struct {
	Sheet     string        `json:"sheet"`
	Table     string        `json:"table"`
	Region    string        `json:"region"`
	Decisions []decisionDoc `json:"decisions"`
}

type sheetDecisions struct {
	Sheet     string        `json:"sheet"`
	Decisions []decisionDoc `json:"decisions"`
}

// JSON writes the full inspect result (`kempt inspect --json`). The format is
// the golden-test contract: two-space indent, trailing newline, file base
// name only, and empty lists rendered as [].
func JSON(w io.Writer, file string, sheets []*plan.Sheet) error {
	doc := fileDoc{File: baseName(file), Sheets: make([]sheetDoc, 0, len(sheets))}
	for _, sh := range sheets {
		if sh == nil {
			continue
		}
		sd := sheetDoc{
			Sheet:      sh.Name,
			Dimensions: dimensions(sh.Grid),
			Decisions:  decisionDocs(sh.Decisions),
			CellRoles:  sh.CellRoles,
			Tables:     make([]tableDoc, 0, len(sh.Tables)),
		}
		if sd.CellRoles == nil {
			sd.CellRoles = map[string]string{}
		}
		for _, p := range sh.Tables {
			if p != nil {
				sd.Tables = append(sd.Tables, tableJSON(p))
			}
		}
		doc.Sheets = append(doc.Sheets, sd)
	}
	return encode(w, doc)
}

// Decisions writes the decision report (`--report report.json`): every
// table's decisions plus the sheet-level ones.
func Decisions(w io.Writer, file string, sheets []*plan.Sheet) error {
	doc := decisionsReport{File: baseName(file), Tables: []tableDecisions{}, SheetDecisions: []sheetDecisions{}}
	for _, sh := range sheets {
		if sh == nil {
			continue
		}
		for _, p := range sh.Tables {
			if p != nil {
				doc.Tables = append(doc.Tables, tableDecisions{
					Sheet: sh.Name, Table: p.Table, Region: p.Region, Decisions: decisionDocs(p.Decisions),
				})
			}
		}
		doc.SheetDecisions = append(doc.SheetDecisions, sheetDecisions{Sheet: sh.Name, Decisions: decisionDocs(sh.Decisions)})
	}
	return encode(w, doc)
}

func tableJSON(p *plan.Plan) tableDoc {
	td := tableDoc{
		Table:     p.Table,
		Region:    p.Region,
		RowCount:  len(p.Rows),
		Columns:   make([]columnDoc, 0, len(p.Columns)),
		Rows:      make([][]any, 0, len(p.Rows)),
		Decisions: decisionDocs(p.Decisions),
	}
	for _, c := range p.Columns {
		td.Columns = append(td.Columns, columnDoc{
			Name:   c.Name,
			Header: c.Header,
			Type:   typeDoc{Base: c.Type.Base.String(), Precision: c.Type.Precision, Scale: c.Type.Scale, MaxLen: c.Type.MaxLen},
			SQLType: sqlTypeDoc{
				MySQL:    sqlType(c, infer.MySQL),
				Postgres: sqlType(c, infer.Postgres),
				SQLite:   sqlType(c, infer.SQLite),
			},
			Unit:       c.Unit,
			SourceCols: nonNil(c.SourceCols),
			Comment:    c.Comment,
		})
	}
	for _, row := range p.Rows {
		td.Rows = append(td.Rows, jsonRow(row))
	}
	return td
}

// jsonNumber matches a Decimal that is also a valid JSON number.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// jsonRow copies row, rendering any Decimal that is not a valid JSON number
// as a string so one odd value cannot make the whole document fail to encode.
func jsonRow(row []any) []any {
	out := make([]any, len(row))
	for i, v := range row {
		if d, ok := v.(normalize.Decimal); ok && !jsonNumber.MatchString(string(d)) {
			v = string(d)
		}
		out[i] = v
	}
	return out
}

func sqlType(c plan.Column, dialect string) string {
	if s, ok := c.SQLType[dialect]; ok && s != "" {
		return s
	}
	return c.Type.SQL(dialect)
}

// decisionDocs copies ds into canonical order.
func decisionDocs(ds []grid.Decision) []decisionDoc {
	sorted := append([]grid.Decision(nil), ds...)
	grid.SortDecisions(sorted)
	out := make([]decisionDoc, len(sorted))
	for i, d := range sorted {
		conf := d.Confidence
		if math.IsNaN(conf) {
			conf = 0 // encoding/json rejects NaN
		}
		out[i] = decisionDoc{Kind: d.Kind, Range: d.Range, Reason: d.Reason, Confidence: conf}
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// dimensions renders the grid extent in A1 notation ("" for an empty grid).
func dimensions(g *grid.Grid) string {
	if g == nil || g.Rows == 0 || g.Cols == 0 {
		return ""
	}
	return grid.Range{R2: g.Rows - 1, C2: g.Cols - 1}.A1()
}

// baseName strips any directory, with either slash style, from file.
func baseName(file string) string {
	return file[strings.LastIndexAny(file, `/\`)+1:]
}

func encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
