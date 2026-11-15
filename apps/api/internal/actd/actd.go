// Package actd is the capability executor `argus actd` (ADR-0022 D1, D5-D8). It runs in its own service with its
// own volume (argus-act, mounted by no other service), reads action requests from actions/in in the shared volume,
// checks the fixed refusal rules before every step, runs each step's capability (Plan, then Apply and Verify, or
// Plan only in dry-run), appends one HMAC-chained audit record per phase and writes redacted results to
// actions/out. It confirms the task's classification and approval from board.db and auth.db, never from the
// request.
package actd

import (
	"bytes"
	"context"
	"crypto/ed25519"
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
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
)

// MaxRequestBytes is the largest request file actd reads.
const MaxRequestBytes = 64 << 10

// StoppedKey is the auth_settings key of the admin switch "Stop all actions" (D8 layer 2).
const StoppedKey = "actions.stopped"

// inboxAuditPerMinute caps the audit records written for request files that cannot be read as a request, so a
// flood of junk files cannot grow the chain without bound (each one is still answered and logged).
const inboxAuditPerMinute = 10

// The states of an action.
const (
	StateDone     = "done"
	StateDryRun   = "dry-run"
	StateRefused  = "refused"
	StateFailed   = "failed"
	StateOperator = "operator" // an operator step stopped the run: "needs the operator"
)

// Request is actions/in/<action-id>.json, written by the worker (D5). The start approval's signature is read from
// board.db (agent_tasks.approval_sig); Continue and Rollback carry their own signature.
type Request struct {
	ActionID     string            `json:"action_id"`
	TaskKey      string            `json:"task_key"`
	PlanVersion  int               `json:"plan_version"`
	PlanSHA256   string            `json:"plan_sha256"`
	Approver     string            `json:"approver"`
	Steps        []ReqStep         `json:"steps"`
	SealedInputs map[string]string `json:"sealed_inputs,omitempty"` // opened by a later capability; ciphertext only
	Continue     *Signed           `json:"continue,omitempty"`      // agent-continue: the operator did step Continue.Step
	Rollback     *Signed           `json:"rollback,omitempty"`      // agent-rollback: roll back step Rollback.Step
}

// Signed is an admin's signed continue or rollback (capability.ApprovalClaim of that kind, signed by the api).
type Signed struct {
	Step      int    `json:"step"`
	Approver  string `json:"approver"`
	At        int64  `json:"at"`
	Signature string `json:"signature"`
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
	TaskKey   string              `json:"task_key"` // confirmed from board.db; empty when the task was not confirmed
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
	Mode   capability.Mode
	ActDir string     // ARGUS_ACT_DIR: the executor's own volume
	Key    secret.Key // the executor key (keys/act.key); nil: no audit, every step refused
	// ApprovalPub is the api's approval public key (keys/approval.pub in ActDir); nil: every step refused (rule 3)
	ApprovalPub ed25519.PublicKey
	ActionsDir  string // <ARGUS_STATE_DIR>/actions: in/, out/, actd-status.json
	BoardDB     string // read only
	AuthDB      string // read only
	OpsDB       string // actions, action_events
	Registry    *capability.Registry
	TTL         time.Duration // ARGUS_ACTIONS_APPROVAL_TTL
	PerDay      int           // ARGUS_ACTIONS_PER_DAY (dry-run actions count too)
	Location    *time.Location
	Poll        time.Duration // how often actions/in is read (default 2 s)
	Now         func() time.Time
	Log         *slog.Logger
	Version     string
	// Credentials lists the stored credentials for the status file (names and last four characters only).
	Credentials func() []Credential
}

// Daemon is one executor. New takes an exclusive lock, so two processes never run at once; Close releases it.
type Daemon struct {
	o       Options
	audit   *Audit // nil: no usable executor volume or key
	store   Store
	ops     Ops
	last    *lastAction
	checked time.Time
	lock    *os.File
	inbox   []time.Time // audit records written for unreadable request files in the last minute
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

// StatusPath is the status file in the shared volume.
func StatusPath(actionsDir string) string { return filepath.Join(actionsDir, "actd-status.json") }

// ErrLocked: another actd holds the lock.
var ErrLocked = errors.New("another actd is running: one action at a time")

func takeLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return f, nil
}

// New takes the lock, prepares the folders and opens the audit chain. With a mode other than off, the executor
// volume must pass CheckActDir. A broken chain is not an error here: the daemon starts, refuses every step and
// reports the chain.
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
	for _, sub := range []string{"in", "out"} {
		if err := os.MkdirAll(filepath.Join(o.ActionsDir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	actErr := CheckActDir(o.ActDir)
	if actErr != nil && o.Mode != capability.ModeOff {
		return nil, actErr
	}
	d := &Daemon{o: o, ops: Ops{Path: o.OpsDB}}
	lockPath := filepath.Join(o.ActionsDir, "actd.lock") // mode off without the executor volume
	if actErr == nil {
		lockPath = filepath.Join(o.ActDir, "actd.lock")
	}
	lock, err := takeLock(lockPath)
	if err != nil {
		return nil, err
	}
	d.lock = lock
	if actErr == nil {
		for _, sub := range []string{"audit", "steps", "keys", "secrets", "backups"} {
			if err := os.MkdirAll(filepath.Join(o.ActDir, sub), 0o700); err != nil {
				d.Close()
				return nil, err
			}
		}
		d.store = Store{Dir: filepath.Join(o.ActDir, "steps")}
		if len(o.Key) == 32 {
			a, err := OpenAudit(o.ActDir, o.Key)
			if a == nil {
				d.Close()
				return nil, err
			}
			d.audit = a
			if e := a.Err(); e != nil {
				d.log().Error("audit chain broken: every step is refused", "error", e.Error())
			}
		}
		d.checked = d.now()
	}
	return d, nil
}

// Close releases the lock.
func (d *Daemon) Close() {
	if d.lock != nil {
		d.lock.Close()
		d.lock = nil
	}
}

// chainErr is the audit chain's state for the gate (the cheap check; Full runs at start and every hour).
func (d *Daemon) chainErr() error {
	if d.audit == nil {
		return errors.New("no audit chain: the executor volume or its key is not usable")
	}
	return d.audit.Check()
}

// Run processes requests until ctx ends; the audit chain is verified fully every hour, the heartbeat written every
// poll.
func (d *Daemon) Run(ctx context.Context) error {
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

// Tick handles every waiting request once, verifies the chain when an hour has passed and writes the heartbeat.
func (d *Daemon) Tick(ctx context.Context) {
	if d.audit != nil && d.now().Sub(d.checked) >= time.Hour {
		d.checked = d.now()
		if err := d.audit.Full(); err != nil {
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

// readRequest opens the file without following a link and without blocking on a FIFO, checks the open file (not
// the name), and reads at most MaxRequestBytes+1 bytes.
func readRequest(path string) ([]byte, *capability.Refusal) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, capability.Refuse(4, capability.CodeArguments, "the request is not a regular file")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, capability.Refuse(4, capability.CodeArguments, "the request is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRequestBytes+1))
	if err != nil {
		return nil, capability.Refuse(4, capability.CodeArguments, "the request cannot be read")
	}
	if len(b) > MaxRequestBytes {
		return nil, capability.Refuse(4, capability.CodeArguments, "the request is larger than %d bytes", MaxRequestBytes)
	}
	return b, nil
}

// ProcessFile answers one request file and removes it. The answer is actions/out/<action-id>.json.
func (d *Daemon) ProcessFile(ctx context.Context, path string) *Response {
	defer os.Remove(path)
	id := strings.TrimSuffix(filepath.Base(path), ".json")
	if !capability.ValidActionID(id) {
		d.log().Warn("request file with a bad name removed", "file", Clean(filepath.Base(path)))
		return nil
	}
	var resp *Response
	b, r := readRequest(path)
	if r == nil {
		var req Request
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		switch err := dec.Decode(&req); {
		case err != nil || dec.More():
			r = capability.Refuse(4, capability.CodeArguments, "the request is not valid JSON of the request form")
		case req.ActionID != id:
			r = capability.Refuse(3, capability.CodeApproval, "the file name is not the request's action id")
		default:
			resp = d.Process(ctx, req)
		}
	}
	if resp == nil {
		resp = d.refuseAll(nil, id, r, d.inboxAuditAllowed())
	}
	if err := d.writeOut(resp); err != nil {
		d.log().Error("result not written", "action", resp.ActionID, "error", err.Error())
	}
	return resp
}

func (d *Daemon) inboxAuditAllowed() bool {
	now := d.now()
	keep := d.inbox[:0]
	for _, t := range d.inbox {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	d.inbox = keep
	if len(d.inbox) >= inboxAuditPerMinute {
		return false
	}
	d.inbox = append(d.inbox, now)
	return true
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
	key      string
	env      capability.Env
	approval capability.Approval
	steps    []planStep // the steps actd acts on: capability steps and operator steps, in order
	writeBad bool       // a WRITE step without a capability: for a person, never executed here
	badN     string     // a step number is missing, not positive or repeated
}

// confirmed are the fields actd writes about a task: only values read from board.db.
type confirmed struct {
	TaskKey, Approver, PlanSHA256 string
	PlanVersion                   int
}

func (t *task) confirmed() *confirmed {
	if t == nil {
		return nil
	}
	return &confirmed{TaskKey: t.key, Approver: t.approval.ApprovedBy, PlanSHA256: t.approval.StoredSHA256, PlanVersion: t.approval.PlanVersion}
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

// loadTask reads the task's stored plan, state and approval (board.db) and the approver (auth.db).
func (d *Daemon) loadTask(ctx context.Context, key string) (*task, error) {
	db, err := openRO(d.o.BoardDB)
	if err != nil {
		return nil, fmt.Errorf("board database: %v", err)
	}
	defer db.Close()
	var hasCol int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agent_tasks') WHERE name IN ('approved_sha256','approval_sig')`).Scan(&hasCol); err != nil {
		return nil, fmt.Errorf("board database: %v", err)
	}
	shaCol, sigCol := `''`, `''`
	if hasCol == 2 {
		shaCol, sigCol = `COALESCE(approved_sha256,'')`, `COALESCE(approval_sig,'')`
	}
	var plan, by, sha, sig, state string
	var ver, aver int
	var at int64
	err = db.QueryRowContext(ctx, `SELECT COALESCE(plan,''), COALESCE(plan_version,0), COALESCE(approved_version,0), COALESCE(approved_by,''),
		COALESCE(approved_at,0), COALESCE(state,''), `+shaCol+`, `+sigCol+` FROM agent_tasks WHERE key=?`, key).Scan(&plan, &ver, &aver, &by, &at, &state, &sha, &sig)
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
	t := &task{key: key, env: capability.Env(env)}
	t.approval = capability.Approval{PlanVersion: ver, StoredSHA256: stored, ApprovedVersion: aver, HashColumn: hasCol == 2,
		ApprovedSHA256: sha, ApprovedBy: by, State: state, TaskKey: key, Signature: sig, PublicKey: d.o.ApprovalPub}
	if at > 0 {
		t.approval.ApprovedAt = time.Unix(at, 0)
	}
	seen := map[int]bool{}
	for i, s := range doc.Plan {
		n, ok := capability.AsInt(s["n"])
		switch {
		case !ok || n <= 0:
			t.badN = fmt.Sprintf("step %d has no positive number", i+1)
		case seen[n]:
			t.badN = fmt.Sprintf("step number %d is used twice", n)
		}
		seen[n] = true
		mode, _ := s["mode"].(string)
		name, _ := s["capability"].(string)
		switch {
		case name != "" || mode == "operator":
			args := capability.Args{}
			if m, ok := s["args"].(map[string]any); ok {
				args = m
			}
			t.steps = append(t.steps, planStep{N: n, Mode: mode, Capability: name, Args: args})
		case mode == "WRITE":
			t.writeBad = true
		}
	}
	if by != "" {
		admin, enabled, err := d.approver(ctx, by)
		if err != nil {
			return nil, err
		}
		t.approval.ApproverAdmin, t.approval.ApproverEnabled = admin, enabled
	}
	return t, nil
}

// approver reads the user from auth.db: (role admin, not disabled).
func (d *Daemon) approver(ctx context.Context, by string) (bool, bool, error) {
	adb, err := openRO(d.o.AuthDB)
	if err != nil {
		return false, false, fmt.Errorf("auth database: %v", err)
	}
	defer adb.Close()
	var role string
	var disabled int
	err = adb.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE username=?`, by).Scan(&role, &disabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("auth database: %v", err)
	}
	return err == nil && role == "admin", err == nil && disabled == 0, nil
}

// checkSigned is D6 rule 3 for a signed continue or rollback: the api's signature over this task, the stored plan
// version and hash, the sealed inputs and the step; an enabled admin; not older than the approval TTL.
func (d *Daemon) checkSigned(ctx context.Context, kind string, t *task, req Request, s *Signed) *capability.Refusal {
	c := capability.ApprovalClaim{Kind: kind, TaskKey: t.key, PlanVersion: t.approval.PlanVersion, PlanSHA256: t.approval.StoredSHA256,
		Approver: s.Approver, At: s.At, SealedSHA256: capability.SealedSHA256(req.SealedInputs), Step: s.Step}
	if err := capability.VerifyApproval(d.o.ApprovalPub, c, s.Signature); err != nil {
		return capability.Refuse(3, capability.CodeApproval, "%s: %s", kind, err.Error())
	}
	admin, enabled, err := d.approver(ctx, s.Approver)
	switch {
	case err != nil:
		return capability.Refuse(3, capability.CodeApproval, "%s", err.Error())
	case !admin || !enabled:
		return capability.Refuse(3, capability.CodeApproval, "%s: the approver is not an enabled admin", kind)
	}
	at := time.Unix(s.At, 0)
	if at.After(d.now().Add(5*time.Minute)) || d.now().Sub(at) > d.o.TTL || d.o.TTL <= 0 {
		return capability.Refuse(3, capability.CodeApproval, "%s: the signed request is older than %s or in the future", kind, d.o.TTL)
	}
	return nil
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

// refuseAll answers a request with one refusal. Only DB-confirmed fields (c) are written; audited when asked and
// the chain is usable.
func (d *Daemon) refuseAll(c *confirmed, id string, r *capability.Refusal, audited bool) *Response {
	now := d.now()
	resp := &Response{ActionID: id, Mode: string(d.o.Mode), State: StateRefused, Refusal: r, Steps: []StepResult{}, Started: now.Unix(), Finished: now.Unix()}
	rec := Record{ActionID: id, Phase: PhaseRefused, Result: StateRefused, Detail: r.Reason}
	if c != nil {
		resp.TaskKey = c.TaskKey
		rec.TaskKey, rec.Approver, rec.PlanVersion, rec.PlanSHA256 = c.TaskKey, c.Approver, c.PlanVersion, c.PlanSHA256
	}
	var events []Record
	if audited && d.audit != nil && d.audit.Err() == nil {
		if out, err := d.audit.Append(rec, now); err == nil {
			events = append(events, out)
		}
	}
	if d.audit != nil {
		resp.AuditHead, _ = d.audit.Head()
	}
	d.finish(resp, c, events)
	d.log().Info("action refused", "action", id, "rule", r.Rule, "code", string(r.Code), "reason", r.Reason)
	return resp
}

func (d *Daemon) finish(resp *Response, c *confirmed, events []Record) {
	resp.Finished = d.now().Unix()
	d.last = &lastAction{ID: resp.ActionID, State: resp.State, At: resp.Finished}
	a := Action{ID: resp.ActionID, Mode: resp.Mode, State: resp.State, Steps: len(resp.Steps), Started: resp.Started, Finished: resp.Finished}
	if c != nil {
		a.TaskKey, a.PlanVersion, a.PlanSHA256, a.Approver = c.TaskKey, c.PlanVersion, c.PlanSHA256, c.Approver
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

// stepModeRule checks a step's mode against its capability: an operator step names no capability, a Write-tier
// capability runs only in a WRITE step, and any capability runs only in a read-only or WRITE step.
func (d *Daemon) stepModeRule(s planStep) *capability.Refusal {
	if s.Capability == "" {
		return nil
	}
	if s.Mode == "operator" {
		return capability.Refuse(4, capability.CodeStepMode, "step %d is an operator step but names a capability", s.N)
	}
	if s.Mode != "read-only" && s.Mode != "WRITE" {
		return capability.Refuse(4, capability.CodeStepMode, "step %d has an unknown mode", s.N)
	}
	if c, ok := d.o.Registry.Get(s.Capability); ok && c.Tier() == capability.Write && s.Mode != "WRITE" {
		return capability.Refuse(4, capability.CodeStepMode, "step %d: a write-tier capability must be in a WRITE step", s.N)
	}
	return nil
}

// argsSecretRule refuses arguments that hold something the secret scan recognises.
func argsSecretRule(n int, a capability.Args) *capability.Refusal {
	b, _ := json.Marshal(a)
	if _, hits := notify.Redact(string(b), nil); len(hits) > 0 {
		return capability.Refuse(4, capability.CodeArguments, "step %d: an argument looks like a secret (%s); secrets enter only as sealed inputs", n, notify.HitsLine(hits))
	}
	return nil
}

// Process runs one request (D5): the action-level checks, then each step in order with the refusal rules checked
// before it (the task is read again from the databases before every step).
func (d *Daemon) Process(ctx context.Context, req Request) *Response {
	id := req.ActionID
	t, terr := d.loadTask(ctx, req.TaskKey)
	c := t.confirmed()
	// Write-tier steps for release and pre-release are refused before any other check (rule 2).
	if terr == nil {
		for _, s := range t.steps {
			if cp, ok := d.o.Registry.Get(s.Capability); ok && cp.Tier() == capability.Write && (t.env == capability.Release || t.env == capability.PreRelease) {
				return d.refuseAll(c, id, capability.Refuse(2, capability.CodeEnvironment, "step %d: a write-tier capability never runs for %s", s.N, t.env), true)
			}
		}
	}
	if r := capability.CheckGate(d.gate(ctx)); r != nil {
		return d.refuseAll(c, id, r, true)
	}
	if want := capability.ActionID(req.TaskKey, req.PlanVersion, req.PlanSHA256); want == "" || want != id {
		return d.refuseAll(c, id, capability.Refuse(3, capability.CodeApproval, "the action id is not <task key>.v<plan version>.<plan sha256[:12]>"), true)
	}
	if terr != nil {
		return d.refuseAll(nil, id, capability.Refuse(3, capability.CodeApproval, "%s", terr.Error()), true)
	}
	sealed := capability.SealedSHA256(req.SealedInputs)
	t.approval.RequestVersion, t.approval.RequestSHA256, t.approval.RequestApprover, t.approval.RequestSealed = req.PlanVersion, req.PlanSHA256, req.Approver, sealed
	if req.Rollback != nil {
		if req.Continue != nil {
			return d.refuseAll(c, id, capability.Refuse(4, capability.CodeArguments, "a request is a continue or a rollback, not both"), true)
		}
		return d.rollback(ctx, req, t, c)
	}
	if r := capability.CheckApproval(t.approval, d.o.TTL, d.now()); r != nil {
		return d.refuseAll(c, id, r, true)
	}
	if t.badN != "" {
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeArguments, "the plan's step numbers are not usable: %s", t.badN), true)
	}
	if t.writeBad {
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeCapability, "a WRITE step has no capability: it is for a person"), true)
	}
	for _, s := range t.steps {
		if r := d.stepModeRule(s); r != nil {
			return d.refuseAll(c, id, r, true)
		}
	}
	if !sameSteps(req.Steps, t.steps) {
		return d.refuseAll(c, id, capability.Refuse(3, capability.CodeApproval, "the request's steps are not the approved plan's steps"), true)
	}
	ncap := 0
	for _, s := range t.steps {
		if s.Capability != "" {
			ncap++
		}
	}
	if ncap == 0 {
		return d.refuseAll(c, id, capability.Refuse(4, capability.CodeCapability, "the plan has no capability step"), true)
	}
	// agent-continue: the operator steps up to the signed one are done; the steps after it run
	doneUpTo := -1
	if req.Continue != nil {
		for i, s := range t.steps {
			if s.Mode == "operator" && s.N == req.Continue.Step {
				doneUpTo = i
			}
		}
		if doneUpTo < 0 {
			return d.refuseAll(c, id, capability.Refuse(4, capability.CodeArguments, "continue: step %d is not an operator step of the plan", req.Continue.Step), true)
		}
		if r := d.checkSigned(ctx, capability.SignContinue, t, req, req.Continue); r != nil {
			return d.refuseAll(c, id, r, true)
		}
	}
	on := d.o.Mode == capability.ModeOn
	newAction := !d.store.Started(id)
	day := d.now().In(d.o.Location)
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, d.o.Location)
	if r := capability.CheckLimits(capability.Limits{Steps: ncap, NewAction: newAction, ActionsToday: d.store.StartedSince(midnight), PerDay: d.o.PerDay}); r != nil {
		return d.refuseAll(c, id, r, true)
	}
	if newAction {
		if err := d.store.MarkStarted(id, d.now()); err != nil {
			return d.refuseAll(c, id, capability.Refuse(6, capability.CodeLimit, "the start could not be recorded: %v", err), true)
		}
	}
	resp := &Response{ActionID: id, TaskKey: c.TaskKey, Mode: string(d.o.Mode), State: StateDone, Steps: []StepResult{}, Started: d.now().Unix()}
	if !on {
		resp.State = StateDryRun
	}
	var events []Record
	base := Record{ActionID: id, TaskKey: c.TaskKey, Approver: c.Approver, PlanVersion: c.PlanVersion, PlanSHA256: c.PlanSHA256}
	audit := func(rec Record) (Record, error) {
		if d.audit == nil {
			return rec, errors.New("no audit chain")
		}
		out, err := d.audit.Append(rec, d.now())
		if err == nil {
			events = append(events, out)
		}
		return out, err
	}
	refuseStep := func(s planStep, r *capability.Refusal) {
		rec := base
		rec.Step, rec.Capability, rec.Phase, rec.Result, rec.Detail = s.N, clip(s.Capability, 64), PhaseRefused, StateRefused, r.Reason
		if cp, ok := d.o.Registry.Get(s.Capability); ok && argsSecretRule(s.N, s.Args) == nil {
			rec.Args = cp.Schema().Redacted(s.Args)
		} else { // unknown capability, or an argument that looks like a secret: every value redacted
			rec.Args = capability.Schema{}.Redacted(s.Args)
		}
		sr := StepResult{N: s.N, Capability: rec.Capability, Phase: PhaseRefused, Status: StateRefused, Detail: r.Reason, Refusal: r, At: d.now().Unix()}
		if r.Code != capability.CodeAuditChain {
			if out, err := audit(rec); err == nil {
				sr.Audit = out.SHA256
			}
		}
		resp.State, resp.Refusal = StateRefused, r
		resp.Steps = append(resp.Steps, sr)
	}
	for i, s := range t.steps {
		if s.Mode == "operator" && i <= doneUpTo {
			resp.Steps = append(resp.Steps, StepResult{N: s.N, Phase: "operator", Status: "done by the operator", At: d.now().Unix()})
			continue
		}
		if s.Mode == "operator" {
			resp.State = StateOperator
			resp.Steps = append(resp.Steps, StepResult{N: s.N, Phase: "operator", Status: "needs the operator", At: d.now().Unix()})
			for _, rest := range t.steps[i+1:] {
				resp.Steps = append(resp.Steps, StepResult{N: rest.N, Capability: rest.Capability, Status: "waiting", At: d.now().Unix()})
			}
			break
		}
		// the task as the databases say now: a stop, a re-plan or a revoked approval acts within one step
		if i > 0 {
			nt, err := d.loadTask(ctx, req.TaskKey)
			if err != nil {
				refuseStep(s, capability.Refuse(3, capability.CodeApproval, "%s", err.Error()))
				break
			}
			if nt.approval.StoredSHA256 != t.approval.StoredSHA256 {
				refuseStep(s, capability.Refuse(3, capability.CodeApproval, "the stored plan changed during the action"))
				break
			}
			nt.approval.RequestVersion, nt.approval.RequestSHA256, nt.approval.RequestApprover, nt.approval.RequestSealed = req.PlanVersion, req.PlanSHA256, req.Approver, sealed
			t.approval = nt.approval
		}
		if r := argsSecretRule(s.N, s.Args); r != nil {
			refuseStep(s, r)
			break
		}
		cp, r := capability.CheckStep(capability.Step{Gate: d.gate(ctx), Env: t.env, Approval: t.approval, TTL: d.o.TTL, Now: d.now(),
			Registry: d.o.Registry, Name: s.Capability, Args: s.Args})
		if r != nil {
			refuseStep(s, r)
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
			if cp.Tier() == capability.Write && d.store.Applying(id, s.N) {
				refuseStep(s, capability.Refuse(5, capability.CodeInterrupted,
					"step %d: an earlier apply stopped before its result was stored; a person must check the target before it runs again", s.N))
				break
			}
		}
		sr, stop := d.runStep(ctx, id, cp, s, base, audit, on)
		resp.Steps = append(resp.Steps, sr)
		if on && sr.Refusal == nil {
			if err := d.store.Put(id, sr); err != nil {
				d.log().Error("step result not stored", "action", id, "step", s.N, "error", err.Error())
			} else if err := d.store.ClearApplying(id, s.N); err != nil {
				d.log().Error("apply marker not removed", "action", id, "step", s.N, "error", err.Error())
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
	d.finish(resp, c, events)
	d.log().Info("action", "action", id, "state", resp.State, "steps", len(resp.Steps))
	return resp
}

func clip(s string, n int) string {
	s = Clean(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// runStep runs the phases of one step. Read tier: plan, apply. Write tier: plan, apply, verify; a failed verify
// rolls back (Rollback reads the target back itself and says whether it holds the old state). Dry-run: plan only.
// Every phase is audited before the next one starts; a record that cannot be written refuses the step with the
// audit-chain code. stop is true when the action must not go on.
func (d *Daemon) runStep(ctx context.Context, id string, c capability.Capability, s planStep, base Record,
	audit func(Record) (Record, error), on bool) (sr StepResult, stop bool) {
	red := c.Schema().Redacted(s.Args)
	sr = StepResult{N: s.N, Capability: c.Name()}
	defer func() { sr.At = d.now().Unix() }()
	chainFail := func(phase string) (StepResult, bool) {
		sr.Phase, sr.Status, sr.Detail = phase, StateRefused, "the audit record could not be written: the step stops"
		sr.Refusal = capability.Refuse(1, capability.CodeAuditChain, "the audit record could not be written")
		return sr, true
	}
	rec := func(phase, result, detail, backup string) bool {
		r := base
		r.Step, r.Capability, r.Args, r.Phase, r.Result, r.Detail, r.BackupRef = s.N, c.Name(), red, phase, result, detail, backup
		out, err := audit(r)
		if err != nil {
			return false
		}
		sr.Audit = out.SHA256
		return true
	}
	call := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, capability.MaxCall) }

	cctx, cancel := call()
	plan, err := c.Plan(cctx, s.Args)
	cancel()
	if err != nil {
		sr.Phase, sr.Status, sr.Detail = PhasePlan, "failed", Clean(err.Error())
		if !rec(PhasePlan, "failed", sr.Detail, "") {
			return chainFail(PhasePlan)
		}
		return sr, true
	}
	sr.Phase, sr.Change, sr.Summary = PhasePlan, plan.Change, Clean(plan.Summary)
	if !on {
		sr.Status = "planned"
		if !rec(PhasePlan, "planned", sr.Summary, "") {
			return chainFail(PhasePlan)
		}
		return sr, false
	}
	if !rec(PhasePlan, "ok", sr.Summary, "") {
		return chainFail(PhasePlan)
	}
	if c.Tier() == capability.Write {
		if err := d.store.MarkApplying(id, s.N, d.now()); err != nil {
			r := capability.Refuse(5, capability.CodeInterrupted, "the apply marker could not be written: nothing was applied")
			sr.Status, sr.Detail, sr.Refusal = StateRefused, r.Reason, r
			if !rec(PhaseRefused, StateRefused, r.Reason, "") {
				return chainFail(PhaseRefused)
			}
			return sr, true
		}
	}
	cctx, cancel = call()
	res, ref, err := c.Apply(cctx, s.Args, plan)
	cancel()
	sr.Phase, sr.BackupRef = PhaseApply, string(ref)
	if errors.Is(err, capability.ErrPrecondition) {
		d.store.ClearApplying(id, s.N) // nothing was written
		r := capability.Refuse(5, capability.CodePrecondition, "the target changed between plan and apply: nothing was written")
		sr.Status, sr.Detail, sr.Refusal = StateRefused, r.Reason, r
		if !rec(PhaseRefused, StateRefused, r.Reason, "") {
			return chainFail(PhaseRefused)
		}
		return sr, true
	}
	if err != nil && res.Status == "" {
		res.Status, res.Detail = "failed", err.Error()
	}
	sr.Status, sr.Detail, sr.Fields = res.Status, Clean(res.Detail), res.Fields
	if !rec(PhaseApply, res.Status, sr.Detail, string(ref)) {
		return chainFail(PhaseApply)
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
		if !rec(PhaseVerify, vres.Status, sr.Detail, string(ref)) {
			return chainFail(PhaseVerify)
		}
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
		return chainFail(PhaseVerify)
	}
	// verify failed: roll back
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
	if !rec(PhaseRollback, rst, sr.Detail, string(ref)) {
		return chainFail(PhaseRollback)
	}
	return sr, true
}
