package read

import (
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"unicode/utf16"

	"github.com/FavioJasso/kempt/internal/grid"
)

// raws flattens a grid to its Raw values and checks the CSV kind rule:
// non-empty fields are String, empty fields are Empty.
func raws(t *testing.T, g *grid.Grid) [][]string {
	t.Helper()
	out := make([][]string, g.Rows)
	for r := range g.Rows {
		if len(g.Cells[r]) != g.Cols {
			t.Fatalf("row %d has %d cells, want %d", r, len(g.Cells[r]), g.Cols)
		}
		out[r] = make([]string, g.Cols)
		for c := range g.Cols {
			cell := g.Cells[r][c]
			if want := map[bool]grid.Kind{true: grid.Empty, false: grid.String}[cell.Raw == ""]; cell.Kind != want {
				t.Errorf("%s kind = %v, want %v", grid.CellRef(r, c), cell.Kind, want)
			}
			out[r][c] = cell.Raw
		}
	}
	return out
}

func utf16Bytes(s string, order binary.AppendByteOrder, bom []byte) []byte {
	out := append([]byte(nil), bom...)
	for _, u := range utf16.Encode([]rune(s)) {
		out = order.AppendUint16(out, u)
	}
	return out
}

func TestSniffDelimiter(t *testing.T) {
	tests := []struct {
		name string
		text string
		want rune
	}{
		{"comma", "a,b,c\n1,2,3\n4,5,6\n", ','},
		{"semicolon", "id;event;amount\n1;x;12.50\n2;y;7.25\n", ';'},
		{"tab", "a\tb\n1\t2\n", '\t'},
		{"pipe", "a|b|c\n1|2|3\n", '|'},
		{"semicolon with decimal commas", "id;price\n1;2,5\n2;3,75\n3;4\n", ';'},
		{"comma with semicolons in text", "name,tags\nA,x;y\nB,p;q;r\n", ','},
		{"quoted delimiters ignored", "\"a;b\",c\n\"d;e\",f\n", ','},
		{"single column", "name\nalice\nbob\n", ','},
		{"empty", "", ','},
		{"header only", "a;b;c", ';'},
		{"ragged comma wins on consistency", "a,b,c\n1,2,3\n4,5\n6,7,8\n", ','},
		{"tie goes to comma", "a,b;c\n1,2;3\n", ','},
		{"pipe beats inconsistent comma", "name|note\nx|hello, world\ny|plain\nz|a, b, c\n", '|'},
	}
	for _, tt := range tests {
		if got := sniffDelimiter(tt.text); got != tt.want {
			t.Errorf("%s: sniffDelimiter = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCSVDecoding(t *testing.T) {
	const text = "name,city\ncafé,Zürich\n"
	want := [][]string{{"name", "city"}, {"café", "Zürich"}}
	tests := []struct {
		name string
		data []byte
	}{
		{"utf-8", []byte(text)},
		{"utf-8 bom", append([]byte{0xEF, 0xBB, 0xBF}, text...)},
		{"utf-16le bom", utf16Bytes(text, binary.LittleEndian, []byte{0xFF, 0xFE})},
		{"utf-16be bom", utf16Bytes(text, binary.BigEndian, []byte{0xFE, 0xFF})},
		{"windows-1252", []byte("name,city\ncaf\xe9,Z\xfcrich\n")},
	}
	for _, tt := range tests {
		g, err := CSV("data.csv", tt.data)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := raws(t, g); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, want)
		}
	}
	g, err := CSV("x.csv", []byte("price\n\x80 5 \x93q\x94\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Cells[1][0].Raw; got != "€ 5 “q”" {
		t.Errorf("windows-1252 specials = %q", got)
	}
}

func TestCSVSepLine(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		data  string
		delim rune
		want  [][]string
	}{
		{"semicolon", "a.csv", "sep=;\na;b\n1;2\n", ';', [][]string{{"a", "b"}, {"1", "2"}}},
		{"pipe crlf", "a.csv", "sep=|\r\na|b,c\r\n1|2\r\n", '|', [][]string{{"a", "b,c"}, {"1", "2"}}},
		{"uppercase quoted", "a.csv", "\"SEP=,\"\na,b\n", ',', [][]string{{"a", "b"}}},
		{"tab", "a.csv", "sep=\t\na\tb\n", '\t', [][]string{{"a", "b"}}},
		{"overrides .tsv", "a.tsv", "sep=,\na,b\n", ',', [][]string{{"a", "b"}}},
		{"after bom", "a.csv", "\xEF\xBB\xBFsep=;\na;b\n", ';', [][]string{{"a", "b"}}},
		{"not a sep line", "a.csv", "sep=;;\na;b\n", ';', [][]string{{"sep=", ""}, {"a", "b"}}},
		{"sep header is data", "a.csv", "sep,x\n1,2\n", ',', [][]string{{"sep", "x"}, {"1", "2"}}},
	}
	for _, tt := range tests {
		g, err := CSV(tt.file, []byte(tt.data))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if g.Delimiter != tt.delim {
			t.Errorf("%s: delimiter = %q, want %q", tt.name, g.Delimiter, tt.delim)
		}
		if got := raws(t, g); fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCSVShape(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		data  string
		delim rune
		want  [][]string
	}{
		{"ragged rows padded", "a.csv", "a,b,c\n1\n2,3\n4,5,6,7\n", ',',
			[][]string{{"a", "b", "c", ""}, {"1", "", "", ""}, {"2", "3", "", ""}, {"4", "5", "6", "7"}}},
		{"quoted newline", "a.csv", "id,note\n1,\"line one\nline two\"\n2,x\n", ',',
			[][]string{{"id", "note"}, {"1", "line one\nline two"}, {"2", "x"}}},
		{"crlf", "a.csv", "a,b\r\n1,2\r\n", ',', [][]string{{"a", "b"}, {"1", "2"}}},
		{"no trailing newline", "a.csv", "a,b\n1,2", ',', [][]string{{"a", "b"}, {"1", "2"}}},
		{"classic mac line endings", "a.csv", "a,b\r1,2\r", ',', [][]string{{"a", "b"}, {"1", "2"}}},
		{"whitespace kept", "a.csv", "a, b \n  1,\t2\n", ',', [][]string{{"a", " b "}, {"  1", "\t2"}}},
		{"quotes removed, inner quotes unescaped", "a.csv", "\"a,b\",\"say \"\"hi\"\"\"\n", ',',
			[][]string{{"a,b", `say "hi"`}}},
		{"bare quotes tolerated", "a.csv", "size,name\n5\" pipe,x\n", ',', [][]string{{"size", "name"}, {"5\" pipe", "x"}}},
		{"blank lines kept as rows", "a.csv", "\n\ntitle\n\na,b\r\n\r\n1,2\n\n\n", ',',
			[][]string{{"", ""}, {"", ""}, {"title", ""}, {"", ""}, {"a", "b"}, {"", ""}, {"1", "2"}}},
		{"empty-field rows kept, trailing ones dropped", "a.csv", "a,b\n,\n1,2\n,\n,,\n", ',',
			[][]string{{"a", "b"}, {"", ""}, {"1", "2"}}},
		{"trailing empty columns dropped", "a.csv", "a,b,,\n1,2,,\n", ',', [][]string{{"a", "b"}, {"1", "2"}}},
		{"tsv extension forces tab", "a.tsv", "a,b\tc,d\n1,2\t3,4\n", '\t', [][]string{{"a,b", "c,d"}, {"1,2", "3,4"}}},
		{"tab extension forces tab", "a.TAB", "x\n1\n", '\t', [][]string{{"x"}, {"1"}}},
		{"empty file", "a.csv", "", ',', [][]string{}},
		{"only blank lines", "a.csv", "\n\n\r\n", ',', [][]string{}},
	}
	for _, tt := range tests {
		g, err := CSV(tt.file, []byte(tt.data))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if g.Delimiter != tt.delim || g.Source != "csv" {
			t.Errorf("%s: delimiter %q source %q", tt.name, g.Delimiter, g.Source)
		}
		if got := raws(t, g); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tt.want) {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestCSVSheetName(t *testing.T) {
	tests := []struct{ name, want string }{
		{"orders.csv", "orders"},
		{"/tmp/up/Q1 Sales.final.csv", "Q1 Sales.final"},
		{"noext", "noext"},
		{".csv", "sheet"},
		{"", "sheet"},
	}
	for _, tt := range tests {
		g, err := CSV(tt.name, []byte("a\n"))
		if err != nil {
			t.Fatal(err)
		}
		if g.Sheet != tt.want {
			t.Errorf("CSV(%q).Sheet = %q, want %q", tt.name, g.Sheet, tt.want)
		}
	}
}

func TestCSVMaxRows(t *testing.T) {
	data := []byte("h\n1\n2\n3\n\n\n")
	tests := []struct {
		max     int
		wantErr bool
	}{{0, false}, {4, false}, {10, false}, {3, true}, {1, true}}
	for _, tt := range tests {
		gs, err := Bytes("x.csv", data, Options{MaxRows: tt.max})
		if errors.Is(err, ErrTooManyRows) != tt.wantErr || (!tt.wantErr && (err != nil || gs[0].Rows != 4)) {
			t.Errorf("MaxRows %d: err = %v", tt.max, err)
		}
	}
}
