package normalize

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DateOrder resolves numeric dates like "03/04/2026".
type DateOrder int

const (
	MDY DateOrder = iota // month first (US)
	DMY                  // day first
)

// ParsedDate is a successfully parsed text date.
type ParsedDate struct {
	T       time.Time // UTC
	HasTime bool      // the time of day is not midnight
	Numeric bool      // numeric day/month form ("03/04/2026"), so order-dependent
	P1, P2  int       // first two numeric components as written, for Numeric forms
	Layout  string    // short human label such as "MM/DD/YYYY" or "D-Mon-YYYY"
}

type monthName struct {
	month time.Month
	full  bool
}

var monthNames = map[string]monthName{
	"jan": {time.January, false}, "january": {time.January, true},
	"feb": {time.February, false}, "february": {time.February, true},
	"mar": {time.March, false}, "march": {time.March, true},
	"apr": {time.April, false}, "april": {time.April, true},
	"may": {time.May, false},
	"jun": {time.June, false}, "june": {time.June, true},
	"jul": {time.July, false}, "july": {time.July, true},
	"aug": {time.August, false}, "august": {time.August, true},
	"sep": {time.September, false}, "sept": {time.September, false}, "september": {time.September, true},
	"oct": {time.October, false}, "october": {time.October, true},
	"nov": {time.November, false}, "november": {time.November, true},
	"dec": {time.December, false}, "december": {time.December, true},
}

// All patterns run on lowercased input with whitespace collapsed.
var (
	timeRe     = regexp.MustCompile(`(t| )(\d{1,2}):(\d{2})(?::(\d{2})(?:\.(\d{1,9}))?)?(?: ?([ap])\.?m\.?)?(z)?$`)
	isoRe      = regexp.MustCompile(`^(\d{4})([-/])(\d{1,2})([-/])(\d{1,2})$`)
	numericRe  = regexp.MustCompile(`^(\d{1,2})([-/.])(\d{1,2})([-/.])(\d{4}|\d{2})$`)
	monFirstRe = regexp.MustCompile(`^([a-z]+)\.?([ -])(\d{1,2})(?:st|nd|rd|th)?(, ?| |-)(\d{4})$`)
	dayFirstRe = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th)?([ -])([a-z]+)\.?(, ?| |-)(\d{4}|\d{2})$`)
)

// ParseDate parses a text date: ISO (optionally with a time), numeric
// day/month forms resolved by order, and English month-name forms. Numeric
// forms with "." separators need a four-digit year so that version strings
// like "1.2.10" stay text. Two-digit years 00–68 map to 20xx, 69–99 to 19xx.
func ParseDate(s string, order DateOrder) (ParsedDate, bool) {
	str := strings.ToLower(strings.Join(strings.FieldsFunc(s, isSpace), " "))
	if str == "" || len(str) > 48 {
		return ParsedDate{}, false
	}

	var (
		hh, mi, sec, nsec int
		clock, tSep, zulu bool
		timeLabel         string
	)
	if m := timeRe.FindStringSubmatch(str); m != nil {
		var ok bool
		if hh, mi, sec, nsec, timeLabel, ok = parseClock(m); !ok {
			return ParsedDate{}, false
		}
		clock, tSep, zulu = true, m[1] == "t", m[7] != ""
		str = str[:len(str)-len(m[0])]
	}

	var (
		p              ParsedDate
		y, mo, d       int
		dateLabel      string
		iso            bool
		yearTwo, valid bool
	)
	if m := isoRe.FindStringSubmatch(str); m != nil && m[2] == m[4] {
		y, mo, d = atoi(m[1]), atoi(m[3]), atoi(m[5])
		dateLabel, iso, valid = "YYYY"+m[2]+"MM"+m[4]+"DD", true, true
	} else if m := numericRe.FindStringSubmatch(str); m != nil && m[2] == m[4] && (m[2] != "." || len(m[5]) == 4) {
		p.Numeric, p.P1, p.P2 = true, atoi(m[1]), atoi(m[3])
		y, yearTwo = atoi(m[5]), len(m[5]) == 2
		a, b := "D", "M"
		mo, d = p.P2, p.P1
		if order == MDY {
			a, b = "M", "D"
			mo, d = p.P1, p.P2
		}
		if len(m[1]) == 2 && len(m[3]) == 2 {
			a, b = a+a, b+b
		}
		dateLabel, valid = a+m[2]+b+m[4]+yearLabel(m[5]), true
	} else if m := monFirstRe.FindStringSubmatch(str); m != nil {
		if mn, ok := monthNames[m[1]]; ok {
			y, mo, d = atoi(m[5]), int(mn.month), atoi(m[3])
			dateLabel, valid = monLabel(mn)+m[2]+"D"+sepLabel(m[4])+"YYYY", true
		}
	} else if m := dayFirstRe.FindStringSubmatch(str); m != nil {
		if mn, ok := monthNames[m[3]]; ok {
			y, mo, d, yearTwo = atoi(m[5]), int(mn.month), atoi(m[1]), len(m[5]) == 2
			dateLabel, valid = "D"+m[2]+monLabel(mn)+sepLabel(m[4])+yearLabel(m[5]), true
		}
	}
	if !valid || ((tSep || zulu) && !iso) {
		return ParsedDate{}, false
	}
	if yearTwo {
		// Excel's window: 00–29 are 2000–2029, 30–99 are 1930–1999.
		y += 2000
		if y >= 2030 {
			y -= 100
		}
	}
	if y < 1000 || y > 9999 || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return ParsedDate{}, false
	}
	t := time.Date(y, time.Month(mo), d, hh, mi, sec, nsec, time.UTC)
	if t.Day() != d || int(t.Month()) != mo {
		return ParsedDate{}, false
	}
	p.T, p.Layout = t, dateLabel
	p.HasTime = hh != 0 || mi != 0 || sec != 0 || nsec != 0
	if clock {
		sep := " "
		if tSep {
			sep = "T"
		}
		p.Layout += sep + timeLabel
		if zulu {
			p.Layout += "Z"
		}
	}
	return p, true
}

// parseClock validates a timeRe match and returns its components and label.
func parseClock(m []string) (hh, mi, sec, nsec int, label string, ok bool) {
	hh, mi = atoi(m[2]), atoi(m[3])
	label = "HH:MM"
	if m[4] != "" {
		sec = atoi(m[4])
		label += ":SS"
	}
	if m[5] != "" {
		nsec = atoi((m[5] + "00000000")[:9])
		label += "." + strings.Repeat("F", len(m[5]))
	}
	if m[6] != "" {
		if m[7] != "" || hh < 1 || hh > 12 {
			return 0, 0, 0, 0, "", false
		}
		hh %= 12
		if m[6] == "p" {
			hh += 12
		}
		label = "H" + label[2:] + " AM/PM"
	}
	return hh, mi, sec, nsec, label, hh <= 23 && mi <= 59 && sec <= 59
}

func monLabel(mn monthName) string {
	if mn.full {
		return "Month"
	}
	return "Mon"
}

func sepLabel(sep string) string {
	if strings.HasPrefix(sep, ",") {
		return ", "
	}
	return sep
}

func yearLabel(y string) string {
	if len(y) == 2 {
		return "YY"
	}
	return "YYYY"
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// maxSerial is the first serial past 9999-12-31 in the 1900 date system.
const maxSerial = 2958466

var (
	epoch1900 = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	epoch1904 = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
)

// ExcelSerial converts an Excel serial date (0 < v < 2958466) to a UTC time
// rounded to the nearest second. hasTime is set when the time of day is not
// midnight. In the 1900 system serials below 60 follow Excel's calendar
// (1 = 1900-01-01), and 60, Excel's fictitious 1900-02-29, is rejected.
func ExcelSerial(v float64, date1904 bool) (t time.Time, hasTime bool, ok bool) {
	if !(v > 0 && v < maxSerial) {
		return time.Time{}, false, false
	}
	day := math.Floor(v)
	ms := math.Round((v - day) * 86400000) // Excel keeps millisecond precision
	if ms >= 86400000 {
		day, ms = day+1, 0
	}
	base := epoch1900
	switch {
	case date1904:
		base = epoch1904
	case day == 60:
		return time.Time{}, false, false
	case day < 60:
		base = base.AddDate(0, 0, 1)
	}
	t = base.AddDate(0, 0, int(day)).Add(time.Duration(ms) * time.Millisecond)
	if t.Year() > 9999 {
		return time.Time{}, false, false
	}
	return t, t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0, true
}

// PlausibleSerial reports whether v falls in the serial range kempt accepts
// as a date on a header hint alone: 20000..80000 (about 1954..2119).
func PlausibleSerial(v float64) bool { return v >= 20000 && v <= 80000 }

// DateHint reports whether a column header suggests dates: it contains
// "date", "day", "month", or "period" (case-insensitive).
func DateHint(header string) bool {
	for _, w := range strings.Split(SnakeCase(header), "_") {
		switch w {
		case "date", "dates", "day", "days", "month", "months", "period", "periods", "dt":
			return true
		}
	}
	return false
}
