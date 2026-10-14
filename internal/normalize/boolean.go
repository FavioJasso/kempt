package normalize

import "strings"

// ParseBool parses true/false, yes/no, y/n, and t/f (trimmed,
// case-insensitive). It deliberately rejects 1/0, which are numbers.
func ParseBool(s string) (v bool, ok bool) {
	switch strings.ToLower(trim(s)) {
	case "true", "yes", "y", "t":
		return true, true
	case "false", "no", "n", "f":
		return false, true
	}
	return false, false
}
