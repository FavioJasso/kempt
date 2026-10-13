package server

import (
	"errors"
	"strings"
	"unicode"
)

// readOnlyStarts are the statements /api/query accepts. The query also runs
// in a READ ONLY transaction under a role that can only SELECT, so this list
// is defense in depth rather than the only guard.
var readOnlyStarts = map[string]bool{"select": true, "with": true, "values": true, "table": true, "explain": true}

// checkQuery verifies that sql is exactly one read-only statement. It returns
// the statement with any trailing semicolon removed and its first keyword.
func checkQuery(sql string) (stmt, keyword string, err error) {
	end, err := statementEnd(sql)
	if err != nil {
		return "", "", err
	}
	stmt = strings.TrimSpace(sql[:end])
	if stmt == "" {
		return "", "", errors.New("the query is empty")
	}
	keyword = strings.ToLower(firstWord(stmt))
	if !readOnlyStarts[keyword] {
		return "", "", errors.New("only read-only queries are allowed (SELECT, WITH, VALUES, TABLE, or EXPLAIN)")
	}
	return stmt, keyword, nil
}

// statementEnd scans sql with a small Postgres lexer (strings, E-strings,
// dollar quotes, quoted identifiers, line and nested block comments) and
// returns the offset of the first top-level semicolon, or len(sql). Anything
// other than whitespace and comments after that semicolon is an error.
func statementEnd(sql string) (int, error) {
	i, n := 0, len(sql)
	end := -1
	for i < n {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < n && sql[i+1] == '*':
			depth := 0
			for i < n {
				if sql[i] == '/' && i+1 < n && sql[i+1] == '*' {
					depth++
					i += 2
				} else if sql[i] == '*' && i+1 < n && sql[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
			if depth != 0 {
				return 0, errors.New("unterminated block comment")
			}
			continue
		}
		if end >= 0 {
			if !unicode.IsSpace(rune(c)) {
				return 0, errors.New("only a single SQL statement is allowed")
			}
			i++
			continue
		}
		switch {
		case c == ';':
			end = i
			i++
		case c == '\'':
			escapes := i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isIdentByte(sql[i-2]))
			j, err := skipQuoted(sql, i, '\'', escapes)
			if err != nil {
				return 0, err
			}
			i = j
		case c == '"':
			j, err := skipQuoted(sql, i, '"', false)
			if err != nil {
				return 0, err
			}
			i = j
		case c == '$' && (i == 0 || !isIdentByte(sql[i-1])):
			if tag, ok := dollarTag(sql[i:]); ok {
				k := strings.Index(sql[i+len(tag):], tag)
				if k < 0 {
					return 0, errors.New("unterminated dollar-quoted string")
				}
				i += 2*len(tag) + k
			} else {
				i++
			}
		default:
			i++
		}
	}
	if end < 0 {
		return n, nil
	}
	return end, nil
}

// skipQuoted returns the offset just past the quoted run starting at i.
// Doubled quotes are escapes; with backslash escapes, \x skips x.
func skipQuoted(s string, i int, q byte, backslash bool) (int, error) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, nil
		}
	}
	return 0, errors.New("unterminated quoted string")
}

// dollarTag returns the opening tag ("$$" or "$name$") at the start of s.
func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		if s[j] == '$' {
			return s[:j+1], true
		}
		if !(isIdentByte(s[j]) && !(j == 1 && s[j] >= '0' && s[j] <= '9')) {
			return "", false
		}
	}
	return "", false
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// firstWord returns the first keyword of a statement, skipping comments and
// opening parentheses.
func firstWord(s string) string {
	for {
		s = strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
		switch {
		case strings.HasPrefix(s, "--"):
			if k := strings.IndexByte(s, '\n'); k >= 0 {
				s = s[k+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			depth, i := 0, 0
			for i < len(s) {
				if strings.HasPrefix(s[i:], "/*") {
					depth, i = depth+1, i+2
				} else if strings.HasPrefix(s[i:], "*/") {
					depth, i = depth-1, i+2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
			if depth != 0 {
				return ""
			}
			s = s[i:]
			continue
		}
		break
	}
	end := strings.IndexFunc(s, func(r rune) bool { return !(r == '_' || unicode.IsLetter(r)) })
	if end < 0 {
		return s
	}
	return s[:end]
}
