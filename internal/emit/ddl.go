// Package emit renders plans as SQL (CREATE TABLE and INSERT statements) and
// loads them into Postgres, MySQL, or SQLite.
package emit

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// MySQL limits that shape the DDL.
const (
	mysqlRowLimit      = 65535 // bytes per row, VARCHAR widths and fixed-width columns included
	mysqlColumnComment = 1024  // max characters in a column COMMENT
	mysqlTableComment  = 2048  // max characters in a table COMMENT
	innodbRecordLimit  = 8126  // worst-case in-page record bytes CREATE TABLE allows under innodb_strict_mode
)

// sqliteExactDigits is how many significant digits a decimal may have and
// still survive SQLite's conversion to REAL in a NUMERIC column.
const sqliteExactDigits = 15

// DDLOptions control CREATE TABLE rendering.
type DDLOptions struct {
	Schema      string // Postgres schema, MySQL database, or SQLite attached database; "" = unqualified
	IfNotExists bool
	NoComments  bool // leave out table and column comments (the original headers)
}

// DDL renders the CREATE TABLE statement for p in dialect, plus column and
// table comments where the dialect supports them. The result ends with a
// newline. Unknown dialects render as Postgres.
func DDL(p *plan.Plan, dialect string, opts DDLOptions) string {
	d := ddlDialect(dialect)
	table := QualifiedTable(d, opts.Schema, p.Table)
	types := columnTypes(p, d)
	comment := tableComment(p)
	cols := p.Columns
	if opts.NoComments {
		comment = ""
		cols = make([]plan.Column, len(p.Columns))
		copy(cols, p.Columns)
		for i := range cols {
			cols[i].Comment = ""
		}
	}

	var b strings.Builder
	switch d {
	case infer.MySQL:
		if moved := fitMySQLRow(types); len(moved) > 0 {
			fmt.Fprintf(&b, "-- kempt: %s changed from VARCHAR to TEXT to stay under the MySQL row size limit.\n",
				columnNames(p, moved))
		}
		if innodbRecordSize(types) >= innodbRecordLimit {
			fmt.Fprintf(&b, "-- kempt: %d columns may exceed InnoDB's %d-byte record limit; run SET SESSION innodb_strict_mode = OFF first (kempt load does).\n",
				len(types), innodbRecordLimit)
		}
	case infer.SQLite:
		if moved := fitSQLiteDecimals(p, types); len(moved) > 0 {
			fmt.Fprintf(&b, "-- kempt: %s stored as TEXT: SQLite NUMERIC keeps only %d significant digits.\n",
				columnNames(p, moved), sqliteExactDigits)
		}
	}
	b.WriteString("CREATE TABLE ")
	if opts.IfNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(table)
	b.WriteString(" (")
	if d == infer.SQLite && comment != "" {
		b.WriteString(" -- " + oneLine(comment))
	}
	b.WriteString("\n")
	for i, c := range cols {
		b.WriteString("  " + QuoteIdent(d, c.Name) + " " + types[i])
		if d == infer.MySQL && c.Comment != "" {
			b.WriteString(" COMMENT " + Literal(d, truncateRunes(mysqlComment(c.Comment), mysqlColumnComment)))
		}
		if i < len(cols)-1 {
			b.WriteString(",")
		}
		if d == infer.SQLite && c.Comment != "" {
			b.WriteString(" -- " + oneLine(c.Comment))
		}
		b.WriteString("\n")
	}
	b.WriteString(")")
	if d == infer.MySQL {
		b.WriteString(" ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
		if comment != "" {
			b.WriteString(" COMMENT=" + Literal(d, truncateRunes(mysqlComment(comment), mysqlTableComment)))
		}
	}
	b.WriteString(";\n")
	if d == infer.Postgres {
		for _, c := range cols {
			if c.Comment != "" {
				fmt.Fprintf(&b, "COMMENT ON COLUMN %s.%s IS %s;\n", table, QuoteIdent(d, c.Name), Literal(d, c.Comment))
			}
		}
		if comment != "" {
			fmt.Fprintf(&b, "COMMENT ON TABLE %s IS %s;\n", table, Literal(d, comment))
		}
	}
	return b.String()
}

// QuoteIdent quotes an identifier for dialect, doubling embedded quote
// characters: "x" for Postgres and SQLite, `x` for MySQL.
func QuoteIdent(dialect, name string) string {
	if ddlDialect(dialect) == infer.MySQL {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QualifiedTable renders schema.table with both parts quoted, or just the
// quoted table when schema is empty.
func QualifiedTable(dialect, schema, table string) string {
	if schema == "" {
		return QuoteIdent(dialect, table)
	}
	return QuoteIdent(dialect, schema) + "." + QuoteIdent(dialect, table)
}

// Literal renders a clean value as a SQL literal: NULL, TRUE/FALSE (1/0 in
// SQLite), integers, decimals, quoted ISO dates and timestamps, and quoted
// text. MySQL literals also escape backslashes. Values of other types are
// quoted as text.
func Literal(dialect string, v any) string {
	d := ddlDialect(dialect)
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		switch {
		case d == infer.SQLite && x:
			return "1"
		case d == infer.SQLite:
			return "0"
		case x:
			return "TRUE"
		default:
			return "FALSE"
		}
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case normalize.Decimal:
		// SQLite reads a bare literal as REAL, losing digits even in a TEXT
		// column; quoted text gets the column's affinity, like a bound value.
		if validDecimal(string(x)) && d != infer.SQLite {
			return string(x)
		}
		return quoteString(d, string(x))
	case normalize.Date:
		return quoteString(d, x.String())
	case normalize.Timestamp:
		return quoteString(d, x.String())
	case string:
		return quoteString(d, x)
	default:
		return quoteString(d, fmt.Sprint(x))
	}
}

// ddlDialect normalizes a dialect name; unknown names render as Postgres.
func ddlDialect(dialect string) string {
	if d, ok := dialectOf(dialect); ok {
		return d
	}
	return infer.Postgres
}

// dialectOf normalizes a dialect name and reports whether it is supported.
func dialectOf(dialect string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case infer.Postgres, "postgresql", "pg":
		return infer.Postgres, true
	case infer.MySQL:
		return infer.MySQL, true
	case infer.SQLite, "sqlite3":
		return infer.SQLite, true
	}
	return "", false
}

// columnTypes returns the rendered SQL type of every column of p.
func columnTypes(p *plan.Plan, d string) []string {
	types := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		types[i] = c.SQLType[d]
		if types[i] == "" {
			types[i] = c.Type.SQL(d)
		}
	}
	return types
}

// fitMySQLRow switches the widest VARCHAR columns to TEXT (leftmost first on
// ties) until the whole row (VARCHARs at 4 bytes per character, fixed-width
// columns, TEXT pointers, and the null bitmap) fits MySQL's 65,535-byte row
// limit. It returns the indexes it changed, in the order changed.
func fitMySQLRow(types []string) []int {
	budget := mysqlRowLimit - (len(types)+7)/8 - 64 // null bitmap and a margin
	total := 0
	for _, t := range types {
		total += mysqlRowBytes(t)
	}
	var moved []int
	for total > budget {
		w := -1
		for i, t := range types {
			if n := varcharLen(t); n > 0 && (w < 0 || n > varcharLen(types[w])) {
				w = i
			}
		}
		if w < 0 {
			break
		}
		total -= mysqlRowBytes(types[w]) - mysqlRowBytes("TEXT")
		types[w] = "TEXT"
		moved = append(moved, w)
	}
	return moved
}

// mysqlRowBytes is how much of MySQL's 65,535-byte row limit a column uses:
// utf8mb4 VARCHARs count their full width, TEXT columns only their pointer.
func mysqlRowBytes(t string) int {
	if n := varcharLen(t); n > 0 {
		if n*4 > 255 {
			return n*4 + 2
		}
		return n*4 + 1
	}
	if u := strings.ToUpper(t); strings.HasSuffix(u, "TEXT") || strings.HasSuffix(u, "BLOB") {
		return 12
	}
	return innodbFieldSize(t)
}

// innodbRecordSize estimates the worst-case in-page record size InnoDB
// checks at CREATE TABLE for columns of the given MySQL types (ROW_FORMAT
// DYNAMIC, no primary key): fixed-size columns count their width and
// variable-length ones at most 40 bytes plus a length byte, on top of the
// record header, the null flags, and the hidden row id, transaction id, and
// roll pointer.
func innodbRecordSize(types []string) int {
	size := 5 + (len(types)+7)/8 + 6 + 6 + 7
	for _, t := range types {
		size += innodbFieldSize(t)
	}
	return size
}

func innodbFieldSize(t string) int {
	t = strings.ToUpper(strings.TrimSpace(t))
	switch {
	case t == "BOOLEAN", t == "BOOL", t == "TINYINT":
		return 1
	case t == "INT", t == "INTEGER":
		return 4
	case t == "BIGINT":
		return 8
	case t == "DATE":
		return 3
	case strings.HasPrefix(t, "DATETIME"), strings.HasPrefix(t, "TIMESTAMP"):
		return 8
	case strings.HasPrefix(t, "DECIMAL("):
		var p, s int
		if _, err := fmt.Sscanf(t, "DECIMAL(%d,%d)", &p, &s); err == nil && p >= s && s >= 0 {
			return decimalBytes(p-s) + decimalBytes(s)
		}
		return 30
	case strings.HasPrefix(t, "VARCHAR("):
		return min(4*varcharLen(t), 40) + 1
	}
	return 41 // TEXT and other variable-length types
}

// decimalBytes is the storage MySQL uses for n decimal digits: 4 bytes per
// 9 digits plus 0-4 bytes for the rest.
func decimalBytes(n int) int {
	return n/9*4 + [9]int{0, 1, 1, 2, 2, 3, 3, 4, 4}[n%9]
}

// fitSQLiteDecimals switches columns whose SQLite type would convert text to
// numbers (any affinity but TEXT or BLOB) to TEXT when a decimal value in
// them has more digits than a REAL keeps. It returns the indexes changed.
func fitSQLiteDecimals(p *plan.Plan, types []string) []int {
	var moved []int
	for j := range types {
		if j < len(p.Columns) && p.Columns[j].Type.Base == infer.Decimal && strings.EqualFold(types[j], "TEXT") {
			moved = append(moved, j) // already TEXT because the precision is too high
			continue
		}
		if !sqliteConvertsNumbers(types[j]) {
			continue
		}
		for _, row := range p.Rows {
			if v, ok := cell(row, j).(normalize.Decimal); ok && !sqliteExact(string(v)) {
				types[j] = "TEXT"
				moved = append(moved, j)
				break
			}
		}
	}
	return moved
}

// sqliteConvertsNumbers reports whether a column declared as t has INTEGER,
// REAL, or NUMERIC affinity, which store numeric-looking text as numbers.
func sqliteConvertsNumbers(t string) bool {
	t = strings.ToUpper(t)
	switch {
	case strings.Contains(t, "INT"):
		return true
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"),
		strings.Contains(t, "BLOB"), strings.TrimSpace(t) == "":
		return false
	}
	return true
}

// sqliteExact reports whether SQLite stores the decimal d without losing
// digits: an integer that fits int64, or at most sqliteExactDigits
// significant digits.
func sqliteExact(d string) bool {
	if !validDecimal(d) {
		return false
	}
	intPart, frac, _ := strings.Cut(d, ".")
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		if _, err := strconv.ParseInt(intPart, 10, 64); err == nil {
			return true
		}
	}
	intPart = strings.TrimPrefix(intPart, "-")
	digits := strings.TrimLeft(intPart+frac, "0")
	if frac == "" {
		digits = strings.TrimRight(digits, "0")
	}
	return len(digits) <= sqliteExactDigits
}

// columnNames lists the names of p's columns at idx for a SQL comment.
func columnNames(p *plan.Plan, idx []int) string {
	names := make([]string, len(idx))
	for i, j := range idx {
		names[i] = oneLine(p.Columns[j].Name)
	}
	return strings.Join(names, ", ")
}

// varcharLen returns n for a "VARCHAR(n)" type and 0 otherwise.
func varcharLen(t string) int {
	t = strings.ToUpper(strings.TrimSpace(t))
	if !strings.HasPrefix(t, "VARCHAR(") || !strings.HasSuffix(t, ")") {
		return 0
	}
	n, err := strconv.Atoi(t[len("VARCHAR(") : len(t)-1])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// tableComment describes where the table came from.
func tableComment(p *plan.Plan) string {
	return strings.TrimSpace("Loaded by kempt from " + strings.TrimSpace(p.Sheet+" "+p.Region))
}

var mysqlEscaper = strings.NewReplacer(`\`, `\\`, "'", "''", "\x00", `\0`, "\x1a", `\Z`)

// quoteString renders s as a single-quoted SQL string literal.
func quoteString(d, s string) string {
	if d == infer.MySQL {
		s = mysqlEscaper.Replace(s)
	} else {
		s = strings.ReplaceAll(s, "'", "''")
	}
	return "'" + s + "'"
}

// validDecimal reports whether s is an optional "-", digits, and optionally
// "." followed by digits.
func validDecimal(s string) bool {
	s = strings.TrimPrefix(s, "-")
	intPart, frac, hasPoint := strings.Cut(s, ".")
	return allDigits(intPart) && (!hasPoint || allDigits(frac))
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// oneLine replaces line breaks so s can follow a "--" SQL comment marker.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// truncateRunes shortens s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// mysqlComment spells out characters beyond the Basic Multilingual Plane
// (emoji) as U+1F525: MySQL stores comments as utf8mb3 and would turn them
// into "?".
func mysqlComment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0xFFFF {
			fmt.Fprintf(&b, "U+%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
