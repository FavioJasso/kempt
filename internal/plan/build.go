package plan

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/FavioJasso/kempt/internal/detect"
	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
)

// maxIdent is the longest identifier kempt emits (Postgres truncates at 63 bytes).
const maxIdent = 63

// Options control detection, inference, and naming.
type Options struct {
	Detect detect.Options
	Infer  infer.Options
	// Table overrides the table name (default: the slugified sheet name).
	// When a sheet holds several tables they get _1, _2, ... suffixes.
	Table string
	// RoleRows limits CellRoles to the first RoleRows rows of the sheet
	// (0 = every row). The web demo only renders the top of each sheet.
	RoleRows int
}

// Build detects every table on the sheet and turns each into a Plan.
func Build(g *grid.Grid, opts Options) *Sheet {
	res := detect.Detect(g, opts.Detect)
	sh := &Sheet{
		Name:      g.Sheet,
		Grid:      g,
		Tables:    []*Plan{},
		Decisions: append([]grid.Decision{}, res.Decisions...),
		CellRoles: map[string]string{},
		Warnings:  res.Warnings,
	}
	iopts := opts.Infer
	iopts.Date1904 = g.Date1904
	if d, ok := decimalComma(g, res); ok && !iopts.DecimalComma {
		iopts.DecimalComma = true
		sh.Decisions = append(sh.Decisions, d)
	}

	base := opts.Table
	if base == "" {
		base = normalize.Slug(g.Sheet)
	}
	n := 0
	for _, rg := range res.Regions {
		if !rg.Skipped {
			n++
		}
	}

	nulls := map[[2]int]bool{}
	k := 0
	for i := range res.Regions {
		rg := &res.Regions[i]
		if rg.Skipped {
			sh.Decisions = append(sh.Decisions, rg.Decisions...)
			continue
		}
		k++
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s_%d", base, k)
		}
		p := buildTable(g, rg, name, iopts, nulls)
		sh.Tables = append(sh.Tables, p)
	}
	grid.SortDecisions(sh.Decisions)

	// Roles for every cell (up to RoleRows), with NULL overrides.
	rows := g.Rows
	if opts.RoleRows > 0 {
		rows = min(rows, opts.RoleRows)
	}
	for r := 0; r < rows; r++ {
		for c := 0; c < g.Cols; c++ {
			role := res.CellRole(g, r, c)
			if role == "" {
				continue
			}
			if nulls[[2]int{r, c}] {
				role = grid.RoleNull
			}
			sh.CellRoles[grid.CellRef(r, c)] = role
		}
	}
	for _, p := range sh.Tables {
		b, err := grid.ParseRange(p.Region)
		if err != nil {
			continue
		}
		for r := b.R1; r <= min(b.R2, rows-1); r++ {
			for c := b.C1; c <= b.C2; c++ {
				ref := grid.CellRef(r, c)
				if role, ok := sh.CellRoles[ref]; ok {
					p.CellRoles[ref] = role
				}
			}
		}
	}
	return sh
}

// buildTable names, types, and casts one region.
func buildTable(g *grid.Grid, rg *detect.Region, table string, iopts infer.Options, nulls map[[2]int]bool) *Plan {
	p := &Plan{
		Sheet:     g.Sheet,
		Table:     table,
		Region:    rg.Bounds.A1(),
		Columns:   make([]Column, 0, len(rg.Cols)),
		Rows:      make([][]any, len(rg.Data)),
		Decisions: append([]grid.Decision{}, rg.Decisions...),
		CellRoles: map[string]string{},
	}
	for i, r := range rg.Data {
		p.Rows[i] = make([]any, len(rg.Cols))
		p.SourceRows = append(p.SourceRows, r+1)
	}
	top, bottom := rg.Bounds.R1, rg.Bounds.R1
	if len(rg.Header) > 0 {
		top, bottom = rg.Header[0], rg.Header[len(rg.Header)-1]
	}

	seen := map[string]bool{}
	for j, hc := range rg.Cols {
		letter := grid.ColName(hc.Col)
		header := strings.Join(hc.Parts, " / ")
		var names []string
		var unit, unitPart string
		for _, part := range hc.Parts {
			rest, u := normalize.ExtractUnit(part)
			if u != "" {
				unit, unitPart = u, part
			}
			if sn := normalize.SnakeCase(rest); sn != "" {
				names = append(names, sn)
			}
		}
		name := strings.Join(names, "_")
		if name == "" {
			name = "col_" + strings.ToLower(letter)
		}

		in := infer.Input{Sheet: g.Sheet, Name: name, Header: header, Col: hc.Col,
			Rows: rg.Data, Cells: make([]*grid.Cell, len(rg.Data))}
		for i, r := range rg.Data {
			if src, ok := rg.Fill[[2]int{r, hc.Col}]; ok {
				in.Cells[i] = g.At(src[0], src[1])
			} else {
				in.Cells[i] = g.At(r, hc.Col)
			}
		}
		res := infer.Column(in, iopts)
		for i, r := range rg.Data {
			p.Rows[i][j] = res.Values[i]
			if i < len(res.Nulls) && res.Nulls[i] {
				nulls[[2]int{r, hc.Col}] = true
			}
		}

		if (unit == "%" || res.Pct) && !strings.HasSuffix(name, "pct") {
			name += "_pct"
		}
		fromHeader := unit != ""
		if unit == "" {
			unit = res.Unit
		}
		final := dedupe(name, seen)

		col := Column{
			Name:       final,
			Header:     header,
			Type:       res.Type,
			SQLType:    map[string]string{},
			Unit:       unit,
			SourceCols: []string{letter},
			Comment:    header,
		}
		for _, d := range infer.Dialects {
			col.SQLType[d] = res.Type.SQL(d)
		}
		if unit != "" {
			if col.Comment != "" {
				col.Comment += " "
			}
			if fromHeader {
				col.Comment += fmt.Sprintf("[unit: %s; values not rescaled]", unit)
			} else {
				col.Comment += fmt.Sprintf("[unit: %s]", unit)
			}
		}
		p.Columns = append(p.Columns, col)
		if final != in.Name {
			// infer named the column before the _pct suffix or deduping.
			re := regexp.MustCompile(`\bcolumn ` + regexp.QuoteMeta(in.Name) + `\b`)
			for i := range res.Decisions {
				res.Decisions[i].Reason = re.ReplaceAllLiteralString(res.Decisions[i].Reason, "column "+final)
			}
		}
		p.Decisions = append(p.Decisions, res.Decisions...)

		hdr := grid.Range{R1: top, C1: hc.Col, R2: bottom, C2: hc.Col}
		if unitPart != "" {
			p.Decisions = append(p.Decisions, grid.NewDecision(grid.KStripUnit, g.Sheet, hdr, 0.9,
				fmt.Sprintf("Unit %q moved from the header %q into the column comment of %s; values are not rescaled.", unit, unitPart, final)))
		}
		switch {
		case header == "":
			p.Decisions = append(p.Decisions, grid.NewDecision(grid.KColumnName, g.Sheet, hdr, 0.9,
				fmt.Sprintf("Column %s has no header; named %s.", letter, final)))
		case final != name:
			p.Decisions = append(p.Decisions, grid.NewDecision(grid.KColumnName, g.Sheet, hdr, 1,
				fmt.Sprintf("Header %q (column %s) would duplicate an earlier name; renamed %s.", header, letter, final)))
		case final != strings.ToLower(strings.TrimSpace(header)):
			p.Decisions = append(p.Decisions, grid.NewDecision(grid.KColumnName, g.Sheet, hdr, 1,
				fmt.Sprintf("Header %q (column %s) became %s.", header, letter, final)))
		}
	}
	grid.SortDecisions(p.Decisions)
	return p
}

// dedupe truncates name to maxIdent bytes and appends _2, _3, ... until it
// is unused, then records it.
func dedupe(name string, seen map[string]bool) string {
	name = truncate(name, maxIdent)
	out := name
	for i := 2; seen[out]; i++ {
		suffix := fmt.Sprintf("_%d", i)
		out = truncate(name, maxIdent-len(suffix)) + suffix
	}
	seen[out] = true
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "_")
}

// UniqueTables renames tables whose names collide across sheets by appending
// _2, _3, ... in sheet order.
func UniqueTables(sheets []*Sheet) {
	seen := map[string]bool{}
	for _, sh := range sheets {
		for _, t := range sh.Tables {
			name := t.Table
			for i := 2; seen[name]; i++ {
				name = fmt.Sprintf("%s_%d", t.Table, i)
			}
			t.Table = name
			seen[name] = true
		}
	}
}

var (
	// Numbers only a decimal comma explains: "1.234,56", "12,50", "€ 3,5".
	euNumber = regexp.MustCompile(`^[-−(]?\s*[$€£¥]?\s*(\d{1,3}(\.\d{3})+,\d+|\d+,\d{1,2})\s*[€%]?\)?$`)
	// Numbers only a decimal point explains: "1,234.56", "12.50".
	usNumber = regexp.MustCompile(`^[-−(]?\s*[$€£¥]?\s*(\d{1,3}(,\d{3})+\.\d+|\d+\.\d{1,2})\s*[€%]?\)?$`)
)

// decimalComma looks at every text value in the sheet's tables. When some
// numbers can only be read with a comma as the decimal point ("1.234,56")
// and none only with a decimal point ("12.50"), the sheet uses decimal
// commas, so ambiguous values like "1.250" mean 1250, not 1.25.
func decimalComma(g *grid.Grid, res detect.Result) (grid.Decision, bool) {
	eu, us := 0, 0
	var first [2]int
	var example string
	for _, rg := range res.Regions {
		if rg.Skipped {
			continue
		}
		for _, r := range rg.Data {
			for _, hc := range rg.Cols {
				c := g.At(r, hc.Col)
				if c == nil || c.Num != nil || c.IsEmpty() {
					continue
				}
				t := strings.TrimSpace(c.Raw)
				switch {
				case euNumber.MatchString(t):
					if eu == 0 {
						first, example = [2]int{r, hc.Col}, t
					}
					eu++
				case usNumber.MatchString(t):
					us++
				}
			}
		}
	}
	if eu == 0 || us > 0 {
		return grid.Decision{}, false
	}
	return grid.NewDecision(grid.KDecimalComma, g.Sheet, grid.Range{R1: first[0], C1: first[1], R2: first[0], C2: first[1]}, 0.85,
		fmt.Sprintf("This sheet writes numbers with a decimal comma (%s %q and %d more), so every number is read with \",\" as the decimal point and \".\" as the thousands separator (\"1.250\" is 1250).",
			grid.CellRef(first[0], first[1]), example, eu-1)), true
}
