package normalize

import (
	"regexp"
	"strings"
)

// bracketRe matches one non-nested "(...)" or "[...]" group.
var bracketRe = regexp.MustCompile(`\(([^()\[\]]*)\)|\[([^()\[\]]*)\]`)

// currencyUnitRe matches a currency symbol with an optional scale: "$",
// "$000s", "$000's", "$K", "$MM", "€000", "£m", "US$".
var currencyUnitRe = regexp.MustCompile(`(?i)^(us)?[$€£¥] ?('?000'?s?|k|m|mm|mn|b|bn|thousands?|millions?|billions?)?$`)

// scaleRe matches a bare scale: "000s", "'000", "thousands", "millions".
var scaleRe = regexp.MustCompile(`(?i)^('?000'?s?|thousands|millions|billions)$`)

var isoCurrencies = setOf("USD EUR GBP JPY CAD AUD NZD MXN CHF CNY INR HKD SGD SEK NOK DKK BRL ZAR KRW RUB TRY PLN ILS AED SAR")

// measureUnits match exactly as written; case matters ("mW" vs "MW").
// "s" and "min" are left out: "Item(s)" and "Temp (min)" are not units.
var measureUnits = setOf(`% pct percent
	Wh kWh MWh GWh TWh W kW MW GW kVA VA V mA kV
	BTU MMBtu therms therm ccf mcf J kJ MJ GJ cal kcal
	mg g kg t tons tonnes lb lbs oz
	mm cm m km in ft yd mi miles inches feet meters
	m2 m² m3 m³ km2 km² ft2 ft² sq ft sqft acres ha
	ml mL l L gal gallons bbl
	sec secs seconds ms mins minutes h hr hrs hours days wks weeks months yrs years
	°F °C ºF ºC
	mph kph km/h m/s psi kPa MPa Pa
	KB kB MB GB TB Mbps Gbps Hz kHz MHz GHz dB ppm ppb mpg`)

// measureUnitsFold holds the units of three or more characters, matched
// case-insensitively ("KWH", "Hours"); shorter ones are too ambiguous with
// codes such as state abbreviations.
var measureUnitsFold = func() map[string]bool {
	m := map[string]bool{}
	for u := range measureUnits {
		if len([]rune(u)) >= 3 {
			m[strings.ToLower(u)] = true
		}
	}
	return m
}()

func setOf(words string) map[string]bool {
	m := map[string]bool{}
	for _, line := range strings.Split(words, "\n") {
		// "sq ft" is the only unit with a space; keep it whole.
		line = strings.ReplaceAll(line, "sq ft", "sq\x00ft")
		for _, w := range strings.Fields(line) {
			m[strings.ReplaceAll(w, "\x00", " ")] = true
		}
	}
	return m
}

// ExtractUnit finds a unit in a header and returns the header without it.
// Units are recognized inside parentheses or brackets ("Revenue ($000s)",
// "Mass [kg]", "Change (%)"), optionally introduced by "in" ("(in USD)"),
// or as a trailing "in thousands"/"in millions"/"in billions" phrase.
// The unit is returned as written, except that the trailing phrase yields
// just the scale word ("thousands"). Years and words that are not units
// ("(2020)", "(see note)") are left alone and return unit "".
func ExtractUnit(header string) (rest, unit string) {
	ms := bracketRe.FindAllStringSubmatchIndex(header, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		lo, hi := m[2], m[3]
		if lo < 0 {
			lo, hi = m[4], m[5]
		}
		if u, ok := unitText(header[lo:hi]); ok {
			return tidy(header[:m[0]] + " " + header[m[1]:]), u
		}
	}

	t := collapse(header)
	for _, w := range []string{"thousands", "millions", "billions"} {
		n := len(t) - len("in "+w)
		if n < 0 || !strings.EqualFold(t[n:], "in "+w) {
			continue
		}
		if head := t[:n]; head == "" || strings.HasSuffix(head, " ") || strings.HasSuffix(head, ",") {
			return tidy(head), w
		}
	}
	return t, ""
}

// unitText reports whether bracket contents are a unit and returns it
// trimmed (without a leading "in ").
func unitText(s string) (string, bool) {
	s = collapse(s)
	if len(s) > 3 && strings.EqualFold(s[:3], "in ") {
		s = s[3:]
	}
	return s, s != "" && isUnit(s)
}

// isUnit reports whether s (trimmed) is a unit: a currency, a scale, a
// measurement, or a ratio of those ("$/hr", "kg/m³", "kWh per day").
func isUnit(s string) bool {
	if isSimpleUnit(s) {
		return true
	}
	for _, sep := range []string{"/", " per "} {
		if a, b, ok := strings.Cut(s, sep); ok {
			a, b = strings.TrimSpace(a), strings.TrimSpace(b)
			return isSimpleUnit(a) && (isSimpleUnit(b) || isWord(b))
		}
	}
	return false
}

func isSimpleUnit(s string) bool {
	if measureUnits[s] || measureUnitsFold[strings.ToLower(s)] || currencyUnitRe.MatchString(s) || scaleRe.MatchString(s) {
		return true
	}
	code, scale, _ := strings.Cut(s, " ")
	return isoCurrencies[code] && (scale == "" || scaleRe.MatchString(scale) || currencyScale(scale))
}

// currencyScale matches the short scale suffixes after an ISO code ("USD M").
func currencyScale(s string) bool {
	switch strings.ToLower(s) {
	case "k", "m", "mm", "mn", "b", "bn":
		return true
	}
	return false
}

// isWord reports whether s is a short plain word, as in "$/unit".
func isWord(s string) bool {
	if s == "" || len(s) > 12 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// collapse trims s and squeezes internal whitespace to single spaces.
func collapse(s string) string { return strings.Join(strings.FieldsFunc(s, isSpace), " ") }

// tidy collapses whitespace and drops separators left dangling at the end
// once a unit is removed ("Revenue, in thousands" → "Revenue").
func tidy(s string) string {
	return strings.TrimRight(collapse(s), " ,;:-–—")
}
