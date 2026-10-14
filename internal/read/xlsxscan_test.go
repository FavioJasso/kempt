package read

import (
	"errors"
	"strings"
	"testing"

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
	xml := `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4">
  <si><t>plain</t></si>
  <si>
    <r><rPr><b/></rPr><t>bold </t></r>
    <r><t xml:space="preserve">and plain</t></r>
  </si>
  <si><t>東京</t><rPh sb="0" eb="2"><t>トウキョウ</t></rPh></si>
  <si><t>line_x000D_break &amp; more</t></si>
</sst>`
	got, err := readSST([]byte(xml))
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

	if _, err := Bytes("book.xlsx", sheetData(t, `<row r="1"><c r="A1" t="s"><v>99</v></c></row>`), Options{}); err == nil || !strings.Contains(err.Error(), "shared string") {
		t.Errorf("bad shared string index: err = %v", err)
	}
	if _, err := Bytes("book.xlsx", sheetData(t, `<row r="0"><c><v>1</v></c></row>`), Options{}); err == nil {
		t.Error("row 0 should be rejected")
	}
}

func TestCalcLimit(t *testing.T) {
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
	gs, err = Bytes("book.xlsx", data, Options{CalcLimit: 3})
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
