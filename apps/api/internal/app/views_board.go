package app

// views_board.py: the Argus task board (/board), the task page, "Create task" from incidents, RCA fix proposals and
// analyses, and the Jira sync status in Admin (/admin/board). The app never writes board.db and never sees the Jira
// token: every action is a request file in settings-in/board-requests/ that root's board sync validates again.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	BoardColumns    = []string{"todo", "progress", "review", "fixer", "done"}
	BoardLabel      = map[string]string{"todo": "To do", "progress": "In progress", "review": "Review", "fixer": "Ready for fixer", "done": "Done"}
	BoardPriorities = []string{"Highest", "High", "Medium", "Low", "Lowest"}
	BoardEnvs       = []string{"develop", "pre-release", "release"}
	BoardFixEnvs    = []string{"develop", "pre-release"}
	// SOURCES, in the Python dict's order.
	BoardSourceKeys  = []string{"incident", "rca", "vm", "disk", "backup", "cicd"}
	BoardSourceNames = map[string]string{"incident": "Incident", "rca": "RCA fix", "vm": "VM analysis", "disk": "Storage analysis",
		"backup": "Backup", "cicd": "CI/CD"}
	// Python's re.match with "$" also accepts one trailing newline: "\n?$" keeps that.
	boardIDRe    = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}\n?$`)
	boardHostRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,79}\n?$`)
	boardLabelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}\n?$`)
	boardAliasRe = regexp.MustCompile(`^(dev|pre)-[a-z0-9][a-z0-9-]*-fix\n?$`)
	boardKeyFull = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}$`)
	boardRcaSid  = regexp.MustCompile(`^[0-9]{1,9}-[0-9]{1,3}$`)
	boardCicdSid = regexp.MustCompile(`^[0-9]{1,9}$`)
)

const (
	BoardMaxTitle   = 200
	BoardMaxText    = 32 * 1024
	BoardMaxPrompt  = 16 * 1024
	BoardMaxComment = 4000
	BoardDoneDays   = 14
)

func boardExists(p string) bool { _, err := os.Stat(p); return err == nil }

func boardIsSource(k string) bool { _, ok := BoardSourceNames[k]; return ok }

// BoardStatus is views_board.status(): settings-out/board-status.json ({} when missing or not an object).
func BoardStatus() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "board-status.json")) }

// BoardShortAgo is views_board.short_ago(ts).
func BoardShortAgo(ts any) string {
	if !py.Truthy(ts) {
		return "-"
	}
	s := int64(Now() - py.Num(ts))
	if s < 0 {
		s = 0
	}
	for _, u := range []struct {
		unit string
		n    int64
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if s >= u.n {
			return I2S(s/u.n) + u.unit
		}
	}
	return "now"
}

// BoardJiraLink is views_board.jira_link(st, key, base).
func BoardJiraLink(st *py.OMap, key any, base any) string {
	b := py.OrS(base, "")
	if b == "" {
		b = py.OrS(st.Get("jira_url"), "")
	}
	if b == "" {
		b = py.OrS(py.AsMap(JiraState().Get("shared")).Get("url"), "")
	}
	if !py.Truthy(key) || !strings.HasPrefix(b, "https://") {
		return ""
	}
	return b + "/browse/" + py.Quote(py.Str(key))
}

// --- requests ------------------------------------------------------------------------------------------

// BoardWriteRequest is views_board.write_request(payload, sess): the request id.
func BoardWriteRequest(payload *py.OMap, sess *Session) (string, error) {
	rid := time.Now().Format("20060102150405") + "-" + auth.TokenHex(4)
	d := filepath.Join(SET_IN, "board-requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		return "", err
	}
	p := payload.Copy()
	p.Set("id", rid)
	p.Set("by", sess.Username)
	p.Set("at", NowInt())
	tmp := filepath.Join(d, "."+rid+".tmp")
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), tmp, []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("board %s requested by %s: %s", py.Str(p.Get("kind")), sess.Username, rid)
	return rid, nil
}

// boardCouldNotSave is server.py's answer to an OSError of a POST handler.
func boardCouldNotSave(c *Ctx, err error) (int, string) {
	logf("save failed: %v", err)
	return 500, "Could not save"
}

// boardDropExcl writes a 0600 request file with O_EXCL|O_NOFOLLOW and renames it (views_jira/_outline _drop).
func boardDropExcl(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// BoardRequestBanner is views_board.request_banner(qs).
func BoardRequestBanner(c *Ctx, qs py.QS) string {
	rid := qs.Get("req")
	if !boardIDRe.MatchString(rid) {
		return ""
	}
	st := BoardStatus()
	if b, ok := requestWaitBanner(c, filepath.Join(SET_IN, "board-requests", rid+".json"), rid, "the board sync",
		"Nothing changed in GitLab or Jira.", py.Num(st.Get("last_run"))); ok {
		return b
	}
	res := py.AsMap(py.AsMap(st.Get("requests")).Get(rid))
	if !py.Truthy(res) {
		if age, ok := requestAge(rid); ok && age >= requestNoResult {
			return Banner("No result: the board sync took this request but reported nothing after "+
				strconv.Itoa(int(age)/60)+" min. Check the board sync log.", "fail")
		}
		c.Refresh = true
		return Banner("Sent. Waiting for the board sync; this page refreshes itself.", "ok")
	}
	r := py.Str(res.GetD("result", ""))
	if r == "choose" {
		return ""
	}
	if strings.HasPrefix(r, "refused") {
		return Banner("Not applied: "+py.Slice(r, len("refused: ")), "fail")
	}
	if r == "exists" && py.Truthy(res.Get("task")) {
		return `<div class="banner warn" role="status">` + Icon("alert") + `<span>A task for this source already exists: ` +
			`<a href="/board/task?id=` + I2S(py.I(res.Get("task"))) + `">open it</a>.</span></div>`
	}
	if r == "created" && py.Truthy(res.Get("task")) {
		return `<div class="banner ok" role="status">` + Icon("check") + `<span>Task created. ` +
			`<a href="/board/task?id=` + I2S(py.I(res.Get("task"))) + `">Open it</a>.</span></div>`
	}
	m := map[string]string{"moved": "Moved.", "commented": "Comment added.", "assigned": "Assigned to you.",
		"saved": "Saved.", "sync requested": "Sync requested; it runs within seconds.",
		"discarded": "Discarded; nothing was sent to Jira."}
	if v, ok := m[r]; ok {
		return Banner(v)
	}
	return Banner(py.Capitalize(r) + ".")
}

// --- the board ---------------------------------------------------------------------------------------

func boardSyncLine(st *py.OMap) string {
	if !py.Truthy(st) {
		return "Board sync has not run yet"
	}
	nUsers := py.I(py.Or(st.Get("users_connected"), int64(0)))
	if !py.Truthy(st.Get("configured")) && nUsers != 0 {
		return "Jira · " + I2S(nUsers) + " personal connection" + py.Plural(int(nUsers))
	}
	if !py.Truthy(st.Get("configured")) {
		return "Local board · Jira not connected"
	}
	if py.Truthy(st.Get("auth_error")) {
		return `<span class="failc">Jira refused the token</span> · <a href="/admin/board">sync status</a>`
	}
	if py.Truthy(st.Get("last_error")) {
		return `<span class="warnc">Jira sync failing</span> · <a href="/admin/board">sync status</a>`
	}
	return "Jira " + E(py.Str(st.GetD("project", ""))) + " · synced " + E(Ago(st.Get("last_ok")))
}

type boardFilter struct{ env, assignee, label, source, show, tracker string }

func (f boardFilter) any() bool {
	return f.env != "" || f.assignee != "" || f.label != "" || f.source != "" || f.show != "" || In(f.tracker, gitlabTrackers...)
}

func boardFiltered(c *Ctx, qs py.QS) ([]Row, boardFilter) {
	env, who, label, src, show := qs.Get("env"), qs.Get("assignee"), qs.Get("label"), qs.Get("source"), qs.Get("show")
	tracker := qs.Get("tracker")
	where := []string{"gone=0", "(status!='done' OR updated_at>=?)"}
	args := []any{NowInt() - BoardDoneDays*86400}
	if In(tracker, gitlabTrackers...) && boardHasTracker() {
		where = append(where, "coalesce(tracker, 'jira')=?")
		args = append(args, tracker)
	} else if tracker != "all" {
		tracker = ""
	}
	if In(env, BoardEnvs...) {
		where = append(where, "env=?")
		args = append(args, env)
	} else if env == "none" {
		where = append(where, "(env IS NULL OR env='')")
	}
	if who == "nobody" {
		where = append(where, "(assignee IS NULL OR assignee='')")
	} else if who != "" {
		where = append(where, "assignee=?")
		args = append(args, who)
	}
	if label != "" && boardLabelRe.MatchString(label) {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)")
		args = append(args, label)
	}
	if boardIsSource(src) {
		where = append(where, "source_kind=?")
		args = append(args, src)
	} else if src == "auto" {
		where = append(where, "created_by='argus-auto'")
	} else if src == "other" {
		where = append(where, "source_kind IS NULL")
	}
	if show == "mine" {
		me := c.Sess.Username
		jn := py.OrS(JiraConn(me).Get("jira_name"), me)
		where = append(where, "(EXISTS (SELECT 1 FROM task_seen s WHERE s.task_id=tasks.id AND s.principal=?) "+
			"OR assignee IN (?, ?) OR requested_by=?)")
		args = append(args, me, me, jn, me)
	}
	rows := BoardBQ("SELECT * FROM tasks WHERE "+strings.Join(where, " AND ")+
		" ORDER BY CASE priority WHEN 'Highest' THEN 0 WHEN 'High' THEN 1 WHEN 'Medium' THEN 2 WHEN 'Low' THEN 3 "+
		"WHEN 'Lowest' THEN 4 ELSE 2 END, updated_at DESC LIMIT 600", args...)
	if show != "mine" {
		show = ""
	}
	return rows, boardFilter{env, who, label, src, show, tracker}
}

// BoardSourceTag is views_board.source_tag(t).
func BoardSourceTag(t Row) string {
	if !t.T("source_kind") {
		if t.T("jira_key") && !t.T("requested_by") {
			return `<span class="tag" title="created in Jira">Jira</span>`
		}
		return ""
	}
	kind, sid := t.Str("source_kind"), t.Str("source_id")
	name := kind
	switch kind {
	case "incident":
		name = "incident #" + sid
	case "rca":
		name = "RCA " + strings.ReplaceAll(sid, "-", " fix ")
	case "vm":
		name = "VM analysis"
	case "disk":
		name = "storage"
	case "backup":
		name = "backup"
	case "cicd":
		name = "CI/CD #" + sid
	}
	if t.T("source_url") && strings.HasPrefix(t.Str("source_url"), "/") {
		return `<a class="tag" href="` + E(t.Str("source_url")) + `">` + E(name) + `</a>`
	}
	return `<span class="tag">` + E(name) + `</span>`
}

func boardFixNote(t Row) string {
	fs := t.S("fix_state")
	if fs == "requested" || fs == "started" {
		return `<div class="kc-fix"><span class="badge warn">fix running</span></div>`
	}
	if strings.HasPrefix(fs, "done") {
		kind := "warn"
		if strings.HasSuffix(fs, "OK") {
			kind = "ok"
		}
		return `<div class="kc-fix"><span class="badge ` + kind + `">` + E(strings.ReplaceAll(fs, "done: ", "fixer ")) + `</span></div>`
	}
	if strings.HasPrefix(fs, "refused") {
		return `<div class="kc-fix"><span class="badge fail" title="` + E(fs) + `">fixer refused</span></div>`
	}
	return ""
}

// BoardCanChange: the board shows its change controls (Move, New task) only to an admin whose changes can reach Jira.
// Presentation only: the POST handlers keep their own checks unchanged.
func BoardCanChange(sess *Session) bool {
	return sess != nil && sess.Role == "admin" && BoardWriteBlock(sess) == ""
}

// BoardReadOnlyNote: one line, once per board, instead of a "Move" on every card that only says it cannot.
func BoardReadOnlyNote(sess *Session) string {
	if BoardCanChange(sess) {
		return ""
	}
	why := "Read-only for viewers: ask an admin to move a task or create one, or change it in Jira."
	if sess != nil && sess.Role == "admin" {
		why = "Read-only: " + BoardWriteBlock(sess)
	}
	return `<p class="section-note kc-readonly">` + E(why) + `</p>`
}

// BoardWriteBlock is views_board.write_block(sess): "" when this user's changes may go to Jira, else the reason.
func BoardWriteBlock(sess *Session) string {
	ok, why := JiraCanWrite(sess.Username)
	if ok {
		return ""
	}
	return why
}

func boardJSONList(v any) []string {
	s := py.OrS(v, "[]")
	x, err := py.ParseJSON([]byte(s))
	if err != nil {
		panic("bad JSON in board.db: " + err.Error())
	}
	var out []string
	for _, it := range py.AsList(x) {
		out = append(out, py.Str(it))
	}
	return out
}

// boardInternalLabel: a label Argus or its workflow sets (argus-inc-*, argus-mx-*, argus-fixer, ...), not one a
// person chose. They fold into their own "Argus labels" disclosure so they never crowd the human ones.
func boardInternalLabel(l string) bool { return strings.HasPrefix(l, "argus") }

// boardPeople is the Assignee choice: one entry per stored identity (the query value), named by its display name,
// most-assigned first. Two identities whose names differ only by case keep their id in brackets.
func boardPeople(rowsAll []Row) [][2]string {
	names, count := map[string]string{}, map[string]int{}
	var ids []string
	for _, r := range rowsAll {
		if !r.T("assignee") {
			continue
		}
		id := r.Str("assignee")
		if _, ok := names[id]; !ok {
			ids = append(ids, id)
			names[id] = ""
		}
		count[id]++
		if names[id] == "" {
			names[id] = py.OrS(r.Get("assignee_name"), "")
		}
	}
	fold := map[string]int{}
	for _, id := range ids {
		if names[id] == "" {
			names[id] = id
		}
		fold[strings.ToLower(names[id])]++
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if count[ids[i]] != count[ids[j]] {
			return count[ids[i]] > count[ids[j]]
		}
		a, b := strings.ToLower(names[ids[i]]), strings.ToLower(names[ids[j]])
		if a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	out := make([][2]string, 0, len(ids))
	for _, id := range ids {
		n := names[id]
		if fold[strings.ToLower(n)] > 1 && n != id {
			n += " (" + id + ")"
		}
		out = append(out, [2]string{id, n})
	}
	return out
}

// boardTopN: how many people or labels a filter row shows before "All …" folds the rest.
const boardTopN = 8

// boardAdvanced is how many of Assignee, Label and Source are set.
func (f boardFilter) advanced() int {
	n := 0
	for _, v := range []string{f.assignee, f.label, f.source} {
		if v != "" {
			n++
		}
	}
	return n
}

// filtered: a filter other than the tracker tab narrows the board.
func (f boardFilter) filtered() bool {
	return f.env != "" || f.show != "" || f.advanced() > 0
}

// boardVocab is what the board's filters offer: environments, people, labels (human, then Argus's own) and sources.
func boardVocab(rowsAll []Row, f boardFilter) (envs, people, human, internal, srcs [][2]string) {
	envs = [][2]string{{"", "all"}, {"develop", EnvLabel("develop")}, {"pre-release", EnvLabel("pre-release")}, {"release", EnvLabel("release")}, {"none", "not set"}}
	people = boardPeople(rowsAll)
	lcount := map[string]int{}
	for _, r := range rowsAll {
		for _, lb := range boardJSONList(r.Get("labels")) {
			if !strings.HasPrefix(lb, "argus-src-") && !strings.HasPrefix(lb, "env-") && lb != "argus" {
				lcount[lb]++
			}
		}
	}
	if f.label != "" && lcount[f.label] == 0 {
		lcount[f.label] = 0 // an active label is always a choice, even when no task carries it any more
	}
	for l := range lcount {
		if boardInternalLabel(l) {
			internal = append(internal, [2]string{l, l})
		} else {
			human = append(human, [2]string{l, l})
		}
	}
	for _, ls := range [][][2]string{human, internal} {
		sort.Slice(ls, func(i, j int) bool {
			if lcount[ls[i][0]] != lcount[ls[j][0]] {
				return lcount[ls[i][0]] > lcount[ls[j][0]]
			}
			return ls[i][0] < ls[j][0]
		})
	}
	srcs = [][2]string{}
	for _, k := range BoardSourceKeys {
		srcs = append(srcs, [2]string{k, BoardSourceNames[k]})
	}
	srcs = append(srcs, [2]string{"auto", "created by Argus"}, [2]string{"other", "other"})
	return
}

// boardChipHref is /board with the current query, name set to val ("" drops it) and the request banner dropped.
func boardChipHref(qs py.QS, name, val string) string {
	keep := qs.Pairs(name, "req")
	if val != "" {
		keep = append(keep, [2]string{name, val})
	}
	if len(keep) == 0 {
		return "/board"
	}
	return "/board?" + py.Urlencode(keep)
}

// kanbanDefault is the column a phone shows first: the first non-empty one in the order Review, In progress, To do,
// Done (by the column's name, so the Jira mirror's own columns follow it too), else the first non-empty, else the first.
func kanbanDefault(labels []string, counts []int) int {
	for _, want := range [][]string{{"review"}, {"progress"}, {"to do", "todo", "selected", "backlog", "open"}, {"done"}} {
		for i, l := range labels {
			ll := strings.ToLower(l)
			for _, w := range want {
				if counts[i] > 0 && strings.Contains(ll, w) {
					return i
				}
			}
		}
	}
	for i, n := range counts {
		if n > 0 {
			return i
		}
	}
	return 0
}

// boardResults is the line above the columns: how many open tasks match, out of how many when filtered, and where
// they are (Review, In progress, To do first: the phone's order).
func boardResults(by map[string][]Row, shown []string, nOpen int, filtered bool, f boardFilter) string {
	head := "<b>" + Itoa(nOpen) + " open task" + py.Plural(nOpen) + "</b>"
	if filtered {
		q := "SELECT count(*) AS n FROM tasks WHERE gone=0 AND status!='done'"
		var args []any
		if In(f.tracker, gitlabTrackers...) && boardHasTracker() {
			q += " AND coalesce(tracker, 'jira')=?"
			args = append(args, f.tracker)
		}
		total := 0
		if r := BoardBQ(q, args...); len(r) > 0 {
			total = int(r[0].I("n"))
		}
		head = "<b>" + Itoa(nOpen) + " of " + Itoa(total) + "</b> open task" + py.Plural(total) + " match"
	}
	parts := []string{head}
	for _, c := range [][2]string{{"review", "in review"}, {"progress", "in progress"}, {"fixer", "ready for fixer"}, {"todo", "to do"}} {
		if n := len(by[c[0]]); n > 0 && In(c[0], shown...) {
			parts = append(parts, Itoa(n)+" "+c[1])
		}
	}
	return `<p class="section-note kres">` + strings.Join(parts, ` · `) + `</p>`
}

// boardEmpty is the one empty state of a board view with no task at all (instead of five "Nothing here" columns):
// what is empty, why, and the one way to fill it.
func boardEmpty(f boardFilter, sess *Session) string {
	if f.filtered() {
		return `<div class="empty"><b>No task matches these filters.</b>Clear filters above shows every task.</div>`
	}
	what, from := "No open task.", "Tasks come from incidents, RCA fixes and analyses, or from Jira."
	if f.tracker == "gitlab" {
		what, from = "No GitLab issue on the board yet.", "Issues of the connected GitLab board appear after the next sync (every two minutes)."
		if GitLabConnected() && sess.Role == "admin" {
			from += ` Or choose New task → Create in GitLab issues.`
		}
	}
	return `<div class="empty"><b>` + what + `</b>` + from + `</div>`
}

// --- one task ------------------------------------------------------------------------------------------

func boardGetTask(qs py.QS) Row {
	key := qs.Get("key")
	if boardKeyFull.MatchString(key) {
		r := BoardBQ("SELECT * FROM tasks WHERE jira_key=?", key)
		if len(r) == 0 {
			return nil
		}
		return r[0]
	}
	tid := qs.Get("id")
	if !py.IsDigit(tid) || py.Len(tid) > 9 {
		return nil
	}
	n, _ := py.ParseInt(tid)
	r := BoardBQ("SELECT * FROM tasks WHERE id=?", n)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func boardScope() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "fix-scope.json")) }

func boardGuessHosts(host string, aliases []string) []string {
	h := strings.ToLower(host)
	var out []string
	for _, a := range aliases {
		_, rest, _ := strings.Cut(a, "-")
		mid := rest
		if i := strings.LastIndex(rest, "-fix"); i >= 0 {
			mid = rest[:i]
		}
		if h != "" && (h == mid || (py.Len(mid) >= 4 && (strings.Contains(h, mid) || strings.Contains(mid, h)))) {
			out = append(out, a)
		}
	}
	return out
}

func boardScopeAliases(env string) []string {
	var out []string
	for _, a := range py.AsList(py.AsMap(boardScope().Get("hosts")).Get(env)) {
		out = append(out, py.Str(a))
	}
	return out
}

func boardFixerBlock(t Row, sess *Session, ret string) string {
	env := t.S("env")
	if env == "release" {
		return `<h2>Fixer</h2><p class="section-note"><strong>Production:</strong> Argus never runs this task. ` +
			`A human makes the change, with the operator's approval for that one change.</p>`
	}
	if !In(env, BoardFixEnvs...) {
		return `<h2>Fixer</h2><p class="section-note">Set the environment to make this task runnable by the fixer: a label ` +
			`such as <code>env:stage</code>, <code>env-develop</code> or <code>env:pre-release</code> (GitLab or Jira).</p>`
	}
	fs := t.S("fix_state")
	state := ""
	if fs != "" {
		link := ""
		if t.T("fix_request") && boardIDRe.MatchString(t.Str("fix_request")) && sess.Role == "admin" {
			link = ` · <a href="/admin/fix/run?id=` + E(t.Str("fix_request")) + `">fix run</a>`
		}
		words := map[string]string{"requested": "sent to the fixer, waiting for the dispatcher", "started": "running",
			"lost": "no result after an hour"}[fs]
		if words == "" {
			words = strings.ReplaceAll(fs, "done: ", "finished: ")
		}
		state = `<p class="section-note">Fixer: ` + E(words) + link + `</p>`
	}
	if sess.Role != "admin" {
		return `<h2>Fixer</h2>` + state + `<p class="section-note">An admin can send this task to the fixer after reviewing its fix prompt.</p>`
	}
	if fs == "requested" || fs == "started" {
		return `<h2>Fixer</h2>` + state
	}
	if py.Strip(t.S("fix_prompt")) == "" {
		return `<h2>Fixer</h2>` + state + `<p class="section-note">No fix prompt yet: add ` + If(TaskTracker(t) == "gitlab",
			`a <code>### Fix prompt</code> section to the GitLab issue`, `an <code>h3. Fix prompt</code> block in Jira`) +
			`, or create the task from a finding.</p>`
	}
	if ro := TaskWriteBlock(sess, t); ro != "" {
		return `<h2>Fixer</h2>` + state + `<p class="section-note">` + E(ro) + `</p>`
	}
	if !FixerTriggerOn() { // ADR-0013: the fixer is reached through In Progress -> plan -> Spin up agent
		return `<h2>Fixer</h2>` + state + `<p class="section-note">Move the task to In Progress: Argus plans it, and an admin ` +
			`spins up the agent, which hands the approved WRITE steps on develop or pre-release to the fixer.</p>`
	}
	if ok, why := FixerAvailability(env); !ok {
		return `<h2>Fixer</h2>` + state + why
	}
	var aliases []string
	for _, a := range boardScopeAliases(env) {
		if boardAliasRe.MatchString(a) {
			aliases = append(aliases, a)
		}
	}
	chosen := map[string]bool{}
	for _, a := range boardGuessHosts(t.S("host"), aliases) {
		chosen[a] = true
	}
	var boxes strings.Builder
	for _, a := range aliases {
		boxes.WriteString(`<label class="hostpick"><input type="checkbox" name="host" value="` + E(a) + `"` +
			If(chosen[a], " checked", "") + `> <span class="mono">` + E(a) + `</span></label>`)
	}
	return `<h2>Send to the fixer</h2>` + state + `<p class="section-note">Moves the task to Ready for fixer and hands the fix ` +
		`prompt above to the fixer on <span class="tag">` + E(EnvLabel(env)) + `</span>, with you as the approver. It runs as ` +
		`<code>argus-fix</code>, one fix at a time; the result comes back here and in Jira, and the task moves to Review.</p>` +
		`<form method="post" action="/board/fixer" class="fixform"><input type="hidden" name="csrf" value="` + sess.CSRF + `">` +
		`<input type="hidden" name="task" value="` + I2S(t.I("id")) + `">` + boardReturnInput(ret) + `<div class="hostlist">` + boxes.String() + `</div>` +
		`<div class="savebar"><label class="check"><input type="checkbox" name="reviewed" value="1" required> ` +
		`I reviewed the fix prompt and the hosts</label><button>Ready for fixer</button></div></form>`
}

var boardEvKind = map[string]string{"created": "finding", "moved": "other", "comment": "other", "assigned": "owner", "jira": "rca",
	"fixer": "reopen", "conflict": "reopen", "error": "reopen"}

func boardStatusLabel(s string) (string, bool) { v, ok := BoardLabel[s]; return v, ok }

// --- create -----------------------------------------------------------------------------------------------

func boardCap(s string, n int) string {
	if py.Len(s) <= n {
		return s
	}
	return py.Head(s, n-1) + "…"
}

// boardRootCause is re.search(r"(?ms)^## Root cause\s*\n(.*?)(?=^## |\Z)", content).group(1), ok=false for no match.
func boardRootCause(content string) (string, bool) {
	const head = "## Root cause"
	for pos := 0; pos <= len(content); {
		i := strings.Index(content[pos:], head)
		if i < 0 {
			return "", false
		}
		i += pos
		pos = i + 1
		if i > 0 && content[i-1] != '\n' {
			continue
		}
		j := i + len(head)
		end := j
		for end < len(content) {
			r, w := utf8.DecodeRuneInString(content[end:])
			if !py.IsSpaceRune(r) {
				break
			}
			end += w
		}
		k := strings.LastIndexByte(content[j:end], '\n')
		if k < 0 {
			continue
		}
		start := j + k + 1
		for p := start; p <= len(content); p++ {
			if p == len(content) {
				return content[start:], true
			}
			if (p == 0 || content[p-1] == '\n') && strings.HasPrefix(content[p:], "## ") {
				return content[start:p], true
			}
		}
	}
	return "", false
}

func boardPrefill(kind, sid string) *py.OMap {
	switch {
	case kind == "backup":
		return BackupPrefill(sid)
	case kind == "incident" && py.IsDigit(sid):
		n, _ := py.ParseInt(sid)
		rr := Q("SELECT * FROM incidents WHERE id=?", n)
		if len(rr) == 0 {
			return nil
		}
		r := rr[0]
		ps := Q("SELECT p.title, p.summary, p.rca, p.fix_prompt, p.environment FROM incident_events e JOIN triage_problems p "+
			"ON p.run_id=e.run_id AND p.idx=e.idx WHERE e.incident_id=? AND e.kind='finding' ORDER BY e.at DESC LIMIT 1", r.Get("id"))
		var p Row
		if len(ps) > 0 {
			p = ps[0]
		}
		ev := "Incident #" + r.Str("id") + " " + r.Str("key") + "\nseverity " + py.OrS(r.Get("severity"), "?") + " · " + r.Str("status") +
			" · seen " + r.Str("occurrences") + "x · first " + When(r.Get("first_seen")) + " · last " + When(r.Get("last_seen")) + "\n"
		if p != nil {
			ev += "\nLatest finding: " + p.S("title") + "\n" + p.S("summary") + "\n\n" + boardCap(p.S("rca"), 12000)
		}
		host := ""
		if r.T("subject") && boardHostRe.MatchString(r.Str("subject")) {
			host = r.Str("subject")
		}
		pri := "Medium"
		if In(r.S("severity"), "critical", "page", "high") {
			pri = "High"
		}
		h, _ := FixHandoffIncident(r)
		fp := h.Prompt
		if fp == "" && p != nil {
			fp = p.S("fix_prompt")
		}
		return py.NewOMap("title", "Incident #"+r.Str("id")+": "+py.Str(py.Or(py.Or(r.Get("title"), r.Get("subject")), r.Get("key"))),
			"env", r.S("env"), "host", host, "priority", pri,
			"description", "Follow-up of incident #"+r.Str("id")+" ("+py.Str(py.Or(r.Get("title"), r.Get("key")))+").",
			"evidence", ev, "fix_prompt", fp,
			"url", "/incidents/view?id="+I2S(r.I("id")), "what", "incident #"+I2S(r.I("id")))
	case kind == "rca" && boardRcaSid.MatchString(sid):
		a, b, _ := strings.Cut(sid, "-")
		rid, _ := py.ParseInt(a)
		n, _ := py.ParseInt(b)
		rr := Q("SELECT * FROM rca_reports WHERE id=?", rid)
		if len(rr) == 0 {
			return nil
		}
		r := rr[0]
		env := ""
		if r.T("incident_id") {
			i := Q("SELECT env FROM incidents WHERE id=?", r.Get("incident_id"))
			if len(i) > 0 {
				env = i[0].S("env")
			}
		}
		h := FixHandoffRca(r, env)
		var opt *py.OMap
		for _, o := range h.Options {
			if v, ok := o.Get("n").(int64); ok && v == n {
				opt = o
				break
			}
		}
		var prop *Proposal
		if opt != nil {
			title := py.Str(opt.Get("title"))
			prop = &Proposal{N: n, Title: py.Head(title, 150), Text: "Chosen option " + I2S(n) + ": " + title + "\n" +
				py.OrS(opt.Get("what"), "") + "\n\n" + h.Prompt}
		} else {
			for _, x := range BoardProposals(r.S("content")) {
				if x.N == n {
					x := x
					prop = &x
					break
				}
			}
		}
		if prop == nil {
			return nil
		}
		if env == "" && strings.Contains(prop.Text, "PRODUCTION") {
			env = "release"
		}
		root, _ := boardRootCause(r.S("content"))
		fp := prop.Text
		if opt == nil {
			fp = "From the RCA \"" + r.Str("title") + "\" (Argus RCA #" + I2S(rid) + "), fix proposal " + I2S(n) + ":\n\n" + prop.Text
		}
		return py.NewOMap("title", "RCA fix "+I2S(n)+": "+prop.Title, "env", env, "host", "", "priority", "Medium",
			"description", "Fix proposal "+I2S(n)+" of the RCA \""+r.Str("title")+"\".",
			"evidence", "RCA #"+I2S(rid)+": "+r.Str("title")+"\n\nRoot cause:\n"+boardCap(py.Strip(root), 8000),
			"fix_prompt", fp, "url", "/rca/view?id="+I2S(rid), "what", "RCA #"+I2S(rid)+", fix "+I2S(n))
	case (kind == "vm" || kind == "disk") && py.IsDigit(sid):
		table := "disk_reports"
		if kind == "vm" {
			table = "vm_reports"
		}
		n, _ := py.ParseInt(sid)
		rr := Q("SELECT r.*, p.title ptitle, p.summary, p.rca, p.fix_prompt, p.environment FROM "+table+" r LEFT JOIN "+
			"triage_problems p ON p.run_id=r.run_id AND p.idx=0 WHERE r.id=?", n)
		if len(rr) == 0 || !rr[0].T("run_id") {
			return nil
		}
		r := rr[0]
		env := py.OrS(py.Or(r.Get("environment"), r.Get("env")), "")
		var host, what, url string
		if kind == "vm" {
			host, what = r.S("name"), "VM analysis of "+r.Str("name")
			url = "/inventory/view?id=" + I2S(r.I("vm_id"))
		} else {
			host, what = r.S("host"), "storage analysis of "+r.Str("host")+" "+r.Str("mount")
			url = "/incidents/problem?run=" + I2S(r.I("run_id")) + "&idx=0"
		}
		hh := host
		if hh == "" {
			hh = "-"
		}
		hostV := ""
		if boardHostRe.MatchString(hh) {
			hostV = host
		}
		fp := FHParse(r.S("rca"), env, r.S("fix_prompt")).Prompt
		if fp == "" {
			fp = r.S("fix_prompt")
		}
		return py.NewOMap("title", py.Head(host+": "+py.OrS(r.Get("ptitle"), what), BoardMaxTitle), "env", env,
			"host", hostV, "priority", "Medium",
			"description", "From the "+what+" of "+When(r.Get("created"))+".",
			"evidence", r.S("summary")+"\n\n"+boardCap(r.S("rca"), 16000),
			"fix_prompt", fp, "url", url, "what", what)
	case kind == "cicd" && boardCicdSid.MatchString(sid):
		return CicdPrefill(sid)
	}
	return nil
}

// --- posts ----------------------------------------------------------------------------------------------

func boardTid(form py.QS) Row {
	v := form.Get("task")
	if !py.IsDigit(v) || py.Len(v) > 9 {
		return nil
	}
	n, _ := py.ParseInt(v)
	r := BoardBQ("SELECT * FROM tasks WHERE id=? AND gone=0", n)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func boardPostCreate(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := func(k string) string { return strings.ReplaceAll(form.Get(k), "\r\n", "\n") }
	kind, sid := get("src"), get("id")
	var qs py.QS
	if kind != "" {
		qs = py.NewQS("src", kind, "id", sid)
	}
	if r := BoardReturn(get("return")); r != "" {
		qs.Add("return", r)
	}
	values := py.NewOMap()
	for _, k := range []string{"title", "env", "host", "priority", "description", "evidence", "fix_prompt", "labels", "tracker",
		"gl_project"} {
		values.Set(k, get(k))
	}
	vs := func(k string) string { return values.S(k) }
	again := func(msg string) (int, string) { return 400, msg }
	title := strings.Join(py.Split(vs("title")), " ")
	if title == "" || py.Len(title) > BoardMaxTitle {
		return again("The title must be 1 to " + Itoa(BoardMaxTitle) + " characters.")
	}
	if vs("env") != "" && !In(vs("env"), BoardEnvs...) {
		return again("Unknown environment.")
	}
	host := py.Strip(vs("host"))
	if host != "" && !boardHostRe.MatchString(host) {
		return again("The host must be a host name or address.")
	}
	if !In(vs("priority"), BoardPriorities...) {
		return again("Unknown priority.")
	}
	labels := py.Split(vs("labels"))
	okL := len(labels) <= 10
	for _, x := range labels {
		if !boardLabelRe.MatchString(x) {
			okL = false
		}
	}
	if !okL {
		return again("Labels: up to 10 words of letters, digits, dot, underscore and dash.")
	}
	if py.Len(vs("description")) > BoardMaxText || py.Len(vs("evidence")) > BoardMaxText {
		return again("Description and evidence are limited to " + Itoa(BoardMaxText/1024) + " KB each.")
	}
	if len(vs("fix_prompt")) > BoardMaxPrompt {
		return again("The fix prompt is limited to " + Itoa(BoardMaxPrompt/1024) + " KB.")
	}
	lab := make([]any, len(labels))
	for i, x := range labels {
		lab[i] = x
	}
	tracker := vs("tracker")
	if tracker == "" {
		tracker = "jira"
	}
	if !In(tracker, gitlabTrackers...) {
		return again("Choose Jira or GitLab issues.")
	}
	p := py.NewOMap("kind", "create", "title", title, "env", vs("env"), "host", host, "priority", vs("priority"), "labels", lab,
		"description", vs("description"), "evidence", vs("evidence"), "fix_prompt", vs("fix_prompt"))
	if tracker == "gitlab" {
		p.Set("tracker", "gitlab") // Jira requests stay exactly as before
		glp, ok := gitlabCheckProject(vs("gl_project"))
		if !ok {
			return again("Choose a GitLab project of the connected board.")
		}
		if glp != "" {
			p.Set("gl_project", glp)
		}
	}
	if kind != "" {
		var pf *py.OMap
		if boardIsSource(kind) {
			pf = boardPrefill(kind, sid)
		}
		if pf == nil {
			return 404, "No such source"
		}
		if !pf.Has("url") {
			panic("prefill without url")
		}
		p.Set("source_kind", kind).Set("source_id", sid).Set("source_url", pf.Get("url"))
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "", "/board?req="+rid)
}

// BoardPost is views_board.post(path, form, sess): (303, location) or (code, html).
func BoardPost(c *Ctx, path string, form py.QS, sess *Session) (int, string) {
	get := form.Get
	switch path {
	case "/account/matrix":
		return MatrixPostAccount(c, form, sess)
	case "/board/agent":
		return AgentPost(c, form, sess)
	case "/board/agent/spinup":
		return AgentPostSpinUp(c, form, sess)
	case "/board/agent/chat":
		return AgentPostChat(c, form, sess)
	case "/board/task/secret":
		return AgentPostSecret(c, form, sess)
	case "/admin/agent/fixer-move":
		return AgentPostFixerMove(c, form, sess)
	case "/board/mxpreview":
		return MatrixPostPreview(c, form, sess)
	case "/admin/agent":
		return AgentPostSettings(c, form, sess)
	case "/board/jmove":
		return JBoardPostMove(c, form, sess)
	case "/board/refresh":
		return JBoardPostRefresh(c, form, sess)
	case "/account/jira":
		return JiraPostAccount(c, form, sess)
	case "/admin/jira-hosts":
		return JiraPostHosts(c, form, sess)
	case "/admin/jira-shared":
		return JiraPostShared(c, form, sess)
	}
	if In(path, "/board/create", "/board/move", "/board/comment", "/board/assign", "/board/fixer") {
		var wb string
		switch {
		case path == "/board/create" && get("tracker") == "gitlab":
			wb = GitLabWriteBlock()
		case path == "/board/create":
			wb = BoardWriteBlock(sess)
		default:
			if t := boardTid(form); t != nil {
				wb = TaskWriteBlock(sess, t)
			}
		}
		if wb != "" {
			return 403, "Not sent: " + wb
		}
	}
	if path == "/board/create" {
		return boardPostCreate(c, form, sess)
	}
	write := func(p *py.OMap, loc func(string) string) (int, string) {
		rid, err := BoardWriteRequest(p, sess)
		if err != nil {
			return boardCouldNotSave(c, err)
		}
		return 303, loc(rid)
	}
	if path == "/admin/board" {
		if sess.Role != "admin" {
			return 403, "Admins only"
		}
		act := get("action")
		var p *py.OMap
		switch act {
		case "sync", "full":
			p = py.NewOMap("kind", "sync", "full", act == "full")
		case "settings":
			p = py.NewOMap("kind", "settings", "auto_incidents", get("auto_incidents") == "1")
		default:
			return 400, "Unknown action"
		}
		return write(p, func(rid string) string { return boardRedirect(form, rid, "", "/admin/board?req="+rid) })
	}
	t := boardTid(form)
	if t == nil {
		return 404, "No such task"
	}
	back := "/board/task?id=" + I2S(t.I("id"))
	switch path {
	case "/board/move":
		to := get("to")
		if !In(to, BoardColumns...) || to == "fixer" {
			return 400, "Pick a column"
		}
		return write(py.NewOMap("kind", "move", "task", t.I("id"), "to", to), func(rid string) string {
			if get("back") == "board" {
				return boardRedirect(form, rid, "", "/board?req="+rid)
			}
			return boardRedirect(form, rid, "", back+"&req="+rid)
		})
	case "/board/comment":
		body := py.Strip(strings.ReplaceAll(get("body"), "\r\n", "\n"))
		if body == "" || py.Len(body) > BoardMaxComment {
			return 400, "A comment is 1 to " + Itoa(BoardMaxComment) + " characters"
		}
		return write(py.NewOMap("kind", "comment", "task", t.I("id"), "body", body), func(rid string) string {
			return boardRedirect(form, rid, "comments", back+"&req="+rid+"#comments")
		})
	case "/board/assign":
		return write(py.NewOMap("kind", "assign", "task", t.I("id")), func(rid string) string { return boardRedirect(form, rid, "", back+"&req="+rid) })
	case "/board/fixer":
		if sess.Role != "admin" {
			return 403, "Only admins can send a task to the fixer"
		}
		if !FixerTriggerOn() {
			return 409, "The fixer column is retired"
		}
		if t.Get("env") == "release" {
			return 403, "Production task"
		}
		if !In(t.S("env"), BoardFixEnvs...) {
			return 400, "Only develop and pre-release tasks can go to the fixer"
		}
		if get("reviewed") != "1" {
			return 400, "Tick \"I reviewed the fix prompt\" first"
		}
		if py.Strip(t.S("fix_prompt")) == "" {
			return 400, "The task has no fix prompt"
		}
		if ok, _ := FixerAvailability(t.Str("env")); !ok {
			return 409, "The fixer is not available for " + t.Str("env")
		}
		allowed := map[string]bool{}
		for _, a := range boardScopeAliases(t.Str("env")) {
			allowed[a] = true
		}
		hosts := form.List("host")
		ok := len(hosts) > 0
		for _, h := range hosts {
			if !allowed[h] || !boardAliasRe.MatchString(h) {
				ok = false
			}
		}
		if !ok {
			return 400, "Pick at least one fixer host of this environment"
		}
		seen := map[string]bool{}
		var uniq []any
		for _, h := range hosts {
			if !seen[h] {
				seen[h] = true
				uniq = append(uniq, h)
			}
		}
		return write(py.NewOMap("kind", "move", "task", t.I("id"), "to", "fixer", "hosts", uniq, "reviewed", true),
			func(rid string) string { return boardRedirect(form, rid, "", back+"&req="+rid) })
	}
	return 404, "Not found"
}

// --- admin: sync status -----------------------------------------------------------------------------------

// BoardPageAdminRejected is Settings → Jira sync again after a refused save: the entries kept, the field marked.
func BoardRefused(c *Ctx, rej *Rejected) string {
	c.Rej = rej
	return rej.Msg
}

// boardAdminState is the sync's state as one banner.
func boardAdminState(st *py.OMap) string {
	switch {
	case !py.Truthy(st):
		return Banner("The board sync has not published its state yet (argus-board-sync.timer on "+ARGUS_HOSTNAME+").", "warn")
	case py.Truthy(st.Get("auth_error")):
		return Banner(py.Str(st.Get("auth_error"))+". Create a new token in Jira and paste it under Replace the token below.", "fail")
	case py.Truthy(st.Get("configured")) && py.Truthy(st.Get("last_error")):
		return Banner("Last sync failed "+Ago(st.Get("last_error_at"))+": "+py.Str(st.Get("last_error")), "fail")
	case py.Truthy(st.Get("configured")):
		return Banner("Connected to Jira as "+py.OrS(st.Get("bot"), "?")+"; last good sync "+Ago(st.Get("last_ok"))+".", "ok")
	case py.Truthy(st.Get("users_connected")):
		return Banner("No shared connection; "+py.Str(st.Get("users_connected"))+" personal connection(s) feed the board.", "ok")
	default:
		return Banner("Jira is not connected yet: the board works locally. Paste a Jira URL and token below.", "warn")
	}
}

// boardAdminKV is the Connection rail of Settings → Jira sync.
func boardAdminKV(st *py.OMap) [][2]string {
	board := "-"
	if strings.HasPrefix(py.Str(st.GetD("jira_url", "")), "https://") && py.IsDigit(py.Str(st.GetD("board_id", ""))) {
		bid := I2S(py.I(st.Get("board_id")))
		board = `<a href="` + E(py.Str(st.Get("jira_url"))) + `/secure/RapidBoard.jspa?rapidView=` + bid + `" ` +
			`rel="noopener noreferrer">board ` + bid + `</a>`
	}
	whenOr := func(k string) string {
		if py.Truthy(st.Get(k)) {
			return When(st.Get(k))
		}
		return "-"
	}
	intOf := func(k string) string { return I2S(py.I(py.Or(st.Get(k), int64(0)))) }
	var appr []string
	for _, a := range py.AsList(py.Or(st.Get("approvers"), []any{})) {
		appr = append(appr, py.Str(a))
	}
	ap := strings.Join(appr, ", ")
	if ap == "" {
		ap = "none (moves in Jira never start the fixer)"
	}
	mode := "local only"
	if py.Truthy(st.Get("configured")) {
		mode = "Jira (source of truth)"
	}
	kv := [][2]string{{"Mode", mode},
		{"Jira", `<span class="mono small">` + E(py.OrS(st.Get("jira_url"), "-")) + `</span>`},
		{"Project", `<span class="mono">` + E(py.OrS(st.Get("project"), "-")) + `</span>`}, {"Board", board},
		{"Bot account", E(py.OrS(st.Get("bot"), "-"))},
		{"Last run", When(st.Get("last_run")) + ` <span class="dim">(` + Ago(st.Get("last_run")) + `)</span>`},
		{"Last good sync", whenOr("last_ok")}, {"Last pull", whenOr("last_pull")}, {"Last full reconcile", whenOr("last_full")},
		{"Jira calls, last run", intOf("calls")}, {"Queued for Jira", intOf("queued")},
		{"Failed / dropped", intOf("failed") + " / " + intOf("dropped")},
		{"Fixer approvers in Jira", E(ap)}}
	return kv
}

// boardNewHref is the New task link; it carries the current board view so Create lands back on it.
func boardNewHref(path string) string {
	if r := BoardReturn(path); r != "" && r != "/board" {
		return "/board/new?" + py.Urlencode([][2]string{{"return", r}})
	}
	return "/board/new"
}
