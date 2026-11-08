package app

// views_gitlab.go: GitLab issues, the second task tracker beside Jira (Go only, ADR-0009, ADR-0010). Settings ->
// Integrations gets the connect card (the issue board's project or group, the board, a project or group access token,
// for a group board the default project); the board, the task page,
// New task and Proposed tasks name each task's tracker and let the person choose where a new one goes. The token is
// written once into settings-in/gitlab-issues/<user>-<16 hex>.json (0600) and forgotten: this module reads only
// settings-out/gitlab-issues-status.json; root's board sync (bin/gitlab_issues.py) does everything else.

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	gitlabTokenRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,200}\n?$`)
	gitlabMapRe     = regexp.MustCompile(`^[^\x00-\x1f<>"]{0,600}$`)
	gitlabTrackers  = []string{"jira", "gitlab"}
	gitlabColumnsRe = regexp.MustCompile(`^(todo|progress|review|fixer|done)$`)
)

// GitLabStatus is settings-out/gitlab-issues-status.json ({} when missing).
func GitLabStatus() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "gitlab-issues-status.json")) }

// GitLabConnected: root holds a working GitLab issues connection.
func GitLabConnected() bool { return py.Truthy(GitLabStatus().Get("connected")) }

// GitLabDefaultTracker is the tracker a new task goes to unless the person picks the other one.
func GitLabDefaultTracker() string {
	if GitLabConnected() && py.Str(py.AsMap(GitLabStatus().Get("settings")).Get("default_tracker")) == "gitlab" {
		return "gitlab"
	}
	return "jira"
}

// TaskTracker is "gitlab" for a GitLab issue, else "jira" (every task from before GitLab issues).
func TaskTracker(t Row) string {
	if t.Has("tracker") && t.Get("tracker") == "gitlab" {
		return "gitlab"
	}
	return "jira"
}

// TrackerTag names where a task lives, in words (DESIGN.md: a neutral .tag for a category, never colour alone).
func TrackerTag(t Row) string {
	if TaskTracker(t) == "gitlab" {
		return `<span class="tag trk" title="GitLab issue">GitLab</span>`
	}
	if t.T("jira_key") || jiraInUse() {
		return `<span class="tag trk" title="Jira issue">Jira</span>`
	}
	return `<span class="tag trk" title="Argus only until Jira is connected">Local</span>`
}

// gitlabGroupMode: the connection mirrors a group issue board (issues of every project in the group).
func gitlabGroupMode(st *py.OMap) bool {
	return py.Str(py.AsMap(st.Get("connection")).Get("mode")) == "group"
}

// gitlabProjects is the group board's projects a new issue may go to: [id, short path] (root publishes them).
func gitlabProjects(st *py.OMap) [][2]string {
	var out [][2]string
	for _, x := range py.AsList(st.Get("projects")) {
		m := py.AsMap(x)
		if id := py.I(m.Get("id")); id > 0 {
			out = append(out, [2]string{I2S(id), py.OrS(m.Get("short"), py.Str(m.Get("path")))})
		}
	}
	return out
}

// gitlabCheckProject: "" or a project id of the connected group board (the "GitLab project" select); ok=false when
// the value is not one of them. Root checks it again.
func gitlabCheckProject(v string) (string, bool) {
	v = py.Strip(v)
	if v == "" {
		return "", true
	}
	for _, p := range gitlabProjects(GitLabStatus()) {
		if p[0] == v {
			return v, true
		}
	}
	return "", false
}

// gitlabProjectOptions: the <option>s of a project select, cur selected ("" adds "Choose a project").
func gitlabProjectOptions(st *py.OMap, cur string) string {
	var opts strings.Builder
	if cur == "" {
		opts.WriteString(`<option value="" selected>Choose a project</option>`)
	}
	for _, p := range gitlabProjects(st) {
		opts.WriteString(`<option value="` + p[0] + `"` + If(p[0] == cur, " selected", "") + `>` + E(p[1]) + `</option>`)
	}
	return opts.String()
}

func gitlabDefaultProject(st *py.OMap) string {
	if d := py.I(py.AsMap(st.Get("connection")).Get("default_project_id")); d > 0 {
		return I2S(d)
	}
	return ""
}

// gitlabProjectSelect is the "GitLab project" select of New task and Accept (a group board only).
func gitlabProjectSelect(st *py.OMap, cur string) string {
	if !gitlabGroupMode(st) || len(gitlabProjects(st)) == 0 {
		return ""
	}
	if cur == "" {
		cur = gitlabDefaultProject(st)
	}
	return `<label class="trk-proj">GitLab project <span class="dim">(GitLab issues only)</span><select name="gl_project">` +
		gitlabProjectOptions(st, cur) + `</select></label>`
}

// GitLabWriteBlock is "" when a change to a GitLab task can be sent, else the reason.
func GitLabWriteBlock() string {
	if GitLabConnected() {
		return ""
	}
	return "GitLab issues is not connected (Settings → Integrations)."
}

// TaskWriteBlock is "" when this user's change to this task can reach its tracker, else the reason.
func TaskWriteBlock(sess *Session, t Row) string {
	if t != nil && TaskTracker(t) == "gitlab" {
		return GitLabWriteBlock()
	}
	return BoardWriteBlock(sess)
}

// boardCanChangeTask: Move controls on one card (admins whose change reaches that task's tracker).
func boardCanChangeTask(sess *Session, t Row) bool {
	return sess != nil && sess.Role == "admin" && TaskWriteBlock(sess, t) == ""
}

// boardCanCreate: the New task button (an admin who can write to at least one tracker).
func boardCanCreate(sess *Session) bool {
	return BoardCanChange(sess) || (sess != nil && sess.Role == "admin" && GitLabConnected())
}

// GitLabIssueLink is the issue's web URL (https only), or "".
func GitLabIssueLink(t Row) string {
	if TaskTracker(t) == "gitlab" && strings.HasPrefix(t.S("gl_url"), "https://") {
		return t.S("gl_url")
	}
	return ""
}

// trackerChoice is the "Create in" control of New task and Accept: shown only when GitLab issues is connected. On a
// group board it adds the "GitLab project" select (curProject: the person's pick, "" for the default project).
func trackerChoice(sess *Session, cur string, curProject ...string) string {
	if !GitLabConnected() {
		return ""
	}
	if cur != "gitlab" {
		cur = "jira"
	}
	st := GitLabStatus()
	gsub := "Project " + py.OrS(py.AsMap(st.Get("connection")).Get("project_path"), "the connected project") +
		", with Argus's project token."
	if gitlabGroupMode(st) {
		gsub = "Group board " + py.OrS(py.AsMap(st.Get("connection")).Get("group_path"), "?") + ", in the project below."
	}
	pick := ""
	if len(curProject) > 0 {
		pick = curProject[0]
	}
	jiraWhy := "Your Jira connection, else the shared one."
	if why := BoardWriteBlock(sess); why != "" {
		jiraWhy = why
	}
	opt := func(v, label, sub string) string {
		return `<label class="opt"><input type="radio" name="tracker" value="` + v + `"` + If(cur == v, " checked", "") +
			`><span><b>` + label + `</b><small>` + E(sub) + `</small></span></label>`
	}
	return `<fieldset class="trk-pick"><legend>Create in</legend>` + opt("jira", "Jira", jiraWhy) +
		opt("gitlab", "GitLab issues", gsub) + gitlabProjectSelect(st, pick) + `</fieldset>`
}

// gitlabSyncLine is the board status line's GitLab part (the GitLab view shows it instead of Jira's).
func gitlabSyncLine(admin bool) string {
	st := GitLabStatus()
	fix := func(word string) string { return If(admin, ` · <a href="/admin/outline#gitlab">`+word+`</a>`, "") }
	run := py.AsMap(py.Or(st.Get("last_run"), py.NewOMap()))
	switch {
	case !py.Truthy(st.Get("connected")):
		return `<span class="warnc">GitLab not connected</span>` + fix("Connect")
	case py.Truthy(run.Get("error")):
		return `<span class="failc">GitLab sync failing</span>` + fix("details")
	case py.Truthy(run.Get("at")):
		return "GitLab · synced " + E(Ago(run.Get("at")))
	}
	return "GitLab · waiting for the first sync"
}

// boardReadOnlyFor is the board's one read-only line for the tracker view: the GitLab view says why GitLab changes
// cannot be sent, never Jira's reason; "All tasks" names the tracker that is read-only.
func boardReadOnlyFor(sess *Session, tracker string) string {
	admin := sess != nil && sess.Role == "admin"
	switch {
	case tracker == "gitlab" && admin && GitLabWriteBlock() == "":
		return ""
	case tracker == "gitlab" && admin:
		return `<p class="section-note kc-readonly">Read-only: ` + E(GitLabWriteBlock()) + `</p>`
	case tracker == "gitlab":
		return `<p class="section-note kc-readonly">Read-only for viewers: ask an admin, or change it in GitLab.</p>`
	case tracker == "all" && admin && !BoardCanChange(sess) && GitLabConnected():
		return `<p class="section-note kc-readonly">Jira tasks are read-only (` + E(strings.TrimSuffix(BoardWriteBlock(sess), ".")) +
			`). GitLab tasks can be moved.</p>`
	}
	return BoardReadOnlyNote(sess)
}

// --- Settings -> Integrations: the GitLab issues card ----------------------------------------------------

func gitlabDrop(sess *Session, payload *py.OMap) (string, error) {
	d := filepath.Join(SET_IN, "gitlab-issues")
	rid := auth.TokenHex(8)
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(d, sess.Username+"-"+rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("gitlab-issues %s requested by %s: %s", py.Str(payload.Get("kind")), sess.Username, rid)
	return rid, nil
}

func gitlabResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	if boardExists(filepath.Join(SET_IN, "gitlab-issues", user+"-"+rid+".json")) {
		c.Refresh = true
		return Banner("Working on "+ARGUS_HOSTNAME+"; this page refreshes itself.", "ok")
	}
	r := py.AsMap(py.AsMap(GitLabStatus().Get("results")).Get(rid))
	if !py.Truthy(r) || r.Get("user") != user {
		c.Refresh = true
		return StateBox(StSynced, "Saved. Waiting for the board sync to apply it.", "Safe to leave this page: the result appears here once it has run. The page refreshes itself.", "")
	}
	res := py.Str(r.GetD("result", ""))
	if strings.HasPrefix(res, "refused: ") {
		return StateBox(StFailed, "GitLab issues not saved.", "The sync refused it: "+SafeErr(py.Slice(res, 9))+". Nothing was changed; correct it and save again.", "")
	}
	return Banner("GitLab issues: "+res+".", "ok")
}

func gitlabPairs(m *py.OMap) string {
	var out []string
	for _, k := range m.Keys() {
		out = append(out, k+"="+py.Str(m.Get(k)))
	}
	return strings.Join(out, "; ")
}

// GitLabPost handles POST /admin/gitlab-issues: (303, location) or (code, html).
func GitLabPost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := form.Get
	act := get("action")
	var rid string
	var err error
	// bad: the page again with the entries kept and the field marked (never the token)
	bad := func(fk, field, msg string) (int, string) {
		return 400, OutlineRefused(c, Reject(fk, field, msg, form))
	}
	switch act {
	case "connect":
		url, project, board, token := py.Strip(get("url")), py.Strip(get("project")), py.Strip(get("board")), py.Strip(get("token"))
		if url != "" && (!(strings.HasPrefix(url, "https://") || (OutlineTestAllowHTTP && strings.HasPrefix(url, "http://"))) ||
			py.Len(url) > 300 || strings.ContainsAny(url, " \t\n")) {
			return bad("gitlab-connect", "url", "The GitLab URL must start with https:// (at most 300 characters, no spaces).")
		}
		if project == "" || py.Len(project) > 400 || strings.ContainsAny(project, " \t\n<>\"") {
			return bad("gitlab-connect", "project", "Paste the issue board URL (group or project), or enter group/project.")
		}
		if py.Len(board) > 400 || strings.ContainsAny(board, " \t\n<>\"") {
			return bad("gitlab-connect", "board", "The board is a number or a board URL.")
		}
		if token != "" && !gitlabTokenRe.MatchString(token) { // empty: the GitLab data source with purpose tracker
			return bad("gitlab-connect", "token", "That does not look like a GitLab access token.")
		}
		rid, err = gitlabDrop(sess, py.NewOMap("kind", "connect", "url", url, "project", project, "board", board, "token", token))
	case "board":
		b := get("board")
		if !py.IsDigit(b) || py.Len(b) > 10 {
			return bad("gitlab-board", "board", "Choose a board.")
		}
		rid, err = gitlabDrop(sess, py.NewOMap("kind", "board", "board", b))
	case "project":
		pid := get("project")
		if !py.IsDigit(pid) || py.Len(pid) > 10 {
			return bad("gitlab-project", "project", "Choose a project.")
		}
		rid, err = gitlabDrop(sess, py.NewOMap("kind", "project", "project", pid))
	case "settings":
		tr, lm, um := get("default_tracker"), py.Strip(get("list_map")), py.Strip(get("user_map"))
		if tr == "" {
			tr = "jira"
		}
		switch {
		case !In(tr, gitlabTrackers...):
			return bad("gitlab-settings", "default_tracker", "Choose where new tasks go: Jira or GitLab issues.")
		case !gitlabMapRe.MatchString(lm):
			return bad("gitlab-settings", "list_map", "List map: at most 600 characters, no < > or quotes.")
		case !gitlabMapRe.MatchString(um):
			return bad("gitlab-settings", "user_map", "User map: at most 600 characters, no < > or quotes.")
		}
		for _, p := range strings.Split(lm, ";") {
			if kv := strings.SplitN(p, "=", 2); len(kv) == 2 && !gitlabColumnsRe.MatchString(py.Strip(kv[1])) {
				return bad("gitlab-settings", "list_map", "List map: the column is one of todo, progress, review, fixer, done.")
			}
		}
		rid, err = gitlabDrop(sess, py.NewOMap("kind", "settings", "default_tracker", tr, "list_map", lm, "user_map", um))
	case "sync", "disconnect":
		rid, err = gitlabDrop(sess, py.NewOMap("kind", act))
	case "hosts":
		hosts, badH := boardSplitHosts(get("hosts"), outlineHostRe)
		if len(badH) > 0 || len(hosts) > 10 {
			return bad("gitlab-hosts", "hosts", "One host per line, optionally host:port; at most 10.")
		}
		hl := make([]any, len(hosts))
		for i, h := range hosts {
			hl[i] = h
		}
		rid, err = gitlabDrop(sess, py.NewOMap("kind", "hosts", "hosts", hl))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/outline?g=" + rid + "#gitlab"
}

// boardHasTracker: board.db has the tracker column (root adds it on its first run after the update).
func boardHasTracker() bool {
	return len(BoardBQ("SELECT 1 FROM pragma_table_info('tasks') WHERE name='tracker'")) > 0
}
