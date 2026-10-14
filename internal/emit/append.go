package emit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/plan"
)

// existingColumn is a column of a table kempt appends to.
type existingColumn struct {
	dataType         string
	precision, scale sql.NullInt64
}

// checkAppend refuses --if-exists append when the existing table cannot hold
// the new values without changing them: a missing column, decimals going
// into an integer column or a NUMERIC with fewer digits (both round
// silently), or timestamps going into a DATE column (drops the time).
func checkAppend(cols map[string]existingColumn, p *plan.Plan, name string) error {
	for _, c := range p.Columns {
		ec, ok := cols[strings.ToLower(c.Name)]
		if !ok {
			return fmt.Errorf("emit: append to %s: the table has no column %s; load into a new table or use --if-exists replace", name, c.Name)
		}
		dt := strings.ToLower(ec.dataType)
		bad := false
		switch c.Type.Base {
		case infer.Decimal:
			switch {
			case strings.Contains(dt, "int") || dt == "boolean" || dt == "bit":
				bad = true
			case dt == "numeric" || dt == "decimal":
				if ec.precision.Valid && ec.scale.Valid {
					bad = int(ec.scale.Int64) < c.Type.Scale || int(ec.precision.Int64-ec.scale.Int64) < c.Type.Precision-c.Type.Scale
				}
			}
		case infer.Int, infer.BigInt:
			if (dt == "numeric" || dt == "decimal") && ec.precision.Valid && ec.scale.Valid {
				bad = int(ec.precision.Int64-ec.scale.Int64) < 19 && c.Type.Base == infer.BigInt
			}
		case infer.Timestamp:
			bad = dt == "date"
		}
		if bad {
			return fmt.Errorf("emit: append to %s: column %s is %s but the new values need %s and would be changed; load into a new table or use --if-exists replace",
				name, c.Name, describeColumn(ec), c.Type)
		}
	}
	return nil
}

func describeColumn(ec existingColumn) string {
	if ec.precision.Valid && ec.scale.Valid && (ec.dataType == "numeric" || ec.dataType == "decimal") {
		return fmt.Sprintf("%s(%d,%d)", ec.dataType, ec.precision.Int64, ec.scale.Int64)
	}
	return ec.dataType
}

// pgColumns reads an existing Postgres table's columns.
func pgColumns(ctx context.Context, tx pgx.Tx, schema, table string) (map[string]existingColumn, error) {
	rows, err := tx.Query(ctx, `SELECT column_name, data_type, numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_schema = coalesce(nullif($1, ''), current_schema()) AND table_name = $2`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]existingColumn{}
	for rows.Next() {
		var name string
		var ec existingColumn
		var p, s *int64
		if err := rows.Scan(&name, &ec.dataType, &p, &s); err != nil {
			return nil, err
		}
		if p != nil {
			ec.precision = sql.NullInt64{Int64: *p, Valid: true}
		}
		if s != nil {
			ec.scale = sql.NullInt64{Int64: *s, Valid: true}
		}
		out[strings.ToLower(name)] = ec
	}
	return out, rows.Err()
}

// mysqlColumns reads an existing MySQL table's columns.
func mysqlColumns(ctx context.Context, q sqlDB, schema, table string) (map[string]existingColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT column_name, data_type, numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_schema = coalesce(nullif(?, ''), DATABASE()) AND table_name = ?`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]existingColumn{}
	for rows.Next() {
		var name string
		var ec existingColumn
		if err := rows.Scan(&name, &ec.dataType, &ec.precision, &ec.scale); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = ec
	}
	return out, rows.Err()
}
