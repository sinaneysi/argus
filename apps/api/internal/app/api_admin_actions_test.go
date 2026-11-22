package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/py"
)

// ADR-0022 PR 5: the plan view shows a capability step's arguments; a sealed input only by its name, a literal in
// a secret field never.
func TestAPIStepArgs(t *testing.T) {
	args := py.NewOMap().Set("project", "group/app").Set("value_ref", "input:NEW_KEY").Set("expect_status", []any{int64(200), int64(204)}).
		Set("matches", "literal-"+"SECRET-77").Set("masked", true)
	got := apiStepArgs(args)
	b, _ := json.Marshal(got)
	s := string(b)
	if strings.Contains(s, "SECRET-77") {
		t.Fatal("a literal in a secret field reached the page:", s)
	}
	want := [][2]string{{"expect_status", "200, 204"}, {"masked", "true"}, {"matches", "<redacted>"}, {"project", "group/app"}, {"value_ref", "NEW_KEY"}}
	if len(got) != len(want) {
		t.Fatal(s)
	}
	for i, w := range want {
		if got[i]["name"] != w[0] || got[i]["value"] != w[1] || (got[i]["input"] == true) != (w[0] == "value_ref") {
			t.Fatalf("arg %d: %v, want %v", i, got[i], w)
		}
	}
	if l := apiStepArgs(nil); l == nil || len(l) != 0 {
		t.Fatal("no arguments must be an empty list")
	}
}

// The admin block: the mode and the allow-lists read-only, the switch, actd's heartbeat from its status file.
func TestAdminActionsBlockAndStopSwitch(t *testing.T) {
	e := boardSetup(t)
	mode, adir, read := ACTIONS_MODE, ACTIONS_DIR, actionsSetting
	ACTIONS_DIR = t.TempDir()
	t.Cleanup(func() { ACTIONS_MODE, ACTIONS_DIR, actionsSetting = mode, adir, read })
	cfg := map[string]string{"ARGUS_ACT_HTTP_HOSTS": "health.example.com, *.svc.example.com", "ARGUS_ACT_GITLAB_PROJECTS": "group/app"}
	actionsSetting = func(k string) string { return cfg[k] }

	ACTIONS_MODE = "off"
	a := apiAdminActions()
	hb := a["heartbeat"].(map[string]any)
	if a["mode"] != "off" || a["stopped"] != false || hb["seen"] != false || hb["kind"] != "neutral" {
		t.Fatalf("%v", a)
	}
	lists := a["lists"].([]map[string]any)
	if v := lists[0]["values"].([]string); len(v) != 2 || v[1] != "*.svc.example.com" {
		t.Fatal(lists[0])
	}
	if v := lists[2]["values"].([]string); len(v) != 0 {
		t.Fatal(lists[2])
	}
	ACTIONS_MODE = "on"
	if hb := apiAdminActions()["heartbeat"].(map[string]any); hb["kind"] != "fail" {
		t.Fatal("mode on without a status must fail", hb)
	}
	ACTIONS_MODE = "sometimes"
	if apiAdminActions()["mode"] != "invalid" {
		t.Fatal("an unknown mode must show as invalid")
	}
	ACTIONS_MODE = "dry-run"
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	pin := func() (ed25519.PublicKey, error) { return pub, nil }
	pinned := actdStatusPub
	t.Cleanup(func() { actdStatusPub = pinned })
	status := func(age int64, chainOK bool, signer ed25519.PrivateKey) {
		st := actd.Status{TS: NowInt() - age, Mode: "dry-run", Version: "test", Chain: actd.ChainStatus{OK: chainOK, Records: 4, Error: "edited line 3"},
			Capabilities: []string{"http.check", "gitlab.ci-variable.get"}, Credentials: []actd.Credential{}, ApprovalKey: "ab12"}
		if signer != nil {
			body, _ := json.Marshal(st)
			st.Sig = capability.SignStatus(signer, body)
		}
		b, _ := json.Marshal(st)
		os.WriteFile(filepath.Join(ACTIONS_DIR, "actd-status.json"), b, 0o644)
	}
	// signed by actd's pinned key and fresh: shown as verified facts
	actdStatusPub = pin
	for _, c := range []struct {
		age  int64
		ok   bool
		kind string
	}{{10, true, "ok"}, {300, true, "warn"}, {10, false, "fail"}} {
		status(c.age, c.ok, key)
		hb := apiAdminActions()["heartbeat"].(map[string]any)
		if hb["kind"] != c.kind || hb["seen"] != true || hb["verified"] != true {
			t.Fatalf("age %d chain %v: %v", c.age, c.ok, hb)
		}
		if c.kind == "ok" && (hb["chain"] != "OK, 4 records" || hb["capabilities"].([]string)[0] != "gitlab.ci-variable.get" || hb["approvalKey"] != true || hb["sealKey"] != false) {
			t.Fatal(hb)
		}
	}
	// not verified: never "Verified" or the chain as a fact
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	for name, setup := range map[string]func(){
		"unsigned":      func() { status(10, true, nil) },
		"another key":   func() { status(10, true, otherKey) },
		"too old":       func() { status(900, true, key) },
		"in the future": func() { status(-900, true, key) },
		"no pinned key": func() {
			status(10, true, key)
			actdStatusPub = func() (ed25519.PublicKey, error) { return nil, errors.New("none pinned") }
		},
	} {
		actdStatusPub = pin
		setup()
		hb := apiAdminActions()["heartbeat"].(map[string]any)
		if hb["verified"] != false || hb["seen"] != false || hb["text"] != "Not verified" || hb["chain"] != nil || len(hb["capabilities"].([]string)) != 0 {
			t.Fatalf("%s: %v", name, hb)
		}
	}
	actdStatusPub = pin

	// the switch: admins only (adminPost), 1 or 0, in the audit log, read by actd as auth_settings actions.stopped
	if _, e := ActionsStopSave("yes", e.admin, "198.51.100.7"); e == nil || e.code != 400 {
		t.Fatal("a value other than 1 or 0 must be refused")
	}
	if msg, err := ActionsStopSave("1", e.admin, "198.51.100.7"); err != nil || !strings.Contains(msg, "stopped") {
		t.Fatal(msg, err)
	}
	if auth.Setting("actions.stopped") != "1" || !ActionsStopped() || apiAdminActions()["stopped"] != true {
		t.Fatal("the switch did not set actions.stopped")
	}
	found := false
	for _, r := range auth.AuditLog(5) {
		found = found || (r.Action == "actions.stopped" && r.Actor == e.admin.Username)
	}
	if !found {
		t.Fatal("the stop is not in the audit log")
	}
	if _, err := ActionsStopSave("0", e.admin, "198.51.100.7"); err != nil || ActionsStopped() {
		t.Fatal("resume did not clear the switch")
	}
	// the page reads the switch as actd does: any value other than empty or "0" is stopped
	for v, want := range map[string]bool{"": false, "0": false, " 0 ": false, "1": true, "yes": true, "true": true} {
		auth.SetSetting("actions.stopped", v)
		if ActionsStopped() != want {
			t.Errorf("%q: stopped %v", v, !want)
		}
	}
}

// ADR-0022 PR 5 review: the approval shows the classification and its source; WRITE steps execute only when the
// board's labels say stage too.
func TestSpinEnvNeedsBoardLabel(t *testing.T) {
	e := boardSetup(t)
	mode := ACTIONS_MODE
	ACTIONS_MODE = "on"
	t.Cleanup(func() { ACTIONS_MODE = mode })
	plan := py.AsMap(agentJSON(`{"classification": {"environment": "develop", "kind": "change"}, "plan": [`+
		`{"n": 1, "text": "set", "mode": "WRITE", "capability": "gitlab.ci-variable.set", "args": {"project": "group/app", "key": "K", "environment_scope": "develop", "value_ref": "input:K"}}]}`, "null"))
	for labels, want := range map[string]bool{`["env:stage"]`: true, `["ops"]`: false, `["env:stage","env:production"]`: false, `["env:production"]`: false} {
		e.board.Exec(`DELETE FROM tasks WHERE id=9`)
		e.board.Exec(`INSERT INTO tasks (id, title, status, created_at, updated_at, gone, tracker, labels) VALUES (9, 't', 'todo', 1, 1, 0, 'gitlab', ?)`, labels)
		if got := agentExecutes(plan, "gl-9"); got != want {
			t.Errorf("%s: executes %v", labels, got)
		}
		env := apiSpinEnv(plan, "gl-9")
		if env["model"] != "develop" || env["boardOk"] != want || env["matters"] != true || env["board"] == "" {
			t.Errorf("%s: %v", labels, env)
		}
		if path, _ := agentSpinUp(plan, "gl-9"); (path == "execute") != want {
			t.Errorf("%s: path %s", labels, path)
		}
	}
	if agentExecutes(plan, "") {
		t.Fatal("no task, no evidence: a write plan must not execute")
	}
}
