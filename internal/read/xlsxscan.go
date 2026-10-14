package read

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
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

// newDecoder reads an XML part in whatever encoding it declares (some
// writers use ISO-8859-1, windows-1252 or GB2312), as excelize does.
func newDecoder(f *excelize.File, data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = f.CharsetReader
	return d
}

// readRels reads a relationships part; a missing part has none.
func readRels(f *excelize.File, name string) ([]xmlRel, error) {
	data, ok := pkgPart(f, name)
	if !ok {
		return nil, nil
	}
	var rels struct {
		Rels []xmlRel `xml:"Relationship"`
	}
	if err := newDecoder(f, data).Decode(&rels); err != nil {
		return nil, fmt.Errorf("read: %s: %w", name, err)
	}
	return rels.Rels, nil
}

// resolvePart resolves a relationship target against the part that owns it.
// Some writers use Windows path separators or percent-encoding in targets.
func resolvePart(owner, target string) string {
	target = strings.ReplaceAll(target, `\`, "/")
	if u, err := url.PathUnescape(target); err == nil {
		target = u
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Clean(path.Join(path.Dir(owner), target)), "/")
}

// relType is the last segment of a relationship type, lowercased:
// ".../relationships/worksheet" → "worksheet".
func relType(t string) string {
	return strings.ToLower(t[strings.LastIndex(t, "/")+1:])
}

// isWorksheet reports whether a sheet's relationship type is a worksheet.
// Writers that leave the type out (or misspell its case) are read as
// worksheets; chart and dialog sheets are not.
func isWorksheet(kind string) bool {
	switch relType(kind) {
	case "chartsheet", "dialogsheet":
		return false
	}
	return true
}

// packageParts maps sheet names to their parts through the package and
// workbook relationships, as excelize does.
func packageParts(f *excelize.File) (pkgParts, error) {
	wb := "xl/workbook.xml"
	rels, err := readRels(f, "_rels/.rels")
	if err != nil {
		return pkgParts{}, err
	}
	for _, r := range rels {
		if relType(r.Type) == "officedocument" {
			wb = resolvePart("", r.Target)
		}
	}
	p := pkgParts{sheets: map[string]sheetPart{}, sst: path.Join(path.Dir(wb), "sharedStrings.xml")}
	byID := map[string]sheetPart{}
	if rels, err = readRels(f, path.Join(path.Dir(wb), "_rels", path.Base(wb)+".rels")); err != nil {
		return p, err
	}
	for _, r := range rels {
		target := resolvePart(wb, r.Target)
		byID[r.ID] = sheetPart{target, r.Type}
		if relType(r.Type) == "sharedstrings" {
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
	if err := newDecoder(f, data).Decode(&w); err != nil {
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
func readSST(d *xml.Decoder) ([]string, error) {
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
// lies below x.maxRows, and with ErrTooManyCells as soon as the values seen
// span more than x.maxCells, so a small file of many cells cannot fill
// memory before the limits are checked.
func (x *xlsxReader) scan(sheet string, data []byte) (scanData, error) {
	var sd scanData
	d := newDecoder(x.f, data)
	var (
		stored int64            // cells kept so far
		lastR  int              // last row (1-based) holding a value
		lastC  = -1             // last column holding a value
		resort = map[int]bool{} // rows that need sorting: a row number repeated
		// shared formula children seen before their master
		pending []pendingFormula
	)
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
		for _, v := range cur {
			if strings.TrimSpace(v.v) != "" {
				lastR, lastC = max(lastR, row), max(lastC, v.c)
			}
		}
		stored += int64(len(cur))
		if x.maxCells > 0 && stored > x.maxCells {
			return fmt.Errorf("%w: sheet %q stores more than %d cells", ErrTooManyCells, sheet, x.maxCells)
		}
		if err := checkCells(sheet, lastR, lastC+1, x.maxCells); err != nil {
			return err
		}
		if !sort.SliceIsSorted(cur, func(i, j int) bool { return cur[i].c < cur[j].c }) {
			sort.SliceStable(cur, func(i, j int) bool { return cur[i].c < cur[j].c })
		}
		for len(sd.rows) < row {
			sd.rows = append(sd.rows, rawRow{})
		}
		r := &sd.rows[row-1]
		if len(r.vals) > 0 {
			resort[row-1] = true // a repeated row number: sorted once at the end
		}
		r.vals = append(r.vals, cur...)
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
				next := row + 1 // a missing or unreadable number means the next row
				if n, err := strconv.Atoi(strings.TrimSpace(attr(t, "r"))); err == nil && n >= 1 {
					next = n
				}
				if next > excelRows {
					return sd, fmt.Errorf("read: sheet %q: row %d is beyond Excel's last row", sheet, next)
				}
				row, col = next, -1
			case "c":
				if row == 0 {
					row = 1
				}
				inCell, hasIS, hasFormula, fShared, fSI = true, false, false, false, ""
				cell = rawVal{c: col + 1, t: attr(t, "t")}
				if ref := strings.ReplaceAll(attr(t, "r"), "$", ""); ref != "" {
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
							// A broken file: the text is lost, so the cell reads as
							// an error value (NULL, logged) rather than failing
							// the whole sheet or passing as empty.
							cell.t, v = "e", "#REF!"
						} else {
							v = x.sst[i]
						}
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
						} else { // the master comes later in the file
							pending = append(pending, pendingFormula{row - 1, cell.c, fSI})
							f = pendingMark
						}
					}
					cell.formula = strings.TrimPrefix(f, "=")
				}
				if cell.v != "" || cell.formula != "" {
					cur = append(cur, cell)
				}
			case "sheetData":
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
	for r := range resort {
		vals := sd.rows[r].vals
		sort.SliceStable(vals, func(i, j int) bool { return vals[i].c < vals[j].c })
	}
	for _, p := range pending {
		f := ""
		if m, ok := shared[p.si]; ok {
			f = strings.TrimPrefix(shiftFormula(m.text, p.r-m.r, p.c-m.c), "=")
		}
		for i := range sd.rows[p.r].vals {
			if v := &sd.rows[p.r].vals[i]; v.c == p.c && v.formula == pendingMark {
				v.formula = f
			}
		}
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

// pendingFormula is a shared formula child whose master comes later.
type pendingFormula struct {
	r, c int
	si   string
}

// pendingMark stands in for a shared formula child's text until its master
// is read. No formula Excel writes starts with a NUL.
const pendingMark = "\x00pending"

// sharedFormula is the master cell of a shared formula.
type sharedFormula struct {
	text string
	r, c int
}

var (
	refRe      = regexp.MustCompile(`^(\$?)([A-Z]{1,3})(\$?)([0-9]{1,7})`)
	colRangeRe = regexp.MustCompile(`^(\$?)([A-Z]{1,3}):(\$?)([A-Z]{1,3})`)
	rowRangeRe = regexp.MustCompile(`^(\$?)([0-9]{1,7}):(\$?)([0-9]{1,7})`)
)

// shiftFormula moves the relative references in a shared formula's text by
// (dr, dc), as Excel does when it fills the formula from its master cell:
// cells (B2), whole columns (B:C) and whole rows (2:3), unless anchored
// with $. Text in double quotes, quoted sheet names and structured
// references (Table1[[#This Row],[Q1]]) is left alone; a reference shifted
// off the sheet becomes #REF!.
func shiftFormula(f string, dr, dc int) string {
	if dr == 0 && dc == 0 {
		return f
	}
	var b strings.Builder
	inStr, inSheet, brackets := false, false, 0
	ends := func(i, n int) bool { // the token f[i:i+n] is not part of a longer name or a call
		j := i + n
		return j >= len(f) || !(identChar(f[j]) || f[j] == '(')
	}
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
		case ch == '[':
			brackets++
		case ch == ']':
			brackets = max(0, brackets-1)
		case brackets > 0:
		case i > 0 && identChar(f[i-1]):
		case ch == '$' || ch >= 'A' && ch <= 'Z':
			if m := colRangeRe.FindStringSubmatch(f[i:]); m != nil && ends(i, len(m[0])) {
				b.WriteString(shiftCol(m[1], m[2], dc) + ":" + shiftCol(m[3], m[4], dc))
				i += len(m[0]) - 1
				continue
			}
			if m := refRe.FindStringSubmatch(f[i:]); m != nil && ends(i, len(m[0])) {
				col, row := shiftCol(m[1], m[2], dc), shiftRow(m[3], m[4], dr)
				if col == "#REF!" || row == "#REF!" {
					b.WriteString("#REF!")
				} else {
					b.WriteString(col + row)
				}
				i += len(m[0]) - 1
				continue
			}
		case ch >= '0' && ch <= '9':
			if m := rowRangeRe.FindStringSubmatch(f[i:]); m != nil && ends(i, len(m[0])) {
				b.WriteString(shiftRow(m[1], m[2], dr) + ":" + shiftRow(m[3], m[4], dr))
				i += len(m[0]) - 1
				continue
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// shiftCol shifts a column name unless it is anchored ("$").
func shiftCol(anchor, name string, dc int) string {
	if anchor != "" {
		return anchor + name
	}
	col := 0
	for _, l := range name {
		col = col*26 + int(l-'A'+1)
	}
	if col += dc; col < 1 || col > excelCols {
		return "#REF!"
	}
	return grid.ColName(col - 1)
}

// shiftRow shifts a row number unless it is anchored ("$").
func shiftRow(anchor, num string, dr int) string {
	if anchor != "" {
		return anchor + num
	}
	row, _ := strconv.Atoi(num)
	if row += dr; row < 1 || row > excelRows {
		return "#REF!"
	}
	return strconv.Itoa(row)
}

func identChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.'
}
