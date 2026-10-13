// Package infer chooses a SQL type for each column and casts its values.
package infer

import (
	"fmt"
	"strings"
)

// Base is a position in kempt's type lattice, narrowest first.
type Base int

const (
	Bool Base = iota
	Int
	BigInt
	Decimal
	Date
	Timestamp
	Text
)

var baseNames = [...]string{"bool", "int", "bigint", "decimal", "date", "timestamp", "text"}

func (b Base) String() string {
	if b >= 0 && int(b) < len(baseNames) {
		return baseNames[b]
	}
	return fmt.Sprintf("base(%d)", int(b))
}

// MarshalText renders the base by name in JSON.
func (b Base) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// Dialects kempt can emit.
const (
	Postgres = "postgres"
	MySQL    = "mysql"
	SQLite   = "sqlite"
)

// Dialects lists every supported dialect in a stable order.
var Dialects = []string{Postgres, MySQL, SQLite}

// MaxPrecision is the largest NUMERIC precision kempt emits.
const MaxPrecision = 38

// Type is a dialect-neutral column type.
type Type struct {
	Base      Base `json:"base"`
	Precision int  `json:"precision,omitempty"` // Decimal: total significant digits (<= 38)
	Scale     int  `json:"scale,omitempty"`     // Decimal: digits after the point
	MaxLen    int  `json:"maxLen,omitempty"`    // Text: longest value in characters (runes)
}

// String renders the neutral type, e.g. "decimal(10,2)".
func (t Type) String() string {
	if t.Base == Decimal {
		return fmt.Sprintf("decimal(%d,%d)", t.Precision, t.Scale)
	}
	return t.Base.String()
}

// SQL renders the type for a dialect.
func (t Type) SQL(dialect string) string {
	switch strings.ToLower(dialect) {
	case MySQL:
		switch t.Base {
		case Bool:
			return "BOOLEAN"
		case Int:
			return "INT"
		case BigInt:
			return "BIGINT"
		case Decimal:
			return fmt.Sprintf("DECIMAL(%d,%d)", t.Precision, t.Scale)
		case Date:
			return "DATE"
		case Timestamp:
			return "DATETIME(6)"
		default:
			return mysqlText(t.MaxLen)
		}
	case SQLite:
		switch t.Base {
		case Bool, Int, BigInt:
			return "INTEGER"
		case Decimal:
			return fmt.Sprintf("NUMERIC(%d,%d)", t.Precision, t.Scale)
		case Date:
			return "DATE"
		case Timestamp:
			return "TIMESTAMP"
		default:
			return "TEXT"
		}
	default: // postgres
		switch t.Base {
		case Bool:
			return "BOOLEAN"
		case Int:
			return "INTEGER"
		case BigInt:
			return "BIGINT"
		case Decimal:
			return fmt.Sprintf("NUMERIC(%d,%d)", t.Precision, t.Scale)
		case Date:
			return "DATE"
		case Timestamp:
			return "TIMESTAMP"
		default:
			return "TEXT"
		}
	}
}

// mysqlText sizes a MySQL text column: VARCHAR(n) with n the next power of
// two at or above the longest value, capped at 1024; TEXT beyond that.
func mysqlText(maxLen int) string {
	if maxLen > 1024 {
		return "TEXT"
	}
	n := 1
	for n < maxLen {
		n <<= 1
	}
	return fmt.Sprintf("VARCHAR(%d)", n)
}
