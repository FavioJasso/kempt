package server

import (
	"errors"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"github.com/FavioJasso/kempt/internal/emit"
	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// sampleNotes describes the bundled fixtures in the sample picker.
var sampleNotes = map[string]string{
	"budget_report.xlsx":     "City budget: titles, a two-row merged header, ($000s) units, accounting negatives, subtotals, footnotes",
	"two_tables.xlsx":        "Two tables of different widths on one sheet, plus a stray note",
	"merged_categories.xlsx": "Vertically merged category cells that need fill-down",
	"messy_values.xlsx":      "Currency, percents, N/A and —, #DIV/0!, Excel serial dates, mixed US text dates",
	"ambiguous_dates.csv":    "Semicolon CSV with a BOM where every date could be DD/MM or MM/DD",
	"tidy.csv":               "An already-clean CSV: kempt only renames the columns",
}

type sampleInfo struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	out := []sampleInfo{}
	if s.cfg.Samples != nil {
		entries, _ := fs.ReadDir(s.cfg.Samples, ".")
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !(strings.HasSuffix(name, ".xlsx") || strings.HasSuffix(name, ".csv")) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, sampleInfo{Name: name, Size: info.Size(), Description: sampleNotes[name]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": out})
}

// Response shapes for /api/inspect.
type (
	inspectResponse struct {
		File   string        `json:"file"`
		Sheets []sheetResult `json:"sheets"`
	}
	sheetResult struct {
		Name       string            `json:"name"`
		Dimensions string            `json:"dimensions"`
		Grid       gridView          `json:"grid"`
		CellRoles  map[string]string `json:"cellRoles"`
		Decisions  []decisionView    `json:"decisions"`
		Tables     []tableResult     `json:"tables"`
		Warnings   []string          `json:"warnings"`
		// The first table's columns, preview, and DDL, for clients that only
		// care about one table per sheet.
		Columns []columnView      `json:"columns"`
		Preview [][]*string       `json:"preview"`
		DDL     map[string]string `json:"ddl"`
	}
	gridView struct {
		Rows      int        `json:"rows"`      // rows included below
		Cols      int        `json:"cols"`      // columns included below
		TotalRows int        `json:"totalRows"` // full sheet size
		TotalCols int        `json:"totalCols"`
		Cells     [][]string `json:"cells"`
		Kinds     []string   `json:"kinds"` // one character per cell: ' ' empty, s text, n number, d date, b bool, e error
		Merges    []string   `json:"merges"`
	}
	decisionView struct {
		Kind       string  `json:"kind"`
		Range      string  `json:"range"` // A1 range without the sheet
		Reason     string  `json:"reason"`
		Confidence float64 `json:"confidence"`
		Table      string  `json:"table,omitempty"`
	}
	columnView struct {
		Name       string            `json:"name"`
		Header     string            `json:"header"`
		Type       string            `json:"type"`
		SQLType    map[string]string `json:"sqlType"`
		Unit       string            `json:"unit,omitempty"`
		SourceCols []string          `json:"sourceCols"`
	}
	tableResult struct {
		Table     string            `json:"table"`
		Region    string            `json:"region"`
		RowCount  int               `json:"rowCount"`
		Columns   []columnView      `json:"columns"`
		Preview   [][]*string       `json:"preview"`
		DDL       map[string]string `json:"ddl"`
		Decisions []decisionView    `json:"decisions"`
	}
)

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	up, err := s.parseUpload(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sheets, err := s.analyze(r.Context(), up)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := inspectResponse{File: up.name, Sheets: []sheetResult{}}
	for _, sh := range sheets {
		resp.Sheets = append(resp.Sheets, sheetView(sh))
	}
	writeJSON(w, http.StatusOK, resp)
}

func sheetView(sh *plan.Sheet) sheetResult {
	g := sh.Grid
	out := sheetResult{
		Name:      sh.Name,
		Grid:      gridOf(g),
		CellRoles: map[string]string{},
		Decisions: []decisionView{},
		Tables:    []tableResult{},
		Warnings:  append([]string{}, sh.Warnings...),
		Columns:   []columnView{},
		Preview:   [][]*string{},
		DDL:       map[string]string{},
	}
	if g.Rows > 0 && g.Cols > 0 {
		out.Dimensions = grid.Range{R2: g.Rows - 1, C2: g.Cols - 1}.A1()
	}
	for ref, role := range sh.CellRoles {
		if r, c, err := grid.ParseRef(ref); err == nil && r < gridRows && c < gridCols {
			out.CellRoles[ref] = role
		}
	}
	type tagged struct {
		d     grid.Decision
		table string
	}
	var all []tagged
	for _, d := range sh.Decisions {
		all = append(all, tagged{d, ""})
	}
	for _, t := range sh.Tables {
		for _, d := range t.Decisions {
			all = append(all, tagged{d, t.Table})
		}
	}
	slices.SortStableFunc(all, func(a, b tagged) int { return grid.CompareDecisions(a.d, b.d) })
	for _, x := range all {
		v := decisionOf(x.d)
		v.Table = x.table
		out.Decisions = append(out.Decisions, v)
	}
	for _, t := range sh.Tables {
		out.Tables = append(out.Tables, tableView(t))
	}
	if len(out.Tables) > 0 {
		out.Columns, out.Preview, out.DDL = out.Tables[0].Columns, out.Tables[0].Preview, out.Tables[0].DDL
	}
	return out
}

func decisionOf(d grid.Decision) decisionView {
	rng := d.Range
	if i := strings.LastIndexByte(rng, '!'); i >= 0 {
		rng = rng[i+1:]
	}
	return decisionView{Kind: d.Kind, Range: rng, Reason: d.Reason, Confidence: d.Confidence}
}

func tableView(t *plan.Plan) tableResult {
	tr := tableResult{
		Table:     t.Table,
		Region:    t.Region,
		RowCount:  len(t.Rows),
		Columns:   []columnView{},
		Preview:   [][]*string{},
		DDL:       map[string]string{},
		Decisions: []decisionView{},
	}
	for _, c := range t.Columns {
		tr.Columns = append(tr.Columns, columnView{Name: c.Name, Header: c.Header, Type: c.Type.String(),
			SQLType: c.SQLType, Unit: c.Unit, SourceCols: c.SourceCols})
	}
	for i, row := range t.Rows {
		if i >= previewRows {
			break
		}
		vals := make([]*string, len(row))
		for j, v := range row {
			if v != nil {
				s := normalize.Format(v)
				vals[j] = &s
			}
		}
		tr.Preview = append(tr.Preview, vals)
	}
	for _, d := range infer.Dialects {
		tr.DDL[d] = emit.DDL(t, d, emit.DDLOptions{})
	}
	for _, d := range t.Decisions {
		v := decisionOf(d)
		v.Table = t.Table
		tr.Decisions = append(tr.Decisions, v)
	}
	return tr
}

// gridOf renders the top-left corner of the sheet for display.
func gridOf(g *grid.Grid) gridView {
	rows, cols := min(g.Rows, gridRows), min(g.Cols, gridCols)
	gv := gridView{Rows: rows, Cols: cols, TotalRows: g.Rows, TotalCols: g.Cols,
		Cells: make([][]string, rows), Kinds: make([]string, rows), Merges: []string{}}
	for r := 0; r < rows; r++ {
		cells := make([]string, cols)
		kinds := make([]byte, cols)
		for c := 0; c < cols; c++ {
			cell := g.At(r, c)
			kinds[c] = ' '
			if cell.IsEmpty() {
				continue
			}
			cells[c], kinds[c] = display(g, cell)
		}
		gv.Cells[r], gv.Kinds[r] = cells, string(kinds)
	}
	for _, m := range g.Merges {
		if m.R1 >= rows || m.C1 >= cols {
			continue
		}
		m.R2, m.C2 = min(m.R2, rows-1), min(m.C2, cols-1)
		gv.Merges = append(gv.Merges, m.A1())
	}
	return gv
}

// display renders a raw cell for the grid view, with a one-letter kind.
func display(g *grid.Grid, c *grid.Cell) (string, byte) {
	switch {
	case c.Kind == grid.Error:
		return c.Raw, 'e'
	case c.Kind == grid.Bool:
		return c.Raw, 'b'
	case c.Num != nil && (c.Kind == grid.Date || c.DateFmt):
		if t, hasTime, ok := normalize.ExcelSerial(*c.Num, g.Date1904); ok {
			if hasTime {
				return normalize.NewTimestamp(t).String(), 'd'
			}
			return normalize.NewDate(t).String(), 'd'
		}
		return c.Raw, 'n'
	case c.Kind == grid.Date:
		return c.Raw, 'd'
	case c.Num != nil:
		return c.Raw, 'n'
	}
	return c.Raw, 's'
}

// fail writes err as a JSON error with a fitting status.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.msg)
		return
	}
	s.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
