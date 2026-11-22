// Package board is `argus board sync` (root): the only writer of the task board database and the two-way sync of
// the board with Jira Server / Data Center through one shared ("bot") connection. It is the port of the reference
// installation's board sync, with the same request files, database rows and status file.
//
// One run:
//
//  1. requests  ReqDir/<id>.json from the web app: create, move, comment, assign, sync, settings, board-move.
//     Every file is consumed and re-validated (size, name, the user exists and is enabled in the auth
//     database, admin for settings). Its result goes to the status file's "requests" map.
//  2. push      queued creates, transitions (by workflow transition name), label changes, assignments and
//     comments, in order per task; transient errors retry with backoff, a conflicting Jira move wins.
//  3. pull      JQL `project = X AND updated >= -Nm` (wider after a gap), and a full reconcile nightly (or on
//     request): new issues become tasks, changes update them, comments are copied, issues no longer
//     seen are hidden. Conflict rule: Jira wins.
//  4. publish   Out/board-status.json for the app (no secret in it).
//
// With no Jira configured (no JIRA_URL / JIRA_PROJECT / token) the board works locally: steps 2 and 3 are skipped
// and the outbox waits.
//
// Jira configuration is read from root-owned, not group/world-writable env files (JIRA_*, BOARD_*, FIXER_TRIGGER
// lines; the first file that sets a key wins) and the token from a root-owned 0600 file.
//
// GitLab issue tasks are pushed and pulled in gitlab_issues.go (ADR-0009, ADR-0010).
//
// The Jira connection requests of Account and Settings -> Jira sync are applied first (jiraconnect.go); the pulls and
// writes with a user's own token are not ported yet (the shared connection is used).
//
// Not handled here (requests of these kinds are refused with a reason): the Jira agile board mirror, the fixer column and its bridge, approval proposals, lessons, the task
// agent and its chat, Matrix requests.
package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
)

// Options say where the job reads and writes, and how it names things.
type Options struct {
	DB       string            // the board database (BOARD_DB)
	ReqDir   string            // request files from the app (BOARD_REQ_DIR)
	Out      string            // status files for the app (BOARD_OUT): board-status.json
	AuthDB   string            // users and roles, read only (BOARD_AUTH_DB)
	EnvFiles []string          // Jira configuration files (BOARD_ENV_FILES)
	PATFile  string            // the Jira token file (BOARD_PAT_FILE)
	Config   map[string]string // Jira settings given directly (Settings -> Data sources); win over EnvFiles
	Token    string            // the Jira token given directly; wins over PATFile
	OwnerUID int               // the owner the config and token files must have (0 = root)
	AppURL   string            // the web app's address, in the Jira description's source link
	Name     string            // the product name in texts written to Jira ("<Name> task · ...")
	Label    string            // the label prefix of tasks this job creates ("<label>", "<label>-auto", "<label>-src-...")
	Ref      string            // the reference prefix of a task without an issue key ("<Ref>-<id>")
	DryRun   bool              // log Jira writes instead of sending them; nothing is changed in the database
	Full     bool              // a full reconcile this run
	Now      func() time.Time
	Log      *slog.Logger
	Allow    notify.Allowlist
	// NewJira builds the client (tests); nil: NewJira.
	NewJira func(base, token, caFile string) (*Jira, error)
	// JiraUser: basic auth (user name + token or password) instead of a bearer token (a Jira data source with a user).
	JiraUser string

	// GitLab issues (gitlab_issues.go)
	GitLabReqDir     string     // Settings -> Integrations requests (settings-in/gitlab-issues)
	EtcDir           string     // gitlab-issues.json, gitlab-issues.token, gitlab-issues-allowed-hosts
	GitLabEnvFile    string     // a KEY=value file whose GITLAB_URL gives the default URL and host
	GitLabDefaultURL string     // the configured GITLAB_URL
	GitLabURL        string     // Settings -> Data sources: the GitLab entry with purpose tracker
	GitLabToken      string     // its credential (never logged)
	Key              secret.Key // the master key: the stored token is sealed with it (one-volume layout)
	Resolve          func(host string) ([]net.IP, error)
	NewGitLab        func(base, token, pin string) (*GitLab, error) // tests; nil: NewGitLab

	// JiraConnectDir: the Account and Settings -> Jira sync connection requests (settings-in/jira-connect,
	// jiraconnect.go); "" turns them off. The connections live in EtcDir/jira.
	JiraConnectDir string

	// Agent turns the task agent on (agent.go); nil: its requests are refused as not handled.
	Agent *AgentOptions
}

// Columns of the board, in order.
var Columns = []string{"todo", "progress", "review", "fixer", "done"}

var columnLabel = map[string]string{"todo": "To do", "progress": "In progress", "review": "Review",
	"fixer": "Ready for fixer", "done": "Done"}

var statusDefault = map[string]string{"to do": "todo", "todo": "todo", "open": "todo", "backlog": "todo", "new": "todo",
	"reopened": "todo", "selected for development": "todo",
	"in progress": "progress", "in development": "progress", "doing": "progress",
	"review": "review", "in review": "review", "code review": "review", "testing": "review",
	"in testing": "review", "qa": "review", "ready for fixer": "fixer",
	"done": "done", "closed": "done", "resolved": "done", "won't do": "done", "cancelled": "done"}

var category = map[string]string{"new": "todo", "indeterminate": "progress", "done": "done"}

var (
	priorities = []string{"Highest", "High", "Medium", "Low", "Lowest"}
	envs       = []string{"develop", "pre-release", "release"}
	// envAliases maps the words teams use in labels to an environment.
	envAliases = map[string]string{"develop": "develop", "dev": "develop", "stage": "develop", "staging": "develop",
		"pre-release": "pre-release", "pre": "pre-release", "preprod": "pre-release", "pre-production": "pre-release",
		"release": "release", "prod": "release", "production": "release"}
	sourcesOK = []string{"incident", "rca", "vm", "disk", "backup", "cicd"}
	idRe      = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}$`)
	userRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,79}$`)
	labelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
	srcIDRe   = regexp.MustCompile(`^[0-9]{1,9}(-[0-9]{1,3})?$`)
	srcURLRe  = regexp.MustCompile(`^/(incidents/view|incidents/problem|rca/view|inventory/view|capacity|admin/design/view|backups|backups/plan|cicd/finding)(\?[A-Za-z0-9=&;_-]{0,80})?$`)
	keyRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}$`)
	projectRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)
	cfgLineRe = regexp.MustCompile(`^\s*(JIRA_[A-Z_]+|BOARD_[A-Z_]+|FIXER_TRIGGER)\s*=\s*(.*?)\s*$`)
)

const (
	maxFile     = 64 * 1024
	maxTitle    = 200
	maxText     = 32 * 1024
	maxPrompt   = 16 * 1024
	maxComment  = 4000
	keepResults = 100
)

var fields = []string{"summary", "status", "assignee", "priority", "labels", "description", "updated", "created", "comment"}

// Schema is the board database, as the reference job creates it (the tables this job owns).
const Schema = `
CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY,
  jira_key TEXT UNIQUE,
  title TEXT NOT NULL,
  description TEXT, evidence TEXT, fix_prompt TEXT,
  status TEXT NOT NULL DEFAULT 'todo',
  jira_status TEXT,
  assignee TEXT, assignee_name TEXT,
  priority TEXT, labels TEXT DEFAULT '[]',
  env TEXT, host TEXT,
  source_kind TEXT, source_id TEXT, source_url TEXT,
  created_by TEXT,
  requested_by TEXT,
  created_at INTEGER, updated_at INTEGER, jira_updated INTEGER, synced_at INTEGER,
  fix_request TEXT, fix_state TEXT, fix_by TEXT, fix_at INTEGER,
  gone INTEGER DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS tasks_source ON tasks(source_kind, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status, updated_at);
CREATE TABLE IF NOT EXISTS comments (
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, jira_id TEXT UNIQUE,
  author TEXT, body TEXT, created_at INTEGER, pending INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS comments_task ON comments(task_id, created_at);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY, task_id INTEGER, at INTEGER NOT NULL, kind TEXT NOT NULL, actor TEXT, text TEXT
);
CREATE INDEX IF NOT EXISTS events_task ON events(task_id, at);
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, kind TEXT NOT NULL,
  payload TEXT, created_at INTEGER NOT NULL, attempts INTEGER DEFAULT 0, next_at INTEGER DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'queued',
  last_error TEXT, done_at INTEGER
);
CREATE INDEX IF NOT EXISTS outbox_state ON outbox(state, id);
CREATE TABLE IF NOT EXISTS notices (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT, task_id INTEGER, actor TEXT, text TEXT);
CREATE TABLE IF NOT EXISTS state (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS task_seen (task_id INTEGER NOT NULL, principal TEXT NOT NULL, at INTEGER,
  PRIMARY KEY (task_id, principal));
CREATE TABLE IF NOT EXISTS board_moves (
  rid TEXT PRIMARY KEY, by TEXT, key TEXT, col INTEGER, transition TEXT, at INTEGER
);
`

// run is one sync run's state.
type run struct {
	o       *Options
	ctx     context.Context
	db      *sql.DB
	tx      *sql.Tx // the open transaction (nil between commits)
	cfg     map[string]string
	mp      mapping
	jira    *Jira
	conf    bool   // Jira configured (URL, project, token)
	botDown string // the bot cannot be used this run
	results map[string]object
	stats   map[string]int

	glResults map[string]object // Settings -> Integrations (GitLab issues) results, by request id
	agentTick string            // the task agent's summary of this run
	jcResults map[string]any    // Jira connection request results, by request id
	chatTickS string            // the task chat's summary of this run
}

func (r *run) now() int64 { return r.o.Now().Unix() }

func (r *run) log() *slog.Logger { return r.o.Log }

// --- database --------------------------------------------------------------------------------------------------------

// row is one result row by column name.
type row map[string]any

func (t row) s(k string) string {
	switch x := t[k].(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func (t row) i(k string) int64 {
	switch x := t[k].(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func (t row) null(k string) bool { return t[k] == nil }

type querier interface {
	QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error)
}

func (r *run) q() querier {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *run) begin() error {
	if r.tx != nil {
		return nil
	}
	tx, err := r.db.BeginTx(r.ctx, nil)
	if err != nil {
		return err
	}
	r.tx = tx
	return nil
}

func (r *run) commit() error {
	if r.tx == nil {
		return nil
	}
	tx := r.tx
	r.tx = nil
	if r.o.DryRun {
		return tx.Rollback()
	}
	return tx.Commit()
}

func (r *run) rollback() {
	if r.tx != nil {
		r.tx.Rollback()
		r.tx = nil
	}
}

func (r *run) exec(q string, a ...any) (sql.Result, error) {
	if err := r.begin(); err != nil {
		return nil, err
	}
	return r.tx.ExecContext(r.ctx, q, a...)
}

func (r *run) must(q string, a ...any) sql.Result {
	res, err := r.exec(q, a...)
	if err != nil {
		panic(dbErr{err})
	}
	return res
}

type dbErr struct{ err error }

func (r *run) rows(q string, a ...any) []row {
	rs, err := r.q().QueryContext(r.ctx, q, a...)
	if err != nil {
		panic(dbErr{err})
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []row
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			panic(dbErr{err})
		}
		m := row{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				m[c] = string(b)
			} else {
				m[c] = vals[i]
			}
		}
		out = append(out, m)
	}
	return out
}

func (r *run) one(q string, a ...any) row {
	if rs := r.rows(q, a...); len(rs) > 0 {
		return rs[0]
	}
	return nil
}

func (r *run) getState(k, def string) string {
	if t := r.one(`SELECT v FROM state WHERE k=?`, k); t != nil && !t.null("v") {
		return t.s("v")
	}
	if t := r.one(`SELECT v FROM state WHERE k=?`, k); t != nil {
		return ""
	}
	return def
}

func (r *run) setState(k, v string) {
	r.must(`INSERT OR REPLACE INTO state (k, v) VALUES (?, ?)`, k, v)
}

func (r *run) stateInt(k string) int64 {
	n, _ := strconv.ParseInt(r.getState(k, "0"), 10, 64)
	return n
}

func (r *run) event(tid any, kind, actor, text string) {
	r.must(`INSERT INTO events (task_id, at, kind, actor, text) VALUES (?,?,?,?,?)`, tid, r.now(), kind, head(actor, 64), head(text, 600))
}

func (r *run) notice(kind string, tid any, actor, text string) {
	r.must(`INSERT INTO notices (at, kind, task_id, actor, text) VALUES (?,?,?,?,?)`, r.now(), kind, tid, head(actor, 64), head(text, 300))
}

func (r *run) queue(tid int64, kind string, payload object) {
	r.must(`INSERT INTO outbox (task_id, kind, payload, created_at) VALUES (?,?,?,?)`, tid, kind, string(pyDumps(payload)), r.now())
}

func (r *run) taskRef(t row) string {
	if t.s("tracker") == "gitlab" {
		if t.null("gl_iid") {
			return r.o.Ref + "-" + t.s("id")
		}
		if g := t.s("gl_ref"); g != "" {
			return "GitLab " + g
		}
		return "GitLab #" + t.s("gl_iid")
	}
	if k := t.s("jira_key"); k != "" {
		return k
	}
	return r.o.Ref + "-" + t.s("id")
}

// Connect opens (and creates or migrates) the board database.
func Connect(ctx context.Context, path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("board database path must be absolute: %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(DELETE)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		db.Close()
		return nil, err
	}
	cols := map[string]bool{}
	rs, err := db.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for rs.Next() {
		var cid int
		var name, typ string
		var nn, pk int
		var def any
		rs.Scan(&cid, &name, &typ, &nn, &def, &pk)
		cols[name] = true
	}
	rs.Close()
	var alters []string
	if !cols["jira_url"] {
		alters = append(alters, `ALTER TABLE tasks ADD COLUMN jira_url TEXT`)
	}
	if !cols["tracker"] {
		alters = append(alters, `ALTER TABLE tasks ADD COLUMN tracker TEXT NOT NULL DEFAULT 'jira'`,
			`UPDATE tasks SET tracker='jira' WHERE tracker IS NULL OR tracker=''`)
	}
	for _, c := range [][2]string{{"gl_project", "INTEGER"}, {"gl_iid", "INTEGER"}, {"gl_url", "TEXT"}, {"gl_ref", "TEXT"}} {
		if !cols[c[0]] {
			alters = append(alters, "ALTER TABLE tasks ADD COLUMN "+c[0]+" "+c[1])
		}
	}
	alters = append(alters, `CREATE UNIQUE INDEX IF NOT EXISTS tasks_gl ON tasks(gl_project, gl_iid) WHERE gl_iid IS NOT NULL`)
	for _, a := range alters {
		if _, err := db.ExecContext(ctx, a); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// --- configuration ---------------------------------------------------------------------------------------------------

func trusted(path string, owner int) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == owner
}

// LoadConfig reads the Jira configuration files. Later files never override earlier ones.
func LoadConfig(files []string, owner int) (map[string]string, []string) {
	cfg := map[string]string{}
	problems := []string{}
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if !trusted(p, owner) {
			problems = append(problems, p+" ignored: must be root-owned and not group/world-writable")
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, 65536))
		f.Close()
		for _, line := range splitLines(string(data)) {
			if m := cfgLineRe.FindStringSubmatch(line); m != nil {
				if _, ok := cfg[m[1]]; !ok {
					cfg[m[1]] = strings.Trim(strings.TrimSpace(m[2]), `'"`)
				}
			}
		}
	}
	if v := cfg["JIRA_PROJECT"]; v != "" && !projectRe.MatchString(v) {
		problems = append(problems, "JIRA_PROJECT must be a project key such as OPS")
		delete(cfg, "JIRA_PROJECT")
	}
	if v := cfg["JIRA_BOARD_ID"]; v != "" && !isDigits(v) {
		problems = append(problems, "JIRA_BOARD_ID must be a number")
		delete(cfg, "JIRA_BOARD_ID")
	}
	return cfg, problems
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// LoadToken reads the token file: a root-owned regular file with mode 0600.
func LoadToken(path string, owner int) (string, string) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "no token file"
	} else if err != nil {
		return "", "cannot read token file"
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != owner || fi.Mode().Perm()&0o077 != 0 {
		return "", path + " must be a root-owned regular file with mode 0600"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "cannot read token file"
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	f.Close()
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", "token file is empty"
	}
	return tok, ""
}

func pairs(text, sep string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(text, sep) {
		if a, b, ok := strings.Cut(part, "="); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(b) != "" {
			out = append(out, [2]string{strings.TrimSpace(a), strings.TrimSpace(b)})
		}
	}
	return out
}

type mapping struct {
	status    map[string]string
	trans     map[string][]string
	users     map[string]string
	userOrder []string
	approvers []string
}

func newMapping(cfg map[string]string) mapping {
	m := mapping{status: map[string]string{}, trans: map[string][]string{}, users: map[string]string{}}
	for k, v := range statusDefault {
		m.status[k] = v
	}
	for _, p := range pairs(cfg["JIRA_STATUS_MAP"], ";") {
		if contains(Columns, p[1]) {
			m.status[strings.ToLower(p[0])] = p[1]
		}
	}
	for _, p := range pairs(cfg["JIRA_TRANSITIONS"], ";") {
		if contains(Columns, p[0]) {
			var names []string
			for _, n := range strings.Split(p[1], "|") {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, strings.ToLower(n))
				}
			}
			m.trans[p[0]] = names
		}
	}
	for _, p := range pairs(cfg["JIRA_USER_MAP"], ",") {
		if _, ok := m.users[p[0]]; !ok {
			m.userOrder = append(m.userOrder, p[0])
		}
		m.users[p[0]] = p[1]
	}
	seen := map[string]bool{}
	for _, u := range strings.Split(cfg["JIRA_FIXER_APPROVERS"], ",") {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			m.approvers = append(m.approvers, u)
		}
	}
	sort.Strings(m.approvers)
	return m
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// column is the board column of a Jira status object {name, statusCategory{key}}.
func (m mapping) column(status any) string {
	name, cat := "", ""
	switch s := status.(type) {
	case map[string]any:
		name = orStr(s["name"])
		cat = orStr(asMap(s["statusCategory"])["key"])
	case string:
		name = s
	}
	if c := m.status[strings.ToLower(name)]; c != "" {
		return c
	}
	if c := category[cat]; c != "" {
		return c
	}
	return "todo"
}

func (m mapping) jiraUser(u string) string {
	if v, ok := m.users[u]; ok {
		return v
	}
	return u
}

func (m mapping) pickTransition(ts []any, col string) any {
	for _, want := range m.trans[col] {
		for _, t := range ts {
			if strings.ToLower(orStr(asMap(t)["name"])) == want {
				return asMap(t)["id"]
			}
		}
	}
	for _, t := range ts {
		to := asMap(t)["to"]
		if to == nil {
			to = map[string]any{}
		}
		if m.column(to) == col {
			return asMap(t)["id"]
		}
	}
	for _, t := range ts {
		if strings.ToLower(orStr(asMap(t)["name"])) == strings.ToLower(columnLabel[col]) {
			return asMap(t)["id"]
		}
	}
	return nil
}

// --- users -----------------------------------------------------------------------------------------------------------

func (r *run) userRole(name string) string {
	db, err := sql.Open("sqlite", "file:"+r.o.AuthDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var role string
	var disabled sql.NullInt64
	if err := db.QueryRowContext(r.ctx, `SELECT role, disabled FROM users WHERE username=?`, name).Scan(&role, &disabled); err != nil {
		return ""
	}
	if disabled.Valid && disabled.Int64 != 0 {
		return ""
	}
	if role == "admin" || role == "viewer" {
		return role
	}
	return ""
}

// --- the Jira description: compose and parse back -------------------------------------------------------------------

func nf(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "{noformat}", "{ noformat}"))
}

func (r *run) compose(t row) string {
	desc := strings.TrimSpace(t.s("description"))
	if desc == "" {
		desc = "Created from " + r.o.Name + "."
	}
	parts := []string{desc}
	if strings.TrimSpace(t.s("evidence")) != "" {
		parts = append(parts, "h3. Evidence\n{noformat}\n"+nf(t.s("evidence"))+"\n{noformat}")
	}
	if strings.TrimSpace(t.s("fix_prompt")) != "" {
		parts = append(parts, "h3. Fix prompt\n{noformat}\n"+nf(t.s("fix_prompt"))+"\n{noformat}")
	}
	env := t.s("env")
	if env == "" {
		env = "unknown"
	}
	foot := []string{"env: " + env}
	if t.s("host") != "" {
		foot = append(foot, "host: "+t.s("host"))
	}
	if t.s("source_kind") != "" {
		src := "source: " + t.s("source_kind") + " " + nullStr(t["source_id"])
		if t.s("source_url") != "" {
			src += " (" + r.o.AppURL + t.s("source_url") + ")"
		}
		foot = append(foot, src)
	}
	by := t.s("requested_by")
	if by == "" {
		by = t.s("created_by")
	}
	if by == "" {
		by = "?"
	}
	foot = append(foot, "by: "+by)
	parts = append(parts, "----\n"+r.o.Name+" task · "+strings.Join(foot, " · "))
	return strings.Join(parts, "\n\n")
}

func nullStr(v any) string {
	if v == nil {
		return "None"
	}
	return row{"x": v}.s("x")
}

func block(text, name string) (string, bool) {
	re := regexp.MustCompile(`(?ms)^h3\. ` + name + `[ \t\f\v\r]*\n\{noformat\}\n(.*?)\n?\{noformat\}`)
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.ReplaceAll(m[1], "{ noformat}", "{noformat}"), true
	}
	return "", false
}

type parsed struct {
	description         string
	evidence, fixPrompt *string
	env, host           string
}

func (r *run) parse(desc string) parsed {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	var p parsed
	if v, ok := block(desc, "Evidence"); ok {
		p.evidence = &v
	}
	if v, ok := block(desc, "Fix prompt"); ok {
		p.fixPrompt = &v
	}
	cut := regexp.MustCompile(`(?m)^h3\. (Evidence|Fix prompt)[ \t\f\v\r]*$|^----\n` + regexp.QuoteMeta(r.o.Name) + ` task`)
	headText := desc
	if loc := cut.FindStringIndex(desc); loc != nil {
		headText = desc[:loc[0]]
	}
	p.description = strings.TrimSpace(headText)
	foot := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(r.o.Name) + ` task · (.*)$`)
	if m := foot.FindStringSubmatch(desc); m != nil {
		for _, part := range strings.Split(m[1], " · ") {
			k, v, _ := strings.Cut(part, ": ")
			if k == "env" && contains(envs, v) {
				p.env = v
			} else if k == "host" && hostRe.MatchString(v) {
				p.host = v
			}
		}
	}
	return p
}

// --- text helpers ----------------------------------------------------------------------------------------------------

func cleanText(v any, cap int) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\x00", "")
	return head(s, cap)
}

func (r *run) scan(text, where string) string {
	out, hits := notify.Redact(text, r.o.Allow)
	if len(hits) > 0 {
		r.log().Warn("secret scan redacted", "label", "board:jira", "where", where, "kinds", notify.HitsLine(hits))
	}
	return out
}

// jiraTS: '2026-10-20T12:34:56.000+0330' -> unix seconds (0 if unparsable).
func jiraTS(v any) int64 {
	m := jiraTSRe.FindStringSubmatch(orStr(v))
	if m == nil {
		return 0
	}
	n := func(s string) int { x, _ := strconv.Atoi(s); return x }
	t := time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]), n(m[4]), n(m[5]), n(m[6]), 0, time.UTC)
	off := int64(n(m[8])*3600 + n(m[9])*60)
	if m[7] == "-" {
		off = -off
	}
	return t.Unix() - off
}

var jiraTSRe = regexp.MustCompile(`^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?([+-])(\d\d):?(\d\d)$`)

func jsonList(s string) []string {
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

func listAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// envFromLabels is the environment named by the first environment label: env-<x>, env:<x> or env::<x> (GitLab
// scoped labels), where <x> is an environment or a common alias (stage, dev, pre, prod, production).
func envFromLabels(labels []string) string {
	for _, lb := range labels {
		l := strings.ToLower(strings.TrimSpace(lb))
		var v string
		switch {
		case strings.HasPrefix(l, "env::"):
			v = l[5:]
		case strings.HasPrefix(l, "env:"), strings.HasPrefix(l, "env-"):
			v = l[4:]
		default:
			continue
		}
		if e, ok := envAliases[strings.TrimSpace(v)]; ok {
			return e
		}
	}
	return ""
}
