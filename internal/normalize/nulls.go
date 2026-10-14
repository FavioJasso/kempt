// Package normalize parses raw spreadsheet text into clean values: nulls,
// numbers, booleans, dates, column names, and header units.
package normalize

import (
	"strings"
	"unicode"
)

// NullKind classifies a raw value that stands for SQL NULL.
type NullKind int

const (
	NotNull        NullKind = iota // a real value
	NullEmpty                      // empty or whitespace only
	NullToken                      // placeholder such as "N/A" or "—"
	NullExcelError                 // Excel error value such as "#DIV/0!"
)

var nullKindNames = [...]string{"not_null", "empty", "null_token", "excel_error"}

func (k NullKind) String() string {
	if k >= 0 && int(k) < len(nullKindNames) {
		return nullKindNames[k]
	}
	return "null_kind(?)"
}

var nullTokens = map[string]bool{
	"-": true, "–": true, "—": true,
	"n/a": true, "na": true, "n.a.": true,
	"null": true, "none": true,
}

var excelErrors = map[string]bool{
	"#n/a": true, "#div/0!": true, "#value!": true, "#ref!": true,
	"#name?": true, "#num!": true, "#null!": true, "#spill!": true,
	"#calc!": true, "#getting_data": true,
}

// ClassifyNull reports whether s (trimmed, case-insensitive) is empty, a
// null placeholder token, or an Excel error value.
func ClassifyNull(s string) NullKind {
	t := strings.ToLower(trim(s))
	switch {
	case t == "":
		return NullEmpty
	case nullTokens[t]:
		return NullToken
	case excelErrors[t]:
		return NullExcelError
	}
	return NotNull
}

// trim removes surrounding Unicode whitespace (NBSP included), zero-width
// spaces, and byte-order marks.
func trim(s string) string {
	return strings.TrimFunc(s, isSpace)
}

func isSpace(r rune) bool {
	return unicode.IsSpace(r) || r == '\u200b' || r == '\ufeff'
}
