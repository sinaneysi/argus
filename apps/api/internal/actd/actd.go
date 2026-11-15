// Package actd is the capability executor `argus actd` (ADR-0022 D1, D5-D8). It runs in its own service with its
// own volume (argus-act, mounted by no other service), reads action requests from actions/in in the shared volume,
// checks the fixed refusal rules before every step, runs each step's capability (Plan, then Apply and Verify, or
// Plan only in dry-run), appends one hash-chained audit record per phase and writes redacted results to
// actions/out. It confirms the task's classification and approval from board.db and auth.db, never from the
// request.
package actd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/capability"
)

// MaxRequestBytes is the largest request file actd reads.
const MaxRequestBytes = 64 << 10

// StoppedKey is the auth_settings key of the admin switch "Stop all actions" (D8 layer 2).
const StoppedKey = "actions.stopped"

// The states of an action.
const (
	StateDone     = "done"
	StateDryRun   = "dry-run"
	StateRefused  = "refused"
	StateFailed   = "failed"
	StateOperator = "operator" // an operator step stopped the run: "needs the operator"
)

// Request is actions/in/<action-id>.json, written by the worker (D5).
type Request struct {
	ActionID     string            `json:"action_id"`
	TaskKey      string            `json:"task_key"`
	PlanVersion  int               `json:"plan_version"`
	PlanSHA256   string            `json:"plan_sha256"`
	Approver     string            `json:"approver"`
	Steps        []ReqStep         `json:"steps"`
	SealedInputs map[string]string `json:"sealed_inputs,omitempty"` // opened by a later capability; ciphertext only
}

// ReqStep is one step of a request.
type ReqStep struct {
	N          int             `json:"n"`
	Mode       string          `json:"mode"`
	Capability string          `json:"capability,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// Response is actions/out/<action-id>.json: redacted results only.
type Response struct {
	ActionID  string              `json:"action_id"`
	TaskKey   string              `json:"task_key"`
	Mode      string              `json:"mode"`
	State     string              `json:"state"`
	Refusal   *capability.Refusal `json:"refusal,omitempty"`
	Steps     []StepResult        `json:"steps"`
	Started   int64               `json:"started"`
	Finished  int64               `json:"finished"`
	AuditHead string              `json:"audit_head,omitempty"`
}

// Options configure the executor.
type Options struct {
	Mode       capability.Mode
	ActDir     string // ARGUS_ACT_DIR: the executor's own volume
	ActionsDir string // <ARGUS_STATE_DIR>/actions: in/, out/, actd-status.json
	BoardDB    string // read only
	AuthDB     string // read only
	OpsDB      string // actions, action_events
	Registry   *capability.Registry
	TTL        time.Duration // ARGUS_ACTIONS_APPROVAL_TTL
	PerDay     int           // ARGUS_ACTIONS_PER_DAY
	Location   *time.Location
	Poll       time.Duration // how often actions/in is read (default 2 s)
	Now        func() time.Time
	Log        *slog.Logger
	Version    string
	// Credentials lists the stored credentials for the status file (names and last four characters only).
	Credentials func() []Credential
}

// Daemon is one executor.
type Daemon struct {
	o       Options
	audit   *Audit // nil: mode off without a usable executor volume
	store   Store
	ops     Ops
	last    *lastAction
	checked time.Time
}

type lastAction struct {
	ID    string `json:"action_id"`
	State string `json:"state"`
	At    int64  `json:"at"`
}

func (d *Daemon) now() time.Time {
	if d.o.Now != nil {
		return d.o.Now()
	}
	return time.Now()
}

func (d *Daemon) log() *slog.Logger {
	if d.o.Log != nil {
		return d.o.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// CheckActDir checks ARGUS_ACT_DIR: it exists, is a folder owned by this process's user and is closed to others.
func CheckActDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("ARGUS_ACT_DIR must be an absolute path")
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("ARGUS_ACT_DIR %s: %v", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ARGUS_ACT_DIR %s is not a folder", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("ARGUS_ACT_DIR %s is owned by uid %d, not by this process (uid %d)", dir, st.Uid, os.Geteuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ARGUS_ACT_DIR %s has mode %04o, must be 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// AuditPath is the authoritative audit file in the executor volume.
func AuditPath(actDir string) string { return filepath.Join(actDir, "audit", "actions.jsonl") }

// StatusPath is the status file in the shared volume.
func StatusPath(actionsDir string) string { return filepath.Join(actionsDir, "actd-status.json") }

// New prepares the folders and opens the audit chain. With a mode other than off, the executor volume must pass
// CheckActDir. A broken chain is not an error here: the daemon starts, refuses every step and reports the chain.
func New(o Options) (*Daemon, error) {
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	if o.ActionsDir == "" {
		return nil, errors.New("no actions folder")
	}
	d := &Daemon{o: o, ops: Ops{Path: o.OpsDB}}
	for _, sub := range []string{"in", "out"} {
		if err := os.MkdirAll(filepath.Join(o.ActionsDir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	actErr := CheckActDir(o.ActDir)
	if actErr != nil && o.Mode != capability.ModeOff {
		return nil, actErr
	}
	if actErr == nil {
		for _, sub := range []string{"audit", "steps", "keys", "secrets", "backups"} {
			if err := os.MkdirAll(filepath.Join(o.ActDir, sub), 0o700); err != nil {
				return nil, err
			}
		}
		d.store = Store{Dir: filepath.Join(o.ActDir, "steps")}
		a, err := OpenAudit(AuditPath(o.ActDir))
		if a == nil {
			return nil, err
		}
		d.audit = a
		if err == nil {
			if st, ok := ReadStatus(StatusPath(o.ActionsDir)); ok {
				a.Expect(st.Chain.Head, st.Chain.Records)
			}
		}
		if e := a.Err(); e != nil {
			d.log().Error("audit chain broken: every step is refused", "error", e.Error())
		}
		d.checked = d.now()
	}
	return d, nil
}

// ChainErr is the audit chain's state for the gate.
func (d *Daemon) chainErr() error {
	if d.audit == nil {
		return errors.New("the executor volume is not usable (ARGUS_ACT_DIR)")
	}
	return d.audit.Check()
}

// Run processes requests until ctx ends: one action at a time (an exclusive lock on the executor volume), the
// audit chain checked every hour, the heartbeat written every poll.
func (d *Daemon) Run(ctx context.Context) error {
	if d.audit != nil {
		f, err := os.OpenFile(filepath.Join(d.o.ActDir, "actd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return errors.New("another actd holds the executor volume: one action at a time")
		}
	}
	t := time.NewTicker(d.o.Poll)
	defer t.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick handles every waiting request once, checks the chain when an hour has passed and writes the heartbeat.
func (d *Daemon) Tick(ctx context.Context) {
	if d.audit != nil && d.now().Sub(d.checked) >= time.Hour {
		d.checked = d.now()
		if err := d.audit.Check(); err != nil {
			d.log().Error("audit chain broken: every step is refused", "error", err.Error())
		}
	}
	m, _ := filepath.Glob(filepath.Join(d.o.ActionsDir, "in", "*.json"))
	sort.Strings(m)
	for _, p := range m {
		if ctx.Err() != nil {
			break
		}
		d.ProcessFile(ctx, p)
	}
	if err := d.WriteStatus(); err != nil {
		d.log().Warn("status file not written", "error", err.Error())
	}
}

// ProcessFile answers one request file and removes it. The answer is actions/out/<action-id>.json.
func (d *Daemon) ProcessFile(ctx context.Context, path string) *Response {
	defer os.Remove(path)
	id := strings.TrimSuffix(filepath.Base(path), ".json")
	if !capability.ValidActionID(id) {
		d.log().Warn("request file with a bad name removed", "file", Clean(filepath.Base(path)))
		return nil
	}
	fi, err := os.Lstat(path)
	var resp *Response
	switch {
	case err != nil:
		return nil
	case !fi.Mode().IsRegular():
		resp = d.refuseAll(nil, id, capability.Refuse(4, capability.CodeArguments, "the request is not a regular file"))
	case fi.Size() > MaxRequestBytes:
		resp = d.refuseAll(nil, id, capability.Refuse(4, capability.CodeArguments, "the request is larger than %d bytes", MaxRequestBytes))
	default:
		b, err := os.ReadFile(path)
		var req Request
		if err == nil {
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			err = dec.Decode(&req)
		}
		if err != nil {
			resp = d.refuseAll(nil, id, capability.Refuse(4, capability.CodeArguments, "the request is not valid JSON of the request form"))
		} else if req.ActionID != id {
			resp = d.refuseAll(&req, id, capability.Refuse(3, capability.CodeApproval, "the file name is not the request's action id"))
		} else {
			resp = d.Process(ctx, req)
		}
	}
	if err := d.writeOut(resp); err != nil {
		d.log().Error("result not written", "action", resp.ActionID, "error", err.Error())
	}
	return resp
}

func (d *Daemon) writeOut(r *Response) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(d.o.ActionsDir, "out", r.ActionID+".json"), b, 0o600)
}

// planStep is one step of the stored plan.
type planStep struct {
	N          int
	Mode       string
	Capability string
	Args       capability.Args
}

// task is what board.db and auth.db say about the task.
type task struct {
	env      capability.Env
	approval capability.Approval
	steps    []planStep // the steps actd acts on: capability steps and operator steps, in order
	writeBad bool       // a WRITE step without a capability: for a person, never executed here
}

func openRO(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("no database path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
}

// loadTask reads the task's stored plan and approval (board.db) and the approver (auth.db).
func (d *Daemon) loadTask(ctx context.Context, key string) (*task, error) {
	db, err := openRO(d.o.BoardDB)
	if err != nil {
		return nil, fmt.Errorf("board database: %v", err)
	}
	defer db.Close()
	var hasCol int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agent_tasks') WHERE name='approved_sha256'`).Scan(&hasCol); err != nil {
		return nil, fmt.Errorf("board database: %v", err)
	}
	q := `SELECT COALESCE(plan,''), COALESCE(plan_version,0), COALESCE(approved_version,0), COALESCE(approved_by,''), COALESCE(approved_at,0), ''`
	if hasCol > 0 {
		q = `SELECT COALESCE(plan,''), COALESCE(plan_version,0), COALESCE(approved_version,0), COALESCE(approved_by,''), COALESCE(approved_at,0), COALESCE(approved_sha256,'')`
	}
	var plan, by, sha string
	var ver, aver int
	var at int64
	err = db.QueryRowContext(ctx, q+` FROM agent_tasks WHERE key=?`, key).Scan(&plan, &ver, &aver, &by, &at, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("the task is not in the board database")
	}
	if err != nil {
		return nil, fmt.Errorf("board database: %v", err)
	}
	var doc struct {
		Classification map[string]any   `json:"classification"`
		Plan           []map[string]any `json:"plan"`
	}
	if err := json.Unmarshal([]byte(plan), &doc); err != nil || doc.Classification == nil {
		return nil, errors.New("the stored plan is not readable")
	}
	var raw struct {
		Classification any `json:"classification"`
		Plan           any `json:"plan"`
	}
	json.Unmarshal([]byte(plan), &raw)
	stored, err := capability.PlanSHA256(raw.Classification, raw.Plan)
	if err != nil {
		return nil, err
	}
	env, _ := doc.Classification["environment"].(string)
	t := &task{env: capability.Env(env)}
	t.approval = capability.Approval{PlanVersion: ver, StoredSHA256: stored, ApprovedVersion: aver, HashColumn: hasCol > 0,
		ApprovedSHA256: sha, ApprovedBy: by}
	if at > 0 {
		t.approval.ApprovedAt = time.Unix(at, 0)
	}
	for i, s := range doc.Plan {
		n, ok := capability.AsInt(s["n"])
		if !ok {
			n = i + 1
		}
		mode, _ := s["mode"].(string)
		name, _ := s["capability"].(string)
		switch {
		case name != "":
			args := capability.Args{}
			if m, ok := s["args"].(map[string]any); ok {
				args = m
			}
			t.steps = append(t.steps, planStep{N: n, Mode: mode, Capability: name, Args: args})
		case mode == "operator":
			t.steps = append(t.steps, planStep{N: n, Mode: mode})
		case mode == "WRITE":
			t.writeBad = true
		}
	}
	if by != "" {
		adb, err := openRO(d.o.AuthDB)
		if err != nil {
			return nil, fmt.Errorf("auth database: %v", err)
		}
		defer adb.Close()
		var role string
		var disabled int
		err = adb.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE username=?`, by).Scan(&role, &disabled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("auth database: %v", err)
		}
		t.approval.ApproverAdmin = err == nil && role == "admin"
		t.approval.ApproverEnabled = err == nil && disabled == 0
	}
	return t, nil
}

// stopped reads the admin switch; an unreadable switch is an error, which the gate treats as stopped.
func (d *Daemon) stopped(ctx context.Context) (bool, error) {
	db, err := openRO(d.o.AuthDB)
	if err != nil {
		return true, err
	}
	defer db.Close()
	var v string
	err = db.QueryRowContext(ctx, `SELECT value FROM auth_settings WHERE key=?`, StoppedKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return strings.TrimSpace(v) != "0" && strings.TrimSpace(v) != "", nil
}

func (d *Daemon) gate(ctx context.Context) capability.Gate {
	st, err := d.stopped(ctx)
	return capability.Gate{Mode: d.o.Mode, Stopped: st, StopErr: err, ChainErr: d.chainErr()}
}

// refuseAll answers a request with one refusal, audited when the chain is usable.
func (d *Daemon) refuseAll(req *Request, id string, r *capability.Refusal) *Response {
	now := d.now()
	resp := &Response{ActionID: id, Mode: string(d.o.Mode), State: StateRefused, Refusal: r, Steps: []StepResult{}, Started: now.Unix(), Finished: now.Unix()}
	rec := Record{ActionID: id, Phase: PhaseRefused, Result: StateRefused, Detail: r.Reason}
	if req != nil {
		resp.TaskKey = clip(req.TaskKey, 64)
		rec.TaskKey, rec.Approver, rec.PlanVersion, rec.PlanSHA256 = clip(req.TaskKey, 64), clip(req.Approver, 64), req.PlanVersion, clip(req.PlanSHA256, 64)
	}
	var events []Record
	if d.audit != nil && d.audit.Err() == nil {
		if out, err := d.audit.Append(rec, now); err == nil {
			events = append(events, out)
		}
		resp.AuditHead, _ = d.audit.Head()
	}
	d.finish(resp, req, events)
	d.log().Info("action refused", "action", id, "rule", r.Rule, "code", string(r.Code), "reason", r.Reason)
	return resp
}

func clip(s string, n int) string {
	s = Clean(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (d *Daemon) finish(resp *Response, req *Request, events []Record) {
	resp.Finished = d.now().Unix()
	d.last = &lastAction{ID: resp.ActionID, State: resp.State, At: resp.Finished}
	a := Action{ID: resp.ActionID, TaskKey: resp.TaskKey, Mode: resp.Mode, State: resp.State, Steps: len(resp.Steps),
		Started: resp.Started, Finished: resp.Finished}
	if req != nil {
		a.PlanVersion, a.PlanSHA256, a.Approver = req.PlanVersion, clip(req.PlanSHA256, 64), clip(req.Approver, 64)
	}
	if resp.Refusal != nil {
		a.Reason = resp.Refusal.Reason
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.ops.Write(ctx, a, events); err != nil {
		d.log().Warn("ops.db rows not written (the audit file holds the record)", "action", resp.ActionID, "error", err.Error())
	}
}

// sameSteps reports whether the request's steps are exactly the stored plan's capability and operator steps.
func sameSteps(req []ReqStep, stored []planStep) bool {
	if len(req) != len(stored) {
		return false
	}
	for i, r := range req {
		s := stored[i]
		if r.N != s.N || r.Capability != s.Capability || (r.Mode == "operator") != (s.Mode == "operator") {
			return false
		}
		ra, err := capability.ParseArgs(r.Args)
		if err != nil {
			return false
		}
		sa := s.Args
		if sa == nil {
			sa = capability.Args{}
		}
		x, _ := json.Marshal(ra)
		y, _ := json.Marshal(sa)
		if !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

// Process runs one request (D5): the action-level checks, then each step in order with the refusal rules checked
// before it.
func (d *Daemon) Process(ctx context.Context, req Request) *Response {
	id := req.ActionID
	t, terr := d.loadTask(ctx, req.TaskKey)
	// Write-tier steps for release and pre-release are refused before any other check (rule 2).
	if terr == nil {
		for _, s := range t.steps {
			if c, ok := d.o.Registry.Get(s.Capability); ok && c.Tier() == capability.Write && (t.env == capability.Release || t.env == capability.PreRelease) {
				return d.refuseAll(&req, id, capability.Refuse(2, capability.CodeEnvironment, "step %d: a write-tier capability never runs for %s", s.N, t.env))
			}
		}
	}
	if r := capability.CheckGate(d.gate(ctx)); r != nil {
		return d.refuseAll(&req, id, r)
	}
	if want := capability.ActionID(req.TaskKey, req.PlanVersion, req.PlanSHA256); want == "" || want != id {
		return d.refuseAll(&req, id, capability.Refuse(3, capability.CodeApproval, "the action id is not <task key>.v<plan version>.<plan sha256[:12]>"))
	}
	if terr != nil {
		return d.refuseAll(&req, id, capability.Refuse(3, capability.CodeApproval, "%s", terr.Error()))
	}
	t.approval.RequestVersion, t.approval.RequestSHA256, t.approval.RequestApprover = req.PlanVersion, req.PlanSHA256, req.Approver
	if r := capability.CheckApproval(t.approval, d.o.TTL, d.now()); r != nil {
		return d.refuseAll(&req, id, r)
	}
	if t.writeBad {
		return d.refuseAll(&req, id, capability.Refuse(4, capability.CodeCapability, "a WRITE step has no capability: it is for a person"))
	}
	if !sameSteps(req.Steps, t.steps) {
		return d.refuseAll(&req, id, capability.Refuse(3, capability.CodeApproval, "the request's steps are not the approved plan's steps"))
	}
	ncap := 0
	for _, s := range t.steps {
		if s.Capability != "" {
			ncap++
		}
	}
	if ncap == 0 {
		return d.refuseAll(&req, id, capability.Refuse(4, capability.CodeCapability, "the plan has no capability step"))
	}
	on := d.o.Mode == capability.ModeOn
	newAction := on && !d.store.Started(id)
	day := d.now().In(d.o.Location)
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, d.o.Location)
	if r := capability.CheckLimits(capability.Limits{Steps: ncap, NewAction: newAction, ActionsToday: d.store.StartedSince(midnight), PerDay: d.o.PerDay}); r != nil {
		return d.refuseAll(&req, id, r)
	}
	if newAction {
		if err := d.store.MarkStarted(id, d.now()); err != nil {
			return d.refuseAll(&req, id, capability.Refuse(6, capability.CodeLimit, "the start could not be recorded: %v", err))
		}
	}
	resp := &Response{ActionID: id, TaskKey: req.TaskKey, Mode: string(d.o.Mode), State: StateDone, Steps: []StepResult{}, Started: d.now().Unix()}
	if !on {
		resp.State = StateDryRun
	}
	var events []Record
	base := Record{ActionID: id, TaskKey: req.TaskKey, Approver: req.Approver, PlanVersion: req.PlanVersion, PlanSHA256: req.PlanSHA256}
	audit := func(rec Record) (Record, error) {
		out, err := d.audit.Append(rec, d.now())
		if err == nil {
			events = append(events, out)
		}
		return out, err
	}
	for i, s := range t.steps {
		if s.Mode == "operator" {
			resp.State = StateOperator
			resp.Steps = append(resp.Steps, StepResult{N: s.N, Phase: "operator", Status: "needs the operator", At: d.now().Unix()})
			for _, rest := range t.steps[i+1:] {
				resp.Steps = append(resp.Steps, StepResult{N: rest.N, Capability: rest.Capability, Status: "waiting", At: d.now().Unix()})
			}
			break
		}
		args := s.Args
		c, r := capability.CheckStep(capability.Step{Gate: d.gate(ctx), Env: t.env, Approval: t.approval, TTL: d.o.TTL, Now: d.now(),
			Registry: d.o.Registry, Name: s.Capability, Args: args})
		if r != nil {
			rec := base
			rec.Step, rec.Capability, rec.Phase, rec.Result, rec.Detail = s.N, clip(s.Capability, 64), PhaseRefused, StateRefused, r.Reason
			if c2, ok := d.o.Registry.Get(s.Capability); ok {
				rec.Args = c2.Schema().Redacted(args)
			} else {
				rec.Args = capability.Schema{}.Redacted(args)
			}
			sr := StepResult{N: s.N, Capability: rec.Capability, Phase: PhaseRefused, Status: StateRefused, Detail: r.Reason, Refusal: r, At: d.now().Unix()}
			if r.Code != capability.CodeAuditChain {
				if out, err := audit(rec); err == nil {
					sr.Audit = out.SHA256
				}
			}
			resp.State, resp.Refusal = StateRefused, r
			resp.Steps = append(resp.Steps, sr)
			break
		}
		if on {
			if prev, ok := d.store.Get(id, s.N); ok {
				prev.Stored = true
				resp.Steps = append(resp.Steps, prev)
				if prev.Status != "ok" && prev.Status != "unchanged" {
					resp.State = StateFailed
					break
				}
				continue
			}
		}
		sr, stop := d.runStep(ctx, c, s, base, audit, on)
		resp.Steps = append(resp.Steps, sr)
		if on && sr.Status != StateRefused {
			if err := d.store.Put(id, sr); err != nil {
				d.log().Error("step result not stored", "action", id, "step", s.N, "error", err.Error())
			}
		}
		if stop {
			resp.State = StateFailed
			if sr.Refusal != nil {
				resp.State, resp.Refusal = StateRefused, sr.Refusal
			}
			break
		}
	}
	if d.audit != nil {
		resp.AuditHead, _ = d.audit.Head()
	}
	d.finish(resp, &req, events)
	d.log().Info("action", "action", id, "state", resp.State, "steps", len(resp.Steps))
	return resp
}

// runStep runs the phases of one step. Read tier: plan, apply. Write tier: plan, apply, verify; a failed verify
// rolls back (Rollback reads the target back itself and says whether it holds the old state). Dry-run: plan only. stop is true when the action must not go on.
func (d *Daemon) runStep(ctx context.Context, c capability.Capability, s planStep, base Record,
	audit func(Record) (Record, error), on bool) (StepResult, bool) {
	red := c.Schema().Redacted(s.Args)
	sr := StepResult{N: s.N, Capability: c.Name()}
	rec := func(phase, result, detail, backup string) bool {
		r := base
		r.Step, r.Capability, r.Args, r.Phase, r.Result, r.Detail, r.BackupRef = s.N, c.Name(), red, phase, result, detail, backup
		out, err := audit(r)
		if err != nil {
			sr.Phase, sr.Status, sr.Detail = phase, StateRefused, "the audit record could not be written: the step stops"
			sr.Refusal = capability.Refuse(1, capability.CodeAuditChain, "the audit record could not be written")
			return false
		}
		sr.Audit = out.SHA256
		return true
	}
	call := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, capability.MaxCall) }
	defer func() { sr.At = d.now().Unix() }()

	cctx, cancel := call()
	plan, err := c.Plan(cctx, s.Args)
	cancel()
	if err != nil {
		sr.Phase, sr.Status, sr.Detail = PhasePlan, "failed", Clean(err.Error())
		rec(PhasePlan, "failed", sr.Detail, "")
		return sr, true
	}
	sr.Phase, sr.Change, sr.Summary = PhasePlan, plan.Change, Clean(plan.Summary)
	if !on {
		sr.Status = "planned"
		if !rec(PhasePlan, "planned", sr.Summary, "") {
			return sr, true
		}
		return sr, false
	}
	if !rec(PhasePlan, "ok", sr.Summary, "") {
		return sr, true
	}
	cctx, cancel = call()
	res, ref, err := c.Apply(cctx, s.Args, plan)
	cancel()
	sr.Phase, sr.BackupRef = PhaseApply, string(ref)
	if errors.Is(err, capability.ErrPrecondition) {
		r := capability.Refuse(5, capability.CodePrecondition, "the target changed between plan and apply: nothing was written")
		sr.Status, sr.Detail, sr.Refusal = StateRefused, r.Reason, r
		rec(PhaseRefused, StateRefused, r.Reason, "")
		return sr, true
	}
	if err != nil && res.Status == "" {
		res.Status, res.Detail = "failed", err.Error()
	}
	sr.Status, sr.Detail, sr.Fields = res.Status, Clean(res.Detail), res.Fields
	if !rec(PhaseApply, res.Status, sr.Detail, string(ref)) {
		return sr, true
	}
	if c.Tier() == capability.Read {
		return sr, err != nil || (res.Status != "ok" && res.Status != "unchanged")
	}
	if err != nil {
		return sr, true
	}
	cctx, cancel = call()
	vres, verr := c.Verify(cctx, s.Args, plan)
	cancel()
	if verr == nil && (vres.Status == "ok" || vres.Status == "unchanged") {
		sr.Phase, sr.Status, sr.Detail = PhaseVerify, vres.Status, Clean(vres.Detail)
		if vres.Fields != nil {
			sr.Fields = vres.Fields
		}
		rec(PhaseVerify, vres.Status, sr.Detail, string(ref))
		return sr, false
	}
	if vres.Status == "" {
		vres.Status = "failed"
	}
	vd := vres.Detail
	if verr != nil {
		vd = verr.Error()
	}
	sr.Phase, sr.Status, sr.Detail = PhaseVerify, vres.Status, Clean(vd)
	if !rec(PhaseVerify, vres.Status, sr.Detail, string(ref)) {
		return sr, true
	}
	// verify failed: roll back, then verify the rollback
	cctx, cancel = call()
	rres, rerr := c.Rollback(cctx, ref)
	cancel()
	rst := rres.Status
	if rerr != nil || rst == "" {
		rst = "failed"
	}
	rd := rres.Detail
	if rerr != nil {
		rd = rerr.Error()
	}
	sr.Phase, sr.Status, sr.Detail = PhaseRollback, "rolled-back", Clean(rd)
	if rst == "failed" {
		sr.Status = "rollback-failed"
	}
	rec(PhaseRollback, rst, sr.Detail, string(ref))
	return sr, true
}
