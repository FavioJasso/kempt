package normalize

import (
	"strconv"
	"time"
)

// Clean values. A Plan row is a []any whose elements are one of:
//
//	nil        SQL NULL
//	bool       BOOLEAN
//	int64      INTEGER / BIGINT
//	Decimal    NUMERIC(p,s)
//	Date       DATE
//	Timestamp  TIMESTAMP (no time zone)
//	string     TEXT
//
// The named types keep exact textual precision and give stable JSON.

// Decimal is a canonical decimal literal: an optional "-", one or more
// digits, and optionally "." followed by one or more digits. It never uses
// exponent notation, so it is safe to splice into SQL and JSON.
type Decimal string

// MarshalJSON renders the decimal as a bare JSON number.
func (d Decimal) MarshalJSON() ([]byte, error) { return []byte(d), nil }

// Float returns the decimal as a float64 (lossy for very long values).
func (d Decimal) Float() float64 {
	f, _ := strconv.ParseFloat(string(d), 64)
	return f
}

func (d Decimal) String() string { return string(d) }

// Date is a calendar date. The time-of-day is always midnight UTC.
type Date struct{ time.Time }

// NewDate truncates t to a UTC calendar date.
func NewDate(t time.Time) Date {
	return Date{time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}
}

func (d Date) String() string { return d.Format("2006-01-02") }

// MarshalJSON renders the date as "YYYY-MM-DD".
func (d Date) MarshalJSON() ([]byte, error) { return []byte(`"` + d.String() + `"`), nil }

// Timestamp is a wall-clock date and time without a zone, stored as UTC.
type Timestamp struct{ time.Time }

// NewTimestamp re-labels t's wall clock as UTC.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)}
}

func (t Timestamp) String() string {
	if t.Nanosecond() != 0 {
		return t.Format("2006-01-02 15:04:05.999999")
	}
	return t.Format("2006-01-02 15:04:05")
}

// MarshalJSON renders the timestamp as "YYYY-MM-DD HH:MM:SS[.ffffff]".
func (t Timestamp) MarshalJSON() ([]byte, error) { return []byte(`"` + t.String() + `"`), nil }

// Format renders any clean value as text the way kempt displays it
// (NULL renders as the empty string).
func Format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case Decimal:
		return string(x)
	case Date:
		return x.String()
	case Timestamp:
		return x.String()
	case string:
		return x
	default:
		return ""
	}
}
