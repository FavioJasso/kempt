package emit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql" // registers "mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	_ "modernc.org/sqlite" // registers "sqlite"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// If-exists modes.
const (
	IfExistsFail    = "fail"
	IfExistsReplace = "replace"
	IfExistsAppend  = "append"
)

// Bound-parameter limits per statement.
const (
	mysqlMaxParams  = 65535
	sqliteMaxParams = 32766
)

// ErrTableExists is returned (wrapped) when the target table exists and the
// if-exists mode is fail.
var ErrTableExists = errors.New("table already exists")

// LoadOptions control Load.
type LoadOptions struct {
	IfExists  string // fail (default) | replace | append
	Schema    string // Postgres schema (created if missing), MySQL database, or SQLite attached database
	BatchRows int    // rows per INSERT for MySQL and SQLite; 0 = 500
}

// LoadResult reports one loaded table.
type LoadResult struct {
	Table string // schema.table, unquoted
	Rows  int64
}

// Load creates and fills one table per plan. connString is a Postgres URL or
// keyword/value string, a go-sql-driver MySQL DSN, or a SQLite path.
// Postgres and SQLite load every plan in one transaction, so an error leaves
// the database unchanged. MySQL commits DDL implicitly, so it loads plan by
// plan; on error the results list the tables already loaded.
func Load(ctx context.Context, dialect, connString string, plans []*plan.Plan, opts LoadOptions) ([]LoadResult, error) {
	d, ok := dialectOf(dialect)
	if !ok {
		return nil, fmt.Errorf("emit: unsupported dialect %q (want postgres, mysql, or sqlite)", dialect)
	}
	if _, err := ifExistsMode(opts.IfExists); err != nil {
		return nil, err
	}
	results := make([]LoadResult, 0, len(plans))
	switch d {
	case infer.Postgres:
		conn, err := pgx.Connect(ctx, connString)
		if err != nil {
			return nil, fmt.Errorf("emit: connect to postgres: %w", err)
		}
		defer conn.Close(context.WithoutCancel(ctx))
		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		for _, p := range plans {
			n, err := loadPostgres(ctx, tx, p, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, LoadResult{Table: resultName(opts.Schema, p.Table), Rows: n})
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
	case infer.SQLite:
		db, err := sql.Open("sqlite", connString)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("emit: open sqlite %s: %w", connString, err)
		}
		defer tx.Rollback()
		for _, p := range plans {
			n, err := loadSQLite(ctx, tx, p, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, LoadResult{Table: resultName(opts.Schema, p.Table), Rows: n})
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	case infer.MySQL:
		db, err := sql.Open("mysql", connString)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		for _, p := range plans {
			n, err := loadMySQL(ctx, db, p, opts)
			if err != nil {
				return results, err
			}
			results = append(results, LoadResult{Table: resultName(opts.Schema, p.Table), Rows: n})
		}
	}
	return results, nil
}

// LoadPostgres creates and fills p's table in its own transaction on conn,
// using COPY for the rows. It returns the number of rows copied.
func LoadPostgres(ctx context.Context, conn *pgx.Conn, p *plan.Plan, opts LoadOptions) (int64, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	n, err := loadPostgres(ctx, tx, p, opts)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// LoadSQL creates and fills p's table in a MySQL or SQLite database with
// batched multi-row INSERTs. SQLite runs in one transaction; MySQL runs the
// DDL first (it commits implicitly) and the inserts in a transaction, and
// drops a table it created if the inserts fail.
func LoadSQL(ctx context.Context, db *sql.DB, dialect string, p *plan.Plan, opts LoadOptions) (int64, error) {
	d, _ := dialectOf(dialect)
	switch d {
	case infer.SQLite:
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		n, err := loadSQLite(ctx, tx, p, opts)
		if err != nil {
			return 0, err
		}
		return n, tx.Commit()
	case infer.MySQL:
		return loadMySQL(ctx, db, p, opts)
	}
	return 0, fmt.Errorf("emit: LoadSQL supports mysql and sqlite, not %q", dialect)
}

func loadPostgres(ctx context.Context, tx pgx.Tx, p *plan.Plan, opts LoadOptions) (int64, error) {
	if err := checkPlan(p); err != nil {
		return 0, err
	}
	d := infer.Postgres
	if opts.Schema != "" {
		// CREATE SCHEMA checks privileges before IF NOT EXISTS, so only
		// issue it when the schema is actually missing.
		var have bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", opts.Schema).Scan(&have); err != nil {
			return 0, fmt.Errorf("emit: check schema %s: %w", opts.Schema, err)
		}
		if !have {
			if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+QuoteIdent(d, opts.Schema)); err != nil {
				return 0, fmt.Errorf("emit: create schema %s: %w", opts.Schema, err)
			}
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", QualifiedTable(d, opts.Schema, p.Table)).Scan(&exists); err != nil {
		return 0, fmt.Errorf("emit: check table %s: %w", resultName(opts.Schema, p.Table), err)
	}
	if err := prepareTable(ctx, d, exists, p, opts, func(q string) error {
		_, err := tx.Exec(ctx, q)
		return err
	}); err != nil {
		return 0, err
	}
	ident := pgx.Identifier{p.Table}
	if opts.Schema != "" {
		ident = pgx.Identifier{opts.Schema, p.Table}
	}
	cols := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = c.Name
	}
	n, err := tx.CopyFrom(ctx, ident, cols, pgx.CopyFromSlice(len(p.Rows), func(i int) ([]any, error) {
		vals := make([]any, len(cols))
		for j := range cols {
			v, err := pgValue(cell(p.Rows[i], j))
			if err != nil {
				return nil, fmt.Errorf("row %d column %s: %w", i+1, cols[j], err)
			}
			vals[j] = v
		}
		return vals, nil
	}))
	if err != nil {
		return 0, fmt.Errorf("emit: copy into %s: %w", resultName(opts.Schema, p.Table), err)
	}
	return n, nil
}

// pgValue converts a clean value for pgx's binary COPY.
func pgValue(v any) (any, error) {
	switch x := v.(type) {
	case normalize.Decimal:
		var n pgtype.Numeric
		if err := n.Scan(string(x)); err != nil {
			return nil, err
		}
		return n, nil
	case normalize.Date:
		return pgtype.Date{Time: x.Time, Valid: true}, nil
	case normalize.Timestamp:
		return pgtype.Timestamp{Time: x.Time, Valid: true}, nil
	}
	return v, nil
}

// sqlDB is the subset of *sql.DB and *sql.Tx the SQL loaders use.
type sqlDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

func loadSQLite(ctx context.Context, tx *sql.Tx, p *plan.Plan, opts LoadOptions) (int64, error) {
	if err := checkPlan(p); err != nil {
		return 0, err
	}
	d := infer.SQLite
	master := "sqlite_master"
	if opts.Schema != "" {
		master = QuoteIdent(d, opts.Schema) + ".sqlite_master"
	}
	var count int
	q := "SELECT count(*) FROM " + master + " WHERE type IN ('table', 'view') AND name = ? COLLATE NOCASE"
	if err := tx.QueryRowContext(ctx, q, p.Table).Scan(&count); err != nil {
		return 0, fmt.Errorf("emit: check table %s: %w", resultName(opts.Schema, p.Table), err)
	}
	if err := prepareTable(ctx, d, count > 0, p, opts, func(q string) error {
		_, err := tx.ExecContext(ctx, q)
		return err
	}); err != nil {
		return 0, err
	}
	return insertRows(ctx, tx, d, p, opts)
}

func loadMySQL(ctx context.Context, db *sql.DB, p *plan.Plan, opts LoadOptions) (n int64, err error) {
	if err := checkPlan(p); err != nil {
		return 0, err
	}
	d := infer.MySQL
	var count int
	q := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE()) AND table_name = ?"
	if err := db.QueryRowContext(ctx, q, opts.Schema, p.Table).Scan(&count); err != nil {
		return 0, fmt.Errorf("emit: check table %s: %w", resultName(opts.Schema, p.Table), err)
	}
	exists := count > 0
	if err := prepareTable(ctx, d, exists, p, opts, func(q string) error {
		_, err := db.ExecContext(ctx, q)
		return err
	}); err != nil {
		return 0, err
	}
	if mode, _ := ifExistsMode(opts.IfExists); !exists || mode == IfExistsReplace {
		// The table is ours: do not leave it behind empty if the inserts fail.
		defer func() {
			if err != nil {
				_, _ = db.ExecContext(context.WithoutCancel(ctx), "DROP TABLE IF EXISTS "+QualifiedTable(d, opts.Schema, p.Table))
			}
		}()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if n, err = insertRows(ctx, tx, d, p, opts); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// prepareTable applies the if-exists mode: it fails, drops, or keeps an
// existing table, and runs the DDL unless appending to an existing table.
func prepareTable(ctx context.Context, d string, exists bool, p *plan.Plan, opts LoadOptions, exec func(string) error) error {
	mode, err := ifExistsMode(opts.IfExists)
	if err != nil {
		return err
	}
	name := resultName(opts.Schema, p.Table)
	if exists {
		switch mode {
		case IfExistsFail:
			return fmt.Errorf("emit: %w: %s (use if-exists replace or append)", ErrTableExists, name)
		case IfExistsAppend:
			return nil
		case IfExistsReplace:
			if err := exec("DROP TABLE IF EXISTS " + QualifiedTable(d, opts.Schema, p.Table)); err != nil {
				return fmt.Errorf("emit: drop %s: %w", name, err)
			}
		}
	}
	ddl := DDL(p, d, DDLOptions{Schema: opts.Schema, IfNotExists: mode == IfExistsAppend})
	if err := exec(ddl); err != nil {
		return fmt.Errorf("emit: create %s: %w", name, err)
	}
	return nil
}

// insertRows runs batched multi-row INSERTs with bound parameters.
func insertRows(ctx context.Context, db sqlDB, d string, p *plan.Plan, opts LoadOptions) (int64, error) {
	cols := len(p.Columns)
	if len(p.Rows) == 0 {
		return 0, nil
	}
	limit := sqliteMaxParams
	if d == infer.MySQL {
		limit = mysqlMaxParams
	}
	batch := opts.BatchRows
	if batch <= 0 {
		batch = defaultBatchRows
	}
	batch = max(1, min(batch, limit/cols))

	head := insertHead(p, d, opts.Schema) + " "
	tuple := "(" + strings.TrimSuffix(strings.Repeat("?, ", cols), ", ") + ")"
	stmtFor := func(rows int) string {
		return head + strings.TrimSuffix(strings.Repeat(tuple+", ", rows), ", ")
	}
	var full *sql.Stmt
	if len(p.Rows) >= batch {
		s, err := db.PrepareContext(ctx, stmtFor(batch))
		if err != nil {
			return 0, fmt.Errorf("emit: prepare insert into %s: %w", resultName(opts.Schema, p.Table), err)
		}
		defer s.Close()
		full = s
	}
	args := make([]any, 0, batch*cols)
	var total int64
	for start := 0; start < len(p.Rows); start += batch {
		rows := p.Rows[start:min(start+batch, len(p.Rows))]
		args = args[:0]
		for _, row := range rows {
			for j := range cols {
				args = append(args, sqlValue(cell(row, j)))
			}
		}
		var err error
		if len(rows) == batch {
			_, err = full.ExecContext(ctx, args...)
		} else {
			_, err = db.ExecContext(ctx, stmtFor(len(rows)), args...)
		}
		if err != nil {
			return 0, fmt.Errorf("emit: insert into %s (rows %d-%d): %w",
				resultName(opts.Schema, p.Table), start+1, start+len(rows), err)
		}
		total += int64(len(rows))
	}
	return total, nil
}

// sqlValue converts a clean value for database/sql: bool as 1/0, decimals
// as text, dates and timestamps as ISO text.
func sqlValue(v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case normalize.Decimal:
		return string(x)
	case normalize.Date:
		return x.String()
	case normalize.Timestamp:
		return x.String()
	}
	return v
}

// ifExistsMode validates and normalizes an if-exists mode.
func ifExistsMode(s string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(s)); m {
	case "":
		return IfExistsFail, nil
	case IfExistsFail, IfExistsReplace, IfExistsAppend:
		return m, nil
	}
	return "", fmt.Errorf("emit: if-exists must be fail, replace, or append, got %q", s)
}

func checkPlan(p *plan.Plan) error {
	switch {
	case p == nil:
		return errors.New("emit: nil plan")
	case p.Table == "":
		return errors.New("emit: plan has no table name")
	case len(p.Columns) == 0:
		return fmt.Errorf("emit: table %s has no columns", p.Table)
	}
	return nil
}

func resultName(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}
