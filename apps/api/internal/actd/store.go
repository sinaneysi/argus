package actd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/capability"

	_ "modernc.org/sqlite"
)

// StepResult is the stored, redacted outcome of one finished step (D3 idempotency): a repeated request for that
// step returns it and does not call the target again.
type StepResult struct {
	N          int                 `json:"n"`
	Capability string              `json:"capability"`
	Phase      string              `json:"phase"`            // the last phase that ran
	Status     string              `json:"status"`           // ok | mismatch | unchanged | failed | refused | planned
	Change     string              `json:"change,omitempty"` // the plan's change
	Summary    string              `json:"summary,omitempty"`
	Detail     string              `json:"detail,omitempty"`
	Fields     map[string]any      `json:"fields,omitempty"`
	BackupRef  string              `json:"backup_ref,omitempty"`
	Audit      string              `json:"audit_sha256,omitempty"` // the sha256 of the step's last audit record
	Refusal    *capability.Refusal `json:"refusal,omitempty"`
	Stored     bool                `json:"stored,omitempty"` // returned from an earlier run of the same action
	At         int64               `json:"at"`
}

// Store keeps the step results under argus-act/steps/<action-id>/ (0700, files 0600).
type Store struct{ Dir string }

func (s Store) actionDir(id string) string { return filepath.Join(s.Dir, id) }

// Started reports whether the action was started before.
func (s Store) Started(id string) bool {
	_, err := os.Stat(filepath.Join(s.actionDir(id), "started"))
	return err == nil
}

// MarkStarted records the action's start (it counts towards ARGUS_ACTIONS_PER_DAY).
func (s Store) MarkStarted(id string, now time.Time) error {
	if err := os.MkdirAll(s.actionDir(id), 0o700); err != nil {
		return err
	}
	return writeFile(filepath.Join(s.actionDir(id), "started"), []byte(strconv.FormatInt(now.Unix(), 10)), 0o600)
}

// StartedSince counts the actions started at or after t.
func (s Store) StartedSince(t time.Time) int {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() || !capability.ValidActionID(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, e.Name(), "started"))
		if err != nil {
			continue
		}
		if ts, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && ts >= t.Unix() {
			n++
		}
	}
	return n
}

// Get is a finished step's stored result.
func (s Store) Get(id string, n int) (StepResult, bool) {
	b, err := os.ReadFile(filepath.Join(s.actionDir(id), fmt.Sprintf("step-%d.json", n)))
	if err != nil {
		return StepResult{}, false
	}
	var r StepResult
	if json.Unmarshal(b, &r) != nil || r.N != n {
		return StepResult{}, false
	}
	return r, true
}

// Put stores a finished step's result.
func (s Store) Put(id string, r StepResult) error {
	if err := os.MkdirAll(s.actionDir(id), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.actionDir(id), fmt.Sprintf("step-%d.json", r.N)), b, 0o600)
}

// writeFile writes atomically: a temporary file in the same folder, fsync, rename.
func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Ops writes the actions and action_events rows of ops.db (for the UI; the audit file stays authoritative). A
// failed write is logged by the caller, never fatal.
type Ops struct{ Path string }

var errNoTables = errors.New("ops.db has no actions tables yet (run argus migrate)")

func (o Ops) open() (*sql.DB, error) {
	if o.Path == "" {
		return nil, errors.New("no ops database")
	}
	if _, err := os.Stat(o.Path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+o.Path+"?_pragma=busy_timeout(10000)&_txlock=immediate")
}

// Action is one actions row.
type Action struct {
	ID, TaskKey, PlanSHA256, Approver, Mode, State, Reason string
	PlanVersion, Steps                                     int
	Started, Finished                                      int64
}

// Write stores the action row and its events in one transaction.
func (o Ops) Write(ctx context.Context, a Action, events []Record) error {
	db, err := o.open()
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('actions','action_events')`).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return errNoTables
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO actions (action_id, task_key, plan_version, plan_sha256, approver, mode, state, reason, steps, started, finished)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(action_id) DO UPDATE SET mode=excluded.mode, state=excluded.state, reason=excluded.reason,
		steps=excluded.steps, finished=excluded.finished`,
		a.ID, a.TaskKey, a.PlanVersion, a.PlanSHA256, a.Approver, a.Mode, a.State, a.Reason, a.Steps, a.Started, a.Finished); err != nil {
		return err
	}
	for _, e := range events {
		args, _ := json.Marshal(e.Args)
		ts, _ := time.Parse(time.RFC3339Nano, e.TS)
		if _, err := tx.ExecContext(ctx, `INSERT INTO action_events (action_id, step, capability, phase, result, detail, args, audit_sha256, at)
			VALUES (?,?,?,?,?,?,?,?,?)`, e.ActionID, e.Step, e.Capability, e.Phase, e.Result, e.Detail, string(args), e.SHA256, ts.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
