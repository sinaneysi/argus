package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
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
	status := func(age int64, chainOK bool) {
		b, _ := json.Marshal(map[string]any{"ts": NowInt() - age, "mode": "dry-run", "version": "test", "chain": map[string]any{"ok": chainOK, "records": 4, "error": "edited line 3"},
			"capabilities": []string{"http.check", "gitlab.ci-variable.get"}, "credentials": []any{}, "approval_key": "ab12", "seal_key": ""})
		os.WriteFile(filepath.Join(ACTIONS_DIR, "actd-status.json"), b, 0o644)
	}
	for _, c := range []struct {
		age  int64
		ok   bool
		kind string
	}{{10, true, "ok"}, {300, true, "warn"}, {900, true, "fail"}, {10, false, "fail"}} {
		status(c.age, c.ok)
		hb := apiAdminActions()["heartbeat"].(map[string]any)
		if hb["kind"] != c.kind || hb["seen"] != true {
			t.Fatalf("age %d chain %v: %v", c.age, c.ok, hb)
		}
		if c.kind == "ok" && (hb["chain"] != "OK, 4 records" || hb["capabilities"].([]string)[0] != "gitlab.ci-variable.get" || hb["approvalKey"] != true || hb["sealKey"] != false) {
			t.Fatal(hb)
		}
	}

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
}
