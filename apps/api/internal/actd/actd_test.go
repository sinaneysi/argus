package actd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/migrate"
)

// fakeTokenDetail is a fake credential the secret scan must catch, built at run time.
var fakeTokenDetail = "token=" + "gl" + "pat-" + "abcdefghijklmnopqrstuv"

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

type fx struct {
	t                *testing.T
	act, actions     string
	board, auth, ops string
	calls            atomic.Int32
	onCall           func(path string)
	reg              *capability.Registry
	now              time.Time
	mode             capability.Mode
	perDay           int
	extra            []capability.Capability
}

func newFx(t *testing.T, extra ...capability.Capability) *fx {
	f := &fx{t: t, now: t0, mode: capability.ModeOn, perDay: 20, extra: extra}
	dir := t.TempDir()
	f.act, f.actions = filepath.Join(dir, "act"), filepath.Join(dir, "data", "actions")
	if err := os.MkdirAll(f.act, 0o700); err != nil {
		t.Fatal(err)
	}
	f.board, f.auth, f.ops = filepath.Join(dir, "data", "board.db"), filepath.Join(dir, "data", "auth.db"), filepath.Join(dir, "data", "ops.db")
	ctx := context.Background()
	for db, p := range map[string]string{migrate.Board: f.board, migrate.Auth: f.auth, migrate.Ops: f.ops} {
		if _, err := migrate.ApplyFile(ctx, p, db); err != nil {
			t.Fatal(err)
		}
	}
	f.exec(f.auth, `INSERT INTO users (username, hash, role, disabled, must_change) VALUES ('ada','x','admin',0,0), ('vic','x','viewer',0,0), ('dis','x','admin',1,0)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.onCall != nil {
			f.onCall(r.URL.Path)
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(503)
		}
		w.Write([]byte("BODY-MARKER-do-not-keep"))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	tr := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
	h, err := capability.NewHTTPCheck([]string{"svc.example.test"}, true, tr)
	if err != nil {
		t.Fatal(err)
	}
	f.reg, err = capability.NewRegistry(append([]capability.Capability{h}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) exec(path, q string, a ...any) {
	f.t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, a...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) daemon() *Daemon {
	f.t.Helper()
	d, err := New(Options{Mode: f.mode, ActDir: f.act, ActionsDir: f.actions, BoardDB: f.board, AuthDB: f.auth, OpsDB: f.ops,
		Registry: f.reg, TTL: 24 * time.Hour, PerDay: f.perDay, Now: func() time.Time { return f.now }})
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func check(path string, status any) map[string]any {
	return map[string]any{"mode": "read-only", "text": "check " + path, "capability": "http.check",
		"args": map[string]any{"url": "http://svc.example.test" + path, "expect_status": status}}
}

// task stores a task with an approved plan and returns the request the worker would write.
func (f *fx) task(key, env string, steps []map[string]any) Request {
	f.t.Helper()
	for i, s := range steps {
		s["n"] = i + 1
	}
	plan := map[string]any{"classification": map[string]any{"environment": env, "kind": "change"}, "plan": steps, "summary": "s"}
	pb, _ := json.Marshal(plan)
	var raw struct{ Classification, Plan any }
	json.Unmarshal(pb, &raw)
	sha, _ := capability.PlanSHA256(raw.Classification, raw.Plan)
	f.exec(f.board, `INSERT OR REPLACE INTO agent_tasks (key, state, plan, plan_version, approved_version, approved_by, approved_at)
		VALUES (?, 'approved', ?, 1, 1, 'ada', ?)`, key, string(pb), f.now.Add(-time.Hour).Unix())
	req := Request{ActionID: capability.ActionID(key, 1, sha), TaskKey: key, PlanVersion: 1, PlanSHA256: sha, Approver: "ada"}
	for _, s := range steps {
		st := ReqStep{N: s["n"].(int), Mode: s["mode"].(string)}
		if c, ok := s["capability"].(string); ok {
			st.Capability = c
			st.Args, _ = json.Marshal(s["args"])
		}
		if st.Capability != "" || st.Mode == "operator" {
			req.Steps = append(req.Steps, st)
		}
	}
	return req
}

func (f *fx) submit(d *Daemon, req Request) *Response {
	f.t.Helper()
	b, _ := json.Marshal(req)
	p := filepath.Join(f.actions, "in", req.ActionID+".json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	resp := d.ProcessFile(context.Background(), p)
	if _, err := os.Stat(p); err == nil {
		f.t.Fatal("the request file was not consumed")
	}
	out, err := os.ReadFile(filepath.Join(f.actions, "out", req.ActionID+".json"))
	if err != nil {
		f.t.Fatal("no result file:", err)
	}
	var got Response
	if err := json.Unmarshal(out, &got); err != nil || got.State != resp.State {
		f.t.Fatal("result file differs", err)
	}
	return resp
}

func (f *fx) auditLines() []Record {
	b, _ := os.ReadFile(AuditPath(f.act))
	var out []Record
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var r Record
		json.Unmarshal([]byte(l), &r)
		out = append(out, r)
	}
	return out
}

func wantRefused(t *testing.T, r *Response, rule int, code capability.Code) {
	t.Helper()
	if r.State != StateRefused || r.Refusal == nil || r.Refusal.Rule != rule || r.Refusal.Code != code || r.Refusal.Reason == "" {
		t.Fatalf("want refused rule %d %s, got %s %+v", rule, code, r.State, r.Refusal)
	}
}

func TestHTTPCheckEndToEnd(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	req := f.task("OPS-1", "develop", []map[string]any{{"mode": "read-only", "text": "look"}, check("/ok", 200)})
	r := f.submit(d, req)
	if r.State != StateDone || len(r.Steps) != 1 || r.Steps[0].Status != "ok" || r.Steps[0].N != 2 {
		t.Fatalf("%+v", r)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls, want 1", f.calls.Load())
	}
	lines := f.auditLines()
	if len(lines) != 2 || lines[0].Phase != PhasePlan || lines[1].Phase != PhaseApply || lines[1].Prev != lines[0].SHA256 {
		t.Fatalf("audit: %+v", lines)
	}
	if _, n, err := VerifyFile(AuditPath(f.act)); err != nil || n != 2 {
		t.Fatal("chain", n, err)
	}
	var na, ne int
	db, _ := sql.Open("sqlite", f.ops)
	defer db.Close()
	db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action_id=? AND state='done'`, req.ActionID).Scan(&na)
	db.QueryRow(`SELECT COUNT(*) FROM action_events WHERE action_id=?`, req.ActionID).Scan(&ne)
	if na != 1 || ne != 2 {
		t.Fatalf("ops rows: %d actions, %d events", na, ne)
	}
	for _, p := range []string{filepath.Join(f.actions, "out", req.ActionID+".json"), AuditPath(f.act)} {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "BODY-MARKER") {
			t.Fatalf("the response body is stored in %s", p)
		}
	}
	d.WriteStatus()
	st, ok := ReadStatus(StatusPath(f.actions))
	if !ok || !st.Chain.OK || st.Chain.Records != 2 || st.Mode != "on" || st.Last == nil || st.Last.State != StateDone {
		t.Fatalf("status %+v", st)
	}
}

func TestIdempotentRepeat(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	req := f.task("OPS-2", "develop", []map[string]any{check("/ok", 200)})
	a := f.submit(d, req)
	b := f.submit(d, req)
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls for a repeated request, want 1", f.calls.Load())
	}
	if b.State != StateDone || !b.Steps[0].Stored || b.Steps[0].Audit != a.Steps[0].Audit || b.Steps[0].Status != a.Steps[0].Status {
		t.Fatalf("repeat: %+v vs %+v", b.Steps[0], a.Steps[0])
	}
	if d.store.StartedSince(t0.Add(-time.Hour)) != 1 {
		t.Fatal("a repeated request counted as a new action")
	}
}

func TestModeOffRefusesWithReason(t *testing.T) {
	f := newFx(t)
	f.mode = capability.ModeOff
	d := f.daemon()
	r := f.submit(d, f.task("OPS-3", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeModeOff)
	if f.calls.Load() != 0 {
		t.Fatal("mode off called the target")
	}
	if l := f.auditLines(); len(l) != 1 || l[0].Phase != PhaseRefused {
		t.Fatalf("the refusal is not audited: %+v", l)
	}
}

func TestModeOffWithoutActDir(t *testing.T) {
	f := newFx(t)
	f.mode = capability.ModeOff
	os.RemoveAll(f.act)
	d := f.daemon()
	r := f.submit(d, f.task("OPS-3", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeModeOff)
	f.mode = capability.ModeOn
	if _, err := New(Options{Mode: f.mode, ActDir: f.act, ActionsDir: f.actions, Registry: f.reg}); err == nil {
		t.Fatal("mode on started without its volume")
	}
	os.MkdirAll(f.act, 0o755)
	if _, err := New(Options{Mode: f.mode, ActDir: f.act, ActionsDir: f.actions, Registry: f.reg}); err == nil {
		t.Fatal("mode on started with a volume open to others")
	}
}

func TestDryRunPlansOnly(t *testing.T) {
	f := newFx(t)
	f.mode = capability.ModeDryRun
	d := f.daemon()
	r := f.submit(d, f.task("OPS-4", "develop", []map[string]any{check("/ok", 200), check("/fail", 200)}))
	if r.State != StateDryRun || len(r.Steps) != 2 || r.Steps[0].Status != "planned" || r.Steps[0].Summary == "" {
		t.Fatalf("%+v", r)
	}
	if f.calls.Load() != 0 {
		t.Fatal("dry-run called the target")
	}
	if l := f.auditLines(); len(l) != 2 || l[0].Phase != PhasePlan || l[1].Phase != PhasePlan {
		t.Fatalf("audit %+v", l)
	}
	if d.store.Started(r.ActionID) {
		t.Fatal("a dry-run counted as a started action")
	}
}

func TestKillSwitch(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	f.exec(f.auth, `INSERT INTO auth_settings (key, value) VALUES ('actions.stopped', '1')`)
	r := f.submit(d, f.task("OPS-5", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeKillSwitch)
	// switched on between two steps: the next step is refused (acts within one step)
	f.exec(f.auth, `DELETE FROM auth_settings`)
	f.onCall = func(string) {
		f.exec(f.auth, `INSERT OR REPLACE INTO auth_settings (key, value) VALUES ('actions.stopped', '1')`)
	}
	r = f.submit(d, f.task("OPS-6", "develop", []map[string]any{check("/ok", 200), check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeKillSwitch)
	if f.calls.Load() != 1 || len(r.Steps) != 2 || r.Steps[0].Status != "ok" || r.Steps[1].Status != StateRefused {
		t.Fatalf("%d calls, %+v", f.calls.Load(), r.Steps)
	}
	// an unreadable switch stops too
	f.onCall = nil
	os.Remove(f.auth)
	r = f.submit(d, f.task("OPS-7", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeKillSwitch)
}

func TestBrokenChainRefuses(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	f.submit(d, f.task("OPS-8", "develop", []map[string]any{check("/ok", 200)}))
	b, _ := os.ReadFile(AuditPath(f.act))
	os.WriteFile(AuditPath(f.act), []byte(strings.Replace(string(b), `"result":"ok"`, `"result":"OK"`, 1)), 0o600)
	// the running daemon checks the chain before each step
	r := f.submit(d, f.task("OPS-9", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeAuditChain)
	// and a new one at start
	d2 := f.daemon()
	r = f.submit(d2, f.task("OPS-10", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeAuditChain)
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls with a broken chain", f.calls.Load())
	}
	if st := d2.CurrentStatus(); st.Chain.OK || st.Chain.Error == "" {
		t.Fatalf("status shows a good chain: %+v", st.Chain)
	}
}

func TestTruncatedChainDetectedAtStart(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	f.submit(d, f.task("OPS-11", "develop", []map[string]any{check("/ok", 200)}))
	d.WriteStatus()
	b, _ := os.ReadFile(AuditPath(f.act))
	lines := strings.SplitAfter(string(b), "\n")
	os.WriteFile(AuditPath(f.act), []byte(lines[0]), 0o600) // the last line removed: the chain alone still verifies
	if _, _, err := VerifyFile(AuditPath(f.act)); err != nil {
		t.Fatal(err)
	}
	d2 := f.daemon()
	r := f.submit(d2, f.task("OPS-12", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 1, capability.CodeAuditChain)
}

func TestClassificationRules(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	for _, env := range []string{"release", "pre-release", "none"} { // read tier runs for every environment (O8)
		r := f.submit(d, f.task("R-"+strings.ReplaceAll(env, "-", ""), env, []map[string]any{check("/ok", 200)}))
		if r.State != StateDone {
			t.Fatalf("read tier on %s: %+v", env, r)
		}
	}
	for _, env := range []string{"release", "pre-release", "none"} {
		r := f.submit(d, f.task("W-"+strings.ReplaceAll(env, "-", ""), env, []map[string]any{writeStep()}))
		wantRefused(t, r, 2, capability.CodeEnvironment)
	}
	// release is refused before any other check, even with a broken approval
	req := f.task("W-early", "release", []map[string]any{writeStep()})
	f.exec(f.board, `UPDATE agent_tasks SET approved_version=0 WHERE key='W-early'`)
	wantRefused(t, f.submit(d, req), 2, capability.CodeEnvironment)
	// and before the mode: a release write step names rule 2 even with the mode off
	f.mode = capability.ModeOff
	wantRefused(t, f.submit(f.daemon(), f.task("W-off", "release", []map[string]any{writeStep()})), 2, capability.CodeEnvironment)
	f.mode = capability.ModeOn
	d = f.daemon() // the other daemon appended to the chain: a new one reads it again
	if w.applies != 0 {
		t.Fatal("a write step ran outside develop")
	}
	if r := f.submit(d, f.task("W-dev", "develop", []map[string]any{writeStep()})); r.State != StateDone || w.applies != 1 {
		t.Fatalf("write tier on develop: %+v", r)
	}
}

func TestApprovalRules(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	cases := []struct {
		name string
		mut  func(key string, r *Request)
	}{
		{"not approved", func(k string, _ *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET approved_version=NULL, approved_by=NULL WHERE key=?`, k)
		}},
		{"version mismatch", func(k string, _ *Request) { f.exec(f.board, `UPDATE agent_tasks SET plan_version=2 WHERE key=?`, k) }},
		{"hash mismatch", func(k string, _ *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET plan=replace(plan, '"expect_status":200', '"expect_status":201') WHERE key=?`, k)
		}},
		{"approver not admin", func(k string, r *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET approved_by='vic' WHERE key=?`, k)
			r.Approver = "vic"
		}},
		{"approver disabled", func(k string, r *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET approved_by='dis' WHERE key=?`, k)
			r.Approver = "dis"
		}},
		{"approver unknown", func(k string, r *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET approved_by='ghost' WHERE key=?`, k)
			r.Approver = "ghost"
		}},
		{"request names another approver", func(_ string, r *Request) { r.Approver = "vic" }},
		{"approval too old", func(k string, _ *Request) {
			f.exec(f.board, `UPDATE agent_tasks SET approved_at=? WHERE key=?`, t0.Add(-25*time.Hour).Unix(), k)
		}},
		{"steps differ from the plan", func(_ string, r *Request) {
			r.Steps[0].Args = json.RawMessage(`{"url":"http://svc.example.test/fail","expect_status":200}`)
		}},
		{"action id differs", func(_ string, r *Request) { r.PlanVersion = 2 }},
		{"task missing", func(k string, _ *Request) { f.exec(f.board, `DELETE FROM agent_tasks WHERE key=?`, k) }},
	}
	for i, c := range cases {
		key := "AP-" + string(rune('A'+i))
		req := f.task(key, "develop", []map[string]any{check("/ok", 200)})
		c.mut(key, &req)
		r := f.submit(d, req)
		if r.State != StateRefused || r.Refusal == nil || r.Refusal.Rule != 3 {
			t.Errorf("%s: %s %+v", c.name, r.State, r.Refusal)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("a refused approval called the target")
	}
}

func TestApprovedHashColumn(t *testing.T) {
	f := newFx(t)
	f.exec(f.board, `ALTER TABLE agent_tasks ADD COLUMN approved_sha256 TEXT`)
	d := f.daemon()
	req := f.task("H-1", "develop", []map[string]any{check("/ok", 200)})
	wantRefused(t, f.submit(d, req), 3, capability.CodeApproval) // the column exists but holds no approved hash
	req = f.task("H-2", "develop", []map[string]any{check("/ok", 200)})
	f.exec(f.board, `UPDATE agent_tasks SET approved_sha256=? WHERE key='H-2'`, req.PlanSHA256)
	if r := f.submit(d, req); r.State != StateDone {
		t.Fatalf("%+v", r)
	}
}

func TestArgumentAndCapabilityRules(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	cases := map[string]map[string]any{
		"unknown capability": {"mode": "WRITE", "text": "x", "capability": "shell.exec", "args": map[string]any{"cmd": "id"}},
		"bad arguments":      {"mode": "read-only", "text": "x", "capability": "http.check", "args": map[string]any{"url": "http://svc.example.test/ok"}},
		"host not on list":   check("/ok", 200),
		"IP literal":         {"mode": "read-only", "text": "x", "capability": "http.check", "args": map[string]any{"url": "http://192.0.2.1/ok", "expect_status": 200}},
	}
	cases["host not on list"]["args"].(map[string]any)["url"] = "http://elsewhere.example.test/ok"
	i := 0
	for name, step := range cases {
		i++
		r := f.submit(d, f.task("AR-"+string(rune('A'+i)), "develop", []map[string]any{step}))
		if r.State != StateRefused || r.Refusal == nil || r.Refusal.Rule != 4 {
			t.Errorf("%s: %s %+v", name, r.State, r.Refusal)
		}
	}
	// a WRITE step without a capability is for a person
	r := f.submit(d, f.task("AR-W", "develop", []map[string]any{check("/ok", 200), {"mode": "WRITE", "text": "edit by hand"}}))
	wantRefused(t, r, 4, capability.CodeCapability)
	if f.calls.Load() != 0 {
		t.Fatal("a refused step called the target")
	}
	// the refused step's arguments are audited redacted when the capability is unknown
	for _, l := range f.auditLines() {
		if l.Capability == "shell.exec" && l.Args["cmd"] != capability.Redacted {
			t.Fatalf("unknown capability arguments audited: %+v", l.Args)
		}
	}
}

func TestLimitsRules(t *testing.T) {
	f := newFx(t)
	var steps []map[string]any
	for i := 0; i < 11; i++ {
		steps = append(steps, check("/ok", 200))
	}
	d := f.daemon()
	wantRefused(t, f.submit(d, f.task("L-1", "develop", steps)), 6, capability.CodeLimit)
	f.perDay = 1
	d = f.daemon()
	if r := f.submit(d, f.task("L-2", "develop", []map[string]any{check("/ok", 200)})); r.State != StateDone {
		t.Fatal(r.State)
	}
	wantRefused(t, f.submit(d, f.task("L-3", "develop", []map[string]any{check("/ok", 200)})), 6, capability.CodeLimit)
	f.now = t0.Add(24 * time.Hour) // the next day
	if r := f.submit(d, f.task("L-4", "develop", []map[string]any{check("/ok", 200)})); r.State != StateDone {
		t.Fatalf("next day: %s %+v", r.State, r.Refusal)
	}
}

func TestOperatorStepStops(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	r := f.submit(d, f.task("OP-1", "develop", []map[string]any{check("/ok", 200), {"mode": "operator", "text": "start the pipeline"}, check("/ok", 200)}))
	if r.State != StateOperator || len(r.Steps) != 3 || r.Steps[1].Status != "needs the operator" || r.Steps[2].Status != "waiting" {
		t.Fatalf("%+v", r)
	}
	if f.calls.Load() != 1 {
		t.Fatal("a step after the operator step ran")
	}
}

func TestFailedCheckStops(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	r := f.submit(d, f.task("F-1", "develop", []map[string]any{check("/fail", 200), check("/ok", 200)}))
	if r.State != StateFailed || len(r.Steps) != 1 || r.Steps[0].Status != "mismatch" {
		t.Fatalf("%+v", r)
	}
}

func TestBadRequestFiles(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	in := filepath.Join(f.actions, "in")
	os.WriteFile(filepath.Join(in, "../../escape.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(in, "bad name.json"), []byte("{}"), 0o600)
	id := "OPS-1.v1.0123456789ab"
	os.WriteFile(filepath.Join(in, id+".json"), []byte(`{"action_id":"`+id+`","extra":1}`), 0o600)
	d.Tick(context.Background())
	if m, _ := filepath.Glob(filepath.Join(in, "*")); len(m) != 0 {
		t.Fatalf("request files left: %v", m)
	}
	b, err := os.ReadFile(filepath.Join(f.actions, "out", id+".json"))
	if err != nil || !strings.Contains(string(b), `"refused"`) {
		t.Fatalf("unknown field accepted: %s %v", b, err)
	}
	if m, _ := filepath.Glob(filepath.Join(f.actions, "out", "*")); len(m) != 1 {
		t.Fatalf("out files: %v", m)
	}
}

// fakeWrite is a Write-tier capability with a secret field: it records calls and can fail verify or the
// precondition.
type fakeWrite struct {
	applies, rollbacks int
	failVerify         bool
	precondition       bool
}

func writeStep() map[string]any {
	return map[string]any{"mode": "WRITE", "text": "set", "capability": "test.set", "args": map[string]any{"key": "K", "value_ref": "input:NEW_VALUE"}}
}

func (w *fakeWrite) Name() string          { return "test.set" }
func (w *fakeWrite) Tier() capability.Tier { return capability.Write }
func (w *fakeWrite) Schema() capability.Schema {
	return capability.Schema{Fields: []capability.Field{{Name: "key", Type: capability.String, Required: true},
		{Name: "value_ref", Type: capability.String, Secret: true}}}
}
func (w *fakeWrite) Validate(a capability.Args, env capability.Env) error {
	if env != capability.Develop {
		return capability.Refuse(2, capability.CodeEnvironment, "develop only")
	}
	return w.Schema().Check(a)
}
func (w *fakeWrite) Plan(context.Context, capability.Args) (capability.Plan, error) {
	return capability.Plan{Change: "updated", Summary: "K would change", Digest: "d1"}, nil
}
func (w *fakeWrite) Apply(context.Context, capability.Args, capability.Plan) (capability.Result, capability.BackupRef, error) {
	if w.precondition {
		return capability.Result{}, "", capability.ErrPrecondition
	}
	w.applies++
	return capability.Result{Status: "ok", Detail: "updated"}, "backup:x/1", nil
}
func (w *fakeWrite) Verify(context.Context, capability.Args, capability.Plan) (capability.Result, error) {
	if w.failVerify {
		return capability.Result{Status: "mismatch", Detail: "read back differs"}, nil
	}
	return capability.Result{Status: "ok"}, nil
}
func (w *fakeWrite) Rollback(context.Context, capability.BackupRef) (capability.Result, error) {
	w.rollbacks++
	return capability.Result{Status: "ok", Detail: "restored"}, nil
}

func TestWriteTierPhases(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	r := f.submit(d, f.task("WT-1", "develop", []map[string]any{writeStep()}))
	if r.State != StateDone || r.Steps[0].Phase != PhaseVerify || r.Steps[0].BackupRef != "backup:x/1" {
		t.Fatalf("%+v", r)
	}
	var phases []string
	for _, l := range f.auditLines() {
		phases = append(phases, l.Phase)
		if l.Args["value_ref"] != capability.Redacted {
			t.Fatalf("a secret field is in the audit: %+v", l.Args)
		}
	}
	if strings.Join(phases, ",") != "plan,apply,verify" {
		t.Fatal(phases)
	}
	b, _ := os.ReadFile(AuditPath(f.act))
	o, _ := os.ReadFile(filepath.Join(f.actions, "out", r.ActionID+".json"))
	if strings.Contains(string(b)+string(o), "NEW_VALUE") {
		t.Fatal("the secret reference leaked")
	}

	w.failVerify = true
	r = f.submit(d, f.task("WT-2", "develop", []map[string]any{writeStep(), check("/ok", 200)}))
	if r.State != StateFailed || w.rollbacks != 1 || r.Steps[0].Status != "rolled-back" || len(r.Steps) != 1 {
		t.Fatalf("verify failure: %+v, %d rollbacks", r, w.rollbacks)
	}

	w.failVerify, w.precondition = false, true
	before := w.applies
	r = f.submit(d, f.task("WT-3", "develop", []map[string]any{writeStep()}))
	wantRefused(t, r, 5, capability.CodePrecondition)
	if w.applies != before {
		t.Fatal("applied although the precondition changed")
	}
}

func TestAuditTamper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit", "actions.jsonl")
	a, err := OpenAudit(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := a.Append(Record{ActionID: "OPS-1.v1.0123456789ab", Step: i, Phase: PhaseApply, Result: "ok",
			Args: capability.Args{"x": i}, Detail: fakeTokenDetail}, t0); err != nil {
			t.Fatal(err)
		}
	}
	good, _ := os.ReadFile(p)
	if strings.Contains(string(good), "glpat-abcdefghij") {
		t.Fatal("a secret in the detail was not scanned")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit mode %o", fi.Mode().Perm())
	}
	if _, n, err := VerifyFile(p); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(good), "\n"), "\n")
	tamper := map[string]string{
		"edited":    strings.Replace(string(good), `"step":2`, `"step":9`, 1),
		"removed":   lines[0] + lines[2] + lines[3],
		"reordered": lines[0] + lines[2] + lines[1] + lines[3],
		"first cut": lines[1] + lines[2] + lines[3],
		"inserted":  lines[0] + lines[0] + lines[1] + lines[2] + lines[3],
		"no hash":   lines[0] + `{"ts":"x"}` + "\n",
	}
	for name, body := range tamper {
		os.WriteFile(p, []byte(body), 0o600)
		if _, _, err := VerifyFile(p); err == nil {
			t.Errorf("%s line not detected", name)
		}
	}
	// a removed last line is caught by the running process and by the recorded head
	os.WriteFile(p, good, 0o600)
	a, _ = OpenAudit(p)
	head, n := a.Head()
	os.WriteFile(p, []byte(lines[0]+lines[1]+lines[2]), 0o600)
	if a.Check() == nil {
		t.Error("a removed last line not detected by Check")
	}
	if _, err := a.Append(Record{Phase: PhasePlan}, t0); !errors.Is(err, a.Err()) || err == nil {
		t.Error("Append went on after a broken chain")
	}
	b, _ := OpenAudit(p)
	if b.Expect(head, n) == nil {
		t.Error("a removed last line not detected against the recorded head")
	}
}
