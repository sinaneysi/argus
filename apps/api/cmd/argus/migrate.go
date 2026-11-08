package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/sinaneysi/argus/internal/migrate"
)

const migrateHelp = `usage: argus migrate [-file argus.env] [-status] [-json]

Applies Argus's schema migrations to the evidence (ARGUS_DB), board (ARGUS_BOARD_DB) and auth (ARGUS_AUTH_DB)
databases, creating them when missing (ADR-0021 D2). Idempotent; refuses a database newer than this binary and a
migration that changed after it was applied. An existing database is adopted: tables and indexes that exist are kept
and missing columns are added. A read-only database is skipped. argus-api and argus worker run the same step at start
unless ARGUS_MIGRATE=off.

-status prints each database's version and its pending migrations and changes nothing.

flags:
`

func runMigrate(args []string, stdout, stderr io.Writer) int {
	if isHelp(args) {
		fmt.Fprint(stdout, migrateHelp)
		return 0
	}
	fs := flag.NewFlagSet("argus migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file")
	status := fs.Bool("status", false, "print versions and pending migrations; change nothing")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Usage = func() { fmt.Fprint(stderr, migrateHelp); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	c, ok := loadForChecks(*file, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	type row struct {
		DB      string   `json:"db"`
		Path    string   `json:"path"`
		Version int      `json:"version"`
		Latest  int      `json:"latest"`
		Pending []string `json:"pending,omitempty"`
		Applied []string `json:"applied,omitempty"`
		Adopted []string `json:"adopted,omitempty"`
		Error   string   `json:"error,omitempty"`
	}
	var rows []row
	code := 0
	paths := migrate.Paths(c)
	for _, db := range migrate.DBs {
		r := row{DB: db, Path: paths[db], Latest: migrate.Latest(db)}
		if *status {
			cur, pend, err := migrate.StatusFile(ctx, r.Path, db)
			r.Version = cur
			for _, m := range pend {
				r.Pending = append(r.Pending, fmt.Sprintf("%04d_%s", m.Version, m.Name))
			}
			if err != nil {
				r.Error, code = err.Error(), 1
			}
		} else {
			res, err := migrate.ApplyFile(ctx, r.Path, db)
			r.Version, r.Applied, r.Adopted = res.To, res.Applied, res.Adopted
			if err != nil {
				r.Error, code = err.Error(), 1
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return code
	}
	for _, r := range rows {
		line := fmt.Sprintf("%-6s version %d of %d  %s", r.DB, r.Version, r.Latest, r.Path)
		switch {
		case r.Error != "":
			line += "  ERROR: " + r.Error
		case *status && len(r.Pending) > 0:
			line += fmt.Sprintf("  pending: %v", r.Pending)
		case len(r.Applied) > 0:
			line += fmt.Sprintf("  applied: %v, %d object(s) adopted", r.Applied, len(r.Adopted))
		default:
			line += "  up to date"
		}
		fmt.Fprintln(stdout, line)
	}
	return code
}
