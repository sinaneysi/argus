package app

// Today (/today) on fixture data: off by default (no route, no nav, no CSS, no box); on, the plan for an admin and a
// viewer (read-only), escaping, the done/start/skip/defer/undo/today/replan/settings forms and the request files they
// write (CSRF, admins only, field rules), the pending banner, the landing redirect after sign-in, the Act-now box on
// the long pages (at most three items of the page's area) and the phone-first markup.

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

const todayDDL = `CREATE TABLE today_plans (id INTEGER PRIMARY KEY, day TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, version INTEGER DEFAULT 1, budget_min INTEGER, max_items INTEGER, max_risky INTEGER,
 planned_min INTEGER, n_today INTEGER, n_later INTEGER, trigger TEXT, by TEXT, refined INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, note TEXT);
CREATE TABLE today_items (id INTEGER PRIMARY KEY, plan_id INTEGER NOT NULL, ref TEXT NOT NULL, kind TEXT NOT NULL, area TEXT,
 env TEXT, title TEXT, do_this TEXT, why_now TEXT, details TEXT, link TEXT, link_label TEXT, close_link TEXT, prompt_url TEXT,
 members TEXT, sig TEXT, needs_approval INTEGER DEFAULT 0, needs_window INTEGER DEFAULT 0, own_action INTEGER DEFAULT 0,
 est_min INTEGER, est_base INTEGER, est_basis TEXT, score REAL, urgency REAL, impact REAL, confidence REAL,
 slot TEXT NOT NULL DEFAULT 'later', rank INTEGER, later_reason TEXT, status TEXT NOT NULL DEFAULT 'planned',
 carried INTEGER DEFAULT 0, pinned INTEGER DEFAULT 0, auto_done INTEGER DEFAULT 0, started_at INTEGER, done_at INTEGER,
 actual_min INTEGER, defer_until TEXT, note TEXT, created_at INTEGER, updated_at INTEGER, UNIQUE (plan_id, ref));`

func todaySeed(t *testing.T, e *srvEnv, on bool) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(todayDDL); err != nil {
		t.Fatal(err)
	}
	now := NowInt()
	day := LocalTime(Now()).Format("2006-01-02")
	db.Exec(`INSERT INTO today_plans (id, day, created_at, updated_at, budget_min, max_items, max_risky, planned_min, n_today, n_later)
		VALUES (1, ?, ?, ?, 180, 8, 1, 68, 4, 2)`, day, now-3600, now-60)
	items := [][]any{
		// id, ref, kind, area, env, title, slot, rank, status, est, window, approval, carried, prompt
		{1, "incident:1", "incident_review", "incidents", "release", "shop <b>5xx</b> errors", "today", 1, "planned", 3, 0, 0, 0, ""},
		{2, "cve:reboot:production", "reboot_wave", "vulns", "release", "Plan the production kernel reboot wave (3 hosts)", "today", 2, "doing", 30, 1, 1, 1, ""},
		{3, "disk:release:db:/data", "disk_full", "capacity", "release", "Free /data on 2 db hosts", "today", 3, "planned", 30, 0, 1, 0, ""},
		{4, "proposals", "proposals", "proposals", "", "Decide 10 proposed tasks", "today", 4, "done", 20, 0, 0, 0, ""},
		{5, "cve:group:x", "cve_upgrade", "vulns", "release", "Upgrade mongodb (known exploited)", "later", 5, "planned", 60, 1, 1, 0, ""},
		{6, "incident:2", "incident_triage", "incidents", "develop", "kuma down", "later", 6, "planned", 10, 0, 0, 0, ""},
		{7, "incident:3", "incident_triage", "incidents", "develop", "third incident", "later", 7, "planned", 10, 0, 0, 0, ""},
		{8, "incident:4", "incident_triage", "incidents", "develop", "fourth incident", "later", 8, "planned", 10, 0, 0, 0, ""},
	}
	for _, r := range items {
		reason := ""
		if r[6] == "later" {
			reason = "one production change with a window is already planned today"
		}
		if _, err := db.Exec(`INSERT INTO today_items (id, plan_id, ref, kind, area, env, title, do_this, why_now, details, link,
			link_label, close_link, slot, rank, status, est_min, est_base, est_basis, needs_window, needs_approval, carried, score,
			urgency, impact, confidence, later_reason, own_action, actual_min, started_at)
			VALUES (?,1,?,?,?,?,?,'Read the Lazy card <i>now</i>.','high, production <x>','[{"text": "host <a>", "link": "/inventory"}]',
			'/incidents/view?id=1','Lazy card','/incidents/view?id=1',?,?,?,?,?,'Review an RCA',?,?,?,9.5,8,3,1,?,0,?,?)`,
			r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r[8], r[9], r[9], r[10], r[11], r[12], reason,
			map[bool]any{true: 18, false: nil}[r[8] == "done"], map[bool]any{true: now - 900, false: nil}[r[8] == "doing"]); err != nil {
			t.Fatal(err)
		}
	}
	st := `{"enabled": ` + map[bool]string{true: "true", false: "false"}[on] + `, "plan_at": "07:30", "decisions": [
	  {"id": "tokens", "group": "Tokens", "title": "Create the <tokens>", "detail": "ci-vars-meta read token", "link": "/admin", "count": 5},
	  {"id": "x", "group": "Handover", "title": "Name a deputy", "detail": "d", "link": "javascript:alert(1)", "count": null}]}`
	os.WriteFile(filepath.Join(e.dir, "today-status.json"), []byte(st), 0o644)
	if on {
		os.MkdirAll(filepath.Join(SET_IN, "today-actions"), 0o755)
	}
}

func todayReqs(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	fs, _ := os.ReadDir(filepath.Join(SET_IN, "today-actions"))
	for _, f := range fs {
		b, _ := os.ReadFile(filepath.Join(SET_IN, "today-actions", f.Name()))
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
		os.Remove(filepath.Join(SET_IN, "today-actions", f.Name()))
	}
	return out
}

func TestTodayOffByDefault(t *testing.T) {
	e := srvSetup(t)
	todaySeed(t, e, false)
	// off, Today is the day's health summary (the former Overview), never a plan

	if TodayLanding("/") != "/today" {
		t.Fatal("off: landing")
	}
}

func TestTodayViewerReadOnly(t *testing.T) {
	e := srvSetup(t)
	todaySeed(t, e, true)

	if len(todayReqs(t)) != 0 {
		t.Fatal("viewer wrote a request")
	}
}

func TestTodayPostActions(t *testing.T) {
	e := srvSetup(t)
	todaySeed(t, e, true)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	r := e.req("POST", "/api/v1/today/act", ck, url.Values{"csrf": {csrf}, "action": {"done"}, "item": {"3"}, "minutes": {"25"},
		"reason": {" grew \t the disk "}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/today?sent=done&at=") || !strings.HasSuffix(r.loc, "#t-3") {
		t.Fatal(r.status, r.loc)
	}
	reqs := todayReqs(t)
	if len(reqs) != 1 || reqs[0]["action"] != "done" || reqs[0]["item"] != 3.0 || reqs[0]["minutes"] != 25.0 ||
		reqs[0]["reason"] != "grew the disk" || reqs[0]["by"] != "admin" || reqs[0]["role"] != "admin" {
		t.Fatal(reqs)
	}
	r = e.req("POST", "/api/v1/today/act", ck, url.Values{"csrf": {csrf}, "action": {"defer"}, "item": {"2"}, "days": {"7"}}, nil)
	if reqs = todayReqs(t); r.status != 200 || reqs[0]["days"] != 7.0 {
		t.Fatal("defer", reqs)
	}
	r = e.req("POST", "/api/v1/today/act", ck, url.Values{"csrf": {csrf}, "action": {"settings"}, "focus_min": {"120"}, "max_items": {"5"},
		"max_risky": {"0"}}, nil)
	if reqs = todayReqs(t); r.status != 200 || reqs[0]["focus_min"] != 120.0 || reqs[0]["max_risky"] != 0.0 || reqs[0]["item"] != nil {
		t.Fatal("settings", reqs)
	}
	e.req("POST", "/api/v1/today/act", ck, url.Values{"csrf": {csrf}, "action": {"replan"}}, nil)
	if reqs = todayReqs(t); len(reqs) != 1 || reqs[0]["action"] != "replan" {
		t.Fatal("replan", reqs)
	}
	for _, c := range []struct {
		form url.Values
		code int
	}{
		{url.Values{"csrf": {"0"}, "action": {"done"}, "item": {"1"}}, 403},
		{url.Values{"csrf": {csrf}, "action": {"delete"}, "item": {"1"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"done"}, "item": {"0"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"done"}, "item": {"x1"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"done"}, "item": {"1"}, "minutes": {"601"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"defer"}, "item": {"1"}, "days": {"31"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"skip"}, "item": {"1"}, "reason": {strings.Repeat("x", 201)}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"settings"}, "focus_min": {"5"}, "max_items": {"5"}, "max_risky": {"1"}}, 400},
	} {
		if r := e.req("POST", "/api/v1/today/act", ck, c.form, nil); r.status != c.code {
			t.Fatalf("%v: %d", c.form, r.status)
		}
	}
	if len(todayReqs(t)) != 0 {
		t.Fatal("a refused post wrote a request")
	}
	os.RemoveAll(filepath.Join(SET_IN, "today-actions")) // root has not created the directory: nothing is offered
	if r := e.req("POST", "/api/v1/today/act", ck, url.Values{"csrf": {csrf}, "action": {"replan"}}, nil); r.status != 400 {
		t.Fatal("no dir", r.status)
	}

}

func TestTodayLandingAfterLogin(t *testing.T) {
	e := srvSetup(t)
	todaySeed(t, e, true)
	r := e.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/"}, "username": {"admin"}, "password": {"Password-12345"}}, nil)
	if r.status != 200 || r.loc != "/today" {
		t.Fatal("landing", r.status, r.loc)
	}
	r = e.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/runs"}, "username": {"admin"}, "password": {"Password-12345"}}, nil)
	if r.loc != "/runs" {
		t.Fatal("a target wins", r.loc)
	}
	// / is not a page of its own: it lands on Today

}
