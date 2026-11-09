package today

import (
	"context"
	"database/sql"
)

// DB is a *sql.DB or a *sql.Tx.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Row is one result row: the columns in order and their values (TEXT as string, INTEGER as int64, REAL as float64).
type Row struct {
	C []string
	V []any
}

// Get is the value of column k (nil when the column is absent).
func (r Row) Get(k string) any {
	for i, c := range r.C {
		if c == k {
			return r.V[i]
		}
	}
	return nil
}

// At is the value of column i.
func (r Row) At(i int) any { return r.V[i] }

func query(ctx context.Context, d DB, q string, args ...any) ([]Row, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if err := rows.Scan(p...); err != nil {
			return nil, err
		}
		for i, x := range v {
			switch y := x.(type) {
			case []byte:
				v[i] = string(y)
			case int:
				v[i] = int64(y)
			case bool:
				if y {
					v[i] = int64(1)
				} else {
					v[i] = int64(0)
				}
			}
		}
		out = append(out, Row{C: cols, V: v})
	}
	return out, rows.Err()
}

// soft is a source query: a missing table or any other error is no rows (one broken source never blocks the plan).
func soft(ctx context.Context, d DB, q string, args ...any) []Row {
	r, err := query(ctx, d, q, args...)
	if err != nil {
		return nil
	}
	return r
}

func one1(ctx context.Context, d DB, q string, args ...any) (*Row, error) {
	r, err := query(ctx, d, q, args...)
	if err != nil || len(r) == 0 {
		return nil, err
	}
	return &r[0], nil
}
