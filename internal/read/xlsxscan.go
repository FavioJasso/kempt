package read

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/FavioJasso/kempt/internal/grid"
)

// Worksheets are read in one streaming pass over their XML. Asking excelize
// for a cell's type, style, formula or the sheet's merges makes it unmarshal
// the whole worksheet into structs, which costs about 1 KB per cell; the
// stream keeps only the values.

// pkgParts locates the parts of an opened workbook package.
type pkgParts struct {
	sheets map[string]sheetPart // by sheet name
	sst    string               // shared strings part
}

type sheetPart struct {
	path string
	kind string // relationship type: .../worksheet, .../chartsheet, ...
}

type xmlRel struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
}

// pkgPart returns a part's bytes. Part names are case-insensitive.
func pkgPart(f *excelize.File, name string) ([]byte, bool) {
	if v, ok := f.Pkg.Load(name); ok {
		b, ok := v.([]byte)
		return b, ok
	}
	var out []byte
	found := false
	f.Pkg.Range(func(k, v any) bool {
		if s, ok := k.(string); ok && strings.EqualFold(s, name) {
			out, found = v.([]byte)
			return false
		}
		return true
	})
	return out, found
}

func readRels(f *excelize.File, name string) []xmlRel {
	data, ok := pkgPart(f, name)
	if !ok {
		return nil
	}
	var rels struct {
		Rels []xmlRel `xml:"Relationship"`
	}
	if xml.Unmarshal(data, &rels) != nil {
		return nil
	}
	return rels.Rels
}

// resolvePart resolves a relationship target against the part that owns it.
func resolvePart(owner, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Clean(path.Join(path.Dir(owner), target)), "/")
}

// packageParts maps sheet names to their parts through the package and
// workbook relationships, as excelize does.
func packageParts(f *excelize.File) (pkgParts, error) {
	wb := "xl/workbook.xml"
	for _, r := range readRels(f, "_rels/.rels") {
		if strings.HasSuffix(r.Type, "/officeDocument") {
			wb = resolvePart("", r.Target)
		}
	}
	p := pkgParts{sheets: map[string]sheetPart{}, sst: path.Join(path.Dir(wb), "sharedStrings.xml")}
	byID := map[string]sheetPart{}
	for _, r := range readRels(f, path.Join(path.Dir(wb), "_rels", path.Base(wb)+".rels")) {
		target := resolvePart(wb, r.Target)
		byID[r.ID] = sheetPart{target, r.Type}
		if strings.HasSuffix(r.Type, "/sharedStrings") {
			p.sst = target
		}
	}
	data, ok := pkgPart(f, wb)
	if !ok {
		return p, fmt.Errorf("read: workbook part %s is missing", wb)
	}
	var w struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"` // r:id
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(data, &w); err != nil {
		return p, fmt.Errorf("read: workbook: %w", err)
	}
	for _, s := range w.Sheets {
		if sp, ok := byID[s.ID]; ok {
			p.sheets[s.Name] = sp
		}
	}
	return p, nil
}

// readSST reads the shared string table: each item is its text plus the text
// of its rich-text runs, without phonetic (rPh) runs, with _xHHHH_ escapes
// decoded.
func readSST(data []byte) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var b strings.Builder
	inSI, inT, rph := false, false, 0
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read: shared strings: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				b.Reset()
			case "rPh":
				rph++
			case "t":
				inT = inSI && rph == 0
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, decodeEscapes(b.String()))
				inSI = false
			case "rPh":
				rph--
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				b.Write(t)
			}
		}
	}
}

var escapeRe = regexp.MustCompile(`_x[0-9A-Fa-f]{4}_`)

// decodeEscapes decodes the _xHHHH_ escapes OOXML uses for characters XML
// cannot hold ("_x000D_" is a carriage return, "_x005F_" a literal "_").
func decodeEscapes(s string) string {
	if !strings.Contains(s, "_x") {
		return s
	}
	return escapeRe.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.ParseUint(m[2:6], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
}

// rawRow is one row of stored cells: the cells holding a value or a
// formula, in column order. Rows are kept sparse so a stray value in column
// XFD costs one entry, not 16,384.
type rawRow struct {
	vals []rawVal
}

// rawVal is one stored cell.
type rawVal struct {
	c       int
	v       string // the raw value: shared and inline strings resolved
	t       string // the cell's type attribute (s, str, inlineStr, b, e, d, n or "")
	style   int    // the cell's style index
	formula string // the formula without "=", or "" (a shared formula's master text)
}

// scanData is a worksheet as read from its XML.
type scanData struct {
	rows   []rawRow
	merges []grid.Range // sorted by position
}

// Excel's grid limits.
const (
	excelRows = 1 << 20
	excelCols = 1 << 14
)

// scan streams one worksheet part. It stops with ErrTooManyRows once a value
// lies below x.maxRows.
func (x *xlsxReader) scan(sheet string, data []byte) (scanData, error) {
	var sd scanData
	d := xml.NewDecoder(bytes.NewReader(data))
	var (
		row, col   = 0, -1 // current row (1-based) and last column (0-based)
		cur        []rawVal
		inCell     bool
		cell       rawVal
		elem       string // the child of <c> being read: v, f or is
		text       strings.Builder
		isText     strings.Builder
		hasIS      bool
		inT        bool // inside a <t> of an inline string
		rph        int
		shared     = map[string]sharedFormula{} // by shared formula index
		fShared    bool
		fSI        string
		fText      strings.Builder
		hasFormula bool
	)
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		if x.maxRows > 0 && row > x.maxRows {
			for _, v := range cur {
				if strings.TrimSpace(v.v) != "" {
					return tooManyRows(sheet, x.maxRows)
				}
			}
		}
		sort.SliceStable(cur, func(i, j int) bool { return cur[i].c < cur[j].c })
		for len(sd.rows) < row {
			sd.rows = append(sd.rows, rawRow{})
		}
		r := &sd.rows[row-1]
		r.vals = append(r.vals, cur...)
		if len(r.vals) > len(cur) { // a repeated row number: keep column order
			sort.SliceStable(r.vals, func(i, j int) bool { return r.vals[i].c < r.vals[j].c })
		}
		cur = nil
		return nil
	}
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sd, fmt.Errorf("read: sheet %q: %w", sheet, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				if err := flush(); err != nil {
					return sd, err
				}
				next := row + 1
				if v := attr(t, "r"); v != "" {
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 || n > excelRows {
						return sd, fmt.Errorf("read: sheet %q: invalid row number %q", sheet, v)
					}
					next = n
				}
				row, col = next, -1
			case "c":
				if row == 0 {
					row = 1
				}
				inCell, hasIS, hasFormula, fShared, fSI = true, false, false, false, ""
				cell = rawVal{c: col + 1, t: attr(t, "t")}
				if ref := attr(t, "r"); ref != "" {
					_, c, err := grid.ParseRef(ref)
					if err != nil {
						return sd, fmt.Errorf("read: sheet %q: invalid cell reference %q", sheet, ref)
					}
					cell.c = c
				}
				if cell.c >= excelCols {
					return sd, fmt.Errorf("read: sheet %q: column %d is beyond Excel's last column", sheet, cell.c+1)
				}
				if s := attr(t, "s"); s != "" {
					cell.style, _ = strconv.Atoi(s)
				}
				col = cell.c
				text.Reset()
				isText.Reset()
			case "v":
				if inCell {
					elem = "v"
					text.Reset()
				}
			case "f":
				if inCell {
					elem, hasFormula = "f", true
					fShared, fSI = attr(t, "t") == "shared", attr(t, "si")
					fText.Reset()
				}
			case "is":
				if inCell {
					elem, hasIS = "is", true
				}
			case "rPh":
				rph++
			case "t":
				inT = inCell && elem == "is" && rph == 0
			case "mergeCell":
				if rg, err := grid.ParseRange(attr(t, "ref")); err == nil {
					sd.merges = append(sd.merges, rg)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "f", "is":
				if t.Name.Local == elem {
					elem = ""
				}
			case "rPh":
				rph--
			case "t":
				inT = false
			case "c":
				if !inCell {
					break
				}
				inCell = false
				v := text.String()
				switch cell.t {
				case "s":
					if strings.TrimSpace(v) != "" {
						i, err := strconv.Atoi(strings.TrimSpace(v))
						if err != nil || i < 0 || i >= len(x.sst) {
							return sd, fmt.Errorf("read: sheet %q: cell %s: invalid shared string index %q", sheet, grid.CellRef(row-1, cell.c), v)
						}
						v = x.sst[i]
					}
				case "inlineStr":
					if hasIS {
						v = decodeEscapes(isText.String())
					}
				}
				cell.v = v
				if hasFormula {
					f := fText.String()
					switch {
					case f != "" && fShared && fSI != "":
						shared[fSI] = sharedFormula{f, row - 1, cell.c}
					case f == "" && fShared:
						if m, ok := shared[fSI]; ok {
							f = shiftFormula(m.text, row-1-m.r, cell.c-m.c)
						}
					}
					cell.formula = strings.TrimPrefix(f, "=")
				}
				if cell.v != "" || cell.formula != "" {
					cur = append(cur, cell)
				}
			case "scanData":
				if err := flush(); err != nil {
					return sd, err
				}
			}
		case xml.CharData:
			switch {
			case !inCell:
			case elem == "v":
				text.Write(t)
			case elem == "f":
				fText.Write(t)
			case inT:
				isText.Write(t)
			}
		}
	}
	if err := flush(); err != nil {
		return sd, err
	}
	sort.Slice(sd.merges, func(i, j int) bool {
		a, b := sd.merges[i], sd.merges[j]
		if a.R1 != b.R1 {
			return a.R1 < b.R1
		}
		if a.C1 != b.C1 {
			return a.C1 < b.C1
		}
		if a.R2 != b.R2 {
			return a.R2 < b.R2
		}
		return a.C2 < b.C2
	})
	return sd, nil
}

// attr returns the value of the attribute with the given local name.
func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// sharedFormula is the master cell of a shared formula.
type sharedFormula struct {
	text string
	r, c int
}

var refRe = regexp.MustCompile(`^(\$?)([A-Z]{1,3})(\$?)([0-9]{1,7})`)

// shiftFormula moves the relative A1 references in a shared formula's text
// by (dr, dc), as Excel does when it fills the formula from its master cell.
// Text in double quotes and quoted sheet names is left alone; a reference
// shifted off the sheet becomes #REF!.
func shiftFormula(f string, dr, dc int) string {
	if dr == 0 && dc == 0 {
		return f
	}
	var b strings.Builder
	inStr, inSheet := false, false
	for i := 0; i < len(f); i++ {
		ch := f[i]
		switch {
		case inStr:
			inStr = ch != '"'
		case inSheet:
			inSheet = ch != '\''
		case ch == '"':
			inStr = true
		case ch == '\'':
			inSheet = true
		case (ch == '$' || ch >= 'A' && ch <= 'Z') && (i == 0 || !identChar(f[i-1])):
			m := refRe.FindStringSubmatch(f[i:])
			if m == nil || (i+len(m[0]) < len(f) && (identChar(f[i+len(m[0])]) || f[i+len(m[0])] == '(')) {
				break
			}
			col := 0
			for _, l := range m[2] {
				col = col*26 + int(l-'A'+1)
			}
			row, _ := strconv.Atoi(m[4])
			if m[1] == "" {
				col += dc
			}
			if m[3] == "" {
				row += dr
			}
			if col < 1 || col > excelCols || row < 1 || row > excelRows {
				b.WriteString("#REF!")
			} else {
				b.WriteString(m[1] + grid.ColName(col-1) + m[3] + strconv.Itoa(row))
			}
			i += len(m[0]) - 1
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func identChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.'
}
