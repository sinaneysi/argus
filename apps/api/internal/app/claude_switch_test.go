package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSwitchSettings(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	settings := filepath.Join(SET_IN, "settings.json")
	live := filepath.Join(SET_OUT, "claude.json")

	if PolicyOf("/admin/claude", nil) != PolicySensitive {
		t.Fatal("/admin/claude must be a sensitive POST (2FA recent)")
	}
	if !ClaudeOn(nil) || ClaudeOffBanner() != "" {
		t.Fatal("no file: Claude usage is on, no banner")
	}

	// without saved settings root would apply defaults (checks off): refuse
	r := e.req("POST", "/api/v1/admin/claude", ck, url.Values{"csrf": {csrf}, "claude": {"off"}}, nil)
	if !strings.Contains(r.body, "Save the general settings once first") {
		t.Fatal("must not write a default settings.json")
	}
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("settings.json written")
	}

	os.WriteFile(settings, []byte(`{"tasks": {"log-rca": {"schedule": "4h", "enabled": true}}, "qa_ask": {"enabled": true}}`), 0o644)
	r = e.req("POST", "/api/v1/admin/claude", ck, url.Values{"csrf": {csrf}, "claude": {"maybe"}}, nil)
	if !strings.Contains(r.body, "Choose on or off.") {
		t.Fatal("bad value accepted")
	}
	r = e.req("POST", "/api/v1/admin/claude", ck, url.Values{"csrf": {csrf}, "claude": {"off"}}, nil)
	if r.status != 200 || !strings.Contains(r.body, "Claude usage off.") {
		t.Fatalf("save: %d", r.status)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(settings)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["claude"].(map[string]any)["enabled"] != false || cfg["tasks"] == nil || cfg["qa_ask"] == nil || cfg["updated_by"] != "admin" {
		t.Fatalf("settings.json: %s", b)
	}
	if ClaudeOn(nil) {
		t.Fatal("saved off, not live yet: off")
	}

	// the live copy root writes wins (also changed by the CLI)
	os.WriteFile(live, []byte(`{"enabled": true}`), 0o644)
	if !ClaudeOn(nil) {
		t.Fatal("live on")
	}
	os.WriteFile(live, []byte(`{"enabled": false}`), 0o644)
	if ClaudeOn(nil) || !strings.Contains(ClaudeOffBanner(), "Claude paused") {
		t.Fatal("live off: banner")
	}

	os.WriteFile(live, []byte(`{not json`), 0o644)
	if ClaudeOn(nil) {
		t.Fatal("unreadable live copy: off, like bin/claude_gate.py")
	}

	// the general form keeps the switch as it is live
	os.WriteFile(live, []byte(`{"enabled": false}`), 0o644)
	form := url.Values{"csrf": {csrf}}
	for _, at := range ADMIN_TASKS {
		form.Set("sched:"+at[0], "4h")
	}
	for _, l := range LIMITS {
		form.Set("th:"+l.k, I2S(l.lo))
	}
	form.Set("th:cert_warn_days", "30")
	form.Set("th:cert_fail_days", "14")
	for _, s := range RCA_AUTO {
		if _, isBool := s.def.(bool); !isBool {
			form.Set("ra:"+s.k, I2S(s.lo))
		}
	}
	if r := e.req("POST", "/api/v1/admin/save", ck, form, nil); !strings.Contains(r.body, "Saved.") {
		t.Fatalf("general save failed: %s", r.body[:200])
	}
	b, _ = os.ReadFile(settings)
	json.Unmarshal(b, &cfg)
	if cfg["claude"].(map[string]any)["enabled"] != false {
		t.Fatalf("general save flipped the Claude switch: %s", b)
	}
}

func TestClaudeSwitchNeeds2FA(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.loginNoMFA("admin")
	os.WriteFile(filepath.Join(SET_IN, "settings.json"), []byte(`{"tasks": {}}`), 0o644)
	r := e.req("POST", "/api/v1/admin/claude", map[string]string{"opsr_session": tok}, url.Values{"csrf": {csrf}, "claude": {"off"}}, nil)
	if r.status != 403 {
		t.Fatalf("POST without 2FA: %d", r.status)
	}
	b, _ := os.ReadFile(filepath.Join(SET_IN, "settings.json"))
	if strings.Contains(string(b), "claude") {
		t.Fatal("written without 2FA")
	}
	vt, vcsrf := e.login("view.er")
	if r := e.req("POST", "/api/v1/admin/claude", map[string]string{"opsr_session": vt}, url.Values{"csrf": {vcsrf}, "claude": {"off"}}, nil); r.status != 403 {
		t.Fatalf("viewer: %d", r.status)
	}
}
