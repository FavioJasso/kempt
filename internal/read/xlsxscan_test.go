package read

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/FavioJasso/kempt/internal/grid"
)

func TestShiftFormula(t *testing.T) {
	tests := []struct {
		in     string
		dr, dc int
		want   string
	}{
		{"B1*2", 0, 1, "C1*2"},
		{"SUM(A1:A3)", 2, 0, "SUM(A3:A5)"},
		{"$A$1+A$1+$A1", 1, 1, "$A$1+B$1+$A2"},
		{`IF(A1="B2",B2,"")`, 1, 0, `IF(A2="B2",B3,"")`},
		{"'Q1 2024'!A1+Sheet2!B2", 1, 0, "'Q1 2024'!A2+Sheet2!B3"},
		{"LOG10(A1)", 1, 0, "LOG10(A2)"},
		{"A1-1", -1, 0, "#REF!-1"},
		{"XFD1", 0, 1, "#REF!"},
		{"my_A1+A1", 1, 0, "my_A1+A2"},
		{"A1", 0, 0, "A1"},
		{"Table1[[#This Row],[FY2025]]-Table1[[#This Row],[FY2024]]", 1, 1, "Table1[[#This Row],[FY2025]]-Table1[[#This Row],[FY2024]]"},
		{"SUM(C$2:C2)+COUNT(B:B)-COUNT(2:2)", 1, 0, "SUM(C$2:C3)+COUNT(B:B)-COUNT(3:3)"},
		{"SUM(A:A)+SUM($1:$1)+A1", 0, 1, "SUM(B:B)+SUM($1:$1)+B1"},
		{"SUM($A:B)", 0, 2, "SUM($A:D)"},
		{"[1]Sheet1!A1", 1, 0, "[1]Sheet1!A2"},
		{"ROUND(A1,2)", 1, 0, "ROUND(A2,2)"},
		{"A1:XFD1", 0, 1, "B1:#REF!"},
	}
	for _, tt := range tests {
		if got := shiftFormula(tt.in, tt.dr, tt.dc); got != tt.want {
			t.Errorf("shiftFormula(%q, %d, %d) = %q, want %q", tt.in, tt.dr, tt.dc, got, tt.want)
		}
	}
}

func TestDecodeEscapes(t *testing.T) {
	tests := map[string]string{
		"plain":            "plain",
		"a_x000D_b":        "a\rb",
		"_x005F_x000D_":    "_x000D_",
		"under_x_score":    "under_x_score",
		"tab_x0009_end":    "tab\tend",
		"bad_xZZZZ_escape": "bad_xZZZZ_escape",
	}
	for in, want := range tests {
		if got := decodeEscapes(in); got != want {
			t.Errorf("decodeEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadSST(t *testing.T) {
	doc := `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4">
  <si><t>plain</t></si>
  <si>
    <r><rPr><b/></rPr><t>bold </t></r>
    <r><t xml:space="preserve">and plain</t></r>
  </si>
  <si><t>東京</t><rPh sb="0" eb="2"><t>トウキョウ</t></rPh></si>
  <si><t>line_x000D_break &amp; more</t></si>
</sst>`
	got, err := readSST(xml.NewDecoder(strings.NewReader(doc)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plain", "bold and plain", "東京", "line\rbreak & more"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestScanSheetXML(t *testing.T) {
	rows := `
<row>
  <c t="inlineStr"><is>
    <t>pretty </t>
    <r><t>printed</t></r>
    <rPh><t>ignored</t></rPh>
  </is></c>
  <c><v>1</v></c>
</row>
<row r="3"><c r="B3"><v>2</v></c><c><v>3</v></c></row>
<x:row r="4"><x:c r="A4" t="b"><x:v>1</x:v></x:c></x:row>`
	gs, err := Bytes("book.xlsx", sheetData(t, rows), Options{})
	if err != nil {
		t.Fatal(err)
	}
	g := gs[0]
	checks := map[string]string{"A1": "pretty printed", "B1": "1", "B3": "2", "C3": "3", "A4": "TRUE"}
	for ref, want := range checks {
		if c := cellAt(t, g, ref); c.Raw != want {
			t.Errorf("%s = %q, want %q", ref, c.Raw, want)
		}
	}

	// A shared string index past the table reads as an error value, which
	// becomes NULL with a logged decision, instead of failing the sheet.
	gs, err = Bytes("book.xlsx", sheetData(t, `<row r="1"><c r="A1" t="s"><v>99</v></c><c r="B1"><v>5</v></c></row>`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c := cellAt(t, gs[0], "A1"); c.Kind != grid.Error || c.Raw != "#REF!" {
		t.Errorf("bad shared string index: A1 = %+v", c)
	}
	if c := cellAt(t, gs[0], "B1"); c.Raw != "5" {
		t.Errorf("B1 = %+v", c)
	}

	// A shared formula child before its master still gets its formula.
	gs, err = Bytes("book.xlsx", sheetData(t, `<row r="1"><c r="A1"><v>1</v></c><c r="B1"><f t="shared" si="0"/><v>2</v></c></row>`+
		`<row r="2"><c r="A2"><v>3</v></c><c r="B2"><f t="shared" ref="B1:B2" si="0">A2*2</f><v>6</v></c></row>`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c := cellAt(t, gs[0], "B1"); c.Kind != grid.Formula || c.Formula != "A1*2" {
		t.Errorf("late master: B1 = %+v", c)
	}
	// Unreadable row numbers count as the next row; $ in a reference is
	// ignored; rows past Excel's last row are an error.
	gs, err = Bytes("book.xlsx", sheetData(t, `<row r="0"><c r="$A$1"><v>1</v></c></row><row r="x"><c><v>2</v></c></row>`), Options{})
	if err != nil || cellAt(t, gs[0], "A1").Raw != "1" || cellAt(t, gs[0], "A2").Raw != "2" {
		t.Errorf("lenient rows: %v", err)
	}
	if _, err := Bytes("book.xlsx", sheetData(t, `<row r="1048577"><c><v>1</v></c></row>`), Options{}); err == nil {
		t.Error("row 1048577 should be rejected")
	}
}

func TestCalcBytes(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "n"))
		must(t, f.SetCellValue("Sheet1", "B1", "double"))
		for r := 2; r <= 5; r++ {
			must(t, f.SetCellValue("Sheet1", "A"+string(rune('0'+r)), r))
			must(t, f.SetCellFormula("Sheet1", "B"+string(rune('0'+r)), "A"+string(rune('0'+r))+"*2"))
		}
	})
	gs, err := Bytes("book.xlsx", data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c := cellAt(t, gs[0], "B3"); c.Kind != grid.Formula || c.Raw != "6" {
		t.Fatalf("evaluated B3 = %+v", c)
	}
	gs, err = Bytes("book.xlsx", data, Options{CalcBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if c := cellAt(t, gs[0], "B3"); c.Kind != grid.Error || c.Raw != "#CALC!" || c.Formula != "A3*2" {
		t.Fatalf("over the limit B3 = %+v", c)
	}
}

func TestChartSheetSkipped(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "x"))
		must(t, f.SetCellValue("Sheet1", "A2", 1))
		must(t, f.AddChartSheet("Chart", &excelize.Chart{Type: excelize.Col,
			Series: []excelize.ChartSeries{{Name: "Sheet1!$A$1", Values: "Sheet1!$A$2:$A$2"}}}))
	})
	gs, err := Bytes("book.xlsx", data, Options{})
	if err != nil || len(gs) != 1 || gs[0].Sheet != "Sheet1" {
		t.Fatalf("grids = %d, err = %v", len(gs), err)
	}
	if _, err := Bytes("book.xlsx", data, Options{Sheet: "Chart"}); !errors.Is(err, errNotWorksheet) {
		t.Fatalf("named chart sheet: err = %v", err)
	}
}

// Excel reads a cell under a merge as empty; excelize evaluates it as the
// merge's anchor unless the merges are removed first.
func TestUncachedFormulasOverMerges(t *testing.T) {
	data := newBook(t, func(f *excelize.File) {
		must(t, f.SetSheetRow("Sheet1", "A1", &[]any{"Region", "Q1", "Q2", "Total"}))
		must(t, f.SetSheetRow("Sheet1", "A2", &[]any{"North", 10, 20}))
		must(t, f.SetSheetRow("Sheet1", "A3", &[]any{"South", 30, 40}))
		must(t, f.SetSheetRow("Sheet1", "A4", &[]any{"East", 50, 60}))
		for r := 2; r <= 4; r++ {
			n := string(rune('0' + r))
			must(t, f.SetCellFormula("Sheet1", "D"+n, "SUM(B"+n+":C"+n+")"))
		}
		must(t, f.SetCellFormula("Sheet1", "C5", "SUM(C2:C4)"))
		must(t, f.MergeCell("Sheet1", "B3", "C3")) // drops C3's 40
	})
	gs, err := Bytes("book.xlsx", data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"D3": "30", "C5": "80", "D2": "30"} {
		if c := cellAt(t, gs[0], ref); c.Raw != want {
			t.Errorf("%s = %q, want %q", ref, c.Raw, want)
		}
	}
}

// A row number repeated many times must not make reading quadratic.
func TestRepeatedRowNumbers(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString(`<row r="1"><c r="B1"><v>1</v></c><c r="A1"><v>2</v></c></row>`)
	}
	start := time.Now()
	gs, err := Bytes("book.xlsx", sheetData(t, b.String()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	if c := cellAt(t, gs[0], "A1"); c.Raw != "2" {
		t.Fatalf("A1 = %q", c.Raw)
	}
}

// The cell budget stops the scan early instead of after the whole sheet.
func TestCellBudgetDuringScan(t *testing.T) {
	var b strings.Builder
	for r := 1; r <= 200; r++ {
		fmt.Fprintf(&b, `<row r="%d">`, r)
		for c := 0; c < 100; c++ {
			b.WriteString(`<c><v>1</v></c>`)
		}
		b.WriteString(`</row>`)
	}
	_, err := Bytes("book.xlsx", sheetData(t, b.String()), Options{MaxCells: 1000})
	if !errors.Is(err, ErrTooManyCells) {
		t.Fatalf("err = %v", err)
	}
	b.Reset()
	for i := 0; i < 2000; i++ {
		b.WriteString(`<row r="1"><c r="A1"><v>1</v></c></row>`)
	}
	if _, err := Bytes("book.xlsx", sheetData(t, b.String()), Options{MaxCells: 1000}); !errors.Is(err, ErrTooManyCells) {
		t.Fatalf("repeated cells: err = %v", err)
	}
}

func TestPartEncodingsAndPaths(t *testing.T) {
	base := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "name"))
		must(t, f.SetCellValue("Sheet1", "A2", "x"))
	})
	decl := regexp.MustCompile(`^<\?xml[^>]*\?>`)
	// A Latin-1 sheet part with an inline string.
	latin := patchPart(t, base, "xl/worksheets/sheet1.xml", func(s string) string {
		if !decl.MatchString(s) {
			t.Fatalf("no XML declaration: %.80s", s)
		}
		s = decl.ReplaceAllLiteralString(s, `<?xml version="1.0" encoding="ISO-8859-1"?>`)
		return regexp.MustCompile(`<sheetData>.*</sheetData>`).ReplaceAllLiteralString(s,
			"<sheetData><row r=\"1\"><c r=\"A1\" t=\"inlineStr\"><is><t>Caf\xe9</t></is></c></row></sheetData>")
	})
	gs, err := Bytes("book.xlsx", latin, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c := cellAt(t, gs[0], "A1"); c.Raw != "Café" {
		t.Errorf("A1 = %q", c.Raw)
	}
	// Windows separators in a relationship target.
	target := regexp.MustCompile(`Target="/?(xl/)?worksheets/sheet1\.xml"`)
	bs := patchPart(t, base, "xl/_rels/workbook.xml.rels", func(s string) string {
		if !target.MatchString(s) {
			t.Fatalf("no sheet target in %s", s)
		}
		return target.ReplaceAllLiteralString(s, `Target="worksheets\sheet1.xml"`)
	})
	if gs, err := Bytes("book.xlsx", bs, Options{}); err != nil || cellAt(t, gs[0], "A2").Raw != "x" {
		t.Fatalf("backslash target: %v", err)
	}
	// Relationship types in another case, or missing, still read as worksheets.
	wsType := regexp.MustCompile(`Type="[^"]*/worksheet"`)
	for _, typ := range []string{`Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/Worksheet"`, `Type=""`} {
		patched := patchPart(t, base, "xl/_rels/workbook.xml.rels", func(s string) string {
			if !wsType.MatchString(s) {
				t.Fatalf("no worksheet type in %s", s)
			}
			return wsType.ReplaceAllLiteralString(s, typ)
		})
		if gs, err := Bytes("book.xlsx", patched, Options{}); err != nil || cellAt(t, gs[0], "A2").Raw != "x" {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	// Percent-encoded targets.
	renamed := renamePart(t, base, "xl/worksheets/sheet1.xml", "xl/worksheets/sheet 1.xml")
	encoded := patchPart(t, renamed, "xl/_rels/workbook.xml.rels", func(s string) string {
		return target.ReplaceAllLiteralString(s, `Target="worksheets/sheet%201.xml"`)
	})
	if gs, err := Bytes("book.xlsx", encoded, Options{}); err != nil || cellAt(t, gs[0], "A2").Raw != "x" {
		t.Fatalf("encoded target: %v", err)
	}
}

// renamePart renames one zip entry.
func renamePart(t *testing.T, data []byte, from, to string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, zf := range zr.File {
		rc, err := zf.Open()
		must(t, err)
		b, err := io.ReadAll(rc)
		must(t, err)
		rc.Close()
		name := zf.Name
		if name == from {
			name = to
		}
		w, err := zw.Create(name)
		must(t, err)
		_, err = w.Write(b)
		must(t, err)
	}
	must(t, zw.Close())
	return out.Bytes()
}
