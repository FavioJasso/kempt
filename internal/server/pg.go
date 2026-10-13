package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/FavioJasso/kempt/internal/emit"
)

// Demo schemas are named demo_<8 random [a-z0-9]>. Each one gets a LOGIN
// role of the same name that can only read that schema; /api/query connects
// as that role, so visitors can never reach the server's own privileges
// (the Railway DATABASE_URL user is a superuser).
var schemaRe = regexp.MustCompile(`^demo_[a-z0-9]{8}$`)

const schemaComment = "kempt demo schema; created "

func newSchemaName() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "demo_" + string(b), nil
}

// rolePassword derives the query role's password from the server secret.
func (s *Server) rolePassword(schema string) string {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write([]byte(schema))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		writeError(w, http.StatusServiceUnavailable, "loading is disabled: this server has no Postgres DSN")
		return
	}
	up, err := s.parseUpload(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sheets, err := s.analyze(up)
	if err != nil {
		s.fail(w, err)
		return
	}
	type loaded struct {
		Table string `json:"table"`
		Rows  int64  `json:"rows"`
	}
	var tables []loaded
	var first string
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	schema, err := newSchemaName()
	if err != nil {
		s.fail(w, err)
		return
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		s.log.Error("acquire connection", "err", err)
		writeError(w, http.StatusServiceUnavailable, "the database is unavailable")
		return
	}
	defer conn.Release()
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.dropSchema(c, conn.Conn(), schema)
	}
	q := pgx.Identifier{schema}.Sanitize()
	created := time.Now().UTC().Format(time.RFC3339)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+q+"; COMMENT ON SCHEMA "+q+" IS '"+schemaComment+created+"'"); err != nil {
		s.log.Error("create schema", "err", err)
		writeError(w, http.StatusServiceUnavailable, "could not create a schema: "+pgMessage(err))
		return
	}
	for _, sh := range sheets {
		for _, t := range sh.Tables {
			n, err := emit.LoadPostgres(ctx, conn.Conn(), t, emit.LoadOptions{Schema: schema, IfExists: "fail"})
			if err != nil {
				cleanup()
				writeError(w, http.StatusBadRequest, fmt.Sprintf("loading %s failed: %s", t.Table, pgMessage(err)))
				return
			}
			if first == "" {
				first = t.Table
			}
			tables = append(tables, loaded{t.Table, n})
		}
	}
	if len(tables) == 0 {
		cleanup()
		writeError(w, http.StatusUnprocessableEntity, "no table was detected in this file, so there is nothing to load")
		return
	}
	queryErr := s.createQueryRole(ctx, conn.Conn(), schema)
	if queryErr != nil {
		s.log.Warn("query role unavailable; /api/query disabled for this schema", "schema", schema, "err", queryErr)
	}
	resp := map[string]any{
		"schema":       schema,
		"table":        first,
		"tables":       tables,
		"queryEnabled": queryErr == nil,
	}
	if s.cfg.Demo {
		resp["expiresAt"] = time.Now().UTC().Add(schemaTTL).Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// createQueryRole creates the read-only LOGIN role for a demo schema.
func (s *Server) createQueryRole(ctx context.Context, conn *pgx.Conn, schema string) error {
	q := pgx.Identifier{schema}.Sanitize()
	until := time.Now().UTC().Add(schemaTTL + time.Hour).Format("2006-01-02 15:04:05+00")
	stmts := []string{
		"CREATE ROLE " + q + " LOGIN PASSWORD '" + s.rolePassword(schema) + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION CONNECTION LIMIT 5 VALID UNTIL '" + until + "'",
		"ALTER ROLE " + q + " SET default_transaction_read_only = on",
		"ALTER ROLE " + q + " SET statement_timeout = '2s'",
		"GRANT USAGE ON SCHEMA " + q + " TO " + q,
		"GRANT SELECT ON ALL TABLES IN SCHEMA " + q + " TO " + q,
	}
	for _, st := range stmts {
		if _, err := conn.Exec(ctx, st); err != nil {
			return err
		}
	}
	return nil
}

// dropSchema removes a demo schema and its query role.
func (s *Server) dropSchema(ctx context.Context, conn *pgx.Conn, schema string) {
	q := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+q+" CASCADE"); err != nil {
		s.log.Warn("drop schema", "schema", schema, "err", err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", schema).Scan(&exists); err == nil && exists {
		if _, err := conn.Exec(ctx, "DROP OWNED BY "+q+"; DROP ROLE "+q); err != nil {
			s.log.Warn("drop role", "role", schema, "err", err)
		}
	}
}

type queryRequest struct {
	Schema string `json:"schema"`
	SQL    string `json:"sql"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		writeError(w, http.StatusServiceUnavailable, "querying is disabled: this server has no Postgres DSN")
		return
	}
	var req queryRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "send JSON like {\"schema\": \"demo_ab12cd34\", \"sql\": \"SELECT ...\"}")
		return
	}
	if !schemaRe.MatchString(req.Schema) {
		writeError(w, http.StatusBadRequest, "schema must be a demo schema returned by /api/load")
		return
	}
	if len(req.SQL) > 10_000 {
		writeError(w, http.StatusBadRequest, "the query is too long")
		return
	}
	stmt, keyword, err := checkQuery(req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if keyword != "explain" {
		// Let the database stop early instead of streaming a huge result.
		stmt = fmt.Sprintf("SELECT * FROM (\n%s\n) AS kempt_q LIMIT %d", stmt, queryRows+1)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cfg := s.pool.Config().ConnConfig.Copy()
	cfg.User, cfg.Password = req.Schema, s.rolePassword(req.Schema)
	cfg.RuntimeParams["application_name"] = "kempt-query"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "28P01" || pe.Code == "28000") || strings.Contains(err.Error(), "password authentication failed") {
			writeError(w, http.StatusNotFound, "that demo schema does not exist or has expired; load the file again")
			return
		}
		s.log.Error("query connect", "err", err)
		writeError(w, http.StatusServiceUnavailable, "the database is unavailable")
		return
	}
	defer conn.Close(context.Background())

	start := time.Now()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not start a transaction: "+pgMessage(err))
		return
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '2s'; SET LOCAL search_path TO "+pgx.Identifier{req.Schema}.Sanitize()); err != nil {
		writeError(w, http.StatusServiceUnavailable, pgMessage(err))
		return
	}
	rows, err := tx.Query(ctx, stmt, pgx.QueryExecModeExec)
	if err != nil {
		writeError(w, http.StatusBadRequest, pgMessage(err))
		return
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	out := [][]*string{}
	truncated := false
	for rows.Next() {
		if len(out) == queryRows {
			truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			writeError(w, http.StatusBadRequest, pgMessage(err))
			return
		}
		row := make([]*string, len(vals))
		for i, v := range vals {
			row[i] = formatPG(v, fds[i].DataTypeOID)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil && !truncated {
		writeError(w, http.StatusBadRequest, pgMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns":   cols,
		"rows":      out,
		"rowCount":  len(out),
		"truncated": truncated,
		"elapsedMs": time.Since(start).Milliseconds(),
	})
}

// formatPG renders a query result value as display text (nil for NULL).
func formatPG(v any, oid uint32) *string {
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		s = x
	case time.Time:
		switch oid {
		case pgtype.DateOID:
			s = x.Format("2006-01-02")
		case pgtype.TimestampOID:
			s = x.Format("2006-01-02 15:04:05.999999")
		default:
			s = x.Format(time.RFC3339Nano)
		}
	case pgtype.Numeric:
		if b, err := x.MarshalJSON(); err == nil {
			s = strings.Trim(string(b), `"`)
		}
	case []byte:
		if utf8.Valid(x) {
			s = string(x)
		} else {
			s = `\x` + hex.EncodeToString(x)
		}
	case fmt.Stringer:
		s = x.String()
	default:
		if b, err := json.Marshal(x); err == nil {
			s = string(b)
		} else {
			s = fmt.Sprint(x)
		}
	}
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return &s
}

// pgMessage extracts a user-facing message from a Postgres error.
func pgMessage(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := pe.Message
		if pe.Code == "57014" {
			msg = "the query took longer than 2 seconds and was cancelled"
		}
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the request timed out"
	}
	return err.Error()
}

// janitor drops demo schemas (and their roles) older than schemaTTL.
func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(janitorEvery)
	defer t.Stop()
	for {
		if n, err := s.sweep(ctx, time.Now().Add(-schemaTTL)); err != nil {
			s.log.Warn("janitor", "err", err)
		} else if n > 0 {
			s.log.Info("janitor dropped expired demo schemas", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweep drops demo schemas created before cutoff, plus demo roles whose
// schema no longer exists. It returns how many schemas it dropped.
func (s *Server) sweep(ctx context.Context, cutoff time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, `
		SELECT n.nspname, coalesce(obj_description(n.oid, 'pg_namespace'), '')
		FROM pg_namespace n WHERE n.nspname ~ '^demo_[a-z0-9]{8}$'
		UNION ALL
		SELECT r.rolname, 'orphan role'
		FROM pg_roles r
		WHERE r.rolname ~ '^demo_[a-z0-9]{8}$'
		  AND NOT EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = r.rolname)`)
	if err != nil {
		return 0, err
	}
	type victim struct{ name, note string }
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.name, &v.note); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, v := range victims {
		if v.note != "orphan role" {
			created, err := time.Parse(time.RFC3339, strings.TrimPrefix(v.note, schemaComment))
			if err != nil || !strings.HasPrefix(v.note, schemaComment) || created.After(cutoff) {
				continue // not ours, or still fresh
			}
			n++
		}
		s.dropSchema(ctx, conn.Conn(), v.name)
	}
	return n, nil
}
