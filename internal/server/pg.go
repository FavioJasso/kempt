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
// (the Railway DATABASE_URL user is a superuser). The role's password is
// derived from the server secret and a random token that only the visitor
// who loaded the schema receives: schema names are visible to every role in
// pg_namespace, so they cannot be the credential.
var (
	schemaRe = regexp.MustCompile(`^demo_[a-z0-9]{8}$`)
	tokenRe  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

const (
	schemaComment = "kempt demo schema; created "
	// roleComment marks the roles kempt created, so the janitor never
	// touches anyone else's role that happens to look like demo_xxxxxxxx.
	roleComment = "kempt demo role; created "
)

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

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// rolePassword derives the query role's password from the server secret and
// the schema's token.
func (s *Server) rolePassword(schema, token string) string {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write([]byte(schema + "\x00" + token))
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
	sheets, err := s.analyze(r.Context(), up)
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
			// Demo comments would show other visitors the original headers.
			n, err := emit.LoadPostgres(ctx, conn.Conn(), t, emit.LoadOptions{Schema: schema, IfExists: "fail", NoComments: s.cfg.Demo})
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
	token, queryErr := newToken()
	if queryErr == nil {
		queryErr = s.createQueryRole(ctx, conn.Conn(), schema, token)
	}
	if queryErr != nil {
		s.log.Warn("query role unavailable; /api/query disabled for this schema", "schema", schema, "err", queryErr)
	}
	resp := map[string]any{
		"schema":       schema,
		"table":        first,
		"tables":       tables,
		"queryEnabled": queryErr == nil,
		"expiresAt":    time.Now().UTC().Add(schemaTTL).Format(time.RFC3339),
	}
	if queryErr == nil {
		resp["token"] = token
	}
	writeJSON(w, http.StatusOK, resp)
}

// createQueryRole creates the read-only LOGIN role for a demo schema, in
// one transaction so a failure leaves no unmarked role behind.
func (s *Server) createQueryRole(ctx context.Context, conn *pgx.Conn, schema, token string) error {
	q := pgx.Identifier{schema}.Sanitize()
	now := time.Now().UTC()
	until := now.Add(schemaTTL + time.Hour).Format("2006-01-02 15:04:05+00")
	stmts := []string{
		"CREATE ROLE " + q + " LOGIN PASSWORD '" + s.rolePassword(schema, token) + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION CONNECTION LIMIT 5 VALID UNTIL '" + until + "'",
		"COMMENT ON ROLE " + q + " IS '" + roleComment + now.Format(time.RFC3339) + "'",
		"ALTER ROLE " + q + " SET default_transaction_read_only = on",
		"ALTER ROLE " + q + " SET statement_timeout = '2s'",
		"GRANT USAGE ON SCHEMA " + q + " TO " + q,
		"GRANT SELECT ON ALL TABLES IN SCHEMA " + q + " TO " + q,
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	for _, st := range stmts {
		if _, err := tx.Exec(ctx, st); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// dropSchema removes a demo schema and, when kempt created it, its query role.
func (s *Server) dropSchema(ctx context.Context, conn *pgx.Conn, schema string) {
	q := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+q+" CASCADE"); err != nil {
		s.log.Warn("drop schema", "schema", schema, "err", err)
	}
	var ours bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles
		WHERE rolname = $1 AND starts_with(coalesce(shobj_description(oid, 'pg_authid'), ''), $2))`, schema, roleComment).Scan(&ours)
	if err == nil && ours {
		if _, err := conn.Exec(ctx, "DROP OWNED BY "+q+"; DROP ROLE "+q); err != nil {
			s.log.Warn("drop role", "role", schema, "err", err)
		}
	}
}

type queryRequest struct {
	Schema string `json:"schema"`
	Token  string `json:"token"`
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
		writeError(w, http.StatusBadRequest, `send JSON like {"schema": "demo_ab12cd34", "token": "...", "sql": "SELECT ..."} with the schema and token /api/load returned`)
		return
	}
	if !schemaRe.MatchString(req.Schema) {
		writeError(w, http.StatusBadRequest, "schema must be a demo schema returned by /api/load")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "send the token /api/load returned with the schema")
		return
	}
	if !tokenRe.MatchString(req.Token) {
		writeError(w, http.StatusNotFound, notFoundMsg) // the same answer as a wrong token
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	release, err := acquire(ctx, s.queries)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer release()
	cfg := s.pool.Config().ConnConfig.Copy()
	cfg.User, cfg.Password = req.Schema, s.rolePassword(req.Schema, req.Token)
	cfg.RuntimeParams["application_name"] = "kempt-query"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "28P01" || pe.Code == "28000") || strings.Contains(err.Error(), "password authentication failed") {
			writeError(w, http.StatusNotFound, notFoundMsg)
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
	var cols []string
	limit := maxCell
	if keyword != "explain" {
		// Describe the statement, then let the database cut every value and
		// stop after queryRows+1 rows, so a huge result never reaches the
		// server: SELECT repeat('x', 100000000) costs 2 KB, not 100 MB.
		sd, err := tx.Conn().PgConn().Prepare(ctx, "", stmt, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, pgMessage(err))
			return
		}
		cols = make([]string, len(sd.Fields))
		for i, fd := range sd.Fields {
			cols[i] = fd.Name
		}
		limit = cellLimit(len(cols))
		stmt = wrapQuery(stmt, len(cols), limit)
	}
	rows, err := tx.Query(ctx, stmt, pgx.QueryExecModeExec)
	if err != nil {
		writeError(w, http.StatusBadRequest, pgMessage(err))
		return
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	if cols == nil {
		cols = make([]string, len(fds))
		for i, fd := range fds {
			cols[i] = fd.Name
		}
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
			row[i] = formatPG(v, fds[i].DataTypeOID, limit)
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

const (
	notFoundMsg = "that demo schema does not exist, has expired, or the token is wrong; load the file again"
	// maxCell is the most characters a result value shows.
	maxCell = 2000
	// rowBudget caps the characters of one result row, so a query with
	// hundreds of wide columns stays small too.
	rowBudget = 100_000
)

// cellLimit is the characters each of n columns may show.
func cellLimit(n int) int {
	if n == 0 {
		return maxCell
	}
	return max(50, min(maxCell, rowBudget/n))
}

// wrapQuery cuts every column of stmt to limit+1 characters (one more than
// shown, so formatPG knows to add an ellipsis) and stops after queryRows+1
// rows. The columns are renamed c1..cn inside the wrapper, because the
// statement's own names may repeat ("?column?") or need quoting.
func wrapQuery(stmt string, n, limit int) string {
	if n == 0 { // SELECT FROM t: rows without columns
		return fmt.Sprintf("SELECT FROM (\n%s\n) AS kempt_q LIMIT %d", stmt, queryRows+1)
	}
	exprs := make([]string, n)
	names := make([]string, n)
	for i := range exprs {
		names[i] = fmt.Sprintf("c%d", i+1)
		exprs[i] = fmt.Sprintf("left(kempt_q.c%d::text, %d)", i+1, limit+1)
	}
	return fmt.Sprintf("SELECT %s FROM (\n%s\n) AS kempt_q(%s) LIMIT %d",
		strings.Join(exprs, ", "), stmt, strings.Join(names, ", "), queryRows+1)
}

// formatPG renders a query result value as display text (nil for NULL),
// cut to limit characters.
func formatPG(v any, oid uint32, limit int) *string {
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
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
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
		  AND starts_with(coalesce(shobj_description(r.oid, 'pg_authid'), ''), $1)
		  AND NOT EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = r.rolname)`, roleComment)
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
