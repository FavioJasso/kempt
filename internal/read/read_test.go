package read

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestBytesDispatch(t *testing.T) {
	book := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "A1", "x"))
	})
	tests := []struct {
		name    string
		file    string
		data    []byte
		source  string // "" when an error is expected
		delim   rune
		errText string
	}{
		{"xlsx", "book.xlsx", book, "xlsx", 0, ""},
		{"xlsx without extension", "upload", book, "xlsx", 0, ""},
		{"zip content beats .csv extension", "book.csv", book, "xlsx", 0, ""},
		{"csv", "data.csv", []byte("a,b\n1,2\n"), "csv", ',', ""},
		{"text under .xls is read as csv", "export.xls", []byte("a\tb\n1\t2\n"), "csv", '\t', ""},
		{"txt", "data.txt", []byte("a;b\n1;2\n"), "csv", ';', ""},
		{"empty csv", "empty.csv", nil, "csv", ',', ""},
		{"csv with angle-bracket header", "tags.csv", []byte("<Name>,Age\nx,1\n"), "csv", ',', ""},
		{"legacy xls", "old.xls", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...), "", 0, "save it as .xlsx"},
		{"encrypted xlsx", "locked.xlsx", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...), "", 0, "password"},
		{"pdf", "report.pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), "", 0, "PDF"},
		{"ods", "sheet.ods", book, "", 0, ".ods"},
		{"xlsb", "sheet.xlsb", book, "", 0, ".xlsb"},
		{"html disguised as xls", "report.xls", []byte("<html><body><table></table></body></html>"), "", 0, "HTML"},
		{"xml spreadsheet", "report.xls", []byte("<?xml version=\"1.0\"?><Workbook/>"), "", 0, "HTML or XML"},
		{"binary", "blob.bin", []byte{0x00, 0x01, 0x02, 0xFF}, "", 0, "save it as .xlsx or .csv"},
		{"empty xlsx", "empty.xlsx", nil, "", 0, "empty"},
	}
	for _, tt := range tests {
		gs, err := Bytes(tt.file, tt.data, Options{})
		if tt.source == "" {
			if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("%s: err = %v, want ErrUnsupported mentioning %q", tt.name, err, tt.errText)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if len(gs) != 1 || gs[0].Source != tt.source || gs[0].Delimiter != tt.delim {
			t.Errorf("%s: got %d grids, source %q delim %q", tt.name, len(gs), gs[0].Source, gs[0].Delimiter)
		}
	}
}

func TestBytesCSVIgnoresSheetOption(t *testing.T) {
	gs, err := Bytes("orders.csv", []byte("a\n"), Options{Sheet: "Other"})
	if err != nil || len(gs) != 1 || gs[0].Sheet != "orders" {
		t.Fatalf("got %v, %v", gs, err)
	}
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	book := newBook(t, func(f *excelize.File) {
		must(t, f.SetCellValue("Sheet1", "B2", 7))
	})
	must(t, os.WriteFile(filepath.Join(dir, "book.xlsx"), book, 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "ambiguous_dates.csv"), []byte("\xEF\xBB\xBFid;event_date\r\n1;03/04/2026\r\n"), 0o600))

	tests := []struct {
		file, sheet, source string
		rows, cols          int
	}{
		{"book.xlsx", "Sheet1", "xlsx", 2, 2},
		{"ambiguous_dates.csv", "ambiguous_dates", "csv", 2, 2},
	}
	for _, tt := range tests {
		gs, err := File(filepath.Join(dir, tt.file), Options{})
		if err != nil {
			t.Fatalf("%s: %v", tt.file, err)
		}
		g := gs[0]
		if g.Sheet != tt.sheet || g.Source != tt.source || g.Rows != tt.rows || g.Cols != tt.cols {
			t.Errorf("%s: got %q %q %dx%d", tt.file, g.Sheet, g.Source, g.Rows, g.Cols)
		}
	}
	gs, _ := File(filepath.Join(dir, "ambiguous_dates.csv"), Options{})
	if got := gs[0].Cells[0][0].Raw; got != "id" {
		t.Errorf("BOM not stripped: %q", got)
	}
	if _, err := File(filepath.Join(dir, "missing.csv"), Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file err = %v", err)
	}
}

func TestCheckDensity(t *testing.T) {
	tests := []struct {
		rows, cols, stored int
		wantErr            bool
	}{
		{0, 0, 0, false},
		{1, 1, 1, false},
		{2048, 2048, 2, false},                 // exactly sparseFloor cells
		{2048, 2049, 1, true},                  // one column past the floor, nearly empty
		{4096, 2048, 4096 * 2048 / 16, false},  // one cell in 16 stored
		{4096, 2048, 4096*2048/16 - 1, true},   // just under one in 16
		{1048576, 16384, 1048576, true},        // A1:XFD1048576 with a column of values
		{1048576, 20, 1048576 * 20 / 4, false}, // a big, reasonably dense CSV
		{50001, 100001, 150002, true},          // one wide row plus many short ones
		{1 << 30, 1 << 30, 1 << 20, true},      // no overflow on absurd sizes
	}
	for _, tt := range tests {
		err := checkDensity("s", tt.rows, tt.cols, tt.stored)
		if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrTooSparse)) {
			t.Errorf("checkDensity(%d, %d, %d) = %v, want error %v", tt.rows, tt.cols, tt.stored, err, tt.wantErr)
		}
	}
}

func TestMaxCells(t *testing.T) {
	data := []byte("a,b,c\n1,2,3\n4,5,6\n")
	if _, err := Bytes("x.csv", data, Options{MaxCells: 9}); err != nil {
		t.Fatalf("3x3 within a 9-cell limit: %v", err)
	}
	if _, err := Bytes("x.csv", data, Options{MaxCells: 8}); !errors.Is(err, ErrTooManyCells) {
		t.Fatalf("3x3 over an 8-cell limit: %v", err)
	}
	f := excelize.NewFile()
	f.SetCellValue("Sheet1", "A1", "x")
	f.SetCellValue("Sheet1", "D4", "y")
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Bytes("x.xlsx", buf.Bytes(), Options{MaxCells: 15}); !errors.Is(err, ErrTooManyCells) {
		t.Fatalf("4x4 xlsx over a 15-cell limit: %v", err)
	}
}
