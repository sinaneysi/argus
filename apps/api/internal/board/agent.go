package board

// agent.go: the task agent's dispatcher inside the board sync (the port of the reference task agent, ADR-0013 P1 and
// docs/ROADMAP.md "Task-agent port requirement"). Argus works the tasks that sit in the agent column (default
// In Progress) and are assigned to an admin: Jira tasks (key = the issue key) and GitLab tasks (key = gl-<task id>).
//
//	intake  read-only planning: a job file <jobs>/<key>.intake.json for the model runner (internal/taskagent, a worker
//	        job). Starts once the task stayed in scope for the debounce time, and again when its description,
//	        comments or the admin's answers/instructions change. Its plan: classification (environment, kind), steps
//	        (read-only or WRITE with targets), up to 5 questions, needs, estimate, confidence.
//	run     "Spin up agent" (an admin) approves exactly the plan version shown:
//	        - release (or kind production) -> a read-only run that writes the proposal for the operator; blocked;
//	        - WRITE steps without an environment -> refused (the admin names the environment; it plans again);
//	        - WRITE steps on develop / pre-release -> the fixer is not ported yet: a read-only run reports the WRITE
//	          steps for a person (status blocked), nothing is changed;
//	        - everything else -> a read-only run.
//
// Never executed: production, and every WRITE step (this version has no fixer). Everything the model reads from the
// tracker is untrusted (the prompts say so); the plan the admin approved is the only authority. Limits: the Claude
// usage switch and the egress guard (nothing starts; tasks wait), the task budget (intake + run spend per task), 20
// intakes and 3 runs a day, one job at a time. Results go back as a comment on the issue (through the outbox) and a
// notice; the issue is never moved by itself unless "auto-move to Done" is on (GitLab tasks only in this port).
//
// Not ported (their requests are refused with a reason): the fixer and its column move, task secrets, lessons, the
// QA intake. Differences from the reference job: the task's text comes from board.db (the board sync mirrors the
// summary, description and comments) instead of a fresh tracker read, so attachments and issue links are not in
// the task file; the model answers the JSON object as its final text (no Write tool).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/notify"
)

// AgentOptions turn the task agent on in the board sync (nil: its requests are refused as not handled).
type AgentOptions struct {
	JobsDir      string                // job files for the model runner
	OutDir       string                // its results (and the progress files the task page reads)
	ClaudeStatus string                // Settings -> Claude status file (claude-token-status.json): the token replacement
	Guard        func() (bool, string) // the model may start now (usage switch, egress guard, a configured provider)
	Environments string                // the installation's environments, for the prompt ("develop: ..., release: ...")
	Debounce     time.Duration         // a task must stay in scope this long before its intake (default 120 s)
	TaskBudget   float64               // USD per task: intake + run (default 8)
	IntakeTO     time.Duration         // a job without a result after this fails (default 12 min)
	RunTO        time.Duration         // the same for a run (default 65 min)
}

const (
	agentIntakePerDay = 20
	agentRunsPerDay   = 3
	agentLimitPrefix  = "claude-limit:"
	agentMaxOut       = 256 * 1024
)

var (
	agentKinds   = []string{"investigate", "documentation", "change", "production", "code", "unclear"}
	agentEnvs    = []string{"develop", "pre-release", "release", "none"}
	agentFixEnvs = []string{"develop", "pre-release"}
	glKeyRe      = regexp.MustCompile(`^gl-([0-9]{1,9})$`)
)

const agentSchema = `
CREATE TABLE IF NOT EXISTS agent_tasks (
  key TEXT PRIMARY KEY, state TEXT NOT NULL DEFAULT 'planning', phase TEXT,
  assignee TEXT, argus_user TEXT, context_hash TEXT, job_started INTEGER,
  plan_version INTEGER DEFAULT 0, plan TEXT, intake_at INTEGER, intake_cost REAL,
  approved_version INTEGER, approved_by TEXT, approved_at INTEGER,
  run_started INTEGER, run_finished INTEGER, run_cost REAL, result TEXT, fix_request TEXT,
  error TEXT, updated INTEGER, plan_entered_at INTEGER, spent_usd REAL DEFAULT 0,
  retry_at INTEGER, limit_since INTEGER, error_detail TEXT);
CREATE TABLE IF NOT EXISTS agent_inputs (key TEXT PRIMARY KEY, instructions TEXT, answers TEXT, post_jira INTEGER DEFAULT 1,
  by TEXT, at INTEGER);
CREATE TABLE IF NOT EXISTS agent_log (id INTEGER PRIMARY KEY, key TEXT, at INTEGER, kind TEXT, text TEXT);
CREATE INDEX IF NOT EXISTS agent_log_key ON agent_log(key, at);
`

var agentCols = [][2]string{{"argus_user", "TEXT"}, {"plan_entered_at", "INTEGER"}, {"spent_usd", "REAL DEFAULT 0"},
	{"retry_at", "INTEGER"}, {"limit_since", "INTEGER"}, {"error_detail", "TEXT"}}

func (a *AgentOptions) defaults() {
	if a.Debounce <= 0 {
		a.Debounce = 120 * time.Second
	}
	if a.TaskBudget <= 0 {
		a.TaskBudget = 8
	}
	if a.IntakeTO <= 0 {
		a.IntakeTO = 12 * time.Minute
	}
	if a.RunTO <= 0 {
		a.RunTO = 65 * time.Minute
	}
}

// agentEnsure creates the agent tables when missing and adds missing columns (an older database).
func (r *run) agentEnsure() {
	for _, stmt := range strings.Split(agentSchema, ";") {
		if strings.TrimSpace(stmt) != "" {
			r.must(stmt)
		}
	}
	have := map[string]bool{}
	for _, c := range r.rows(`PRAGMA table_info(agent_tasks)`) {
		have[c.s("name")] = true
	}
	for _, c := range agentCols {
		if !have[c[0]] {
			r.must(`ALTER TABLE agent_tasks ADD COLUMN ` + c[0] + ` ` + c[1])
		}
	}
}

func (r *run) alog(key, kind, text string) {
	r.must(`INSERT INTO agent_log (key, at, kind, text) VALUES (?,?,?,?)`, key, r.now(), kind, head(text, 2000))
}

func (r *run) actor() string { return strings.ToLower(r.o.Name) }

func f64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// --- settings ------------------------------------------------------------------------------------------------------

type agentSettings struct {
	Enabled     bool     `json:"enabled"`
	Assignees   []string `json:"assignees"`
	Column      string   `json:"column"`
	AutoDone    bool     `json:"auto_done"`
	QAEnabled   bool     `json:"qa_enabled"`
	QAThreshold string   `json:"qa_threshold"`
	QACap       int64    `json:"qa_cap"`
	QAOwner     string   `json:"qa_owner"`
	Fixer       string   `json:"fixer_trigger"`
	Chat        bool     `json:"chat"`
	Secrets     bool     `json:"secrets"`
}

func (r *run) agentSettings() agentSettings {
	var d map[string]any
	json.Unmarshal([]byte(r.getState("agent_settings", "{}")), &d)
	s := agentSettings{Enabled: true, Column: "In Progress", QAEnabled: true, QAThreshold: "high", QACap: 10, Fixer: "off"}
	if b, ok := d["enabled"].(bool); ok {
		s.Enabled = b
	}
	for _, x := range asList(d["assignees"]) {
		if u, ok := x.(string); ok {
			s.Assignees = append(s.Assignees, u)
		}
	}
	if c, _ := d["column"].(string); c != "" {
		s.Column = c
	}
	s.AutoDone, _ = d["auto_done"].(bool)
	if b, ok := d["qa_enabled"].(bool); ok {
		s.QAEnabled = b
	}
	if t, _ := d["qa_threshold"].(string); t != "" {
		s.QAThreshold = t
	}
	if n, ok := d["qa_cap"].(float64); ok && n > 0 {
		s.QACap = int64(n)
	}
	s.QAOwner, _ = d["qa_owner"].(string)
	if f, _ := d["fixer_trigger"].(string); f == "column" {
		s.Fixer = f
	}
	s.Chat, _ = d["chat"].(bool)
	s.Secrets, _ = d["secrets"].(bool)
	return s
}

func asList(v any) []any { l, _ := v.([]any); return l }

func (s agentSettings) object() object {
	as := make([]any, 0, len(s.Assignees))
	for _, a := range s.Assignees {
		as = append(as, a)
	}
	return object{{"enabled", s.Enabled}, {"assignees", as}, {"column", s.Column}, {"auto_done", s.AutoDone},
		{"qa_enabled", s.QAEnabled}, {"qa_threshold", s.QAThreshold}, {"qa_cap", s.QACap}, {"qa_owner", s.QAOwner},
		{"fixer_trigger", s.Fixer}, {"chat", s.Chat}, {"secrets", s.Secrets}}
}

// columnKey is the board column a setting names ("In Progress" -> progress).
func (r *run) columnKey(name string) string {
	l := strings.ToLower(strings.TrimSpace(name))
	if contains(Columns, l) {
		return l
	}
	if c := r.mp.status[l]; c != "" {
		return c
	}
	return "progress"
}

// --- scope -----------------------------------------------------------------------------------------------------------

type scoped struct {
	task     row
	assignee string // the tracker's assignee name
	user     string // the Argus admin the agent works for
}

func agentKeyOf(t row) string {
	if t.s("tracker") == "gitlab" {
		if t.null("gl_iid") {
			return ""
		}
		return "gl-" + t.s("id")
	}
	if k := t.s("jira_key"); keyRe.MatchString(k) {
		return k
	}
	return ""
}

// agentScope is {key: task} of the tasks in the agent column assigned to an admin the agent works for.
func (r *run) agentScope(st agentSettings) map[string]scoped {
	out := map[string]scoped{}
	if !st.Enabled {
		return out
	}
	allowed := map[string]bool{}
	for _, u := range st.Assignees {
		allowed[u] = true
	}
	gm := r.o.glMetaRead()
	roles := map[string]string{}
	role := func(u string) string {
		if v, ok := roles[u]; ok {
			return v
		}
		roles[u] = r.userRole(u)
		return roles[u]
	}
	for _, t := range r.rows(`SELECT * FROM tasks WHERE gone=0 AND status=? AND assignee IS NOT NULL AND assignee != '' ORDER BY id`, r.columnKey(st.Column)) {
		key := agentKeyOf(t)
		if key == "" {
			continue
		}
		a := t.s("assignee")
		var cands []string
		users := r.mp.users
		if t.s("tracker") == "gitlab" {
			users = gm.UserMap
		}
		var mapped []string
		for u, name := range users {
			if name == a {
				mapped = append(mapped, u)
			}
		}
		sort.Strings(mapped)
		cands = append(mapped, a)
		for _, u := range cands {
			if userRe.MatchString(u) && role(u) == "admin" && (len(allowed) == 0 || allowed[u]) {
				out[key] = scoped{task: t, assignee: a, user: u}
				break
			}
		}
	}
	return out
}

// --- the task file ---------------------------------------------------------------------------------------------------

func (r *run) agentInputs(key string) (string, []any, bool) {
	in := r.one(`SELECT * FROM agent_inputs WHERE key=?`, key)
	if in == nil {
		return "", []any{}, true
	}
	var ans []any
	json.Unmarshal([]byte(in.s("answers")), &ans)
	if ans == nil {
		ans = []any{}
	}
	return in.s("instructions"), ans, in.null("post_jira") || in.i("post_jira") != 0
}

// agentContext is everything the model may read about the task (untrusted data), from board.db.
func (r *run) agentContext(t row, key string, prevPlan any) map[string]any {
	ins, ans, _ := r.agentInputs(key)
	var comments []any
	cs := r.rows(`SELECT author, body, created_at FROM comments WHERE task_id=? ORDER BY created_at DESC, id DESC LIMIT 30`, t.i("id"))
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		comments = append(comments, map[string]any{"author": c.s("author"), "created": c.i("created_at"), "body": head(c.s("body"), 8000)})
	}
	if comments == nil {
		comments = []any{}
	}
	labels := []any{}
	for _, l := range jsonList(t.s("labels")) {
		labels = append(labels, l)
	}
	tracker := t.s("tracker")
	if tracker == "" {
		tracker = "jira"
	}
	status := t.s("jira_status")
	if status == "" {
		status = columnLabel[t.s("status")]
	}
	return map[string]any{"key": key, "tracker": tracker, "ref": r.taskRef(t), "summary": t.s("title"),
		"description": head(t.s("description"), 32000), "status": status, "priority": t.s("priority"), "labels": labels,
		"assignee": t.s("assignee"), "env": t.s("env"), "comments": comments, "links": []any{}, "attachments": []any{},
		"environments": r.o.Agent.Environments,
		"argus":        map[string]any{"instructions": ins, "answers": ans, "previous_plan": prevPlan}}
}

func (r *run) contextHash(c map[string]any) string {
	own := "[" + r.o.Name + "]"
	var cs []any
	for _, x := range c["comments"].([]any) {
		if !strings.HasPrefix(orStr(asMap(x)["body"]), own) {
			cs = append(cs, x)
		}
	}
	ar := asMap(c["argus"])
	keep := map[string]any{"summary": c["summary"], "description": c["description"], "attachments": c["attachments"],
		"comments": cs, "argus": map[string]any{"instructions": ar["instructions"], "answers": ar["answers"]}}
	b, _ := json.Marshal(keep)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

func (r *run) writeJob(name string, doc map[string]any) error {
	dir := r.o.Agent.JobsDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(doc, "", " ")
	return writeAtomic(filepath.Join(dir, name), b, 0o644)
}

// stopJob removes the job file: the runner cancels a call whose file is gone.
func (r *run) stopJob(phase, key string) {
	if phase == "" {
		phase = "intake"
	}
	os.Remove(filepath.Join(r.o.Agent.JobsDir, key+"."+phase+".json"))
}

func (r *run) busy() bool {
	return r.one(`SELECT key FROM agent_tasks WHERE state IN ('planning','running') AND job_started IS NOT NULL AND job_started > ?`,
		r.now()-int64(r.o.Agent.RunTO/time.Second)) != nil
}

func (r *run) dayCount(what string) int64 {
	day := r.o.Now().Format("2006-01-02")
	if r.getState("agent_day_"+what, "") != day {
		r.setState("agent_day_"+what, day)
		r.setState("agent_count_"+what, "0")
	}
	return r.stateInt("agent_count_" + what)
}

func (r *run) bump(what string) {
	r.setState("agent_count_"+what, strconv.FormatInt(r.dayCount(what)+1, 10))
}

func (r *run) budgetNote() string {
	return fmt.Sprintf("task budget of $%.2f spent (planning, chat and runs); no new planning, chat or run", r.o.Agent.TaskBudget)
}

// --- the loop --------------------------------------------------------------------------------------------------------

// agentTick stops out-of-scope jobs, reads finished results and starts the next job. A short summary back.
func (r *run) agentRunTick() string {
	r.agentEnsure()
	st := r.agentSettings()
	scope := r.agentScope(st)
	for _, row := range r.rows(`SELECT * FROM agent_tasks`) {
		key := row.s("key")
		_, in := scope[key]
		if !in && !row.null("plan_entered_at") {
			r.must(`UPDATE agent_tasks SET plan_entered_at=NULL WHERE key=?`, key)
		}
		if !in && (row.s("state") == "planning" || row.s("state") == "running") {
			r.stopJob(row.s("phase"), key)
			r.must(`UPDATE agent_tasks SET state='stopped', job_started=NULL, error=?, updated=? WHERE key=?`,
				"moved out of the agent column or assigned to someone else", r.now(), key)
			r.alog(key, "stopped", "Stopped: the task left the agent column or its assignee changed")
		}
	}
	r.agentIngest()
	for key, sc := range scope {
		row := r.one(`SELECT * FROM agent_tasks WHERE key=?`, key)
		if row == nil {
			r.must(`INSERT INTO agent_tasks (key, state, assignee, argus_user, updated, plan_entered_at) VALUES (?,?,?,?,?,?)`,
				key, "planning", sc.assignee, sc.user, r.now(), r.now())
			r.alog(key, "scope", fmt.Sprintf("In %s, assigned to %s: %s works this task (%s)", st.Column, sc.assignee, r.o.Name, r.taskRef(sc.task)))
			continue
		}
		if row.s("argus_user") != sc.user {
			r.must(`UPDATE agent_tasks SET assignee=?, argus_user=? WHERE key=?`, sc.assignee, sc.user, key)
		}
		if row.null("plan_entered_at") {
			r.must(`UPDATE agent_tasks SET plan_entered_at=? WHERE key=?`, r.now(), key)
		}
	}
	r.commit()
	r.replanAfterToken(scope)
	if r.busy() {
		return "agent busy"
	}
	if ok, why := r.o.Agent.Guard(); !ok {
		return why
	}
	for _, row := range r.rows(`SELECT * FROM agent_tasks WHERE state='approved' ORDER BY approved_at`) {
		if sc, ok := scope[row.s("key")]; ok {
			return r.dispatchRun(row, sc)
		}
	}
	for _, row := range r.rows(`SELECT * FROM agent_tasks WHERE state NOT IN ('running','approved') ORDER BY updated`) {
		key := row.s("key")
		sc, ok := scope[key]
		if !ok || ((row.s("state") == "done" || row.s("state") == "blocked") && (row.s("phase") == "run" || row.s("phase") == "fix")) {
			continue
		}
		if row.null("plan_entered_at") || r.now()-row.i("plan_entered_at") < int64(r.o.Agent.Debounce/time.Second) {
			continue
		}
		if f64(row["spent_usd"]) >= r.o.Agent.TaskBudget {
			if row.s("error") != r.budgetNote() {
				r.must(`UPDATE agent_tasks SET error=? WHERE key=?`, r.budgetNote(), key)
				r.commit()
			}
			continue
		}
		var prev any
		json.Unmarshal([]byte(row.s("plan")), &prev)
		ctxd := r.agentContext(sc.task, key, prev)
		h := r.contextHash(ctxd)
		limited := row.s("state") == "failed" && strings.HasPrefix(row.s("error"), agentLimitPrefix) && !row.null("retry_at")
		if limited && r.now() < row.i("retry_at") {
			continue
		}
		switch {
		case limited:
			r.alog(key, "retry", "Claude usage limit should have reset: planning again")
		case h == row.s("context_hash") && row.s("state") != "planning":
			continue
		case row.s("state") == "planning" && row.s("context_hash") == h && !row.null("intake_at"):
			continue
		}
		if r.dayCount("intake") >= agentIntakePerDay {
			return "intake daily cap reached"
		}
		ctxd["phase"], ctxd["context_hash"], ctxd["board_columns"] = "intake", h, r.boardColumns()
		if err := r.writeJob(key+".intake.json", ctxd); err != nil {
			r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error='could not start the intake', error_detail=? WHERE key=?`,
				r.errorDetail("intake", "could not start the intake", nil, "the job file cannot be written: "+err.Error()), key)
			r.commit()
			return "intake not started"
		}
		r.must(`UPDATE agent_tasks SET state='planning', phase='intake', context_hash=?, job_started=?, error=NULL, error_detail=NULL,
			updated=? WHERE key=?`, h, r.now(), r.now(), key)
		r.bump("intake")
		r.alog(key, "intake", "Reading the task and planning (read-only)")
		r.commit()
		return "intake " + key
	}
	return "idle"
}

func (r *run) boardColumns() []any {
	var out []any
	for _, c := range Columns {
		if c != "fixer" {
			out = append(out, columnLabel[c])
		}
	}
	return out
}

// readOut is a result or meta file: the JSON object, {"_error": ...} when it is not one, nil when missing.
func readOut(path string) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	b := make([]byte, agentMaxOut+1)
	n, _ := f.Read(b)
	if n > agentMaxOut {
		return map[string]any{"_error": "output too large"}
	}
	var v any
	if json.Unmarshal(b[:n], &v) != nil {
		return map[string]any{"_error": "output is not valid JSON"}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{"_error": "output is not a JSON object"}
	}
	return m
}

func (r *run) taskIDOf(key string) any {
	if m := glKeyRe.FindStringSubmatch(key); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		return id
	}
	if t := r.one(`SELECT id FROM tasks WHERE jira_key=?`, key); t != nil {
		return t.i("id")
	}
	return nil
}

func (r *run) agentComment(key, body, as string) {
	if tid, ok := r.taskIDOf(key).(int64); ok {
		r.queue(tid, "comment", object{{"body", body}, {"as", as}, {"optional", true}})
	}
}

// agentIngest reads the finished intake and run results.
func (r *run) agentIngest() {
	a := r.o.Agent
	for _, row := range r.rows(`SELECT * FROM agent_tasks WHERE state IN ('planning','running')`) {
		key, phase := row.s("key"), row.s("phase")
		if phase == "fix" || row.null("job_started") {
			continue
		}
		base := filepath.Join(a.OutDir, key+"."+phase)
		meta := readOut(base + ".meta.json")
		if meta == nil {
			to := a.IntakeTO
			if phase == "run" {
				to = a.RunTO
			}
			if r.now()-row.i("job_started") > int64(to/time.Second) {
				mins := int(to / time.Minute)
				r.stopJob(phase, key)
				r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error='no result (timeout)', error_detail=?, updated=? WHERE key=?`,
					r.errorDetail(phase, "no result (timeout)", nil, fmt.Sprintf("no result file after %d min", mins)), r.now(), key)
				r.alog(key, "failed", fmt.Sprintf("The %s produced no result in time (%d min)", phase, mins))
				r.notice("agent-failed", r.taskIDOf(key), r.actor(), key)
			}
			continue
		}
		if int64(f64(meta["finished"])) < row.i("job_started") {
			continue // an older file; this job has not finished yet
		}
		if sk := orStr(meta["skipped"]); sk != "" { // Claude off or the egress guard at the start: not the task's fault
			r.must(`UPDATE agent_tasks SET job_started=NULL, context_hash=NULL, updated=? WHERE key=?`, r.now(), key)
			if phase == "run" {
				r.must(`UPDATE agent_tasks SET state='approved' WHERE key=?`, key)
			}
			r.alog(key, "waiting", "Not started: "+head(sk, 300))
			continue
		}
		cost := f64(meta["cost_usd"])
		d := readOut(base + ".json")
		bad := d == nil || d["_error"] != nil || len(d) == 0
		var me string
		var limit bool
		if bad {
			me, limit = r.modelError(meta, phase)
		}
		if phase == "intake" {
			if limit {
				since := r.now()
				if !row.null("retry_at") && !row.null("limit_since") {
					since = row.i("limit_since")
				}
				retry := nextRetry(me, since, r.now(), r.o.Now().Location())
				r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, intake_cost=?, spent_usd=COALESCE(spent_usd,0)+?, error=?,
					error_detail=?, retry_at=?, limit_since=?, updated=? WHERE key=?`, cost, cost, me, r.errorDetail("intake", me, meta, ""),
					retry, since, r.now(), key)
				tail := "; no more automatic retries"
				if retry != nil {
					tail = "; planning again then"
				}
				r.alog(key, "failed", "Claude usage limit reached: "+strings.TrimSpace(strings.TrimPrefix(me, agentLimitPrefix))+tail)
				continue
			}
			plan, err := validatePlan(d)
			if me != "" {
				_, after, _ := strings.Cut(me, ": ")
				plan, err = nil, fmt.Errorf("%s", after)
			}
			if err != nil {
				e := "intake: " + err.Error()
				raw := ""
				if me == "" {
					raw = fmt.Sprintf("%s (Claude said: %s)", err, orDash(orStr(meta["status"])))
				}
				r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, intake_cost=?, spent_usd=COALESCE(spent_usd,0)+?, error=?,
					error_detail=?, retry_at=NULL, limit_since=NULL, updated=? WHERE key=?`, cost, cost, e, r.errorDetail("intake", e, meta, raw), r.now(), key)
				r.alog(key, "failed", "Intake failed: "+err.Error())
				r.notice("agent-failed", r.taskIDOf(key), r.actor(), key)
				continue
			}
			var old map[string]any
			json.Unmarshal([]byte(row.s("plan")), &old)
			same := old != nil && jsonEq(old["classification"], plan["classification"]) && jsonEq(old["plan"], plan["plan"])
			version := row.i("plan_version")
			if !same {
				version++
			}
			state := "ready"
			if len(r.unanswered(plan, key)) > 0 {
				state = "waiting"
			}
			pb, _ := json.Marshal(plan)
			r.must(`UPDATE agent_tasks SET state=?, phase='intake', plan=?, plan_version=?, intake_at=?, intake_cost=?, spent_usd=COALESCE(spent_usd,0)+?,
				job_started=NULL, error=NULL, error_detail=NULL, retry_at=NULL, limit_since=NULL, updated=? WHERE key=?`,
				state, string(pb), version, r.now(), cost, cost, r.now(), key)
			cl := asMap(plan["classification"])
			r.alog(key, "plan", fmt.Sprintf("Plan v%d: %s on %s, %d steps, %d questions ($%.2f)", version, cl["kind"], cl["environment"],
				len(asList(plan["plan"])), len(asList(plan["questions"])), cost))
			if state == "waiting" {
				r.notice("agent-waiting", r.taskIDOf(key), r.actor(), key)
			}
			continue
		}
		res, err := validateResult(d)
		if me != "" {
			_, after, _ := strings.Cut(me, ": ")
			err = fmt.Errorf("%s", after)
		}
		if err != nil {
			res = map[string]any{"status": "failed", "summary": "run produced no valid result: " + err.Error(), "report_md": "", "mr_url": "",
				"evidence": []any{}, "rollback": []any{}, "needs": []any{}, "suggested_column": ""}
			e, raw := me, ""
			if e == "" {
				e = "run: " + err.Error()
				raw = fmt.Sprintf("%s (Claude said: %s)", err, orDash(orStr(meta["status"])))
			}
			r.must(`UPDATE agent_tasks SET error=?, error_detail=? WHERE key=?`, e, r.errorDetail("run", e, meta, raw), key)
		} else {
			r.must(`UPDATE agent_tasks SET error_detail=NULL WHERE key=?`, key)
		}
		var plan map[string]any
		json.Unmarshal([]byte(row.s("plan")), &plan)
		if res["status"] == "done" && (asMap(plan["classification"])["kind"] == "production" || hasWrite(plan)) {
			res["status"] = "blocked" // a proposal, or WRITE steps for the fixer: a person acts next
		}
		r.finish(row, res, cost)
	}
	r.commit()
}

func jsonEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (r *run) finish(row row, res map[string]any, cost float64) {
	key := row.s("key")
	rb, _ := json.Marshal(res)
	status := orStr(res["status"])
	r.must(`UPDATE agent_tasks SET state=?, job_started=NULL, run_finished=?, run_cost=?, spent_usd=COALESCE(spent_usd,0)+?, result=?, updated=? WHERE key=?`,
		status, r.now(), cost, cost, string(rb), r.now(), key)
	r.alog(key, status, head(orStr(res["summary"]), 500))
	lines := []string{fmt.Sprintf("[%s] Task %s: %s", r.o.Name, status, orStr(res["summary"]))}
	for _, part := range [][2]string{{"evidence", "Evidence"}, {"rollback", "Rollback"}, {"needs", "Needs from the operator"}} {
		if l := asList(res[part[0]]); len(l) > 0 {
			var b strings.Builder
			b.WriteString(part[1] + ":")
			for i, x := range l {
				if i == 10 {
					break
				}
				b.WriteString("\n- " + orStr(x))
			}
			lines = append(lines, b.String())
		}
	}
	if status == "blocked" {
		lines = append(lines, "Blocked: needs the operator. "+r.o.Name+" never executes production changes or WRITE steps.")
	}
	lines = append(lines, "Full report: "+r.agentTaskURL(key))
	as := row.s("approved_by")
	if as == "" {
		as = row.s("argus_user")
	}
	body, _ := notify.Redact(strings.Join(lines, "\n\n"), r.o.Allow)
	r.agentComment(key, head(body, maxComment), as)
	kind := map[string]string{"done": "agent-done", "failed": "agent-failed"}[status]
	if kind == "" {
		kind = "agent-blocked"
	}
	actor := row.s("approved_by")
	if actor == "" {
		actor = r.actor()
	}
	r.notice(kind, r.taskIDOf(key), actor, key)
	if status == "done" && r.agentSettings().AutoDone {
		if m := glKeyRe.FindStringSubmatch(key); m != nil {
			if t := r.one(`SELECT * FROM tasks WHERE id=?`, m[1]); t != nil && t.s("status") != "done" {
				r.move(t, "done", as, "task agent: auto-move to Done", true)
				r.alog(key, "moved", "Auto-move to Done (queued for GitLab)")
			}
		}
	}
}

func (r *run) agentTaskURL(key string) string {
	app := strings.TrimRight(r.o.AppURL, "/")
	if m := glKeyRe.FindStringSubmatch(key); m != nil {
		return app + "/board/task?id=" + m[1]
	}
	return app + "/board/task?key=" + key
}

func hasWrite(plan map[string]any) bool {
	for _, s := range asList(plan["plan"]) {
		if asMap(s)["mode"] == "WRITE" {
			return true
		}
	}
	return false
}

// spinUp is what Spin up agent does with a plan: (path, label); path proposal | write-report | run | refused.
func spinUp(plan map[string]any) (string, string) {
	cl := asMap(plan["classification"])
	env := orStr(cl["environment"])
	if env == "release" || cl["kind"] == "production" {
		return "proposal", "Write proposal for the operator"
	}
	if hasWrite(plan) {
		if contains(agentFixEnvs, env) {
			return "write-report", "Spin up agent: a read-only run that reports the WRITE steps on " + env + " for a person (no fixer in this version)"
		}
		return "refused", "the plan has WRITE steps but no environment: tell " + "the agent the environment; it plans again"
	}
	return "run", "Spin up agent (read-only)"
}

func (r *run) dispatchRun(row row, sc scoped) string {
	key := row.s("key")
	var plan map[string]any
	json.Unmarshal([]byte(row.s("plan")), &plan)
	if row.i("approved_version") != row.i("plan_version") {
		r.must(`UPDATE agent_tasks SET state='ready', error=? WHERE key=?`, fmt.Sprintf("the plan changed: review v%d and spin up again", row.i("plan_version")), key)
		r.commit()
		return "plan changed"
	}
	if r.dayCount("run") >= agentRunsPerDay {
		r.must(`UPDATE agent_tasks SET state='ready', error=? WHERE key=?`, fmt.Sprintf("daily limit of %d runs reached", agentRunsPerDay), key)
		r.commit()
		return "run cap"
	}
	path, why := spinUp(plan)
	if path == "refused" {
		r.must(`UPDATE agent_tasks SET state='ready', error=? WHERE key=?`, why, key)
		r.alog(key, "refused", why)
		r.commit()
		return "refused"
	}
	ctxd := r.agentContext(sc.task, key, plan)
	ctxd["phase"], ctxd["approved_plan"], ctxd["plan_version"], ctxd["approved_by"] = "run", plan, row.i("plan_version"), row.s("approved_by")
	ctxd["production"], ctxd["board_columns"] = path == "proposal", r.boardColumns()
	ctxd["write_steps_for_a_person"] = path == "write-report"
	if err := r.writeJob(key+".run.json", ctxd); err != nil {
		r.must(`UPDATE agent_tasks SET state='failed', job_started=NULL, error='could not start the run', error_detail=? WHERE key=?`,
			r.errorDetail("run", "could not start the run", nil, "the job file cannot be written: "+err.Error()), key)
		r.commit()
		return "run not started"
	}
	r.must(`UPDATE agent_tasks SET state='running', phase='run', run_started=?, job_started=?, error=NULL WHERE key=?`, r.now(), r.now(), key)
	r.bump("run")
	r.alog(key, "run", fmt.Sprintf("Run started for plan v%d (approved by %s)", row.i("plan_version"), row.s("approved_by")))
	r.commit()
	return "run " + key
}

// --- validation ------------------------------------------------------------------------------------------------------

func sOf(v any, n int) string { return head(strings.ReplaceAll(orStr(v), "\x00", ""), n) }

func strList(v any, max, n int) []any {
	out := []any{}
	for i, x := range asList(v) {
		if i == max {
			break
		}
		if s, ok := x.(string); ok {
			out = append(out, sOf(s, n))
		}
	}
	return out
}

// validatePlan is the intake's output, cleaned.
func validatePlan(d map[string]any) (map[string]any, error) {
	if d == nil || len(d) == 0 {
		return nil, fmt.Errorf("no output")
	}
	if e := orStr(d["_error"]); e != "" {
		return nil, fmt.Errorf("%s", e)
	}
	c := asMap(d["classification"])
	env, kind := orStr(c["environment"]), orStr(c["kind"])
	if !contains(agentEnvs, env) || !contains(agentKinds, kind) {
		return nil, fmt.Errorf("classification missing or unknown")
	}
	steps := []any{}
	for i, x := range asList(d["plan"]) {
		if i == 30 {
			break
		}
		s := asMap(x)
		if orStr(s["text"]) == "" {
			continue
		}
		mode := "read-only"
		if strings.ToUpper(orStr(s["mode"])) == "WRITE" {
			mode = "WRITE"
		}
		steps = append(steps, map[string]any{"n": i + 1, "text": sOf(s["text"], 2000), "mode": mode, "targets": strList(s["targets"], 10, 200)})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no plan steps")
	}
	qs := []any{}
	for i, x := range asList(d["questions"]) {
		if i == 5 {
			break
		}
		q := asMap(x)
		if orStr(q["text"]) != "" {
			qs = append(qs, map[string]any{"id": fmt.Sprintf("q%d", i+1), "text": sOf(q["text"], 600), "blocking": q["blocking"] != false})
		}
	}
	est := asMap(d["estimate"])
	cost, _ := est["cost_usd"].(float64)
	conf := orStr(d["confidence"])
	if !contains([]string{"low", "medium", "high"}, conf) {
		conf = "low"
	}
	if env == "release" {
		kind = "production" // whatever the model wrote, production is a proposal only
	}
	return map[string]any{"classification": map[string]any{"environment": env, "kind": kind}, "summary": sOf(d["summary"], 1500),
		"plan": steps, "questions": qs, "needs": strList(d["needs"], 10, 400),
		"estimate": map[string]any{"effort": sOf(est["effort"], 100), "cost_usd": cost}, "confidence": conf,
		"suggested_column": sOf(d["suggested_column"], 60)}, nil
}

var mrURLRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+/[\w./-]+/-/merge_requests/\d+$`)

func validateResult(d map[string]any) (map[string]any, error) {
	if d == nil || len(d) == 0 {
		return nil, fmt.Errorf("no output")
	}
	if e := orStr(d["_error"]); e != "" {
		return nil, fmt.Errorf("%s", e)
	}
	st := orStr(d["status"])
	if !contains([]string{"done", "failed", "blocked"}, st) {
		st = "failed"
	}
	mr := sOf(d["mr_url"], 300)
	if !mrURLRe.MatchString(mr) {
		mr = ""
	}
	return map[string]any{"status": st, "summary": sOf(d["summary"], 3000), "report_md": sOf(d["report_md"], 60000), "mr_url": mr,
		"evidence": strList(d["evidence"], 20, 600), "rollback": strList(d["rollback"], 20, 600), "needs": strList(d["needs"], 10, 400),
		"suggested_column": sOf(d["suggested_column"], 60)}, nil
}

func (r *run) unanswered(plan map[string]any, key string) []any {
	_, ans, _ := r.agentInputs(key)
	done := map[string]bool{}
	for _, a := range ans {
		m := asMap(a)
		if strings.TrimSpace(orStr(m["a"])) != "" {
			done[orStr(m["q"])] = true
		}
	}
	var out []any
	for _, q := range asList(plan["questions"]) {
		m := asMap(q)
		if m["blocking"] == true && !done[orStr(m["text"])] {
			out = append(out, q)
		}
	}
	return out
}

// --- model errors and failure classes (the same words and order as the task page's agentErrClass) ----------------

var (
	limitRe  = regexp.MustCompile(`(?i)hit (?:your|the) [\w\s-]{0,30}limit|usage limit|rate[ _-]?limit|too many requests|\b429\b`)
	resetRe  = regexp.MustCompile(`(?i)\bresets?\b(?:\s+(?:at|on))?\s*:?\s*(.+)`)
	authRe   = regexp.MustCompile(`(?i)organi[sz]ation has disabled|subscription access|invalid (?:api[ _-]?key|bearer|token|x-api-key)|oauth|token (?:has )?(?:expired|been revoked)|revoked|unauthori[sz]ed|authenticat|\b401\b|not logged in|please run /login|/login\b|login (?:failed|required|expired)`)
	egressRe = regexp.MustCompile(`(?i)\b403\b|request not allowed|unsupported (?:country|region)|not available in your (?:country|region)|egress|unable to connect|connection (?:error|refused|reset)|getaddrinfo|ENOTFOUND|ECONNREFUSED|ETIMEDOUT|EAI_AGAIN`)
	outputRe = regexp.MustCompile(`no output|not valid JSON|not a JSON object|output too large|classification missing|no valid result`)
	runIDRe  = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// modelError is (short safe error, is a usage limit) for a call that ended with is_error; ("", false) otherwise.
func (r *run) modelError(meta map[string]any, phase string) (string, bool) {
	if meta["is_error"] != true {
		return "", false
	}
	text := strings.TrimSpace(strings.ReplaceAll(orStr(meta["status"]), "\x00", ""))
	if limitRe.MatchString(text) {
		when := ""
		if m := resetRe.FindStringSubmatch(text); m != nil {
			when = head(strings.Join(strings.Fields(m[1]), " "), 60)
			if red, hits := notify.Redact(when, nil); len(hits) > 0 || red != when {
				when = ""
			}
		}
		if when != "" {
			return agentLimitPrefix + " resets " + when, true
		}
		return agentLimitPrefix + " reset time unknown", true
	}
	line := ""
	if ls := strings.Split(text, "\n"); len(ls) > 0 {
		line = head(strings.TrimSpace(ls[0]), 120)
	}
	if red, hits := notify.Redact(line, nil); len(hits) > 0 || red != line {
		line = ""
	}
	if line == "" {
		return phase + ": model error", false
	}
	return phase + ": " + line, false
}

// errorClass is auth | egress | limit | timeout | start | no-steps | output | other.
func errorClass(e, raw string) string {
	text := e + " " + raw
	switch {
	case strings.HasPrefix(e, agentLimitPrefix):
		return "limit"
	case authRe.MatchString(text):
		return "auth"
	case egressRe.MatchString(text):
		return "egress"
	case strings.HasPrefix(e, "no result") || strings.Contains(e, "timeout"):
		return "timeout"
	case strings.HasPrefix(e, "could not start"):
		return "start"
	case strings.Contains(e, "no plan steps"):
		return "no-steps"
	case outputRe.MatchString(e):
		return "output"
	}
	return "other"
}

// errorDetail is the last failure as stored JSON: phase, class, raw (redacted, 300), is_error, exit_code, run_id, at.
func (r *run) errorDetail(phase, e string, meta map[string]any, raw string) string {
	text := raw
	if text == "" {
		text = orStr(meta["status"])
	}
	if text == "" {
		text = e
	}
	text = head(strings.Join(strings.Fields(strings.ReplaceAll(text, "\x00", "")), " "), 300)
	text, _ = notify.Redact(text, r.o.Allow)
	var isErr, exit any
	if b, ok := meta["is_error"].(bool); ok {
		isErr = b
	}
	if x, ok := meta["exit_code"].(float64); ok && x == float64(int64(x)) {
		exit = int64(x)
	}
	rid := orStr(meta["run_id"])
	if !runIDRe.MatchString(rid) {
		rid = ""
	}
	b, _ := json.Marshal(map[string]any{"phase": phase, "class": errorClass(e, text), "raw": text, "is_error": isErr,
		"exit_code": exit, "run_id": rid, "at": r.now()})
	return string(b)
}

var (
	resetWhenRe = regexp.MustCompile(`(?i)^(?:(?P<mon>[A-Za-z]{3})[a-z]*\.?\s+(?P<day>\d{1,2}),?\s+(?:at\s+)?)?(?P<h>\d{1,2})(?::(?P<m>\d{2}))?\s*(?P<ap>[ap]\.?m\.?)?\s*(?:\((?P<tz>[A-Za-z_]+(?:/[A-Za-z_+-]+)*)\))?`)
	months      = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
)

// resetEpoch is the time the reset text of a claude-limit error names, or 0.
func resetEpoch(e string, t int64, local *time.Location) int64 {
	text := strings.TrimSpace(strings.TrimPrefix(e, agentLimitPrefix))
	if !strings.HasPrefix(text, "resets") {
		return 0
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "resets"))
	m := resetWhenRe.FindStringSubmatch(text)
	if text == "" || m == nil {
		return 0
	}
	g := func(n string) string { return m[resetWhenRe.SubexpIndex(n)] }
	loc := local
	if g("tz") != "" {
		if l, err := time.LoadLocation(g("tz")); err == nil {
			loc = l
		}
	}
	h, _ := strconv.Atoi(g("h"))
	mi, _ := strconv.Atoi(g("m"))
	ap := strings.ToLower(strings.ReplaceAll(g("ap"), ".", ""))
	if ap != "" {
		if h < 1 || h > 12 {
			return 0
		}
		h = h % 12
		if ap == "pm" {
			h += 12
		}
	} else if g("m") == "" {
		return 0 // a bare number is not a time
	}
	if h > 23 || mi > 59 {
		return 0
	}
	cur := time.Unix(t, 0).In(loc)
	var d time.Time
	if g("mon") != "" {
		mon := -1
		for i, x := range months {
			if strings.ToLower(g("mon"))[:3] == x {
				mon = i
			}
		}
		if mon < 0 {
			return 0
		}
		day, _ := strconv.Atoi(g("day"))
		d = time.Date(cur.Year(), time.Month(mon+1), day, h, mi, 0, 0, loc)
		if d.Day() != day {
			return 0
		}
		if d.Unix() < t-86400 {
			d = d.AddDate(1, 0, 0)
		}
	} else {
		d = time.Date(cur.Year(), cur.Month(), cur.Day(), h, mi, 0, 0, loc)
		if d.Unix() <= t {
			d = d.AddDate(0, 0, 1)
		}
	}
	if d.Unix() > t+8*86400 {
		return 0
	}
	return d.Unix()
}

// nextRetry is when to plan a limited intake again: just after the named reset, else 30 min then hourly; nil after 24 h.
func nextRetry(e string, since, t int64, local *time.Location) any {
	if ep := resetEpoch(e, t, local); ep > 0 {
		return ep + 120
	}
	nxt := t + 3600
	if since >= t {
		nxt = t + 1800
	}
	if nxt-since > 24*3600 {
		return nil
	}
	return nxt
}

// tokenReplaced is the time of the last Claude token replacement whose test passed (ok or a usage limit), else 0.
func (r *run) tokenReplaced() int64 {
	b, err := os.ReadFile(r.o.Agent.ClaudeStatus)
	if err != nil {
		return 0
	}
	var d struct {
		Replaced struct {
			At *float64 `json:"at"`
		} `json:"replaced"`
		Test struct {
			Result string  `json:"result"`
			At     float64 `json:"at"`
		} `json:"test"`
	}
	if json.Unmarshal(b, &d) != nil || d.Replaced.At == nil || (d.Test.Result != "ok" && d.Test.Result != "limit") {
		return 0
	}
	if at := int64(*d.Replaced.At); int64(d.Test.At) >= at {
		return at
	}
	return 0
}

// replanAfterToken plans again the tasks whose last failure was the login or the network and that failed before the
// token was replaced (and tested), once per replacement. Generic failures stay failed.
func (r *run) replanAfterToken(scope map[string]scoped) int {
	at := r.tokenReplaced()
	if at == 0 {
		return 0
	}
	n := 0
	for _, row := range r.rows(`SELECT * FROM agent_tasks WHERE state='failed' AND COALESCE(updated,0) < ?`, at) {
		if _, ok := scope[row.s("key")]; !ok {
			continue
		}
		var d map[string]any
		json.Unmarshal([]byte(row.s("error_detail")), &d)
		if c := errorClass(row.s("error"), orStr(d["raw"])); c != "auth" && c != "egress" {
			continue
		}
		r.must(`UPDATE agent_tasks SET state='planning', context_hash=NULL, job_started=NULL, error=NULL, error_detail=NULL, retry_at=NULL,
			limit_since=NULL, updated=? WHERE key=?`, r.now(), row.s("key"))
		r.alog(row.s("key"), "retry", "Re-planning after the Claude token was replaced")
		n++
	}
	if n > 0 {
		r.commit()
	}
	return n
}

// --- requests from the app (board-requests kinds agent-*) -------------------------------------------------------

func (r *run) agentHandle(req map[string]any, by, role string) (string, any) {
	kind := orStr(req["kind"])
	if role != "admin" {
		panic(refused("only admins direct the task agent"))
	}
	r.agentEnsure()
	switch kind {
	case "agent-settings":
		return r.agentSaveSettings(req, by), nil
	case "agent-fixer-move":
		panic(refused("the fixer is not in this version: tasks in Ready for fixer stay where they are"))
	}
	key := orStr(req["key"])
	if !keyRe.MatchString(key) && !glKeyRe.MatchString(key) {
		panic(refused("bad issue key"))
	}
	row := r.one(`SELECT * FROM agent_tasks WHERE key=?`, key)
	if row == nil {
		panic(refused(r.o.Name + " is not working this task (not in the agent column, or not assigned to its admins)"))
	}
	switch kind {
	case "agent-answers":
		ins := strings.TrimSpace(cleanText(req["instructions"], 8001))
		if runes(ins) > 8000 {
			panic(refused("instructions are limited to 8000 characters"))
		}
		raw, ok := req["answers"].(map[string]any)
		if req["answers"] != nil && !ok {
			panic(refused("bad answers"))
		}
		var plan map[string]any
		json.Unmarshal([]byte(row.s("plan")), &plan)
		qtext := map[string]string{}
		for _, q := range asList(plan["questions"]) {
			qtext[orStr(asMap(q)["id"])] = orStr(asMap(q)["text"])
		}
		_, oldAns, _ := r.agentInputs(key)
		type qa struct{ q, a string }
		var order []string
		ans := map[string]string{}
		for _, a := range oldAns {
			q := orStr(asMap(a)["q"])
			if _, seen := ans[q]; !seen {
				order = append(order, q)
			}
			ans[q] = orStr(asMap(a)["a"])
		}
		ids := make([]string, 0, len(raw))
		for id := range raw {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i, id := range ids {
			if i == 5 {
				break
			}
			if t, ok := qtext[id]; ok {
				a := strings.TrimSpace(cleanText(raw[id], 2001))
				if runes(a) > 2000 {
					panic(refused("an answer is limited to 2000 characters"))
				}
				if _, seen := ans[t]; !seen {
					order = append(order, t)
				}
				ans[t] = a
			}
		}
		out := []any{}
		for _, q := range order {
			if ans[q] != "" && len(out) < 20 {
				out = append(out, map[string]any{"q": q, "a": ans[q]})
			}
		}
		post := req["post_jira"] != false
		ab, _ := json.Marshal(out)
		r.must(`INSERT OR REPLACE INTO agent_inputs VALUES (?,?,?,?,?,?)`, key, ins, string(ab), map[bool]int{true: 1, false: 0}[post], by, r.now())
		r.must(`UPDATE agent_tasks SET state='planning', context_hash=NULL, error=NULL, updated=? WHERE key=? AND state NOT IN ('running')`, r.now(), key)
		r.alog(key, "answers", "Answers/instructions saved by "+by+"; re-planning")
		if post && (ins != "" || len(out) > 0) {
			body := "[" + r.o.Name + "] Answers and instructions for the task agent (" + by + "):\n"
			if ins != "" {
				body += "\nInstructions:\n" + ins + "\n"
			}
			for _, a := range out {
				body += "\nQ: " + orStr(asMap(a)["q"]) + "\nA: " + orStr(asMap(a)["a"]) + "\n"
			}
			r.agentComment(key, head(body, maxComment), by)
		}
		return "saved; re-planning", nil
	case "agent-replan":
		return r.agentReplan(row, by), nil
	case "agent-start":
		if glKeyRe.MatchString(key) {
			if !r.o.glConnected(r.o.glMetaRead()) {
				panic(refused("GitLab issues is not connected"))
			}
		} else if ok, why := r.canWrite(); !ok {
			panic(refused(why))
		}
		ver, isInt := asInt(req["plan_version"])
		if !isInt || ver != row.i("plan_version") || row.s("plan") == "" {
			panic(refused(fmt.Sprintf("the plan changed: review v%d", row.i("plan_version"))))
		}
		if !contains([]string{"ready", "waiting", "done", "failed", "blocked", "stopped"}, row.s("state")) {
			panic(refused("cannot start while " + row.s("state")))
		}
		var plan map[string]any
		json.Unmarshal([]byte(row.s("plan")), &plan)
		if len(r.unanswered(plan, key)) > 0 {
			panic(refused("answer the blocking questions first (Save answers & re-plan)"))
		}
		path, why := spinUp(plan)
		if path == "refused" {
			panic(refused(why))
		}
		if f64(row["spent_usd"]) >= r.o.Agent.TaskBudget {
			panic(refused(r.budgetNote()))
		}
		r.must(`UPDATE agent_tasks SET state='approved', approved_version=?, approved_by=?, approved_at=?, error=NULL, updated=? WHERE key=?`,
			ver, by, r.now(), r.now(), key)
		what := map[string]string{"proposal": "write proposal for the operator", "write-report": "a read-only run that reports the WRITE steps",
			"run": "spin up a read-only run"}[path]
		r.alog(key, "approved", fmt.Sprintf("Plan v%d approved by %s: %s", ver, by, what))
		if path == "proposal" {
			return "proposal requested", nil
		}
		return "approved; spinning up", nil
	case "agent-stop":
		if contains([]string{"planning", "running", "approved"}, row.s("state")) {
			r.stopJob(row.s("phase"), key)
			r.must(`UPDATE agent_tasks SET state='stopped', job_started=NULL, error=?, updated=? WHERE key=?`, "stopped by "+by, r.now(), key)
			r.alog(key, "stopped", "Stopped by "+by)
		}
		return "stopped", nil
	}
	panic(refused("unknown request kind"))
}

// agentReplan is "Plan again": the intake runs again with the saved instructions and answers. Idempotent while queued.
func (r *run) agentReplan(row row, by string) string {
	key, state := row.s("key"), row.s("state")
	if state == "planning" && row.null("job_started") {
		return "already queued; planning starts at the next sync"
	}
	if contains([]string{"planning", "running", "approved"}, state) {
		panic(refused("a job is already running for this task; Stop it first or wait for it to finish"))
	}
	if !contains([]string{"failed", "stopped", "waiting"}, state) || (state == "waiting" && row.s("plan") != "") {
		msg := "nothing to plan again while " + state
		if state == "waiting" {
			msg += "; answer the questions, the answers re-plan"
		}
		panic(refused(msg))
	}
	st := r.agentSettings()
	if !st.Enabled {
		panic(refused("the task agent is off (Admin → Task agent)"))
	}
	if row.null("plan_entered_at") || strings.HasPrefix(row.s("error"), "moved out of the agent column") {
		panic(refused("the task is not in " + st.Column + " assigned to an admin " + r.o.Name + " works for"))
	}
	if ok, why := r.o.Agent.Guard(); !ok {
		panic(refused(why))
	}
	if f64(row["spent_usd"]) >= r.o.Agent.TaskBudget {
		panic(refused(r.budgetNote()))
	}
	if r.dayCount("intake") >= agentIntakePerDay {
		panic(refused(fmt.Sprintf("the daily limit of %d planning runs is reached; plan again tomorrow", agentIntakePerDay)))
	}
	r.must(`UPDATE agent_tasks SET state='planning', context_hash=NULL, job_started=NULL, error=NULL, error_detail=NULL, retry_at=NULL,
		limit_since=NULL, updated=? WHERE key=?`, r.now(), key)
	r.alog(key, "replan", "Plan again requested by "+by+" (was "+state+"); instructions and answers kept")
	if r.busy() {
		return "queued; planning starts after the running job"
	}
	return "queued; planning starts within seconds"
}

func (r *run) agentSaveSettings(req map[string]any, by string) string {
	old := r.agentSettings()
	st := old
	for k, p := range map[string]*bool{"enabled": &st.Enabled, "auto_done": &st.AutoDone, "qa_enabled": &st.QAEnabled} {
		b, ok := req[k].(bool)
		if !ok {
			panic(refused("bad setting"))
		}
		*p = b
	}
	st.Assignees = nil
	users := asList(req["assignees"])
	if req["assignees"] != nil && users == nil || len(users) > 20 {
		panic(refused("assignees: " + r.o.Name + " user names"))
	}
	for _, u := range users {
		s, ok := u.(string)
		if !ok || !userRe.MatchString(s) {
			panic(refused("assignees: " + r.o.Name + " user names"))
		}
		st.Assignees = append(st.Assignees, s)
	}
	col := strings.TrimSpace(cleanText(req["column"], 61))
	if col == "" {
		col = "In Progress"
	}
	st.Column = head(col, 60)
	thr := orStr(req["qa_threshold"])
	if !contains([]string{"critical", "high", "medium"}, thr) {
		panic(refused("threshold: critical, high or medium"))
	}
	st.QAThreshold = thr
	cap, ok := asInt(req["qa_cap"])
	if !ok || cap < 0 || cap > 50 {
		panic(refused("daily cap 0..50"))
	}
	st.QACap = cap
	owner := orStr(req["qa_owner"])
	if owner != "" && !userRe.MatchString(owner) {
		panic(refused("QA intake owner: a user"))
	}
	st.QAOwner = owner
	if v, ok := req["fixer_trigger"]; ok && v != nil {
		if s := orStr(v); s != "off" && s != "column" {
			panic(refused("fixer trigger: off or column"))
		} else {
			st.Fixer = s
		}
	}
	for k, p := range map[string]*bool{"chat": &st.Chat, "secrets": &st.Secrets} {
		if v, ok := req[k]; ok && v != nil {
			b, isB := v.(bool)
			if !isB {
				panic(refused("task " + k + ": on or off"))
			}
			*p = b
		}
	}
	r.setState("agent_settings", string(pyDumps(st.object())))
	r.event(nil, "settings", by, "Task agent settings saved")
	return "saved"
}

// agentStatus is the "agent" block of board-status.json.
func (r *run) agentStatus(tick string, old json.RawMessage) object {
	var chat any = object{}
	var prev map[string]json.RawMessage
	if json.Unmarshal(old, &prev) == nil && len(prev["chat"]) > 0 {
		chat = rawOr(prev["chat"], object{})
	}
	return object{{"settings", r.agentSettings().object()}, {"tick", tick}, {"error", r.getState("agent_error", "")},
		{"qa_created", int64(0)}, {"rw_users", []any{}}, {"fixer_tasks", []any{}},
		{"debounce", int64(r.o.Agent.Debounce / time.Second)}, {"task_budget", r.o.Agent.TaskBudget}, {"chat", chat}}
}

func (r *run) agentBlock(old json.RawMessage) any {
	if r.o.Agent == nil {
		return rawOr(old, object{})
	}
	return r.agentStatus(r.agentTick, old)
}
