// Package dsn finds the database connection string for kempt and converts
// connection URLs into the form each driver expects.
package dsn

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrNoDSN means no source held a connection string.
var ErrNoDSN = errors.New("no database DSN: pass --dsn or set KEMPT_DSN, DATABASE_PUBLIC_URL, or DATABASE_URL")

// Target is a resolved, driver-ready connection.
type Target struct {
	Dialect  string // "postgres" | "mysql" | "sqlite"
	DSN      string // driver-ready: postgres URL as-is; go-sql-driver DSN; sqlite file path
	Source   string // "--dsn" | "$KEMPT_DSN" | "$DATABASE_PUBLIC_URL" | "$DATABASE_URL"
	Redacted string // the original string with any password replaced by "****"
}

const accepted = "postgres://, postgresql://, mysql://user:pass@host:port/db, sqlite://path, sqlite:path, file: URIs, or a path ending in .db, .sqlite, or .sqlite3"

// Resolve picks the first non-blank DSN from the flag, $KEMPT_DSN,
// $DATABASE_PUBLIC_URL, and $DATABASE_URL, in that order, and parses it.
// A nil getenv reads the process environment.
func Resolve(flag string, getenv func(string) string) (Target, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	sources := []struct{ name, value string }{
		{"--dsn", flag},
		{"$KEMPT_DSN", getenv("KEMPT_DSN")},
		{"$DATABASE_PUBLIC_URL", getenv("DATABASE_PUBLIC_URL")},
		{"$DATABASE_URL", getenv("DATABASE_URL")},
	}
	for _, s := range sources {
		if strings.TrimSpace(s.value) == "" {
			continue
		}
		t, err := Parse(s.value)
		if err != nil {
			return Target{}, fmt.Errorf("%s: %w", s.name, err)
		}
		t.Source = s.name
		return t, nil
	}
	return Target{}, ErrNoDSN
}

// Parse classifies raw by scheme and converts it to a driver DSN. Source is
// left empty.
func Parse(raw string) (Target, error) {
	s := strings.TrimSpace(raw)
	t := Target{Redacted: Redact(s)}
	lower := strings.ToLower(s)
	switch {
	case s == "":
		return Target{}, errors.New("empty DSN")
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		i := strings.Index(s, ":")
		t.Dialect, t.DSN = "postgres", lower[:i]+s[i:]
	case strings.HasPrefix(lower, "mysql://"):
		d, err := mysqlDSN(s)
		if err != nil {
			return Target{}, fmt.Errorf("mysql DSN %s: %w", t.Redacted, err)
		}
		t.Dialect, t.DSN = "mysql", d
	case strings.HasPrefix(lower, "sqlite:"):
		path := strings.TrimPrefix(s[len("sqlite:"):], "//")
		if path == "" {
			return Target{}, fmt.Errorf("sqlite DSN %s has no file path", t.Redacted)
		}
		t.Dialect, t.DSN = "sqlite", path
	case strings.HasPrefix(lower, "file:"), isSQLitePath(lower):
		t.Dialect, t.DSN = "sqlite", s
	default:
		return Target{}, fmt.Errorf("unsupported DSN %q: use %s", t.Redacted, accepted)
	}
	return t, nil
}

func isSQLitePath(lower string) bool {
	if strings.Contains(lower, "://") {
		return false
	}
	for _, ext := range []string{".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// sslModes maps the MySQL client's ssl-mode values, which hosted providers
// put in their connection URLs, to go-sql-driver tls settings.
var sslModes = map[string]string{
	"disabled":        "false",
	"preferred":       "preferred",
	"required":        "skip-verify",
	"verify_ca":       "true",
	"verify_identity": "true",
}

// mysqlDSN converts mysql://user:pass@host:port/db?x=y to the go-sql-driver
// form user:pass@tcp(host:port)/db?parseTime=true&x=y.
func mysqlDSN(s string) (string, error) {
	const hint = "percent-encode special characters in the user name and password"
	if strings.Contains(s, "#") {
		return "", fmt.Errorf("unexpected '#' (%s)", hint)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid URL (%s)", hint)
	}
	db := strings.TrimPrefix(u.Path, "/")
	if strings.Contains(db, "/") {
		return "", fmt.Errorf("database name %q contains '/'", db)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid query string: %w", err)
	}
	if q.Has("strict") {
		return "", errors.New(`the "strict" parameter was removed from go-sql-driver/mysql`)
	}
	// The query string is already in the driver's parameter encoding; ParseDSN
	// validates and applies the known parameters and keeps the rest. Escaping
	// "/" keeps its last-slash split on the (empty) database name.
	cfg, err := mysql.ParseDSN("/?" + strings.ReplaceAll(u.RawQuery, "/", "%2F"))
	if err != nil {
		return "", err
	}
	if !q.Has("parseTime") {
		cfg.ParseTime = true
	}
	for _, k := range []string{"ssl-mode", "sslmode", "ssl_mode"} {
		if !q.Has(k) {
			continue
		}
		mode, ok := sslModes[strings.ToLower(q.Get(k))]
		if !ok {
			return "", fmt.Errorf("unknown %s %q", k, q.Get(k))
		}
		delete(cfg.Params, k)
		if !q.Has("tls") {
			cfg.TLSConfig = mode
		}
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "3306"
	}
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	if strings.Contains(cfg.User, ":") {
		return "", errors.New("user names containing ':' cannot be expressed in a go-sql-driver DSN")
	}
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", net.JoinHostPort(host, port), db
	return cfg.FormatDSN(), nil
}

var (
	// driverAt matches the "@" that ends the user info of a go-sql-driver
	// DSN, followed by an optional protocol and address and the "/" before
	// the database name: "@/", "@tcp(host:3306)/".
	driverAt = regexp.MustCompile(`@(\w+\([^)]*\))?/`)
	// kvPassword matches password=value in key/value connection strings and
	// URL query strings; the value may be single- or double-quoted.
	kvPassword = regexp.MustCompile(`(?i)(^|[\s;&?])((?:ssl)?password|passwd|pwd)(\s*=\s*)('(?:\\.|[^'\\])*'|"(?:[^"]|"")*"|[^\s;&]*)`)
)

// Redact replaces any password in raw with "****": the password of a URL
// (the user name is kept), of a go-sql-driver user:pass@tcp(...)/db DSN, and
// password=... pairs in key/value connection strings or query strings.
// When the text is ambiguous it hides too much rather than too little.
func Redact(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[:i+3] + redactUserinfo(s[i+3:])
	} else if m := driverAt.FindAllStringIndex(s, -1); len(m) > 0 {
		at := m[len(m)-1][0]
		s = redactPassword(s[:at]) + s[at:]
	}
	return kvPassword.ReplaceAllString(s, "${1}${2}${3}****")
}

// redactUserinfo redacts the password in the part of a URL after "://".
// In a well-formed URL the user info ends at the last "@" before the first
// "/", "?", or "#". A password holding one of those unescaped pushes the
// "@" further right, so fall back to the last "@" anywhere.
func redactUserinfo(rest string) string {
	auth := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		auth = rest[:i]
	}
	at := strings.LastIndex(auth, "@")
	if at < 0 {
		at = strings.LastIndex(rest, "@")
	}
	if at < 0 {
		return rest
	}
	return redactPassword(rest[:at]) + rest[at:]
}

// redactPassword replaces everything after the first ":" in user info.
func redactPassword(userinfo string) string {
	user, pass, ok := strings.Cut(userinfo, ":")
	if !ok || pass == "" {
		return userinfo
	}
	return user + ":****"
}

// Describe names the dialect, the source, and the redacted DSN, e.g.
// "postgres DSN from $DATABASE_PUBLIC_URL: postgres://user:****@host:5432/db".
func (t Target) Describe() string {
	if t.Source == "" {
		return fmt.Sprintf("%s DSN: %s", t.Dialect, t.Redacted)
	}
	return fmt.Sprintf("%s DSN from %s: %s", t.Dialect, t.Source, t.Redacted)
}
