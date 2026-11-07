package app

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeTokenSetup(t *testing.T) (*srvEnv, map[string]string, string, *bytes.Buffer) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	old := CLAUDE_EGRESS_CHECK
	CLAUDE_EGRESS_CHECK = filepath.Join(e.dir, "claude-egress-check.json")
	os.MkdirAll(filepath.Join(SET_IN, "claude-token"), 0o755)
	var log bytes.Buffer
	Out = &log
	t.Cleanup(func() { CLAUDE_EGRESS_CHECK = old })
	return e, map[string]string{"opsr_session": tok}, csrf, &log
}

func claudeRequests(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(SET_IN, "claude-token", "*.json"))
	return m
}

func TestClaudeTokenPage(t *testing.T) {

	// status only, never a value: the page shows the last 4 characters root published
	os.WriteFile(filepath.Join(SET_OUT, "claude-token-status.json"), []byte(`{"token": {"CLAUDE_CODE_OAUTH_TOKEN": {"set": true,
		"last4": "wxyz"}, "ANTHROPIC_API_KEY": {"set": false}}, "replaced": {"at": 1700000000, "by": "admin", "last4": "wxyz"},
		"test": {"at": 1700000000, "by": "admin", "result": "error", "message": "Your organization has disabled Claude subscription access"}}`), 0o644)
	os.WriteFile(CLAUDE_EGRESS_CHECK, []byte(`{"checked_at": 1700000000, "ip": "203.0.113.9", "loc": "IR", "allowed": false,
		"reason": "AI stopped: egress IP 203.0.113.9 is in IR", "stopped": 3, "last_stopped_at": 1700000000}`), 0o644)

	// guard off: no stop banner, the card says so
	os.WriteFile(filepath.Join(SET_OUT, "claude-egress.json"), []byte(`{"guard": false, "blocked": ["IR"], "fail": "closed"}`), 0o644)
	if stopped, _ := claudeEgressStopped(); stopped {
		t.Fatal("guard off: not stopped")
	}
}

func TestClaudeTokenReplaceWritesOneRequest(t *testing.T) {
	e, ck, csrf, log := claudeTokenSetup(t)
	token := "sk-ant-oat01-" + strings.Repeat("Ab3_-", 20)
	// bad shapes: nothing written, the value never echoed
	for _, bad := range []string{"", "sk-ant-api03-" + strings.Repeat("a", 40), "sk-ant-oat01-short", "sk-ant-oat01-" + strings.Repeat("a", 30) + "<x>",
		"sk-ant-oat01-" + strings.Repeat("a", 301)} {
		r := e.req("POST", "/api/v1/admin/claude-token", ck, url.Values{"csrf": {csrf}, "action": {"replace"}, "token": {bad}}, nil)
		if r.status != 400 || !strings.Contains(r.body, "It starts with sk-ant-oat01-") || (bad != "" && strings.Contains(r.body, bad)) ||
			!strings.Contains(r.body, `"field":"token"`) || !strings.Contains(r.body, `"form":"claude-token"`) {
			t.Fatalf("bad token %q: %d", bad, r.status)
		}
	}
	if r := e.req("POST", "/api/v1/admin/claude-token", ck, url.Values{"csrf": {csrf}, "action": {"replace"}, "kind": {"api"}, "token": {token}}, nil); r.status != 400 {
		t.Fatalf("API key kind accepted: %d", r.status)
	}
	if n := len(claudeRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written for rejected input", n)
	}
	r := e.req("POST", "/api/v1/admin/claude-token", ck, url.Values{"csrf": {csrf}, "action": {"replace"}, "token": {"  " + token + "\n"},
		"save_anyway": {"1"}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/admin/claude?ct=") {
		t.Fatalf("replace: %d %s", r.status, r.loc)
	}
	reqs := claudeRequests(t)
	if len(reqs) != 1 {
		t.Fatalf("requests: %v", reqs)
	}
	st, _ := os.Stat(reqs[0])
	if st.Mode().Perm() != 0o600 || !strings.HasPrefix(filepath.Base(reqs[0]), "admin-") {
		t.Fatalf("request file %s mode %v", reqs[0], st.Mode())
	}
	var doc map[string]any
	b, _ := os.ReadFile(reqs[0])
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["kind"] != "replace" || doc["var"] != "CLAUDE_CODE_OAUTH_TOKEN" || doc["token"] != token || doc["save_anyway"] != true || doc["user"] != "admin" {
		t.Fatalf("request: %v", doc["kind"])
	}
	if strings.Contains(log.String(), token) || strings.Contains(log.String(), token[13:30]) {
		t.Fatal("the token reached the log")
	}
	// while root has not run: working, the page refreshes; the token is never on the page
	// root's result
	rid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(reqs[0]), "admin-"), ".json")
	os.Remove(reqs[0])
	os.WriteFile(filepath.Join(SET_OUT, "claude-token-status.json"), []byte(`{"results": {"`+rid+`": {"user": "admin",
		"result": "replaced …b3_-, test error: Invalid API key; previous token restored"}}}`), 0o644)

	// test and egress: no secret, one request each
	for _, act := range []string{"test", "egress"} {
		r := e.req("POST", "/api/v1/admin/claude-token", ck, url.Values{"csrf": {csrf}, "action": {act}}, nil)
		if r.status != 200 {
			t.Fatalf("%s: %d", act, r.status)
		}
	}
	if n := len(claudeRequests(t)); n != 2 {
		t.Fatalf("test + egress: %d requests", n)
	}
}

func TestClaudeEgressSettingsSave(t *testing.T) {
	e, ck, csrf, _ := claudeTokenSetup(t)
	for _, bad := range []url.Values{{"blocked": {"IRN"}}, {"blocked": {"I1"}}, {"fail": {"maybe"}}} {
		bad.Set("csrf", csrf)
		if r := e.req("POST", "/api/v1/admin/claude-egress", ck, bad, nil); r.status != 400 {
			t.Fatalf("bad %v accepted: %d", bad, r.status)
		}
	}
	if n := len(claudeRequests(t)); n != 0 {
		t.Fatalf("%d request(s) for rejected input", n)
	}
	r := e.req("POST", "/api/v1/admin/claude-egress", ck, url.Values{"csrf": {csrf}, "blocked": {"ir, kp ir"}, "fail": {"open"}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/admin/claude?ct=") {
		t.Fatalf("save: %d %s", r.status, r.loc)
	}
	reqs := claudeRequests(t)
	if len(reqs) != 1 {
		t.Fatal(reqs)
	}
	var doc map[string]any
	b, _ := os.ReadFile(reqs[0])
	json.Unmarshal(b, &doc)
	bl, _ := doc["blocked"].([]any)
	if doc["kind"] != "egress-settings" || doc["guard"] != false || doc["fail"] != "open" || len(bl) != 2 || bl[0] != "IR" || doc["user"] != "admin" {
		t.Fatalf("request: %s", b)
	}
	// defaults until root has written the live copy; then the live copy
	if g, bl, f := ClaudeEgressSettings(); !g || strings.Join(bl, ",") != "IR" || f != "closed" {
		t.Fatalf("defaults: %v %v %v", g, bl, f)
	}
	os.WriteFile(filepath.Join(SET_OUT, "claude-egress.json"), []byte(`{"guard": false, "blocked": ["IR", "KP"], "fail": "open"}`), 0o644)
	if g, bl, f := ClaudeEgressSettings(); g || len(bl) != 2 || f != "open" {
		t.Fatalf("live: %v %v %v", g, bl, f)
	}
}

func TestClaudeTokenNeeds2FAAndAdmin(t *testing.T) {
	e := srvSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "claude-token"), 0o755)
	token := "sk-ant-oat01-" + strings.Repeat("a", 40)
	tok, csrf := e.loginNoMFA("admin")
	ck := map[string]string{"opsr_session": tok}
	for _, p := range []string{"/api/v1/admin/claude-token", "/api/v1/admin/claude-egress"} {
		if PolicyOf(p, nil) != PolicySensitive {
			t.Fatalf("%s must be sensitive", p)
		}
		if r := e.req("POST", p, ck, url.Values{"csrf": {csrf}, "action": {"replace"}, "token": {token}}, nil); r.status != 403 {
			t.Fatalf("%s without 2FA: %d", p, r.status)
		}
	}
	at, _ := e.login("admin")
	if r := e.req("POST", "/api/v1/admin/claude-token", map[string]string{"opsr_session": at}, url.Values{"csrf": {"wrong"}, "action": {"test"}}, nil); r.status != 403 {
		t.Fatalf("bad CSRF: %d", r.status)
	}
	vt, vcsrf := e.login("view.er")
	vck := map[string]string{"opsr_session": vt}

	for _, p := range []string{"/api/v1/admin/claude-token", "/api/v1/admin/claude-egress"} {
		if r := e.req("POST", p, vck, url.Values{"csrf": {vcsrf}, "action": {"test"}}, nil); r.status != 403 {
			t.Fatalf("viewer %s: %d", p, r.status)
		}
	}
	if n := len(claudeRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written without 2FA, CSRF or the admin role", n)
	}
}
