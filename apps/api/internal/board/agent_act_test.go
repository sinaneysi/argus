package board

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/secret"
)

// testRegistry is http.check on svc.example.test, dialled to a local test server.
func testRegistry(t *testing.T, calls *atomic.Int32) *capability.Registry {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/fail" {
			w.WriteHeader(503)
		}
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
	reg, err := capability.NewRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func httpStep(path string) map[string]any {
	return map[string]any{"text": "check " + path, "mode": "read-only", "capability": "http.check",
		"args": map[string]any{"url": "http://svc.example.test" + path, "expect_status": 200}}
}

func classified(env, kind string, steps ...map[string]any) map[string]any {
	p := plan(steps...)
	p["classification"] = map[string]any{"environment": env, "kind": kind}
	return p
}

func TestSpinUpTable(t *testing.T) {
	var calls atomic.Int32
	reg := testRegistry(t, &calls)
	write := map[string]any{"text": "edit by hand", "mode": "WRITE"}
	badWrite := map[string]any{"text": "set it", "mode": "WRITE", "capability": "shell.exec", "args": map[string]any{"cmd": "id"}}
	operator := map[string]any{"text": "start the pipeline", "mode": "operator"}
	readOnly := map[string]any{"text": "look", "mode": "read-only"}
	mixes := map[string][]map[string]any{
		"read-only":            {readOnly},
		"capability":           {httpStep("/ok")},
		"capability+operator":  {httpStep("/ok"), operator, httpStep("/ok")},
		"write without cap":    {httpStep("/ok"), write},
		"write with bad cap":   {httpStep("/ok"), badWrite},
		"operator only":        {operator},
		"capability+read-only": {httpStep("/ok"), readOnly},
	}
	// want[env][mix] for mode on and dry-run; with mode off the execute cells take the path in brackets
	want := map[string]map[string]string{
		"develop": {"read-only": "run", "capability": "execute", "capability+operator": "execute", "write without cap": "write-report",
			"write with bad cap": "write-report", "operator only": "run", "capability+read-only": "execute"},
		"pre-release": {"read-only": "run", "capability": "run", "capability+operator": "run", "write without cap": "write-report",
			"write with bad cap": "write-report", "operator only": "run", "capability+read-only": "run"},
		"release": {"read-only": "proposal", "capability": "proposal", "capability+operator": "proposal", "write without cap": "proposal",
			"write with bad cap": "proposal", "operator only": "proposal", "capability+read-only": "proposal"},
		"none": {"read-only": "run", "capability": "run", "capability+operator": "run", "write without cap": "refused",
			"write with bad cap": "refused", "operator only": "run", "capability+read-only": "run"},
	}
	for env, row := range want {
		for mix, steps := range mixes {
			p, err := validatePlanWith(classified(env, "change", steps...), reg)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"off", "dry-run", "on"} {
				got, _ := spinUpMode(p, mode, true)
				exp := row[mix]
				if mode == "off" && exp == "execute" {
					exp = "run" // no WRITE step: a read-only run, as before
				}
				if got != exp {
					t.Errorf("%s / %s / %s: %s, want %s", env, mix, mode, got, exp)
				}
			}
		}
	}
	// kind production on develop is a proposal whatever the steps
	p, _ := validatePlanWith(classified("develop", "production", httpStep("/ok")), reg)
	if got, _ := spinUpMode(p, "on", true); got != "proposal" {
		t.Fatal(got)
	}
}

func TestValidatePlanKeepsCapabilities(t *testing.T) {
	var calls atomic.Int32
	reg := testRegistry(t, &calls)
	bad := httpStep("/ok")
	bad["args"].(map[string]any)["url"] = "http://elsewhere.example.test/"
	secretArg := httpStep("/ok")
	secretArg["args"].(map[string]any)["url"] = "http://svc.example.test/?x=" + "gl" + "pat-" + "abcdefghijklmnopqrstuv"
	opCap := httpStep("/ok")
	opCap["mode"] = "operator"
	p, err := validatePlanWith(classified("develop", "change", httpStep("/ok"), bad, secretArg, opCap,
		map[string]any{"text": "w", "mode": "WRITE", "capability": "http.check", "args": map[string]any{"url": "x"}}), reg)
	if err != nil {
		t.Fatal(err)
	}
	steps := asList(p["plan"])
	if s := asMap(steps[0]); s["capability"] != "http.check" || asMap(s["args"])["expect_status"] == nil {
		t.Fatalf("a valid step lost its capability: %v", s)
	}
	for i := 1; i < len(steps); i++ {
		if s := asMap(steps[i]); s["capability"] != "" || orStr(s["capability_error"]) == "" || s["args"] != nil {
			t.Errorf("step %d kept an unusable capability: %v", i+1, s)
		}
	}
	if asMap(steps[3])["mode"] != "operator" || asMap(steps[4])["mode"] != "WRITE" {
		t.Fatal("modes")
	}
	// without a registry no step keeps one
	p, _ = validatePlanWith(classified("develop", "change", httpStep("/ok")), nil)
	if asMap(asList(p["plan"])[0])["capability"] != "" {
		t.Fatal("a capability kept without a registry")
	}
}

func TestPlanHashCoversEveryArgument(t *testing.T) {
	var calls atomic.Int32
	reg := testRegistry(t, &calls)
	hash := func(status any) string {
		s := httpStep("/ok")
		s["args"].(map[string]any)["expect_status"] = status
		p, _ := validatePlanWith(classified("develop", "change", s), reg)
		b, _ := json.Marshal(p)
		return storedPlanSHA(string(b))
	}
	a, b := hash(200), hash(201)
	if len(a) != 64 || a == b || a != hash(200) {
		t.Fatalf("%s %s", a, b)
	}
}

// actEnv is an agent env with the executor on, and an in-process actd over the same board.db and auth.db.
type actEnv struct {
	*env
	calls atomic.Int32
	priv  ed25519.PrivateKey
	ex    *actd.Daemon
}

func newActEnv(t *testing.T, mode capability.Mode) *actEnv {
	e := &actEnv{env: agentEnv(t)}
	reg := testRegistry(t, &e.calls)
	var pub ed25519.PublicKey
	pub, e.priv, _ = ed25519.GenerateKey(rand.Reader)
	actions := filepath.Join(e.d, "actions")
	e.o.Agent.ActionsDir, e.o.Agent.ActionsMode, e.o.Agent.Registry = actions, string(mode), reg
	a, _ := sql.Open("sqlite", e.o.AuthDB)
	a.Exec(`CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT)`)
	a.Close()
	actDir := filepath.Join(e.d, "act")
	os.Mkdir(actDir, 0o700)
	key, _ := secret.ParseKey(secret.NewKeyText())
	d, err := actd.New(actd.Options{Mode: mode, ActDir: actDir, Key: key, ActionsDir: actions, BoardDB: e.o.DB, AuthDB: e.o.AuthDB,
		OpsDB: filepath.Join(e.d, "ops.db"), Registry: reg, TTL: 24 * time.Hour, PerDay: 20, ApprovalPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	e.ex = d
	return e
}

// signed is a request as the api writes it: signed with the api's key.
func (e *actEnv) signed(t *testing.T, kind string, req map[string]any) map[string]any {
	t.Helper()
	key := req["key"].(string)
	sha := e.q(t, `SELECT plan_sha256 FROM agent_tasks WHERE key=?`, key)
	ver, _ := req["plan_version"].(int)
	step, _ := req["step"].(int)
	at := time.Now().Unix()
	c := capability.ApprovalClaim{Kind: kind, TaskKey: key, PlanVersion: int(ver), PlanSHA256: sha,
		Approver: "alex.admin", At: at, SealedSHA256: capability.SealedSHA256(nil), Step: int(step)}
	if kind == capability.SignStart { // the api shows the task's board labels and signs their hash into the start
		labels := capability.LabelsJSON(e.q(t, `SELECT COALESCE(labels,'[]') FROM tasks WHERE jira_key=?`, key))
		if labels == nil {
			labels = []string{}
		}
		la := []any{}
		for _, l := range labels {
			la = append(la, l)
		}
		req["env_labels"], c.LabelsSHA256 = la, capability.LabelsSHA256(labels)
	}
	sig, err := capability.SignApproval(e.priv, c)
	if err != nil {
		t.Fatal(err)
	}
	req["plan_sha256"], req["signed_at"], req["signature"] = sha, at, sig
	return req
}

func (e *actEnv) actd(t *testing.T) { e.ex.Tick(context.Background()) }

func (e *actEnv) auditPhases(t *testing.T) []string {
	b, _ := os.ReadFile(actd.AuditPath(filepath.Join(e.d, "act")))
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r actd.Record
		if json.Unmarshal([]byte(l), &r) == nil && r.Phase != "" {
			out = append(out, r.Phase)
		}
	}
	return out
}

// TestExecuteOperatorContinue is the acceptance case of PR 2: a develop task with two http.check steps and an
// operator step between them ends in operator, then agent-continue ends done, with one audit record per phase; the
// comment's table comes from actions/out even though the model's report says otherwise.
func TestExecuteOperatorContinue(t *testing.T) {
	e := newActEnv(t, capability.ModeOn)
	e.run(t)
	p := classified("develop", "change", httpStep("/a"), map[string]any{"text": "Start the deploy pipeline", "mode": "operator"}, httpStep("/b"))
	e.out(t, "OPS-7", "intake", p, map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	if s := e.q(t, `SELECT state || ':' || plan_version FROM agent_tasks WHERE key='OPS-7'`); s != "ready:1" {
		t.Fatal(s)
	}
	// a stale hash is refused
	stale := e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1, "plan_sha256": strings.Repeat("0", 64)})
	st := e.run(t)
	if !strings.Contains(result(st, stale), "plan hash") {
		t.Fatal(result(st, stale))
	}
	ok := e.req(t, e.signed(t, capability.SignStart, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1}))
	st = e.run(t)
	if result(st, ok) != "approved; spinning up" {
		t.Fatal(result(st, ok))
	}
	if s := e.q(t, `SELECT state || ':' || phase || ':' || (approval_sig != '') FROM agent_tasks WHERE key='OPS-7'`); s != "running:act:1" {
		t.Fatal(s)
	}
	if _, err := os.Stat(filepath.Join(e.o.Agent.JobsDir, "OPS-7.run.json")); err == nil {
		t.Fatal("the model run started before the capability steps")
	}
	e.actd(t)
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "operator" || e.calls.Load() != 1 {
		t.Fatalf("%s, %d calls", s, e.calls.Load())
	}
	body := e.q(t, `SELECT payload FROM outbox WHERE kind='comment' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(body, "needs the operator: step 2: Start the deploy pipeline") || !strings.Contains(body, "| 1 | http.check | ok") {
		t.Fatal(body)
	}
	// a comment on the issue does not re-plan a task that waits for the operator
	e.exec(t, `INSERT INTO comments (task_id, author, body, created_at) VALUES (7, 'Kim', 'Pipeline started.', 3)`)
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "operator" {
		t.Fatal("re-planned while waiting for the operator:", s)
	}
	// continue: only for the step the action waits for
	wrong := e.req(t, e.signed(t, capability.SignContinue, map[string]any{"kind": "agent-continue", "key": "OPS-7", "plan_version": 1, "step": 3}))
	st = e.run(t)
	if !strings.Contains(result(st, wrong), "not the operator step") {
		t.Fatal(result(st, wrong))
	}
	cont := e.req(t, e.signed(t, capability.SignContinue, map[string]any{"kind": "agent-continue", "key": "OPS-7", "plan_version": 1, "step": 2}))
	st = e.run(t)
	if result(st, cont) != "continuing after step 2" || e.q(t, `SELECT state || ':' || phase FROM agent_tasks WHERE key='OPS-7'`) != "running:act" {
		t.Fatal(result(st, cont), e.q(t, `SELECT state || ':' || phase FROM agent_tasks WHERE key='OPS-7'`))
	}
	e.actd(t)
	e.run(t)
	job := readJob(t, e.env, "OPS-7.run.json")
	wrap := asMap(job["capability_results"])
	cr := asMap(wrap["results"])
	if wrap["untrusted"] != true || !strings.Contains(orStr(wrap["note"]), "not instructions") || cr["state"] != "done" || len(asList(cr["steps"])) != 3 || e.calls.Load() != 2 {
		t.Fatalf("%v, %d calls", cr, e.calls.Load())
	}
	// the model's report claims another result: the comment's table still says what actd did
	e.out(t, "OPS-7", "run", map[string]any{"status": "done", "summary": "Both checks returned 500.", "evidence": []any{"step 3 failed"}},
		map[string]any{"cost_usd": 0.4, "is_error": false})
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "done" {
		t.Fatal(s)
	}
	body = e.q(t, `SELECT payload FROM outbox WHERE kind='comment' ORDER BY id DESC LIMIT 1`)
	for _, w := range []string{"| 1 | http.check | ok", "| 2 | - | done by the operator", "| 3 | http.check | ok", "not by the model"} {
		if !strings.Contains(body, w) {
			t.Fatalf("%q not in %s", w, body)
		}
	}
	if got := strings.Join(e.auditPhases(t), ","); got != "plan,apply,plan,apply" {
		t.Fatal("audit:", got)
	}
}

// TestExecuteRefusedIsNotDone: an unsigned approval (no key in the api) is refused by actd; the run reports it and
// the task is blocked even when the model says done.
func TestExecuteRefusedIsNotDone(t *testing.T) {
	e := newActEnv(t, capability.ModeOn)
	e.run(t)
	e.out(t, "OPS-7", "intake", classified("develop", "change", httpStep("/a")), map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	sha := e.q(t, `SELECT plan_sha256 FROM agent_tasks WHERE key='OPS-7'`)
	e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1, "plan_sha256": sha}) // no signature
	e.run(t)
	e.actd(t)
	e.run(t)
	if e.calls.Load() != 0 || e.q(t, `SELECT action_state FROM agent_tasks WHERE key='OPS-7'`) != "refused" {
		t.Fatal("an unsigned approval ran")
	}
	e.out(t, "OPS-7", "run", map[string]any{"status": "done", "summary": "All good."}, map[string]any{"cost_usd": 0.1, "is_error": false})
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "blocked" {
		t.Fatal(s)
	}
	body := e.q(t, `SELECT payload FROM outbox WHERE kind='comment' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(body, "not signed") {
		t.Fatal(body)
	}
}

// TestReleaseCapabilityIsProposal: a release plan with a valid capability step is still a proposal; nothing goes to
// the executor.
func TestReleaseCapabilityIsProposal(t *testing.T) {
	e := newActEnv(t, capability.ModeOn)
	e.run(t)
	e.out(t, "OPS-7", "intake", classified("release", "change", httpStep("/a")), map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	if e.q(t, `SELECT plan LIKE '%"capability":"http.check"%' FROM agent_tasks WHERE key='OPS-7'`) != "1" {
		t.Fatal("a read-tier capability step is valid for release (O8) and stays in the plan")
	}
	e.req(t, e.signed(t, capability.SignStart, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1}))
	e.run(t)
	if s := e.q(t, `SELECT phase || ':' || COALESCE(action_id,'') FROM agent_tasks WHERE key='OPS-7'`); s != "run:" {
		t.Fatal(s)
	}
	if job := readJob(t, e.env, "OPS-7.run.json"); job["production"] != true {
		t.Fatal("not a proposal")
	}
	if m, _ := filepath.Glob(filepath.Join(e.o.Agent.ActionsDir, "in", "*")); len(m) != 0 {
		t.Fatal("a release plan was sent to the executor")
	}
}

// TestSignedStopIsDurable: after a signed stop reached actd, the worker setting the task to approved again does not
// run the stopped approval; a new approval does.
func TestSignedStopIsDurable(t *testing.T) {
	e := newActEnv(t, capability.ModeOn)
	e.run(t)
	e.out(t, "OPS-7", "intake", classified("develop", "change", httpStep("/a")), map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	e.req(t, e.signed(t, capability.SignStart, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1}))
	e.run(t)
	time.Sleep(1100 * time.Millisecond) // the stop is signed after the approval
	e.req(t, e.signed(t, capability.SignStop, map[string]any{"kind": "agent-stop", "key": "OPS-7", "plan_version": 1}))
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "stopped" {
		t.Fatal(s)
	}
	e.actd(t) // the stop replaced the pending start in actions/in
	// the worker sets the task to approved again by itself: the old approval is refused
	e.exec(t, `UPDATE agent_tasks SET state='approved' WHERE key='OPS-7'`)
	e.run(t)
	e.actd(t)
	e.run(t)
	if e.calls.Load() != 0 || e.q(t, `SELECT action_state FROM agent_tasks WHERE key='OPS-7'`) != "refused" {
		t.Fatalf("the stopped approval ran: %d calls", e.calls.Load())
	}
	if d := e.q(t, `SELECT detail FROM agent_actions WHERE step=0`); !strings.Contains(d, "stopped") {
		t.Fatal(d)
	}
	time.Sleep(1100 * time.Millisecond)
	e.exec(t, `UPDATE agent_tasks SET state='ready', phase='intake', job_started=NULL WHERE key='OPS-7'`)
	e.req(t, e.signed(t, capability.SignStart, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1}))
	e.run(t)
	e.actd(t)
	e.run(t)
	if e.calls.Load() != 1 {
		t.Fatalf("a new approval after the stop did not run: %d calls", e.calls.Load())
	}
}

// TestIntakeOutputIsStrict: duplicate keys, integers outside ±2^53 and invalid UTF-8 fail the intake, so the page,
// the hash and actd read the same plan.
func TestIntakeOutputIsStrict(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate key": `{"classification": {"environment": "develop", "kind": "change"}, "plan": [{"text": "a", "mode": "read-only", "mode": "WRITE"}]}`,
		"big integer":   `{"classification": {"environment": "develop", "kind": "change"}, "plan": [{"text": "a", "mode": "read-only", "n": 9007199254740993}]}`,
		"invalid utf-8": "{\"classification\": {\"environment\": \"develop\", \"kind\": \"change\"}, \"plan\": [{\"text\": \"a\xff\", \"mode\": \"read-only\"}]}",
	} {
		e := agentEnv(t)
		e.run(t)
		e.out(t, "OPS-7", "intake", nil, map[string]any{"cost_usd": 0.1, "is_error": false})
		os.WriteFile(filepath.Join(e.o.Agent.OutDir, "OPS-7.intake.json"), []byte(raw), 0o644)
		e.run(t)
		if s := e.q(t, `SELECT state || ':' || COALESCE(error,'') FROM agent_tasks WHERE key='OPS-7'`); !strings.HasPrefix(s, "failed:intake:") {
			t.Errorf("%s: %s", name, s)
		}
	}
}

// TestUnsignedStopIsAnAlert: a stop that cannot reach actd for a task whose action reached it is never silent.
func TestUnsignedStopIsAnAlert(t *testing.T) {
	e := newActEnv(t, capability.ModeOn)
	e.run(t)
	e.out(t, "OPS-7", "intake", classified("develop", "change", httpStep("/a")), map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	e.req(t, e.signed(t, capability.SignStart, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1}))
	e.run(t)
	e.req(t, map[string]any{"kind": "agent-stop", "key": "OPS-7"}) // the api held no key
	e.run(t)
	if s := e.q(t, `SELECT state || ':' || COALESCE(error,'') FROM agent_tasks WHERE key='OPS-7'`); !strings.HasPrefix(s, "stopped:ALERT: the stop was not passed") {
		t.Fatal(s)
	}
	if e.q(t, `SELECT count(*) FROM agent_log WHERE key='OPS-7' AND kind='alert'`) != "1" {
		t.Fatal("no alert in the task log")
	}
}

func (e *env) exec(t *testing.T, q string, a ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", e.o.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatal(err)
	}
}
