package read

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/FavioJasso/kempt/internal/grid"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// delimiters are the sniffed candidates in tie-break order.
var delimiters = []rune{',', ';', '\t', '|'}

// sniffRecords is how many records each delimiter candidate is tried on.
const sniffRecords = 50

// CSV reads delimited text into a single grid named after the file stem.
// It decodes UTF-8 (BOM optional), UTF-16 with a BOM, or Windows-1252; honors
// an Excel "sep=X" first line; uses tabs for .tsv/.tab files and otherwise
// sniffs the delimiter. Every non-empty field becomes a String cell holding
// the field exactly as read; blank lines are kept as empty rows.
func CSV(name string, data []byte) (*grid.Grid, error) {
	return readCSV(name, data, 0)
}

func readCSV(name string, data []byte, maxRows int) (*grid.Grid, error) {
	text := decodeText(data)
	if !strings.Contains(text, "\n") && strings.Contains(text, "\r") {
		text = strings.ReplaceAll(text, "\r", "\n") // classic Mac line endings
	}
	delim, rest, ok := sepLine(text)
	if ok {
		text = rest
	} else {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".tsv", ".tab":
			delim = '\t'
		default:
			delim = sniffDelimiter(text)
		}
	}

	stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if stem == "" || stem == "." || stem == string(filepath.Separator) {
		stem = "sheet"
	}
	recs, err := parseRecords(text, delim, maxRows)
	if err != nil {
		if errors.Is(err, ErrTooManyRows) {
			return nil, tooManyRows(stem, maxRows)
		}
		return nil, fmt.Errorf("read: %s: %w", filepath.Base(name), err)
	}

	rows, cols := 0, 0
	for r, rec := range recs {
		for c, f := range rec {
			if strings.TrimSpace(f) != "" {
				rows, cols = r+1, max(cols, c+1)
			}
		}
	}
	g := grid.New(stem, rows, cols)
	g.Source, g.Delimiter = "csv", delim
	for r := 0; r < rows; r++ {
		for c, f := range recs[r] {
			if c < cols && f != "" {
				g.Cells[r][c] = grid.Cell{Kind: grid.String, Raw: f}
			}
		}
	}
	return g, nil
}

// decodeText returns data as UTF-8 without a byte-order mark.
func decodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, bomUTF16LE):
		return decodeUTF16(data[2:], binary.LittleEndian)
	case bytes.HasPrefix(data, bomUTF16BE):
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	data = bytes.TrimPrefix(data, bomUTF8)
	if utf8.Valid(data) {
		return string(data)
	}
	if out, err := charmap.Windows1252.NewDecoder().Bytes(data); err == nil {
		return string(out)
	}
	return strings.ToValidUTF8(string(data), "�")
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// sepLine recognizes Excel's optional `sep=X` first line and returns the
// delimiter and the text after that line.
func sepLine(text string) (delim rune, rest string, ok bool) {
	line, rest, _ := strings.Cut(text, "\n")
	line = strings.TrimSuffix(line, "\r")
	if len(line) >= 2 && line[0] == '"' && line[len(line)-1] == '"' {
		line = line[1 : len(line)-1]
	}
	if len(line) < 5 || !strings.EqualFold(line[:4], "sep=") {
		return 0, text, false
	}
	d, size := utf8.DecodeRuneInString(line[4:])
	if 4+size != len(line) || !validDelim(d) {
		return 0, text, false
	}
	return d, rest, true
}

func validDelim(r rune) bool {
	return r != 0 && r != '"' && r != '\r' && r != '\n' && r != utf8.RuneError && utf8.ValidRune(r)
}

// sniffDelimiter picks the candidate whose modal field count is greater than
// one and whose sampled records most consistently have that count. Ties go to
// the candidate order; single-column text gets a comma.
func sniffDelimiter(text string) rune {
	best, bestCons := ',', 0.0
	for _, d := range delimiters {
		r := newCSVReader(text, d)
		freq := map[int]int{}
		n := 0
		for n < sniffRecords {
			rec, err := r.Read()
			if err != nil {
				break
			}
			freq[len(rec)]++
			n++
		}
		mode, modeN := 0, 0
		for width, k := range freq {
			if k > modeN || k == modeN && width > mode {
				mode, modeN = width, k
			}
		}
		if mode <= 1 {
			continue
		}
		cons := float64(modeN) / float64(n)
		if cons > bestCons {
			best, bestCons = d, cons
		}
	}
	return best
}

func newCSVReader(text string, delim rune) *csv.Reader {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	return r
}

// parseRecords reads every record. encoding/csv skips blank lines; they are
// restored as nil records so row numbers match the file (trailing blank lines
// are dropped).
func parseRecords(text string, delim rune, maxRows int) ([][]string, error) {
	r := newCSVReader(text, delim)
	var recs [][]string
	var off int64
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return recs, nil
		}
		if err != nil {
			return nil, err
		}
		for range blankLines(text[off:]) {
			recs = append(recs, nil)
		}
		recs = append(recs, rec)
		off = r.InputOffset()
		if maxRows > 0 && len(recs) > maxRows && hasValue(rec) {
			return nil, ErrTooManyRows
		}
	}
}

// blankLines counts the empty lines ("\n" or "\r\n") at the start of s.
func blankLines(s string) int {
	n := 0
	for {
		switch {
		case strings.HasPrefix(s, "\n"):
			s = s[1:]
		case strings.HasPrefix(s, "\r\n"):
			s = s[2:]
		default:
			return n
		}
		n++
	}
}
