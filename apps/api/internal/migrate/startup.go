package migrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/sinaneysi/argus/internal/config"
)

// Paths is the file of each database in a configuration.
func Paths(c *config.Config) map[string]string {
	return map[string]string{Ops: c.DB, Board: c.BoardDB, Auth: c.AuthDB}
}

// All applies the migrations of every database of c. A read-only database is reported and skipped; any other
// problem (a downgrade, a changed migration, a failed statement) stops at that database.
func All(ctx context.Context, c *config.Config, logf func(string, ...any)) ([]Result, error) {
	paths := Paths(c)
	var out []Result
	for _, db := range DBs {
		p := paths[db]
		if p == "" {
			continue
		}
		r, err := ApplyFile(ctx, p, db)
		switch {
		case errors.Is(err, ErrReadOnly):
			logf("migrate: %s (%s) is read-only: not migrated", db, p)
			continue
		case err != nil:
			return out, fmt.Errorf("migrate %s (%s): %w", db, p, err)
		}
		out = append(out, r)
		if len(r.Applied) > 0 {
			logf("migrate: %s %d -> %d (%d object(s) adopted)", db, r.From, r.To, len(r.Adopted))
		}
	}
	return out, nil
}

// OnStart is the step argus-api and argus worker run before they open a database: nothing in demo mode (the
// generator owns the data) or with ARGUS_MIGRATE=off, else All.
func OnStart(ctx context.Context, c *config.Config, logf func(string, ...any)) error {
	if c.DemoMode || c.Values["ARGUS_MIGRATE"].Raw == "off" {
		return nil
	}
	_, err := All(ctx, c, logf)
	return err
}
