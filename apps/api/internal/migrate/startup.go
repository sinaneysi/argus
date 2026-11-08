package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/secret"
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
	if c.DemoMode {
		return nil
	}
	if err := EnsureDirs(c); err != nil {
		return err
	}
	// the master key of the volume's secrets: created at the first start, refused when secrets exist without it
	if _, from, err := secret.ForVolume(func(k string) string { return c.Values[k].Raw }, true); err != nil {
		return err
	} else if from != "" {
		logf("secrets: master key from %s", from)
	}
	if c.Values["ARGUS_MIGRATE"].Raw == "off" {
		return nil
	}
	_, err := All(ctx, c, logf)
	return err
}

// RequestDirs are the folders under ARGUS_SETTINGS_IN the API writes request files into.
var RequestDirs = []string{"claude-token", "kube", "sources", "source-actions", "jira-connect", "outline-requests",
	"gitlab-issues", "board-requests"}

// EnsureDirs creates the folders of the one-volume layout (ARGUS_LAYOUT=volume) that the API and the worker write
// into; nothing in the legacy layout, where the host owns the folders.
func EnsureDirs(c *config.Config) error {
	if c.Values["ARGUS_LAYOUT"].Raw != "volume" {
		return nil
	}
	v := func(k string) string { return c.Values[k].Raw }
	for _, d := range []string{c.StateDir, c.SettingsIn, c.SettingsOut, c.SecretIn, c.SecretsDir, c.EtcDir, c.ReportsDir,
		filepath.Dir(c.AuthDB), filepath.Dir(c.BoardDB), filepath.Dir(v("TOTP_KEY_FILE")), v("ARGUS_SOURCES_DIR")} {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("layout: %w", err)
		}
	}
	// the request folders the API drops one file into without creating the folder (the legacy host creates them)
	for _, d := range RequestDirs {
		if err := os.MkdirAll(filepath.Join(c.SettingsIn, d), 0o750); err != nil {
			return fmt.Errorf("layout: %w", err)
		}
	}
	return nil
}
