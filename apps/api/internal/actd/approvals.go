package actd

// approvals.go: what actd itself remembers about approvals, in its own volume (argus-act/approvals and the action's
// folder under steps/). board.db is writable by the worker, so it cannot say whether an approval was used, stopped
// or continued; these records can. A start approval starts an action once: a later start needs a newer approval.
// An admin's signed stop revokes every approval of the task signed before it. Each operator step is done once per
// start, by a signed continue for exactly the first operator step not yet done.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/capability"
)

// taskRecord is argus-act/approvals/<task key>.json.
type taskRecord struct {
	StartAt       int64  `json:"start_at"`   // the approval time of the last accepted start
	RevokedAt     int64  `json:"revoked_at"` // the time of the last signed stop
	RevokedAction string `json:"revoked_action,omitempty"`
	RevokedBy     string `json:"revoked_by,omitempty"`
}

func (d *Daemon) recordPath(key string) string {
	return filepath.Join(d.o.ActDir, "approvals", key+".json")
}

func (d *Daemon) taskRecord(key string) (taskRecord, error) {
	var r taskRecord
	if d.o.ActDir == "" {
		return r, errors.New("no executor volume")
	}
	b, err := os.ReadFile(d.recordPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("the approval record of %s is not readable", key)
	}
	return r, nil
}

func (d *Daemon) putTaskRecord(key string, r taskRecord) error {
	if err := os.MkdirAll(filepath.Dir(d.recordPath(key)), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return writeFile(d.recordPath(key), b, 0o600)
}

// operatorRecord is steps/<action-id>/operator-<n>.json: the operator step done, confirmed by a signed continue.
type operatorRecord struct {
	StartAt    int64  `json:"start_at"` // the start approval it belongs to; another start must confirm it again
	ContinueAt int64  `json:"continue_at"`
	Approver   string `json:"approver"`
}

func (s Store) operatorPath(id string, n int) string {
	return filepath.Join(s.actionDir(id), fmt.Sprintf("operator-%d.json", n))
}

// OperatorDone reports whether operator step n was confirmed for the start approved at startAt.
func (s Store) OperatorDone(id string, n int, startAt int64) bool {
	b, err := os.ReadFile(s.operatorPath(id, n))
	if err != nil {
		return false
	}
	var r operatorRecord
	return json.Unmarshal(b, &r) == nil && r.StartAt == startAt
}

// MarkOperatorDone records a consumed continue.
func (s Store) MarkOperatorDone(id string, n int, r operatorRecord) error {
	if err := os.MkdirAll(s.actionDir(id), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return writeFile(s.operatorPath(id, n), b, 0o600)
}

// stop records an admin's signed stop of the task: every approval of the task signed at or before it is refused
// from now on, so the worker cannot run the stopped action again with the old approval.
func (d *Daemon) stop(ctx context.Context, req Request, t *task, c *confirmed) *Response {
	id, s := req.ActionID, req.Stop
	if r := d.checkSigned(ctx, capability.SignStop, t, req, s); r != nil {
		return d.refuseAll(c, id, r, d.audit != nil)
	}
	rec, err := d.taskRecord(t.key)
	if err != nil {
		return d.refuseAll(c, id, capability.Refuse(3, capability.CodeApproval, "%s", err.Error()), d.audit != nil)
	}
	if s.At > rec.RevokedAt {
		rec.RevokedAt, rec.RevokedAction, rec.RevokedBy = s.At, id, s.Approver
		if err := d.putTaskRecord(t.key, rec); err != nil {
			return d.refuseAll(c, id, capability.Refuse(3, capability.CodeApproval, "the stop could not be recorded: %v", err), d.audit != nil)
		}
	}
	now := d.now()
	resp := &Response{ActionID: id, TaskKey: c.TaskKey, Mode: string(d.o.Mode), State: StateStopped, Steps: []StepResult{}, Started: now.Unix()}
	var events []Record
	if d.audit != nil && d.audit.Err() == nil {
		r := Record{ActionID: id, TaskKey: c.TaskKey, Approver: s.Approver, PlanVersion: c.PlanVersion, PlanSHA256: c.PlanSHA256,
			Phase: PhaseStop, Result: StateStopped, Detail: "approvals signed before this stop are revoked"}
		if out, err := d.audit.Append(r, now); err == nil {
			events = append(events, out)
		}
		resp.AuditHead, _ = d.audit.Head()
	}
	d.finish(resp, c, events)
	d.log().Info("stop recorded", "action", id, "by", s.Approver)
	return resp
}
