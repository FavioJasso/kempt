package read

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/FavioJasso/kempt/internal/grid"
)

// newBook builds a workbook in memory and returns its bytes.
func newBook(t *testing.T, build func(f *excelize.File)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	build(f)
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// must fails the test on a setup error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// numFmtStyle registers a style with a built-in id or a custom code.
func numFmtStyle(t *testing.T, f *excelize.File, id int, custom string) int {
	t.Helper()
	st := &excelize.Style{NumFmt: id}
	if custom != "" {
		st.CustomNumFmt = &custom
	}
	s, err := f.NewStyle(st)
	must(t, err)
	return s
}

// patchPart rewrites one part of an xlsx zip, for cells excelize cannot
// write (errors, "d" cells, Excel-style formula results).
func patchPart(t *testing.T, data []byte, part string, edit func(string) string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	found := false
	for _, zf := range zr.File {
		rc, err := zf.Open()
		must(t, err)
		b, err := io.ReadAll(rc)
		must(t, err)
		rc.Close()
		if zf.Name == part {
			found = true
			b = []byte(edit(string(b)))
		}
		w, err := zw.Create(zf.Name)
		must(t, err)
		_, err = w.Write(b)
		must(t, err)
	}
	must(t, zw.Close())
	if !found {
		t.Fatalf("part %s not found", part)
	}
	return out.Bytes()
}

// replaceCells swaps whole <c r="REF">…</c> elements in sheet1.xml.
func replaceCells(t *testing.T, data []byte, cells map[string]string) []byte {
	t.Helper()
	return patchPart(t, data, "xl/worksheets/sheet1.xml", func(s string) string {
		for ref, xml := range cells {
			re := regexp.MustCompile(`<c r="` + ref + `"[^>]*?(/>|>.*?</c>)`)
			if !re.MatchString(s) {
				t.Fatalf("cell %s not in sheet xml", ref)
			}
			s = re.ReplaceAllLiteralString(s, xml)
		}
		return s
	})
}

func readXLSX(t *testing.T, data []byte, opts Options) []*grid.Grid {
	t.Helper()
	gs, err := Bytes("book.xlsx", data, opts)
	if err != nil {
		t.Fatal(err)
	}
	return gs
}

func sheetNames(gs []*grid.Grid) []string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Sheet)
	}
	return out
}

func cellAt(t *testing.T, g *grid.Grid, ref string) *grid.Cell {
	t.Helper()
	r, c, err := grid.ParseRef(ref)
	must(t, err)
	if cell := g.At(r, c); cell != nil {
		return cell
	}
	return &grid.Cell{}
}

func TestIsDateFormat(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"yyyy-mm-dd", true},
		{"m/d/yy h:mm", true},
		{"[$-409]mmm d, yyyy", true},
		{"[h]:mm:ss", false},
		{"[mm]:ss", false},
		{"[ss].00", false},
		{"mm:ss.0", false},
		{"hh:mm AM/PM", false},
		{"h:mm a/p", false},
		{"MMMM D, YYYY", true},
		{"d-mmm-yy", true},
		{"mmm-yy", true},
		{"dd/mm/yyyy;@", true},
		{`[$-F800]dddd\,\ mmmm\ dd\,\ yyyy`, true},
		{`[DBNum1][$-804]yyyy"年"m"月"d"日"`, true},
		{`[$-411]ge.m.d`, true},
		{`yyyy"年"`, true},
		{`[Red][<=30]d "days";0`, true},
		{"0.00%", false},
		{"#,##0;(#,##0)", false},
		{`"mm"0`, false},
		{"[Red]0.00", false},
		{"General", false},
		{"[$-409]General", false},
		{"@", false},
		{"0", false},
		{"0.00", false},
		{"0.00E+00", false},
		{"##0.0E+0", false},
		{"# ?/?", false},
		{"00000", false},
		{"000-00-0000", false},
		{"[<=9999999]###-####;(###) ###-####", false},
		{`_(* #,##0_);_(* \(#,##0\);_(* "-"_);_(@_)`, false},
		{`_("$"* #,##0.00_);_("$"* \(#,##0.00\);_("$"* "-"??_);_(@_)`, false},
		{`_-* #,##0.00 _€_-;-* #,##0.00 _€_-`, false},
		{`#,##0 "€"`, false},
		{`[$€-407] #,##0.00`, false},
		{`[$USD] #,##0`, false},
		{`0.0,,"M"`, false},
		{`0" days"`, false},
		{`\d0`, false},
		{`0\h`, false},
		{"[Color10]0", false},
		{"[>=100]0;0.0", false},
		{"0.00_);[Red](0.00)", false},
		{`"Q"0`, false},
		{`"unterminated`, false},
		{"[h", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDateFormat(tt.code); got != tt.want {
			t.Errorf("isDateFormat(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestIsBuiltinDateFormat(t *testing.T) {
	tests := []struct {
		id   int
		want bool
	}{
		{0, false}, {1, false}, {2, false}, {4, false}, {9, false}, {10, false}, {13, false},
		{14, true}, {15, true}, {17, true}, {18, false}, {21, false}, {22, true}, {23, false},
		{27, true}, {32, false}, {35, false}, {36, true}, {37, false}, {44, false}, {45, false}, {46, false}, {47, false},
		{48, false}, {49, false}, {50, true}, {55, false}, {58, true}, {59, false}, {71, true}, {75, false}, {81, true}, {164, false},
	}
	for _, tt := range tests {
		if got := isBuiltinDateFormat(tt.id); got != tt.want {
			t.Errorf("isBuiltinDateFormat(%d) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

// Time-only and elapsed formats are not dates; "%" formats are percentages.
func TestFormatKind(t *testing.T) {
	tests := []struct {
		code string
		want numFmt
	}{
		{"h:mm AM/PM", fmtPlain},
		{"[h]:mm", fmtPlain},
		{"hh:mm:ss", fmtPlain},
		{"m/d/yy h:mm", fmtDate},
		{"yyyy-mm-dd hh:mm:ss", fmtDate},
		{"mmm", fmtDate},
		{"0%", fmtPct},
		{"0.00%", fmtPct},
		{`0.0" %"`, fmtPlain},
		{`0.0\%`, fmtPlain},
		{"#,##0", fmtPlain},
		{"d/m/bb", fmtDate},
	}
	for _, tt := range tests {
		if got := formatKind(tt.code); got != tt.want {
			t.Errorf("formatKind(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
	for id, want := range map[int]numFmt{9: fmtPct, 10: fmtPct, 14: fmtDate, 20: fmtPlain, 2: fmtPlain} {
		if got := builtinKind(id); got != want {
			t.Errorf("builtinKind(%d) = %v, want %v", id, got, want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		v    float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{42, "42"},
		{100, "100"},
		{0.1 + 0.2, "0.3"},
		{1e-7, "0.0000001"},
		{2.5e-10, "0.00000000025"},
		{-0.5, "-0.5"},
		{46096.5, "46096.5"},
		{1.0 / 3, "0.333333333333333"},
		{2.0 / 3, "0.666666666666667"},
		{1234567890123456789, "1234567890123460000"},
		{1.2345678901234567e20, "123456789012346000000"},
		{1e21, "1000000000000000000000"},
		{99999999999999.99, "100000000000000"},
		{1.0000000000000002, "1"},
		{123.456, "123.456"},
		{-1234.5, "-1234.5"},
		{0.000123, "0.000123"},
	}
	for _, tt := range tests {
		if got := formatNumber(tt.v); got != tt.want {
			t.Errorf("formatNumber(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestXLSXCellKinds(t *testing.T) {
	var dateStyle int
	data := newBook(t, func(f *excelize.File) {
		const s = "Sheet1"
		must(t, f.SetCellValue(s, "A1", "Name"))
		must(t, f.SetCellValue(s, "B1", 42))
		must(t, f.SetCellValue(s, "C1", true))
		must(t, f.SetCellValue(s, "D1", false))
		must(t, f.SetCellValue(s, "E1", 46096))
		must(t, f.SetCellStyle(s, "E1", "E1", numFmtStyle(t, f, 14, "")))
		must(t, f.SetCellValue(s, "F1", 46096.5))
		must(t, f.SetCellStyle(s, "F1", "F1", numFmtStyle(t, f, 0, "yyyy-mm-dd hh:mm")))
		must(t, f.SetCellValue(s, "G1", 1234.5))
		must(t, f.SetCellStyle(s, "G1", "G1", numFmtStyle(t, f, 0, "#,##0.00")))
		must(t, f.SetCellValue(s, "H1", 0.125))
		must(t, f.SetCellStyle(s, "H1", "H1", numFmtStyle(t, f, 10, "")))
		dateStyle = numFmtStyle(t, f, 0, "[$-409]mmm d, yyyy")
		must(t, f.SetCellValue(s, "I1", "2026-03-15"))
		must(t, f.SetCellStyle(s, "I1", "I1", dateStyle))
		must(t, f.SetCellValue(s, "J1", " padded "))
		must(t, f.SetCellValue(s, "K1", 3))
		must(t, f.SetCellFormula(s, "K1", "B1-39")) // excelize stores the cached value as t="str"
		must(t, f.SetCellFormula(s, "L1", "B1*2"))  // no cached value: kempt evaluates it
		must(t, f.SetCellValue(s, "M1", 0.1))
		must(t, f.SetCellStyle(s, "M1", "M1", numFmtStyle(t, f, 0, `"mm"0`)))
		for _, ref := range []string{"A2", "B2", "C2", "D2", "E2", "F2", "G2", "H2", "I2", "J2", "K2", "L2", "A3", "B3", "C3", "D3", "E3"} {
			must(t, f.SetCellValue(s, ref, "x"))
		}
	})
	data = replaceCells(t, data, map[string]string{
		"A2": `<c r="A2" t="e"><v>#DIV/0!</v></c>`,
		"B2": `<c r="B2" t="d"><v>2026-03-15T00:00:00Z</v></c>`,
		"C2": `<c r="C2"><f>B1*2</f><v>84</v></c>`,
		"D2": `<c r="D2" t="e"><f>1/0</f><v>#DIV/0!</v></c>`,
		"E2": `<c r="E2" t="str"><f>"00"&amp;"123"</f><v>00123</v></c>`,
		"F2": fmt.Sprintf(`<c r="F2" s="%d"><f>E1+1</f><v>46097</v></c>`, dateStyle),
		"G2": `<c r="G2" t="b"><f>B1&gt;1</f><v>1</v></c>`,
		"H2": `<c r="H2"><v>0.30000000000000004</v></c>`,
		"I2": `<c r="I2"><v>1E-7</v></c>`,
		"J2": `<c r="J2" t="n"><v>1.2345678901234567E+20</v></c>`,
		"K2": `<c r="K2" t="str"><v>plain</v></c>`,
		"L2": `<c r="L2" t="inlineStr"><is><t>inline</t></is></c>`,
		"A3": `<c r="A3"><v>abc</v></c>`,
		"B3": `<c r="B3" t="str"><f>A1&amp;""</f><v>1.50</v></c>`,
		"C3": `<c r="C3" t="b"><v>0</v></c>`,
		"D3": `<c r="D3"><f t="shared" ref="D3:E3" si="0">B1*2</f><v>84</v></c>`,
		"E3": `<c r="E3"><f t="shared" si="0"/><v>2</v></c>`,
	})
	g := readXLSX(t, data, Options{})[0]

	num := func(v float64) *float64 { return &v }
	tests := []struct {
		ref     string
		kind    grid.Kind
		raw     string
		num     *float64
		dateFmt bool
		formula string
	}{
		{"A1", grid.String, "Name", nil, false, ""},
		{"B1", grid.Number, "42", num(42), false, ""},
		{"C1", grid.Bool, "TRUE", nil, false, ""},
		{"D1", grid.Bool, "FALSE", nil, false, ""},
		{"E1", grid.Date, "46096", num(46096), true, ""},
		{"F1", grid.Date, "46096.5", num(46096.5), true, ""},
		{"G1", grid.Number, "1234.5", num(1234.5), false, ""},
		{"H1", grid.Number, "0.125", num(0.125), false, ""},
		{"I1", grid.String, "2026-03-15", nil, false, ""},
		{"J1", grid.String, " padded ", nil, false, ""},
		{"K1", grid.Formula, "3", num(3), false, "B1-39"},
		{"L1", grid.Formula, "84", num(84), false, "B1*2"}, // evaluated: the file has no cached value
		{"M1", grid.Number, "0.1", num(0.1), false, ""},
		{"A2", grid.Error, "#DIV/0!", nil, false, ""},
		{"B2", grid.Date, "2026-03-15T00:00:00Z", nil, true, ""},
		{"C2", grid.Formula, "84", num(84), false, "B1*2"},
		{"D2", grid.Error, "#DIV/0!", nil, false, ""},
		{"E2", grid.Formula, "00123", nil, false, `"00"&"123"`},
		{"F2", grid.Formula, "46097", num(46097), true, "E1+1"},
		{"G2", grid.Formula, "TRUE", nil, false, "B1>1"},
		{"H2", grid.Number, "0.3", num(0.30000000000000004), false, ""},
		{"I2", grid.Number, "0.0000001", num(1e-7), false, ""},
		{"J2", grid.Number, "123456789012346000000", num(1.2345678901234567e20), false, ""},
		{"K2", grid.String, "plain", nil, false, ""},
		{"L2", grid.String, "inline", nil, false, ""},
		{"A3", grid.String, "abc", nil, false, ""},
		{"B3", grid.Formula, "1.50", nil, false, `A1&""`},
		{"C3", grid.Bool, "FALSE", nil, false, ""},
		{"D3", grid.Formula, "84", num(84), false, "B1*2"},
		{"E3", grid.Formula, "2", num(2), false, "C1*2"},
	}
	for _, tt := range tests {
		c := cellAt(t, g, tt.ref)
		if c.Kind != tt.kind || c.Raw != tt.raw || c.DateFmt != tt.dateFmt || c.Formula != tt.formula {
			t.Errorf("%s = {%v %q dateFmt=%v formula=%q}, want {%v %q dateFmt=%v formula=%q}",
				tt.ref, c.Kind, c.Raw, c.DateFmt, c.Formula, tt.kind, tt.raw, tt.dateFmt, tt.formula)
		}
		switch {
		case tt.num == nil && c.Num != nil:
			t.Errorf("%s Num = %v, want nil", tt.ref, *c.Num)
		case tt.num != nil && (c.Num == nil || *c.Num != *tt.num):
			t.Errorf("%s Num = %v, want %v", tt.ref, c.Num, *tt.num)
		}
	}
	// A formula saved without a cached result is evaluated.
	if c := cellAt(t, g, "L1"); c.Kind != grid.Formula || c.Raw != "84" || c.Num == nil || *c.Num != 84 || c.Formula != "B1*2" {
		t.Errorf("uncached formula L1 = %+v, want its computed value 84", c)
	}
	if g.Source != "xlsx" || g.Delimiter != 0 || g.Sheet != "Sheet1" || g.Date1904 {
		t.Errorf("grid meta = %q %q %q %v", g.Source, g.Delimiter, g.Sheet, g.Date1904)
	}
}

func TestXLSXBoundsAndMerges(t *testing.T) {
	tests := []struct {
		name       string
		build      func(f *excelize.File)
		patch      func(t *testing.T, data []byte) []byte
		rows, cols int
		merges     []string
		values     map[string]string // ref → Raw ("" = must be empty)
	}{
		{
			name: "leading blank rows and columns are kept",
			build: func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "C3", "x"))
			},
			rows: 3, cols: 3,
			values: map[string]string{"C3": "x", "A1": ""},
		},
		{
			name: "merge extents widen the grid and merges come back sorted",
			build: func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "Title"))
				must(t, f.SetCellValue("Sheet1", "A2", "Group"))
				must(t, f.SetCellValue("Sheet1", "B2", 1))
				must(t, f.SetCellValue("Sheet1", "B3", 2))
				must(t, f.MergeCell("Sheet1", "F8", "G9"))
				must(t, f.MergeCell("Sheet1", "A2", "A4"))
				must(t, f.MergeCell("Sheet1", "A1", "D1"))
			},
			rows: 9, cols: 7,
			merges: []string{"A1:D1", "A2:A4", "F8:G9"},
			values: map[string]string{"A1": "Title", "A2": "Group", "B3": "2", "A3": "", "G9": ""},
		},
		{
			name: "values hidden under a merge are dropped",
			build: func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "Title"))
				must(t, f.SetCellValue("Sheet1", "B1", "ghost"))
			},
			patch: func(t *testing.T, data []byte) []byte {
				return patchPart(t, data, "xl/worksheets/sheet1.xml", func(s string) string {
					return strings.Replace(s, "</sheetData>", `</sheetData><mergeCells count="1"><mergeCell ref="A1:C1"/></mergeCells>`, 1)
				})
			},
			rows: 1, cols: 3,
			merges: []string{"A1:C1"},
			values: map[string]string{"A1": "Title", "B1": ""},
		},
		{
			name: "whitespace and formulas that evaluate to empty past the data do not widen the grid",
			build: func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "a"))
				must(t, f.SetCellValue("Sheet1", "B2", "b"))
				must(t, f.SetCellValue("Sheet1", "D1", "   "))
				must(t, f.SetCellFormula("Sheet1", "A5", `IF(B2="x","y","")`))
			},
			rows: 2, cols: 2,
			values: map[string]string{"A1": "a", "B2": "b"},
		},
		{
			name: "uncached formulas past the data are evaluated and widen the grid",
			build: func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "a"))
				must(t, f.SetCellValue("Sheet1", "B2", 2))
				must(t, f.SetCellFormula("Sheet1", "A5", "B2*21"))
				must(t, f.SetCellFormula("Sheet1", "B5", "1/0"))
			},
			rows: 5, cols: 2,
			values: map[string]string{"A1": "a", "B2": "2", "A5": "42", "B5": "#CALC!"},
		},
		{
			name:  "empty sheet",
			build: func(f *excelize.File) {},
			rows:  0, cols: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := newBook(t, tt.build)
			if tt.patch != nil {
				data = tt.patch(t, data)
			}
			gs := readXLSX(t, data, Options{})
			if len(gs) != 1 {
				t.Fatalf("got %d grids", len(gs))
			}
			g := gs[0]
			if g.Rows != tt.rows || g.Cols != tt.cols || len(g.Cells) != tt.rows {
				t.Fatalf("size = %dx%d (%d row slices), want %dx%d", g.Rows, g.Cols, len(g.Cells), tt.rows, tt.cols)
			}
			var merges []string
			for _, m := range g.Merges {
				merges = append(merges, m.A1())
			}
			if fmt.Sprint(merges) != fmt.Sprint(tt.merges) {
				t.Errorf("merges = %v, want %v", merges, tt.merges)
			}
			for _, m := range g.Merges {
				for r := m.R1; r <= m.R2; r++ {
					for c := m.C1; c <= m.C2; c++ {
						if got := g.At(r, c).Merge; got == nil || *got != m {
							t.Errorf("%s merge = %v, want %v", grid.CellRef(r, c), got, m)
						}
					}
				}
			}
			for ref, want := range tt.values {
				c := cellAt(t, g, ref)
				if c.Raw != want || (want == "") != (c.Kind == grid.Empty) {
					t.Errorf("%s = {%v %q}, want %q", ref, c.Kind, c.Raw, want)
				}
			}
		})
	}
}

func TestXLSXSheetSelection(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "visible"))
		for _, n := range []string{"Hidden", "Empty", "VeryHidden"} {
			_, err := f.NewSheet(n)
			must(t, err)
		}
		must(t, f.SetCellValue("Hidden", "B2", "secret"))
		must(t, f.SetSheetVisible("Hidden", false))
		must(t, f.SetSheetVisible("VeryHidden", false, true))
		must(t, f.AddChartSheet("Chart", &excelize.Chart{
			Type:   excelize.Col,
			Series: []excelize.ChartSeries{{Name: "Sheet1!$A$1", Categories: "Sheet1!$A$1", Values: "Sheet1!$A$1"}},
		}))
		must(t, f.SetWorkbookProps(&excelize.WorkbookPropsOptions{Date1904: ptr(true)}))
	})

	tests := []struct {
		sheet   string
		want    []string
		wantErr error
	}{
		{"", []string{"Sheet1", "Empty"}, nil},
		{"Hidden", []string{"Hidden"}, nil},
		{"hidden", []string{"Hidden"}, nil},
		{"VeryHidden", []string{"VeryHidden"}, nil},
		{"Empty", []string{"Empty"}, nil},
		{"Missing", nil, ErrSheetNotFound},
		{"Chart", nil, errNotWorksheet},
	}
	for _, tt := range tests {
		gs, err := Bytes("book.xlsx", data, Options{Sheet: tt.sheet})
		if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
			t.Errorf("sheet %q: err = %v, want %v", tt.sheet, err, tt.wantErr)
			continue
		}
		if got := sheetNames(gs); fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("sheet %q: got %v, want %v", tt.sheet, got, tt.want)
		}
		for _, g := range gs {
			if !g.Date1904 || g.Source != "xlsx" {
				t.Errorf("sheet %q: Date1904=%v Source=%q", g.Sheet, g.Date1904, g.Source)
			}
		}
	}
	gs := readXLSX(t, data, Options{Sheet: "Hidden"})
	if c := cellAt(t, gs[0], "B2"); c.Raw != "secret" || gs[0].Rows != 2 || gs[0].Cols != 2 {
		t.Errorf("hidden sheet B2 = %q, size %dx%d", c.Raw, gs[0].Rows, gs[0].Cols)
	}
	gs = readXLSX(t, data, Options{})
	if g := gs[1]; g.Rows != 0 || g.Cols != 0 || len(g.Cells) != 0 {
		t.Errorf("empty sheet size = %dx%d", g.Rows, g.Cols)
	}
}

func TestXLSXNoVisibleWorksheet(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "x"))
		must(t, f.AddChartSheet("Chart", &excelize.Chart{
			Type:   excelize.Col,
			Series: []excelize.ChartSeries{{Name: "Sheet1!$A$1", Categories: "Sheet1!$A$1", Values: "Sheet1!$A$1"}},
		}))
	})
	data = patchPart(t, data, "xl/workbook.xml", func(s string) string {
		return strings.Replace(s, `<sheet name="Sheet1"`, `<sheet state="hidden" name="Sheet1"`, 1)
	})
	if _, err := Bytes("book.xlsx", data, Options{}); err == nil || !strings.Contains(err.Error(), "Sheet1") {
		t.Errorf("err = %v, want a hint naming the hidden sheet", err)
	}
	if gs := readXLSX(t, data, Options{Sheet: "Sheet1"}); cellAt(t, gs[0], "A1").Raw != "x" {
		t.Error("named hidden sheet not read")
	}
}

func TestXLSXMaxRows(t *testing.T) {
	plain := newBook(t, func(f *excelize.File) {
		for r := 1; r <= 5; r++ {
			must(t, f.SetCellValue("Sheet1", fmt.Sprintf("A%d", r), r))
		}
	})
	merged := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "x"))
		must(t, f.MergeCell("Sheet1", "A1", "A6"))
	})
	tests := []struct {
		name    string
		data    []byte
		max     int
		wantErr bool
	}{
		{"unlimited", plain, 0, false},
		{"at limit", plain, 5, false},
		{"over limit", plain, 4, true},
		{"merge extent counts", merged, 5, true},
		{"merge extent at limit", merged, 6, false},
	}
	for _, tt := range tests {
		_, err := Bytes("book.xlsx", tt.data, Options{MaxRows: tt.max})
		if got := errors.Is(err, ErrTooManyRows); got != tt.wantErr || (!tt.wantErr && err != nil) {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestXLSXStyleCache(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		date := numFmtStyle(t, f, 14, "")
		money := numFmtStyle(t, f, 0, "#,##0.00")
		for r := 1; r <= 50; r++ {
			must(t, f.SetCellValue("Sheet1", fmt.Sprintf("A%d", r), 46000+r))
			must(t, f.SetCellValue("Sheet1", fmt.Sprintf("B%d", r), float64(r)/4))
			must(t, f.SetCellValue("Sheet1", fmt.Sprintf("C%d", r), r))
			must(t, f.SetCellValue("Sheet1", fmt.Sprintf("D%d", r), "text"))
		}
		must(t, f.SetCellStyle("Sheet1", "A1", "A50", date))
		must(t, f.SetCellStyle("Sheet1", "B1", "B50", money))
	})
	f, err := excelize.OpenReader(bytes.NewReader(data))
	must(t, err)
	defer f.Close()
	x := &xlsxReader{f: f, styles: map[int]numFmt{}}
	g, err := x.sheet("Sheet1")
	must(t, err)
	if len(x.styles) != 3 { // default, date, money
		t.Errorf("cached %d style ids, want 3: %v", len(x.styles), x.styles)
	}
	for r := 0; r < 50; r++ {
		if a, b := g.At(r, 0), g.At(r, 1); a.Kind != grid.Date || !a.DateFmt || b.Kind != grid.Number || b.DateFmt {
			t.Fatalf("row %d: A=%v/%v B=%v/%v", r+1, a.Kind, a.DateFmt, b.Kind, b.DateFmt)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestXLSXStringFormulaResults(t *testing.T) {
	tests := []struct {
		raw string
		num *float64 // nil = stays text
	}{
		{"3", ptr(3.0)},
		{"0.1", ptr(0.1)},
		{"-12.5", ptr(-12.5)},
		{"123456789012345", ptr(123456789012345.0)},
		{"4111111111111111", nil}, // 16 digits: a float would round it
		{"12345678901234567890", nil},
		{"0.1234567890123456", nil},
		{"-0", nil},
		{"1.50", nil},
		{"00123", nil},
		{"1E3", nil},
		{"abc", nil},
	}
	refs := make([]string, len(tests))
	data := newBook(t, func(f *excelize.File) {
		for i := range tests {
			refs[i] = fmt.Sprintf("A%d", i+1)
			must(t, f.SetCellValue("Sheet1", refs[i], "x"))
		}
	})
	cells := map[string]string{}
	for i, tt := range tests {
		cells[refs[i]] = fmt.Sprintf(`<c r="%s" t="str"><f>B1&amp;""</f><v>%s</v></c>`, refs[i], tt.raw)
	}
	g := readXLSX(t, replaceCells(t, data, cells), Options{})[0]
	for i, tt := range tests {
		c := cellAt(t, g, refs[i])
		if c.Kind != grid.Formula || c.Raw != tt.raw || c.Formula != `B1&""` {
			t.Errorf("%q: got {%v %q formula=%q}", tt.raw, c.Kind, c.Raw, c.Formula)
		}
		switch {
		case tt.num == nil && c.Num != nil:
			t.Errorf("%q: Num = %v, want nil (text kept)", tt.raw, *c.Num)
		case tt.num != nil && (c.Num == nil || *c.Num != *tt.num):
			t.Errorf("%q: Num = %v, want %v", tt.raw, c.Num, *tt.num)
		}
	}
}

// sheetData replaces the rows of sheet1.xml, for sheets too sparse to build
// quickly through excelize.
func sheetData(t *testing.T, rows string) []byte {
	t.Helper()
	data := newBook(t, func(f *excelize.File) { must(t, f.SetCellValue("Sheet1", "A1", "x")) })
	return patchPart(t, data, "xl/worksheets/sheet1.xml", func(s string) string {
		return regexp.MustCompile(`<sheetData>.*</sheetData>`).ReplaceAllLiteralString(s, "<sheetData>"+rows+"</sheetData>")
	})
}

func TestXLSXTooSparse(t *testing.T) {
	var farRows strings.Builder
	for r := 1; r <= 300; r++ {
		fmt.Fprintf(&farRows, `<row r="%d"><c r="A%d"><v>%d</v></c><c r="XFD%d"><v>1</v></c></row>`, r, r, r, r)
	}
	tests := []struct {
		name       string
		data       []byte
		opts       Options
		wantErr    error
		rows, cols int
	}{
		{
			name:    "far corner value",
			data:    sheetData(t, `<row r="1"><c r="A1"><v>1</v></c></row><row r="300"><c r="XFD300"><v>2</v></c></row>`),
			wantErr: ErrTooSparse,
		},
		{
			name:    "far value in every row",
			data:    sheetData(t, farRows.String()),
			opts:    Options{MaxRows: 50000},
			wantErr: ErrTooSparse,
		},
		{
			name: "far merge with no values",
			data: newBook(t, func(f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "x"))
				must(t, f.MergeCell("Sheet1", "XFC299", "XFD300"))
			}),
			wantErr: ErrTooSparse,
		},
		{
			name:    "MaxRows is checked first",
			data:    sheetData(t, `<row r="1"><c r="A1"><v>1</v></c></row><row r="300"><c r="XFD300"><v>2</v></c></row>`),
			opts:    Options{MaxRows: 100},
			wantErr: ErrTooManyRows,
		},
		{
			name: "sparse but small enough",
			data: sheetData(t, `<row r="1"><c r="A1"><v>1</v></c></row><row r="2000"><c r="ZZ2000"><v>2</v></c></row>`),
			rows: 2000, cols: 702,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gs, err := Bytes("book.xlsx", tt.data, tt.opts)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := gs[0]; g.Rows != tt.rows || g.Cols != tt.cols {
				t.Errorf("size = %dx%d, want %dx%d", g.Rows, g.Cols, tt.rows, tt.cols)
			}
		})
	}
}
