package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// --- push ------------------------------------------------------------------------------------------------------------

func (r *run) setLabel(tid int64, label string, add bool) {
	t := r.one(`SELECT labels FROM tasks WHERE id=?`, tid)
	var labels []string
	if t != nil {
		for _, x := range jsonList(orJSON(t.s("labels"))) {
			if x != label {
				labels = append(labels, x)
			}
		}
	}
	if add {
		labels = append(labels, label)
	}
	r.must(`UPDATE tasks SET labels=? WHERE id=?`, string(pyDumps(listAny(labels))), tid)
}

func orJSON(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func (r *run) fixerLabel() string { return r.o.Label + "-fixer" }

// errStop ends the push: the bot cannot be used any more this run.
var errStop = errors.New("stop")

func (r *run) push() error {
	blocked := map[int64]bool{}
	for _, op := range r.rows(`SELECT * FROM outbox WHERE state='queued' AND next_at<=? ORDER BY id`, r.now()) {
		tid := op.i("task_id")
		t := r.one(`SELECT * FROM tasks WHERE id=?`, tid)
		if t == nil {
			r.must(`UPDATE outbox SET state='dropped', last_error='task gone' WHERE id=?`, op.i("id"))
			continue
		}
		if blocked[tid] || t.s("tracker") == "gitlab" {
			continue
		}
		var p map[string]any
		d := json.NewDecoder(strings.NewReader(orObj(op.s("payload"))))
		d.UseNumber()
		d.Decode(&p)
		if p == nil {
			p = map[string]any{}
		}
		kind := op.s("kind")
		if kind != "create" && t.s("jira_key") == "" {
			blocked[tid] = true
			continue
		}
		jira, principal, base := r.writer()
		if jira == nil {
			if p["optional"] == true {
				r.must(`UPDATE outbox SET state='dropped', last_error=?, done_at=? WHERE id=?`, "shown in "+r.o.Name+" only: "+principal, r.now(), op.i("id"))
				r.event(tid, "jira", r.o.Label, "Not written to Jira ("+kind+"): "+principal)
			} else {
				r.must(`UPDATE outbox SET last_error=? WHERE id=?`, "waiting: "+principal, op.i("id"))
				blocked[tid] = true
			}
			r.commit()
			continue
		}
		err := r.pushOne(jira, principal, base, op, t, p)
		if err == errDone {
			continue
		}
		if err == nil {
			r.must(`UPDATE outbox SET state='done', done_at=?, last_error=NULL WHERE id=?`, r.now(), op.i("id"))
			r.commit()
			continue
		}
		je, _ := asJira(err)
		if isStop(err) {
			r.must(`UPDATE outbox SET last_error=? WHERE id=?`, principal+": "+head(err.Error(), 250), op.i("id"))
			r.commit()
			r.botDown = err.Error()
			return err
		}
		attempts := op.i("attempts") + 1
		transient := je != nil && (je.Status == 0 || je.Status >= 500)
		if transient && attempts < 10 {
			r.must(`UPDATE outbox SET attempts=?, next_at=?, last_error=? WHERE id=?`, attempts, r.now()+min(int64(60)<<attempts, 3600), head(err.Error(), 300), op.i("id"))
		} else {
			r.must(`UPDATE outbox SET state='failed', attempts=?, last_error=?, done_at=? WHERE id=?`, attempts, head(err.Error(), 300), r.now(), op.i("id"))
			r.event(tid, "error", r.o.Label, "Jira "+kind+" failed: "+head(err.Error(), 200))
			r.stats["failed"]++
		}
		blocked[tid] = true
		r.commit()
	}
	return nil
}

func orObj(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// errDone: the op was handled (dropped as a conflict) and committed.
var errDone = errors.New("done")

func (r *run) writer() (*Jira, string, string) {
	if r.conf && !r.botWrites() {
		return nil, "the shared Jira connection is read-only", ""
	}
	if r.conf {
		if r.botDown != "" {
			return nil, "the bot token cannot be used this run (" + head(r.botDown, 120) + ")", ""
		}
		return r.jira, "bot", strings.TrimRight(r.cfg["JIRA_URL"], "/")
	}
	return nil, "no Jira connection", ""
}

func (r *run) pushOne(jira *Jira, principal, base string, op, t row, p map[string]any) error {
	ctx, tid, key := r.ctx, t.i("id"), t.s("jira_key")
	switch op.s("kind") {
	case "create":
		if key != "" {
			return nil
		}
		project := orStr(p["project"])
		if project == "" {
			project = r.cfg["JIRA_PROJECT"]
		}
		if project == "" {
			return jerr(409, "", "%s has no project for new tasks (Account > Jira)", principal)
		}
		itype := orStr(p["issuetype"])
		if itype == "" {
			itype = r.cfg["JIRA_ISSUE_TYPE"]
		}
		if itype == "" {
			itype = "Task"
		}
		f := object{{"project", object{{"key", project}}}, {"issuetype", object{{"name", itype}}},
			{"summary", head(r.scan(t.s("title"), fmt.Sprintf("task:%d:summary", tid)), 255)},
			{"description", r.scan(r.compose(t), fmt.Sprintf("task:%d:description", tid))},
			{"labels", listAny(jsonList(orJSON(t.s("labels"))))}}
		if t.s("priority") != "" {
			f = append(f, kv{"priority", object{{"name", t.s("priority")}}})
		}
		if orStr(p["assignee"]) != "" {
			f = append(f, kv{"assignee", object{{"name", p["assignee"]}}})
		}
		if c := r.cfg["JIRA_COMPONENT"]; c != "" && project == r.cfg["JIRA_PROJECT"] {
			f = append(f, kv{"components", []any{object{{"name", c}}}})
		}
		res, err := jira.Create(ctx, f)
		if je, ok := asJira(err); ok && je.Status == 400 {
			low := strings.ToLower(err.Error())
			switch {
			case strings.Contains(low, "priority") && f.get("priority") != nil:
				f = without(f, "priority")
				res, err = jira.Create(ctx, f)
			case strings.Contains(low, "component") && r.cfg["JIRA_COMPONENT"] == "":
				return jerr(400, "", "%s Choose \"Component for new issues\" in Admin > Jira sync.", err.Error())
			}
		}
		if err != nil {
			return err
		}
		k := orStr(res["key"])
		if !keyRe.MatchString(k) && !(jira.DryRun && strings.HasPrefix(k, "DRYRUN-")) {
			return jerr(500, "", "Jira returned no issue key")
		}
		r.must(`UPDATE tasks SET jira_key=?, jira_url=?, synced_at=? WHERE id=?`, k, base, r.now(), tid)
		r.must(`INSERT OR IGNORE INTO task_seen VALUES (?,?,?)`, tid, principal, r.now())
		r.event(tid, "jira", principal, "Created in Jira as "+k)
		r.stats["pushed"]++
	case "transition":
		to := orStr(p["to"])
		cur, err := jira.Issue(ctx, key, []string{"status", "updated", "labels"}, "")
		if err != nil {
			return err
		}
		f := asMap(cur["fields"])
		st := f["status"]
		if st == nil {
			st = map[string]any{}
		}
		jstatus := asMap(st)["name"]
		var labels []string
		if ls, ok := f["labels"].([]any); ok {
			for _, x := range ls {
				labels = append(labels, pyStr(x))
			}
		}
		jupd := jiraTS(f["updated"])
		at, _ := asInt(p["at"])
		if orStr(p["from_jira"]) != "" && !sameStr(jstatus, p["from_jira"]) && jupd != 0 && jupd > at && r.mp.column(st) != to {
			r.must(`UPDATE outbox SET state='dropped', last_error=?, done_at=? WHERE id=?`, "conflict: Jira moved it to "+pyStr(jstatus)+" meanwhile", r.now(), op.i("id"))
			col := r.mp.column(st)
			r.must(`UPDATE tasks SET status=?, jira_status=?, updated_at=? WHERE id=?`, col, jstatus, r.now(), tid)
			lbl := columnLabel[to]
			if lbl == "" {
				lbl = to
			}
			r.event(tid, "conflict", r.o.Label, "Move to "+lbl+" dropped: Jira changed this task meanwhile ("+pyStr(jstatus)+"); Jira wins")
			r.stats["conflicts"]++
			r.commit()
			return errDone
		}
		if to != "fixer" && contains(labels, r.fixerLabel()) {
			if err := jira.AddLabel(ctx, key, r.fixerLabel(), false); err != nil {
				return err
			}
			r.setLabel(tid, r.fixerLabel(), false)
		}
		if r.mp.column(st) != to {
			ts, err := jira.Transitions(ctx, key)
			if err != nil {
				return err
			}
			if id := r.mp.pickTransition(ts, to); id != nil {
				if err := jira.Transition(ctx, key, pyStr(id)); err != nil {
					return err
				}
				after, err := jira.Issue(ctx, key, []string{"status"}, "")
				if err != nil {
					return err
				}
				if n := asMap(asMap(after["fields"])["status"])["name"]; orStr(n) != "" {
					jstatus = n
				}
			} else {
				lbl := columnLabel[to]
				if lbl == "" {
					lbl = to
				}
				return jerr(409, "", "no workflow transition from %s to a %s status (add one, or name it in JIRA_TRANSITIONS)", pyStr(jstatus), lbl)
			}
		}
		r.must(`UPDATE tasks SET jira_status=?, synced_at=? WHERE id=?`, jstatus, r.now(), tid)
		r.stats["pushed"]++
	case "label":
		lbl, add := orStr(p["remove"]), false
		if lbl == "" {
			lbl, add = pyStr(p["add"]), true
		}
		if err := jira.AddLabel(ctx, key, lbl, add); err != nil {
			return err
		}
		r.setLabel(tid, lbl, add)
		r.stats["pushed"]++
	case "comment":
		res, err := jira.Comment(ctx, key, head(r.scan(str(p["body"]), fmt.Sprintf("task:%d:comment", tid)), maxComment+200))
		if err != nil {
			return err
		}
		if l, ok := asInt(p["local"]); ok && l != 0 {
			r.must(`UPDATE comments SET jira_id=?, pending=0 WHERE id=?`, orNil(orStr(res["id"])), l)
		}
		r.stats["pushed"]++
	case "assign":
		if err := jira.Assign(ctx, key, r.mp.jiraUser(orStr(p["user"]))); err != nil {
			return err
		}
		r.stats["pushed"]++
	}
	return nil
}

func sameStr(a, b any) bool {
	return a == nil && b == nil || (a != nil && b != nil && pyStr(a) == pyStr(b))
}

func without(o object, k string) object {
	var out object
	for _, x := range o {
		if x.k != k {
			out = append(out, x)
		}
	}
	return out
}

// --- pull ------------------------------------------------------------------------------------------------------------

func (r *run) labelsSource(labels []string) (any, any) {
	pfx := r.o.Label + "-src-"
	for _, lb := range labels {
		if !strings.HasPrefix(lb, pfx) {
			continue
		}
		rest := lb[len(pfx):]
		for _, k := range []string{"incident", "rca", "vm", "disk"} {
			if strings.HasPrefix(rest, k+"-") && srcIDRe.MatchString(rest[len(k)+1:]) {
				return k, rest[len(k)+1:]
			}
		}
	}
	return nil, nil
}

func strOrNil(v any) any {
	if v == nil {
		return nil
	}
	return pyStr(v)
}

func (r *run) upsert(base string, issue map[string]any) string {
	key := orStr(issue["key"])
	if !keyRe.MatchString(key) {
		return ""
	}
	f := asMap(issue["fields"])
	var labels []string
	if ls, ok := f["labels"].([]any); ok {
		for _, x := range ls {
			if s, ok := x.(string); ok {
				labels = append(labels, s)
			}
		}
	}
	if len(labels) > 30 {
		labels = labels[:30]
	}
	st := f["status"]
	if orStr(st) == "" {
		st = map[string]any{}
	}
	col := r.mp.column(st)
	jstatus := strOrNil(asMap(st)["name"])
	asg := asMap(f["assignee"])
	pr := strOrNil(asMap(f["priority"])["name"])
	ps := r.parse(orStr(f["description"]))
	env := ps.env
	if env == "" {
		env = envFromLabels(labels)
	}
	jupd := jiraTS(f["updated"])
	if jupd == 0 {
		jupd = r.now()
	}
	t := r.one(`SELECT * FROM tasks WHERE jira_key=?`, key)
	title := head(orStr(f["summary"]), maxTitle)
	if title == "" {
		title = head(key, maxTitle)
	}
	var tid int64
	if t == nil {
		kind, sid := r.labelsSource(labels)
		if kind != nil && r.one(`SELECT 1 FROM tasks WHERE source_kind=? AND source_id=?`, kind, sid) != nil {
			kind, sid = nil, nil
		}
		ts := jiraTS(f["created"])
		if ts == 0 {
			ts = r.now()
		}
		by := "human"
		if contains(labels, r.o.Label+"-auto") {
			by = "auto"
		} else if contains(labels, r.o.Label+"-proposed") {
			by = "proposal"
		}
		res := r.must(`INSERT INTO tasks (jira_key, jira_url, title, description, evidence, fix_prompt, status, jira_status, assignee, `+
			`assignee_name, priority, labels, env, host, source_kind, source_id, created_by, created_at, updated_at, jira_updated, `+
			`synced_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			key, base, title, ps.description, ptr(ps.evidence), ptr(ps.fixPrompt), col, jstatus, strOrNil(asg["name"]),
			strOrNil(asg["displayName"]), pr, string(pyDumps(listAny(labels))), orNil(env), orNil(ps.host), kind, sid, by,
			ts, r.now(), jupd, r.now())
		tid, _ = res.LastInsertId()
		r.event(tid, "jira", "jira", "Seen in Jira ("+pyStr(jstatus)+")")
		r.stats["new"]++
	} else {
		tid = t.i("id")
		pending := r.one(`SELECT 1 FROM outbox WHERE task_id=? AND kind IN ('transition','label') AND state='queued' AND created_at>=?`, tid, jupd) != nil
		if pending {
			col = t.s("status")
		}
		jurl := t.s("jira_url")
		if jurl == "" {
			jurl = base
		}
		sets := object{{"title", title}, {"description", ps.description}, {"status", col}, {"jira_status", jstatus},
			{"assignee", strOrNil(asg["name"])}, {"assignee_name", strOrNil(asg["displayName"])}, {"priority", pr},
			{"labels", string(pyDumps(listAny(labels)))}, {"jira_updated", jupd}, {"synced_at", r.now()}, {"gone", int64(0)},
			{"jira_url", jurl}}
		if ps.evidence != nil {
			sets = append(sets, kv{"evidence", *ps.evidence})
		}
		if ps.fixPrompt != nil {
			sets = append(sets, kv{"fix_prompt", *ps.fixPrompt})
		}
		if env != "" {
			sets = append(sets, kv{"env", env})
		}
		if ps.host != "" {
			sets = append(sets, kv{"host", ps.host})
		}
		var changed []string
		for _, k := range []string{"title", "status", "assignee", "priority", "fix_prompt"} {
			for _, s := range sets {
				if s.k == k && !sameStr(s.v, t[k]) {
					changed = append(changed, k)
				}
			}
		}
		if len(changed) > 0 {
			sets = append(sets, kv{"updated_at", r.now()})
			r.stats["updated"]++
		}
		var cols []string
		var args []any
		for _, s := range sets {
			cols = append(cols, s.k+"=?")
			args = append(args, s.v)
		}
		r.must("UPDATE tasks SET "+strings.Join(cols, ", ")+" WHERE id=?", append(args, tid)...)
		if contains(changed, "status") && !pending {
			from := columnLabel[t.s("status")]
			if from == "" {
				from = t.s("status")
			}
			r.event(tid, "jira", "jira", "Jira: "+from+" → "+columnLabel[col]+" ("+pyStr(jstatus)+")")
		}
		if contains(changed, "assignee") {
			who := orStr(asg["displayName"])
			if who == "" {
				who = orStr(asg["name"])
			}
			if who == "" {
				who = "nobody"
			}
			r.event(tid, "jira", "jira", "Jira: assigned to "+who)
		}
		if contains(changed, "fix_prompt") {
			r.event(tid, "jira", "jira", "Jira: fix prompt edited")
		}
	}
	r.must(`INSERT OR REPLACE INTO task_seen VALUES (?,?,?)`, tid, "bot", r.now())
	cs, _ := asMap(f["comment"])["comments"].([]any)
	if len(cs) > 50 {
		cs = cs[len(cs)-50:]
	}
	for _, c := range cs {
		cm := asMap(c)
		cid := orStr(cm["id"])
		if cid == "" || r.one(`SELECT 1 FROM comments WHERE jira_id=?`, cid) != nil {
			continue
		}
		a := asMap(cm["author"])
		who := orStr(a["displayName"])
		if who == "" {
			who = orStr(a["name"])
		}
		if who == "" {
			who = "?"
		}
		at := jiraTS(cm["created"])
		if at == 0 {
			at = r.now()
		}
		r.must(`INSERT INTO comments (task_id, jira_id, author, body, created_at) VALUES (?,?,?,?,?)`, tid, cid, who, head(orStr(cm["body"]), 20000), at)
	}
	return key
}

func ptr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// pull reads the bot's view. It returns the keys seen and whether the view was complete.
func (r *run) pull(base, scope string, full bool) (map[string]bool, bool, error) {
	extra := ""
	if r.cfg["JIRA_JQL"] != "" {
		extra = " AND (" + r.cfg["JIRA_JQL"] + ")"
	}
	last := r.stateInt("last_pull")
	var jql string
	var limit int
	if full {
		jql, limit = scope+extra+" ORDER BY updated DESC", 5000
	} else {
		mins := int64(5)
		if last != 0 {
			mins = max(5, (r.now()-last)/60+2)
		}
		jql, limit = scope+extra+" AND updated >= -"+strconv.FormatInt(mins, 10)+"m ORDER BY updated ASC", 1000
	}
	started, seen, start := r.now(), map[string]bool{}, 0
	for start < limit {
		page, err := r.jira.Search(r.ctx, jql, fields, start, 50)
		if err != nil {
			return seen, false, err
		}
		issues, _ := page["issues"].([]any)
		for _, is := range issues {
			if k := r.upsert(base, asMap(is)); k != "" {
				seen[k] = true
			}
		}
		r.commit()
		start += len(issues)
		total, _ := asInt(page["total"])
		if len(issues) == 0 || int64(start) >= total {
			break
		}
	}
	r.setState("last_pull", strconv.FormatInt(started, 10))
	if full {
		r.setState("last_full", strconv.FormatInt(started, 10))
	}
	r.commit()
	return seen, start < limit, nil
}

func (r *run) fullDue() bool {
	if r.getState("want_full", "") == "1" {
		return true
	}
	last := r.stateInt("last_full")
	h := r.o.Now().Hour()
	return last == 0 || (r.now()-last > 20*3600 && h >= 2 && h < 6) || r.now()-last > 48*3600
}

// --- publish ---------------------------------------------------------------------------------------------------------

// carried are the status keys other jobs own: kept from the last status file as they were.
var carried = []string{"proposals", "agent"}

func (r *run) publish(problems []string, calls int) error {
	path := filepath.Join(r.o.Out, "board-status.json")
	var old map[string]json.RawMessage
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &old)
	}
	reqs := map[string]any{}
	if raw, ok := old["requests"]; ok {
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		d.Decode(&reqs)
	}
	for k, v := range r.results {
		reqs[k] = v
	}
	keys := make([]string, 0, len(reqs))
	for k := range reqs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > keepResults {
		keys = keys[len(keys)-keepResults:]
	}
	ro := object{}
	for _, k := range keys {
		ro = append(ro, kv{k, keepOrder(reqs[k])})
	}
	q := map[string]int64{}
	for _, x := range r.rows(`SELECT state, count(*) n FROM outbox GROUP BY state`) {
		q[x.s("state")] = x.i("n")
	}
	failed := []any{}
	for _, x := range r.rows(`SELECT o.id, o.kind, o.last_error, o.done_at, o.task_id, t.jira_key, t.title FROM outbox o LEFT JOIN tasks t ` +
		`ON t.id=o.task_id WHERE o.state IN ('failed','dropped') ORDER BY o.id DESC LIMIT 10`) {
		failed = append(failed, rowObj(x, "id", "kind", "last_error", "done_at", "task_id", "jira_key", "title"))
	}
	waiting := []any{}
	for _, x := range r.rows(`SELECT o.id, o.kind, o.last_error, o.attempts, o.next_at, o.task_id FROM outbox o WHERE o.state='queued' ` +
		`AND o.last_error IS NOT NULL ORDER BY o.id LIMIT 10`) {
		waiting = append(waiting, rowObj(x, "id", "kind", "last_error", "attempts", "next_at", "task_id"))
	}
	mode := "local"
	jurl := ""
	if r.conf {
		mode, jurl = "jira", r.cfg["JIRA_URL"]
	}
	um := object{}
	for _, u := range r.mp.userOrder {
		um = append(um, kv{u, r.mp.users[u]})
	}
	shared := r.cfg["JIRA_MODE"]
	if shared == "" {
		shared = "rw"
	}
	counts := map[string]any{}
	json.Unmarshal([]byte(r.getState("board_counts", "{}")), &counts)
	st := object{}
	for _, k := range []string{"pushed", "failed", "conflicts", "new", "updated", "gone"} {
		st = append(st, kv{k, int64(r.stats[k])})
	}
	if problems == nil {
		problems = []string{}
	}
	doc := object{{"updated_at", r.now()}, {"configured", r.conf}, {"mode", mode}, {"users_connected", int64(0)},
		{"in_use", r.conf}, {"jira_url", jurl}, {"project", r.cfg["JIRA_PROJECT"]}, {"board_id", r.cfg["JIRA_BOARD_ID"]},
		{"bot", r.getState("bot", "")}, {"problems", listAny(problems)}, {"last_run", r.now()},
		{"last_ok", r.stateInt("last_ok")}, {"last_pull", r.stateInt("last_pull")}, {"last_full", r.stateInt("last_full")},
		{"last_error", r.getState("last_error", "")}, {"last_error_at", r.stateInt("last_error_at")},
		{"auth_error", r.getState("auth_error", "")}, {"calls", int64(calls)}, {"stats", st}, {"queued", q["queued"]},
		{"failed", q["failed"]}, {"dropped", q["dropped"]}, {"recent_failures", failed}, {"retrying", waiting},
		{"settings", object{{"auto_incidents", r.getState("auto_incidents", "0") == "1"}}}}
	for _, k := range carried[:1] {
		doc = append(doc, kv{k, rawOr(old[k], object{})})
	}
	doc = append(doc, kv{"approvers", listAny(r.mp.approvers)}, kv{"shared_mode", shared}, kv{"board", nil},
		kv{"user_map", um}, kv{"board_snap_at", r.stateInt("board_snap_at")}, kv{"board_error", r.getState("board_error", "")},
		kv{"board_counts", counts}, kv{"agent", rawOr(old["agent"], object{})}, kv{"requests", ro})
	if r.o.DryRun {
		r.log().Info("dry run: board-status.json not written")
		return nil
	}
	if err := os.MkdirAll(r.o.Out, 0o755); err != nil {
		return err
	}
	return writeAtomic(path, pyDumpsIndent(doc, 1), 0o644)
}

// keepOrder re-encodes a decoded JSON value with its object keys in the order the result objects use.
func keepOrder(v any) any {
	if o, ok := v.(object); ok {
		return o
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	var o object
	used := map[string]bool{}
	for _, k := range []string{"at", "result", "task", "kind", "by", "choices", "key", "col"} {
		if x, ok := m[k]; ok {
			o = append(o, kv{k, x})
			used[k] = true
		}
	}
	var rest []string
	for k := range m {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		o = append(o, kv{k, m[k]})
	}
	return o
}

type rawJSON []byte

func rawOr(raw json.RawMessage, def any) any {
	if len(raw) == 0 {
		return def
	}
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return def
	}
	return v
}

func rowObj(x row, cols ...string) object {
	o := object{}
	for _, c := range cols {
		o = append(o, kv{c, x[c]})
	}
	return o
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- run -------------------------------------------------------------------------------------------------------------

// Run is one board sync run.
func Run(ctx context.Context, o Options) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if o.Name == "" {
		o.Name = "Argus"
	}
	if o.Label == "" {
		o.Label = "argus"
	}
	if o.Ref == "" {
		o.Ref = "ARG"
	}
	if o.NewJira == nil {
		o.NewJira = NewJira
	}
	syscall.Umask(0o022)
	if err := os.MkdirAll(filepath.Dir(o.DB), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(o.DB), ".lock"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	db, err := Connect(ctx, o.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	if !o.DryRun {
		os.Chmod(o.DB, 0o644)
	}
	r := &run{o: &o, ctx: ctx, db: db, results: map[string]object{}, glResults: map[string]object{},
		stats: map[string]int{"pushed": 0, "failed": 0, "conflicts": 0, "new": 0, "updated": 0, "gone": 0}}
	defer func() {
		if x := recover(); x != nil {
			r.rollback()
			if e, ok := x.(dbErr); ok {
				err = e.err
				return
			}
			panic(x)
		}
	}()
	return r.main()
}

func (r *run) main() error {
	o := r.o
	cfg, problems := LoadConfig(o.EnvFiles, o.OwnerUID)
	for k, v := range o.Config {
		if v != "" {
			cfg[k] = v
		}
	}
	r.cfg, r.mp = cfg, newMapping(cfg)
	token, tproblem := LoadToken(o.PATFile, o.OwnerUID)
	if o.Token != "" {
		token, tproblem = o.Token, ""
	}
	r.conf = cfg["JIRA_URL"] != "" && cfg["JIRA_PROJECT"] != "" && token != ""
	if cfg["JIRA_URL"] != "" && cfg["JIRA_PROJECT"] != "" && token == "" {
		problems = append(problems, "token: "+tproblem)
	}
	if r.conf {
		j, err := o.NewJira(cfg["JIRA_URL"], token, cfg["JIRA_CA_FILE"])
		if err != nil {
			problems = append(problems, "JIRA_URL: "+err.Error())
			r.conf = false
		} else {
			j.DryRun, j.Log, j.User = o.DryRun, o.Log, o.JiraUser
			r.jira = j
		}
	}
	r.glProcessRequests()
	r.processRequests()
	if o.Full {
		r.setState("want_full", "1")
	}
	calls := 0
	if r.conf {
		full := r.fullDue()
		complete := true
		seenAll := map[string]bool{}
		err := func() error {
			me, err := r.jira.Myself(r.ctx)
			if err != nil {
				return err
			}
			bot := orStr(me["name"])
			if bot == "" {
				bot = orStr(me["key"])
			}
			if bot == "" {
				bot = "?"
			}
			r.setState("bot", head(bot, 64))
			r.setState("auth_error", "")
			if err := r.push(); err != nil {
				return err
			}
			seen, ok, err := r.pull(strings.TrimRight(r.cfg["JIRA_URL"], "/"), `project = "`+r.cfg["JIRA_PROJECT"]+`"`, full)
			for k := range seen {
				seenAll[k] = true
			}
			if err != nil {
				return err
			}
			complete = complete && ok
			r.setState("last_error", "")
			return nil
		}()
		if err != nil {
			complete = false
			r.botDown = err.Error()
			if IsAuth(err) {
				r.setState("auth_error", head(err.Error(), 300))
			}
			r.setState("last_error", head(err.Error(), 300))
			r.setState("last_error_at", strconv.FormatInt(r.now(), 10))
		}
		if err := r.push(); err != nil {
			r.setState("last_error", head(err.Error(), 300))
			r.setState("last_error_at", strconv.FormatInt(r.now(), 10))
		}
		for _, mv := range r.rows(`SELECT * FROM board_moves ORDER BY at`) {
			res := "refused: no board is configured"
			prev := keepOrder(map[string]any{})
			if old, ok := r.results[mv.s("rid")]; ok {
				prev = old
			}
			r.results[mv.s("rid")] = mergeResult(prev.(object), object{{"result", res}, {"choices", nil}, {"key", mv.s("key")}, {"col", mv.i("col")}, {"at", r.now()}})
			r.must(`DELETE FROM board_moves WHERE rid=?`, mv.s("rid"))
			r.log().Info("board move", "key", mv.s("key"), "col", mv.i("col"), "by", mv.s("by"), "result", res)
		}
		r.commit()
		if full && complete {
			for _, t := range r.rows(`SELECT id, jira_key FROM tasks WHERE jira_key IS NOT NULL AND gone=0`) {
				if !seenAll[t.s("jira_key")] {
					r.must(`UPDATE tasks SET gone=1, updated_at=? WHERE id=?`, r.now(), t.i("id"))
					r.event(t.i("id"), "jira", "jira", "No longer visible in Jira (deleted, moved, or no connection sees it); hidden from the board")
					r.stats["gone"]++
				}
			}
			r.setState("last_full", strconv.FormatInt(r.now(), 10))
		}
		if full {
			r.setState("want_full", "0")
		}
		r.setState("want_pull", "0")
		if r.getState("last_error", "") == "" {
			r.setState("last_ok", strconv.FormatInt(r.now(), 10))
		}
		calls = r.jira.Calls
		r.commit()
	} else {
		// board-move requests without Jira: answered on the next run that has Jira (as the reference job does)
		r.commit()
	}
	glRun := r.glSync()
	r.kickNotice()
	r.commit()
	if err := r.publish(problems, calls); err != nil {
		return err
	}
	if err := r.glPublish(glRun); err != nil {
		return err
	}
	mode := "local"
	if r.conf {
		mode = "jira"
	}
	s := r.stats
	r.log().Info("board sync", "mode", mode, "requests", len(r.results), "pushed", s["pushed"], "failed", s["failed"],
		"conflicts", s["conflicts"], "new", s["new"], "updated", s["updated"], "error", orDash(r.getState("last_error", "")),
		"dry_run", o.DryRun)
	return nil
}

func mergeResult(prev, upd object) object {
	out := object{}
	for _, x := range prev {
		if upd.get(x.k) == nil && !hasKey(upd, x.k) {
			out = append(out, x)
		} else {
			out = append(out, kv{x.k, upd.get(x.k)})
		}
	}
	for _, x := range upd {
		if !hasKey(out, x.k) {
			out = append(out, x)
		}
	}
	return out
}

func hasKey(o object, k string) bool {
	for _, x := range o {
		if x.k == k {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// kickNotice records the newest notice; the notice job (not ported) picks them up from the table.
func (r *run) kickNotice() {
	top := r.one(`SELECT max(id) m FROM notices`)
	n := int64(0)
	if top != nil {
		n = top.i("m")
	}
	r.setState("notice_kicked", strconv.FormatInt(n, 10))
}
