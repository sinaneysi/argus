package actd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/secret"
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
	key              secret.Key
	cur              *Daemon
	priv             ed25519.PrivateKey // the api's approval key; the daemon gets the public half
	pub              ed25519.PublicKey
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
	f.pub, f.priv, _ = ed25519.GenerateKey(rand.Reader)
	f.key, _ = secret.ParseKey(secret.NewKeyText())
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
	if f.cur != nil {
		f.cur.Close()
	}
	d, err := New(Options{Mode: f.mode, ActDir: f.act, Key: f.key, ActionsDir: f.actions, BoardDB: f.board, AuthDB: f.auth, OpsDB: f.ops,
		Registry: f.reg, TTL: 24 * time.Hour, PerDay: f.perDay, Now: func() time.Time { return f.now }, ApprovalPub: f.pub})
	if err != nil {
		f.t.Fatal(err)
	}
	f.cur = d
	f.t.Cleanup(d.Close)
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
		if _, ok := s["n"]; !ok {
			s["n"] = i + 1
		}
	}
	plan := map[string]any{"classification": map[string]any{"environment": env, "kind": "change"}, "plan": steps, "summary": "s"}
	pb, _ := json.Marshal(plan)
	var raw struct{ Classification, Plan any }
	json.Unmarshal(pb, &raw)
	sha, _ := capability.PlanSHA256(raw.Classification, raw.Plan)
	at := f.now.Add(-time.Hour).Unix()
	f.exec(f.board, `INSERT OR REPLACE INTO agent_tasks (key, state, plan, plan_version, approved_version, approved_by, approved_at)
		VALUES (?, 'approved', ?, 1, 1, 'ada', ?)`, key, string(pb), at)
	if f.hasHashCol() {
		sig := f.sign(capability.ApprovalClaim{Kind: capability.SignStart, TaskKey: key, PlanVersion: 1, PlanSHA256: sha, Approver: "ada", At: at,
			SealedSHA256: capability.SealedSHA256(nil)})
		f.exec(f.board, `UPDATE agent_tasks SET approved_sha256=?, approval_sig=? WHERE key=?`, sha, sig, key)
	}
	req := Request{ActionID: capability.ActionID(key, 1, sha), TaskKey: key, PlanVersion: 1, PlanSHA256: sha, Approver: "ada"}
	for _, s := range steps {
		n, _ := capability.AsInt(s["n"])
		st := ReqStep{N: n, Mode: s["mode"].(string)}
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

// sign is the api's signature of a claim.
func (f *fx) sign(c capability.ApprovalClaim) string {
	f.t.Helper()
	s, err := capability.SignApproval(f.priv, c)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fx) hasHashCol() bool {
	db, _ := sql.Open("sqlite", f.board)
	defer db.Close()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_tasks') WHERE name='approved_sha256'`).Scan(&n)
	return n > 0
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
	if _, n, err := VerifyDir(f.act, f.key); err != nil || n != 2 {
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
	if !d.store.Started(r.ActionID) {
		t.Fatal("a dry-run did not count as a started action (it counts towards ARGUS_ACTIONS_PER_DAY)")
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

func lastReplace(s, old, new string) string {
	i := strings.LastIndex(s, old)
	return s[:i] + new + s[i+len(old):]
}

func TestBrokenChainRefuses(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	f.submit(d, f.task("OPS-8", "develop", []map[string]any{check("/ok", 200)}))
	good, _ := os.ReadFile(AuditPath(f.act))
	// the last line edited: the running daemon's check before each step sees it
	os.WriteFile(AuditPath(f.act), []byte(lastReplace(string(good), `"result":"ok"`, `"result":"OK"`)), 0o600)
	wantRefused(t, f.submit(d, f.task("OPS-9", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	if st := d.CurrentStatus(); st.Chain.OK || st.Chain.Error == "" {
		t.Fatalf("status shows a good chain: %+v", st.Chain)
	}
	// an earlier line edited: a new daemon sees it at start
	os.WriteFile(AuditPath(f.act), []byte(strings.Replace(string(good), `"result":"ok"`, `"result":"OK"`, 1)), 0o600)
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-10", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	// and a running one at its hourly full check
	os.WriteFile(AuditPath(f.act), good, 0o600)
	d = f.daemon()
	os.WriteFile(AuditPath(f.act), []byte(strings.Replace(string(good), `"result":"ok"`, `"result":"OK"`, 1)), 0o600)
	f.now = f.now.Add(2 * time.Hour)
	d.Tick(context.Background())
	wantRefused(t, f.submit(d, f.task("OPS-10b", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls with a broken chain", f.calls.Load())
	}
}

func TestAnchorDetectsTruncationAndLoss(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	f.submit(d, f.task("OPS-11", "develop", []map[string]any{check("/ok", 200)}))
	chain, anchor := AuditPaths(f.act)
	good, _ := os.ReadFile(chain)
	ga, _ := os.ReadFile(anchor)
	lines := strings.SplitAfter(string(good), "\n")
	os.WriteFile(chain, []byte(lines[0]), 0o600) // the last line removed: the lines alone still chain
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-12", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	os.WriteFile(chain, good, 0o600)
	os.Remove(anchor) // the anchor lost while records exist
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-13", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	os.WriteFile(anchor, []byte(strings.Replace(string(ga), `"records":2`, `"records":1`, 1)), 0o600) // a forged anchor
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-14", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	os.WriteFile(anchor, ga, 0o600)
	os.Remove(chain) // the chain emptied while the anchor says 2
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-15", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	os.WriteFile(chain, good, 0o600)
	f.key, _ = secret.ParseKey(secret.NewKeyText()) // another key cannot verify (nor extend) the chain
	wantRefused(t, f.submit(f.daemon(), f.task("OPS-16", "develop", []map[string]any{check("/ok", 200)})), 1, capability.CodeAuditChain)
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls", f.calls.Load())
	}
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
	d := f.daemon()
	req := f.task("H-1", "develop", []map[string]any{check("/ok", 200)})
	f.exec(f.board, `UPDATE agent_tasks SET approved_sha256=NULL WHERE key='H-1'`)
	wantRefused(t, f.submit(d, req), 3, capability.CodeApproval) // the column exists but holds no approved hash
	if r := f.submit(d, f.task("H-2", "develop", []map[string]any{check("/ok", 200)})); r.State != StateDone {
		t.Fatalf("%+v", r)
	}
	// before the PR 2 board migration there is no approved hash: nothing runs
	f.exec(f.board, `ALTER TABLE agent_tasks DROP COLUMN approved_sha256`)
	r := f.submit(d, f.task("H-3", "develop", []map[string]any{check("/ok", 200)}))
	wantRefused(t, r, 3, capability.CodeApproval)
	if !strings.Contains(r.Refusal.Reason, "approved_sha256") || f.calls.Load() != 1 {
		t.Fatalf("%+v, %d calls", r.Refusal, f.calls.Load())
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
	key, _ := secret.ParseKey(secret.NewKeyText())
	a, err := OpenAudit(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := a.Append(Record{ActionID: "OPS-1.v1.0123456789ab", Step: i, Phase: PhaseApply, Result: "ok",
			Args: capability.Args{"x": i}, Detail: fakeTokenDetail}, t0); err != nil {
			t.Fatal(err)
		}
	}
	p, anchor := AuditPaths(dir)
	good, _ := os.ReadFile(p)
	ga, _ := os.ReadFile(anchor)
	if strings.Contains(string(good), "abcdefghijklmnopqrstuv") {
		t.Fatal("a secret in the detail was not scanned")
	}
	for _, f := range []string{p, anchor} {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", f, fi.Mode().Perm())
		}
	}
	if _, n, err := VerifyDir(dir, key); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(good), "\n"), "\n")
	lines[3] += "\n"
	// an edited line whose tag is recomputed without the key: plain SHA-256 is not accepted
	tampered := map[string]string{
		"edited":    strings.Replace(string(good), `"step":2`, `"step":9`, 1),
		"removed":   lines[0] + lines[2] + lines[3],
		"reordered": lines[0] + lines[2] + lines[1] + lines[3],
		"first cut": lines[1] + lines[2] + lines[3],
		"inserted":  lines[0] + lines[0] + lines[1] + lines[2] + lines[3],
		"no tag":    lines[0] + `{"ts":"x"}` + "\n",
		"last cut":  lines[0] + lines[1] + lines[2],
		"half line": string(good) + `{"ts":"x"`,
	}
	for name, body := range tampered {
		os.WriteFile(p, []byte(body), 0o600)
		os.WriteFile(anchor, ga, 0o600)
		if _, _, err := VerifyDir(dir, key); err == nil {
			t.Errorf("%s not detected", name)
		}
	}
	other, _ := secret.ParseKey(secret.NewKeyText())
	os.WriteFile(p, good, 0o600)
	if _, _, err := VerifyDir(dir, other); err == nil {
		t.Error("the chain verified under another key")
	}
	// the cheap check before each step: a change of size or of the last line, by anyone but this process
	for name, body := range map[string]string{"appended": string(good) + lines[0], "last line edited": strings.Replace(string(good), `"step":4`, `"step":8`, 1)} {
		os.WriteFile(p, good, 0o600)
		os.WriteFile(anchor, ga, 0o600)
		a, _ = OpenAudit(dir, key)
		os.WriteFile(p, []byte(body), 0o600)
		if a.Check() == nil {
			t.Errorf("Check missed: %s", name)
		}
		if _, err := a.Append(Record{Phase: PhasePlan}, t0); err == nil {
			t.Errorf("Append went on after a broken chain (%s)", name)
		}
	}
}

func TestOneExecutorAtATime(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	if _, err := New(Options{Mode: f.mode, ActDir: f.act, Key: f.key, ActionsDir: f.actions, Registry: f.reg}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second executor started: %v", err)
	}
	d.Close()
	d2, err := New(Options{Mode: f.mode, ActDir: f.act, Key: f.key, ActionsDir: f.actions, Registry: f.reg})
	if err != nil {
		t.Fatal("the lock was not released:", err)
	}
	d2.Close()
}

func TestTaskStateRule(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	for _, st := range []string{"stopped", "failed", "planning", "ready", "done"} {
		req := f.task("ST-"+st, "develop", []map[string]any{check("/ok", 200)})
		f.exec(f.board, `UPDATE agent_tasks SET state=? WHERE key=?`, st, req.TaskKey)
		wantRefused(t, f.submit(d, req), 3, capability.CodeApproval)
	}
	if r := f.submit(d, f.task("ST-run", "develop", []map[string]any{check("/ok", 200)})); r.State != StateDone {
		t.Fatal(r.State)
	}
	// stopped by an admin while the action runs: the next step is refused
	req := f.task("ST-mid", "develop", []map[string]any{check("/ok", 200), check("/ok", 200)})
	f.onCall = func(string) { f.exec(f.board, `UPDATE agent_tasks SET state='stopped' WHERE key='ST-mid'`) }
	r := f.submit(d, req)
	wantRefused(t, r, 3, capability.CodeApproval)
	if len(r.Steps) != 2 || r.Steps[0].Status != "ok" || r.Steps[1].Status != StateRefused {
		t.Fatalf("%+v", r.Steps)
	}
}

func TestStepModeAndNumbers(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	ws := writeStep()
	ws["mode"] = "read-only"
	op := check("/ok", 200)
	op["mode"] = "operator"
	odd := check("/ok", 200)
	odd["mode"] = "sometimes"
	for i, steps := range [][]map[string]any{{ws}, {op}, {odd}} {
		name := fmt.Sprintf("SM-%d", i)
		r := f.submit(d, f.task(name, "develop", steps))
		if r.State != StateRefused || r.Refusal.Code != capability.CodeStepMode {
			t.Errorf("%s: %s %+v", name, r.State, r.Refusal)
		}
	}
	a, b := check("/ok", 200), check("/ok", 200)
	a["n"], b["n"] = 1, 1
	wantRefused(t, f.submit(d, f.task("SN-dup", "develop", []map[string]any{a, b})), 4, capability.CodeArguments)
	z := check("/ok", 200)
	z["n"] = 0
	wantRefused(t, f.submit(d, f.task("SN-zero", "develop", []map[string]any{z})), 4, capability.CodeArguments)
	m := check("/ok", 200)
	m["n"] = -3
	wantRefused(t, f.submit(d, f.task("SN-neg", "develop", []map[string]any{m})), 4, capability.CodeArguments)
	if w.applies != 0 || f.calls.Load() != 0 {
		t.Fatal("a refused step ran")
	}
}

func TestDryRunCountsTowardsTheDailyCap(t *testing.T) {
	f := newFx(t)
	f.mode, f.perDay = capability.ModeDryRun, 2
	d := f.daemon()
	for i, want := range []string{StateDryRun, StateDryRun, StateRefused} {
		r := f.submit(d, f.task(fmt.Sprintf("DR-%d", i), "develop", []map[string]any{check("/ok", 200)}))
		if r.State != want {
			t.Fatalf("dry-run %d: %s", i, r.State)
		}
	}
}

func TestInterruptedApplyNeedsAPerson(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	req := f.task("IA-1", "develop", []map[string]any{writeStep()})
	// an earlier run wrote the marker and stopped before the result was stored
	if err := d.store.MarkApplying(req.ActionID, 1, f.now); err != nil {
		t.Fatal(err)
	}
	r := f.submit(d, req)
	wantRefused(t, r, 5, capability.CodeInterrupted)
	if w.applies != 0 {
		t.Fatal("applied a second time")
	}
	// a normal run leaves no marker behind
	req = f.task("IA-2", "develop", []map[string]any{writeStep()})
	if r := f.submit(d, req); r.State != StateDone || d.store.Applying(req.ActionID, 1) {
		t.Fatalf("%s, marker %v", r.State, d.store.Applying(req.ActionID, 1))
	}
}

func TestInboxFileChecks(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	in := filepath.Join(f.actions, "in")
	outside := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(outside, []byte("{}"), 0o600)
	link := "LN-1.v1.0123456789ab"
	os.Symlink(outside, filepath.Join(in, link+".json"))
	fifo := "FI-1.v1.0123456789ab"
	if err := syscall.Mkfifo(filepath.Join(in, fifo+".json"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := "BG-1.v1.0123456789ab"
	os.WriteFile(filepath.Join(in, big+".json"), bytes.Repeat([]byte(" "), MaxRequestBytes+1), 0o600)
	done := make(chan struct{})
	go func() { d.Tick(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO in the inbox blocked actd")
	}
	for _, id := range []string{link, fifo, big} {
		b, err := os.ReadFile(filepath.Join(f.actions, "out", id+".json"))
		if err != nil || !strings.Contains(string(b), `"refused"`) {
			t.Errorf("%s: %s %v", id, b, err)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "{}" {
		t.Fatal("the link target was touched")
	}
	if m, _ := filepath.Glob(filepath.Join(in, "*")); len(m) != 0 {
		t.Fatalf("left in the inbox: %v", m)
	}
}

func TestInboxAuditRateLimit(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	in := filepath.Join(f.actions, "in")
	for i := 0; i < 25; i++ {
		os.WriteFile(filepath.Join(in, fmt.Sprintf("JK-%d.v1.0123456789ab.json", i)), []byte("not json"), 0o600)
	}
	d.Tick(context.Background())
	if n := len(f.auditLines()); n != inboxAuditPerMinute {
		t.Fatalf("%d audit records for junk files, want %d", n, inboxAuditPerMinute)
	}
	if m, _ := filepath.Glob(filepath.Join(f.actions, "out", "JK-*")); len(m) != 25 {
		t.Fatalf("%d answers, want 25", len(m))
	}
}

func TestRefusalsWriteOnlyConfirmedFields(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	req := f.task("CF-1", "develop", []map[string]any{check("/ok", 200)})
	req.Approver = "mallory"
	wantRefused(t, f.submit(d, req), 3, capability.CodeApproval)
	ghost := f.task("CF-2", "develop", []map[string]any{check("/ok", 200)})
	f.exec(f.board, `DELETE FROM agent_tasks WHERE key='CF-2'`)
	ghost.Approver = "mallory"
	f.submit(d, ghost)
	for _, l := range f.auditLines() {
		if l.Approver == "mallory" || l.TaskKey == "CF-2" {
			t.Fatalf("an unconfirmed request field was audited: %+v", l)
		}
	}
	db, _ := sql.Open("sqlite", f.ops)
	defer db.Close()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM actions WHERE approver='mallory' OR task_key='CF-2'`).Scan(&n)
	var appr string
	db.QueryRow(`SELECT approver FROM actions WHERE action_id=?`, req.ActionID).Scan(&appr)
	if n != 0 || appr != "ada" {
		t.Fatalf("ops rows: %d unconfirmed, approver %q", n, appr)
	}
}

func TestAuditWriteFailureRefusesTheStep(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	f := newFx(t)
	d := f.daemon()
	f.submit(d, f.task("AW-0", "develop", []map[string]any{check("/ok", 200)}))
	f.onCall = func(string) { os.Chmod(AuditPath(f.act), 0o400) } // the apply record cannot be written
	r := f.submit(d, f.task("AW-1", "develop", []map[string]any{check("/ok", 200), check("/ok", 200)}))
	os.Chmod(AuditPath(f.act), 0o600)
	wantRefused(t, r, 1, capability.CodeAuditChain)
	if len(r.Steps) != 1 || r.Steps[0].Refusal == nil || f.calls.Load() != 2 {
		t.Fatalf("%+v, %d calls", r.Steps, f.calls.Load())
	}
	if _, ok := d.store.Get(r.ActionID, 1); ok {
		t.Fatal("a step without its audit record was stored as finished")
	}
}

func TestSecretLookingArgumentsRefused(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	st := check("/ok/"+strings.TrimPrefix(fakeTokenDetail, "token="), 200)
	r := f.submit(d, f.task("SA-1", "develop", []map[string]any{st}))
	wantRefused(t, r, 4, capability.CodeArguments)
	if !strings.Contains(r.Refusal.Reason, "looks like a secret") || f.calls.Load() != 0 {
		t.Fatalf("%+v", r.Refusal)
	}
	b, _ := os.ReadFile(AuditPath(f.act))
	o, _ := os.ReadFile(filepath.Join(f.actions, "out", r.ActionID+".json"))
	if strings.Contains(string(b)+string(o), "abcdefghijklmnopqrstuv") {
		t.Fatal("the secret-looking value was stored")
	}
}

// TestSignedApprovalRequired: the worker can write board.db but not sign, so an approval without the api's
// signature, with a signature of another key, or over other sealed inputs never runs a step (amendment 1, A and B).
func TestSignedApprovalRequired(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	steps := func() []map[string]any { return []map[string]any{check("/ok", 200)} }

	req := f.task("SG-none", "develop", steps())
	f.exec(f.board, `UPDATE agent_tasks SET approval_sig=NULL WHERE key='SG-none'`)
	r := f.submit(d, req)
	wantRefused(t, r, 3, capability.CodeApproval)
	if !strings.Contains(r.Refusal.Reason, "not signed") {
		t.Fatal(r.Refusal.Reason)
	}

	// a forged approval: the worker made its own key and signed the row it wrote
	_, wpriv, _ := ed25519.GenerateKey(rand.Reader)
	req = f.task("SG-forged", "develop", steps())
	forged, _ := capability.SignApproval(wpriv, capability.ApprovalClaim{Kind: capability.SignStart, TaskKey: "SG-forged", PlanVersion: 1,
		PlanSHA256: req.PlanSHA256, Approver: "ada", At: f.now.Add(-time.Hour).Unix(), SealedSHA256: capability.SealedSHA256(nil)})
	f.exec(f.board, `UPDATE agent_tasks SET approval_sig=? WHERE key='SG-forged'`, forged)
	r = f.submit(d, req)
	wantRefused(t, r, 3, capability.CodeApproval)
	if !strings.Contains(r.Refusal.Reason, "not valid") {
		t.Fatal(r.Refusal.Reason)
	}

	// a valid signature moved to a later approval time (a re-dated row) does not verify
	req = f.task("SG-redated", "develop", steps())
	f.exec(f.board, `UPDATE agent_tasks SET approved_at=approved_at+60 WHERE key='SG-redated'`)
	wantRefused(t, f.submit(d, req), 3, capability.CodeApproval)

	// sealed inputs swapped after the approval: the signature covers the hash of the inputs it was given
	req = f.task("SG-sealed", "develop", steps())
	req.SealedInputs = map[string]string{"NEW_VALUE": "v1.sealed-by-someone-else"}
	r = f.submit(d, req)
	wantRefused(t, r, 3, capability.CodeApproval)

	// a signature for another task's approval does not carry over
	a := f.task("SG-a", "develop", steps())
	b := f.task("SG-b", "develop", steps())
	f.exec(f.board, `UPDATE agent_tasks SET approval_sig=(SELECT approval_sig FROM agent_tasks WHERE key='SG-a') WHERE key='SG-b'`)
	wantRefused(t, f.submit(d, b), 3, capability.CodeApproval)
	if r := f.submit(d, a); r.State != StateDone {
		t.Fatalf("the signed approval did not run: %+v", r.Refusal)
	}

	// an executor without the api's public key refuses every step
	f.pub = nil
	d = f.daemon()
	r = f.submit(d, f.task("SG-nokey", "develop", steps()))
	wantRefused(t, r, 3, capability.CodeApproval)
	if !strings.Contains(r.Refusal.Reason, "public key") {
		t.Fatal(r.Refusal.Reason)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("%d calls: a refused approval called the target", f.calls.Load())
	}
}

func TestContinueAfterOperatorStep(t *testing.T) {
	f := newFx(t)
	d := f.daemon()
	steps := []map[string]any{check("/ok", 200), {"mode": "operator", "text": "start the pipeline"}, check("/ok", 200)}
	req := f.task("CT-1", "develop", steps)
	r := f.submit(d, req)
	if r.State != StateOperator || f.calls.Load() != 1 {
		t.Fatalf("%+v", r)
	}
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='CT-1'`)
	claim := capability.ApprovalClaim{Kind: capability.SignContinue, TaskKey: "CT-1", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(nil), Step: 2}

	// unsigned, signed by another key, or for another step: refused, nothing runs
	_, wpriv, _ := ed25519.GenerateKey(rand.Reader)
	forged, _ := capability.SignApproval(wpriv, claim)
	for name, c := range map[string]*Signed{
		"unsigned": {Step: 2, Approver: "ada", At: claim.At},
		"forged":   {Step: 2, Approver: "ada", At: claim.At, Signature: forged},
		"viewer":   {Step: 2, Approver: "vic", At: claim.At, Signature: f.sign(func() capability.ApprovalClaim { c := claim; c.Approver = "vic"; return c }())},
		"old":      {Step: 2, Approver: "ada", At: t0.Add(-25 * time.Hour).Unix(), Signature: f.sign(func() capability.ApprovalClaim { c := claim; c.At = t0.Add(-25 * time.Hour).Unix(); return c }())},
	} {
		rq := req
		rq.Continue = c
		if r := f.submit(d, rq); r.State != StateRefused || r.Refusal.Rule != 3 {
			t.Errorf("%s: %s %+v", name, r.State, r.Refusal)
		}
	}
	rq := req
	rq.Continue = &Signed{Step: 3, Approver: "ada", At: claim.At, Signature: f.sign(func() capability.ApprovalClaim { c := claim; c.Step = 3; return c }())}
	wantRefused(t, f.submit(d, rq), 4, capability.CodeArguments) // step 3 is not an operator step
	if f.calls.Load() != 1 {
		t.Fatal("a refused continue ran a step")
	}

	rq = req
	rq.Continue = &Signed{Step: 2, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
	r = f.submit(d, rq)
	if r.State != StateDone || len(r.Steps) != 3 || !r.Steps[0].Stored || r.Steps[1].Status != "done by the operator" || r.Steps[2].Status != "ok" {
		t.Fatalf("%+v", r)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("%d calls: step 1 must come from the store, step 3 must run once", f.calls.Load())
	}
	var phases []string
	for _, l := range f.auditLines() {
		if l.ActionID == req.ActionID && l.Phase != PhaseRefused {
			phases = append(phases, fmt.Sprintf("%d:%s", l.Step, l.Phase))
		}
	}
	if strings.Join(phases, ",") != "1:plan,1:apply,3:plan,3:apply" {
		t.Fatal(phases)
	}
}

func TestManualRollback(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	req := f.task("RB-1", "develop", []map[string]any{writeStep()})
	if r := f.submit(d, req); r.State != StateDone {
		t.Fatalf("%+v", r)
	}
	f.exec(f.board, `UPDATE agent_tasks SET state='done' WHERE key='RB-1'`) // the action is over
	claim := capability.ApprovalClaim{Kind: capability.SignRollback, TaskKey: "RB-1", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(nil), Step: 1}
	rq := req
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At}
	wantRefused(t, f.submit(d, rq), 3, capability.CodeApproval)
	start := claim
	start.Kind, start.Step = capability.SignContinue, 1
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At, Signature: f.sign(start)} // a signature of another kind
	wantRefused(t, f.submit(d, rq), 3, capability.CodeApproval)
	if w.rollbacks != 0 {
		t.Fatal("an unsigned rollback ran")
	}
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
	r := f.submit(d, rq)
	if r.State != StateDone || w.rollbacks != 1 || len(r.Steps) != 1 || r.Steps[0].Status != "rolled-back" {
		t.Fatalf("%+v", r)
	}
	last := f.auditLines()[len(f.auditLines())-1]
	if last.Phase != PhaseRollback || last.Step != 1 || last.BackupRef != "backup:x/1" || last.Args["value_ref"] != capability.Redacted {
		t.Fatalf("%+v", last)
	}
	wantRefused(t, f.submit(d, rq), 4, capability.CodeArguments) // already rolled back
	if w.rollbacks != 1 {
		t.Fatal("rolled back twice")
	}
}

func TestApprovalPubFile(t *testing.T) {
	dir := t.TempDir()
	if p, err := LoadApprovalPub(dir); p != nil || err != nil {
		t.Fatal("a missing key is not (nil, nil)")
	}
	_, pub, _ := capability.NewApprovalKey()
	p, err := SetApprovalPub(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	q, err := LoadApprovalPub(dir)
	if err != nil || !q.Equal(p) {
		t.Fatal(err)
	}
	if _, err := SetApprovalPub(dir, []byte("not a key")); err == nil {
		t.Fatal("junk stored as the key")
	}
}
