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
	mysqlRowBudget     = 60000 // bytes of VARCHAR storage allowed per row (hard limit 65535)
	mysqlColumnComment = 1024  // max characters in a column COMMENT
	mysqlTableComment  = 2048  // max characters in a table COMMENT
)

// DDLOptions control CREATE TABLE rendering.
type DDLOptions struct {
	Schema      string // Postgres schema, MySQL database, or SQLite attached database; "" = unqualified
	IfNotExists bool
}

// DDL renders the CREATE TABLE statement for p in dialect, plus column and
// table comments where the dialect supports them. The result ends with a
// newline. Unknown dialects render as Postgres.
func DDL(p *plan.Plan, dialect string, opts DDLOptions) string {
	d := ddlDialect(dialect)
	table := QualifiedTable(d, opts.Schema, p.Table)
	types := columnTypes(p, d)
	comment := tableComment(p)

	var b strings.Builder
	if d == infer.MySQL {
		if moved := fitMySQLRow(types); len(moved) > 0 {
			names := make([]string, len(moved))
			for i, j := range moved {
				names[i] = oneLine(p.Columns[j].Name)
			}
			fmt.Fprintf(&b, "-- kempt: %s changed from VARCHAR to TEXT to stay under the MySQL row size limit.\n",
				strings.Join(names, ", "))
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
	for i, c := range p.Columns {
		b.WriteString("  " + QuoteIdent(d, c.Name) + " " + types[i])
		if d == infer.MySQL && c.Comment != "" {
			b.WriteString(" COMMENT " + Literal(d, truncateRunes(c.Comment, mysqlColumnComment)))
		}
		if i < len(p.Columns)-1 {
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
			b.WriteString(" COMMENT=" + Literal(d, truncateRunes(comment, mysqlTableComment)))
		}
	}
	b.WriteString(";\n")
	if d == infer.Postgres {
		for _, c := range p.Columns {
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
		if validDecimal(string(x)) {
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
// ties) until the VARCHAR columns fit mysqlRowBudget at 4 bytes per
// character. It returns the indexes it changed, in the order changed.
func fitMySQLRow(types []string) []int {
	widths := make([]int, len(types))
	total := 0
	for i, t := range types {
		widths[i] = varcharLen(t)
		total += widths[i] * 4
	}
	var moved []int
	for total > mysqlRowBudget {
		w := 0
		for i, n := range widths {
			if n > widths[w] {
				w = i
			}
		}
		if widths[w] == 0 {
			break
		}
		total -= widths[w] * 4
		widths[w] = 0
		types[w] = "TEXT"
		moved = append(moved, w)
	}
	return moved
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
