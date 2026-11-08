// Package collect holds Argus's first Go collectors (ADR-0016 D1, roadmap P3/P4): the alerts snapshot, the
// certificate check and the backup check. Each one is read-only towards its targets and writes exactly what the web
// app reads, nowhere else:
//
//	alerts   ARGUS_ALERTS_FILE (alerts-firing.json), written atomically (temp file + rename in the same directory)
//	certs    ARGUS_DB: one "cert-check" run with its cert_groups and cert_results rows, in one transaction
//	backups  ARGUS_DB: one "backup-check" run with its backup_results rows, in one transaction
//
// The shapes are the contract of the existing pages (/alerts, /certs, /backups) and of the demo generator
// (internal/demo): a collector's output renders on the pages unchanged.
package collect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Result is what one collector run reports: a status for the log and the exit code.
type Result struct {
	Status  string // OK, WARN, FAIL (findings) or ERROR (the collector itself failed)
	Summary string
	Items   int // targets checked
}

// writeFileAtomic writes data to path through a temporary file in the same directory and a rename, so a reader
// never sees a partial file. The file mode is 0644 (the web app runs as another user).
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// schema: the tables the collectors write, exactly as in the evidence database (internal/migrate/migrations/ops).
// CREATE TABLE IF NOT EXISTS leaves an existing database untouched.
const schema = `
CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY, task TEXT NOT NULL, started_at INTEGER NOT NULL, duration_ms INTEGER, status TEXT,
  summary TEXT, cost_usd REAL, turns INTEGER, denials INTEGER, is_error INTEGER, mr_url TEXT, log_path TEXT);
CREATE TABLE IF NOT EXISTS cert_groups (
  run_id INTEGER NOT NULL, kind TEXT NOT NULL, gid TEXT, label TEXT, status TEXT, sev TEXT,
  days_left INTEGER, not_after TEXT, issuer TEXT, n_names INTEGER, names TEXT, sans TEXT,
  san_mismatch TEXT, note TEXT);
CREATE TABLE IF NOT EXISTS cert_results (
  run_id INTEGER NOT NULL REFERENCES runs(id), host TEXT, status TEXT, days_left INTEGER, not_after TEXT, issuer TEXT);
CREATE TABLE IF NOT EXISTS backup_results (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  name TEXT, status TEXT, newest TEXT, age_h INTEGER, size INTEGER, median_prev7 INTEGER, kinds INTEGER);
CREATE INDEX IF NOT EXISTS cert_groups_run ON cert_groups(run_id, kind);
`

// openDB opens the evidence database for writing (created when missing, with the collector tables).
func openDB(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database path must be absolute: %q", path)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

// insertRun adds one runs row inside tx and returns its id.
func insertRun(ctx context.Context, tx *sql.Tx, task string, started time.Time, status, summary string) (int64, error) {
	isErr := 0
	if status == "ERROR" {
		isErr = 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO runs (task, started_at, duration_ms, status, summary, is_error) VALUES (?,?,?,?,?,?)`,
		task, started.Unix(), time.Since(started).Milliseconds(), status, summary, isErr)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// rootPool is the system roots plus the PEM certificates of caFile (when set).
func rootPool(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile == "" {
		return pool, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no PEM certificate found", caFile)
	}
	return pool, nil
}

func tlsConfig(roots *x509.CertPool, verify bool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: !verify} //nolint:gosec // verify is on by default; off only by explicit configuration
}

// Logger is the structured log of the collectors (key=value lines on stderr by default).
var Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

var errDisabled = errors.New("disabled")

// IsDisabled reports whether err means "the collector's feature flag is off".
func IsDisabled(err error) bool { return errors.Is(err, errDisabled) }
