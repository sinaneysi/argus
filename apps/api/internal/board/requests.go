package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
)

// refused is a request the job will not apply; its text is the result the page shows.
type refused string

func (r refused) Error() string { return string(r) }

// unsupported are request kinds of the reference job that this port does not apply yet.
var unsupported = map[string]string{
	"proposal": "approval proposals", "proposal-settings": "approval proposals", "mx-preview": "Matrix requests",
	"agent-answers": "the task agent", "agent-replan": "the task agent", "agent-start": "the task agent",
	"agent-stop": "the task agent", "agent-settings": "the task agent", "agent-fixer-move": "the task agent",
	"lesson": "lessons", "lesson-settings": "lessons",
}

func readRequest(path string) (map[string]any, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open request (%s)", strerror(err))
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("request is not a regular file")
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxFile+1))
	if len(data) > maxFile {
		return nil, errors.New("request file too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || !validUTF8JSON(data) {
		return nil, errors.New("request is not valid JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("request is not a JSON object")
	}
	return m, nil
}

func validUTF8JSON(b []byte) bool { return json.Valid(b) }

func strerror(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		s := en.Error()
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return err.Error()
}

// asInt is a JSON integer (not a bool, not a float).
func asInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil && !strings.ContainsAny(n.String(), ".eE")
}

func (r *run) task(req map[string]any) row {
	tid, ok := asInt(req["task"])
	if !ok || tid <= 0 {
		panic(refused("no task id"))
	}
	t := r.one(`SELECT * FROM tasks WHERE id=? AND gone=0`, tid)
	if t == nil {
		panic(refused("no such task"))
	}
	return t
}

type created struct {
	title, env, host, prio  string
	labels                  []string
	kind, sid, url          any
	desc, evidence, fixText string
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truthyStr(v any) (string, bool) {
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

func validateCreate(req map[string]any) created {
	title := strings.Join(strings.FieldsFunc(cleanText(req["title"], 1000), unicode.IsSpace), " ")
	if title == "" || runes(title) > maxTitle {
		panic(refused(fmt.Sprintf("title must be 1 to %d characters", maxTitle)))
	}
	env, _ := req["env"].(string)
	if orStr(req["env"]) != "" && !contains(envs, env) {
		panic(refused("unknown environment"))
	}
	host, _ := req["host"].(string)
	if orStr(req["host"]) != "" && !hostRe.MatchString(host) {
		panic(refused("host must be a host name or address"))
	}
	prio := "Medium"
	if orStr(req["priority"]) != "" {
		prio, _ = req["priority"].(string)
		if !contains(priorities, prio) {
			panic(refused("unknown priority"))
		}
	}
	var labels []string
	if orStr(req["labels"]) != "" {
		l, ok := req["labels"].([]any)
		if !ok || len(l) > 10 {
			panic(refused("labels: up to 10 words of letters, digits, . _ -"))
		}
		for _, x := range l {
			s, ok := x.(string)
			if !ok || !labelRe.MatchString(s) {
				panic(refused("labels: up to 10 words of letters, digits, . _ -"))
			}
			labels = append(labels, s)
		}
	}
	or := func(k string) any {
		if orStr(req[k]) == "" {
			return nil
		}
		return req[k]
	}
	kind, sid, url := or("source_kind"), or("source_id"), or("source_url")
	if kind != nil {
		ks, _ := kind.(string)
		ss, ok := sid.(string)
		if !contains(sourcesOK, ks) || !ok || !srcIDRe.MatchString(ss) {
			panic(refused("bad source"))
		}
	}
	if kind == nil && (sid != nil || url != nil) {
		panic(refused("bad source"))
	}
	if url != nil {
		if s, ok := url.(string); !ok || !srcURLRe.MatchString(s) {
			panic(refused("bad source link"))
		}
	}
	v := created{title: title, env: env, host: host, prio: prio, labels: dedupe(labels), kind: kind, sid: sid, url: url,
		desc: cleanText(req["description"], maxText+1), evidence: cleanText(req["evidence"], maxText+1),
		fixText: cleanText(req["fix_prompt"], maxPrompt+1)}
	if runes(v.desc) > maxText || runes(v.evidence) > maxText {
		panic(refused(fmt.Sprintf("description and evidence are limited to %d KB each", maxText/1024)))
	}
	if len(v.fixText) > maxPrompt {
		panic(refused(fmt.Sprintf("fix prompt is limited to %d KB", maxPrompt/1024)))
	}
	return v
}

func (r *run) insertTask(v created, by, createdBy string) (int64, bool) {
	return r.insertTaskIn(v, by, createdBy, "jira", 0)
}

// insertTaskIn is a new task and its queued create in its tracker (Jira, or GitLab issues; glProject: the project of
// a group board's issue). It returns (task id, existed?): a task with the same source exists already.
func (r *run) insertTaskIn(v created, by, createdBy, tracker string, glProject int64) (int64, bool) {
	if v.kind != nil {
		if old := r.one(`SELECT id FROM tasks WHERE source_kind=? AND source_id=?`, v.kind, v.sid); old != nil {
			return old.i("id"), true
		}
	}
	p := r.o.Label
	labels := []string{p}
	switch createdBy {
	case "auto":
		labels = append(labels, p+"-auto")
	case "proposal":
		labels = append(labels, p+"-proposed")
	}
	if v.env != "" {
		labels = append(labels, "env-"+v.env)
	}
	if v.kind != nil {
		labels = append(labels, p+"-src-"+v.kind.(string)+"-"+v.sid.(string))
	}
	labels = dedupe(append(labels, v.labels...))
	ts := r.now()
	res := r.must(`INSERT INTO tasks (tracker, title, description, evidence, fix_prompt, status, priority, labels, env, host, source_kind, `+
		`source_id, source_url, created_by, requested_by, created_at, updated_at) VALUES (?,?,?,?,?,'todo',?,?,?,?,?,?,?,?,?,?,?)`,
		tracker, v.title, v.desc, v.evidence, v.fixText, v.prio, string(pyDumps(listAny(labels))), orNil(v.env), orNil(v.host),
		v.kind, v.sid, v.url, createdBy, by, ts, ts)
	tid, _ := res.LastInsertId()
	if tracker == "gitlab" && glProject != 0 {
		r.must(`UPDATE tasks SET gl_project=? WHERE id=?`, glProject, tid)
	}
	r.queue(tid, "create", object{{"as", by}})
	src := ""
	if v.kind != nil {
		src = " from " + v.kind.(string) + " " + v.sid.(string)
	}
	gl := ""
	if tracker == "gitlab" {
		gl = " · GitLab issue"
	}
	r.event(tid, "created", by, "Created"+src+" ("+createdBy+")"+gl)
	r.notice("created", tid, by, v.title)
	return tid, false
}

// move changes a task's column and queues the Jira transition.
func (r *run) move(t row, to, by, note string, optional bool) {
	if to == t.s("status") {
		return
	}
	r.must(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, to, r.now(), t.i("id"))
	r.queue(t.i("id"), "transition", object{{"to", to}, {"from_jira", t["jira_status"]}, {"at", r.now()}, {"by", by},
		{"as", by}, {"optional", optional}})
	from := columnLabel[t.s("status")]
	if from == "" {
		from = t.s("status")
	}
	text := from + " → " + columnLabel[to]
	if note != "" {
		text += " · " + note
	}
	r.event(t.i("id"), "moved", by, text)
}

// canWrite: the bot path only (per-user connections are not ported).
func (r *run) canWrite() (bool, string) {
	if !r.conf {
		return true, ""
	}
	if r.botWrites() {
		return true, ""
	}
	return false, "the shared Jira connection is read-only: connect your own Jira in Account (read and write)"
}

func (r *run) botWrites() bool {
	m := r.cfg["JIRA_MODE"]
	return r.conf && (m == "" || m == "rw")
}

func (r *run) handle(req map[string]any, rid string) (string, any) {
	kind, _ := req["kind"].(string)
	by, ok := req["by"].(string)
	if !ok || !userRe.MatchString(by) {
		panic(refused("bad user"))
	}
	role := r.userRole(by)
	if role == "" {
		panic(refused("unknown or disabled user"))
	}
	tracker := "jira"
	switch kind {
	case "create":
		if orStr(req["tracker"]) != "" {
			tracker, _ = req["tracker"].(string)
		}
		if tracker != "jira" && tracker != "gitlab" {
			panic(refused("unknown tracker"))
		}
	case "move", "comment", "assign":
		tracker = r.task(req).s("tracker")
	}
	var gm *glMeta
	if tracker == "gitlab" && (kind == "create" || kind == "move" || kind == "comment" || kind == "assign") {
		if gm = r.o.glMetaRead(); !r.o.glConnected(gm) {
			panic(refused("GitLab issues is not connected (Settings → Integrations → GitLab issues)"))
		}
	} else if kind == "create" || kind == "move" || kind == "comment" || kind == "assign" || kind == "board-move" {
		if ok, why := r.canWrite(); !ok {
			panic(refused(why))
		}
	}
	switch kind {
	case "create":
		v := validateCreate(req)
		var glp int64
		if tracker == "gitlab" {
			glp = glCheckProject(req["gl_project"], gm)
		}
		tid, existed := r.insertTaskIn(v, by, "human", tracker, glp)
		if existed {
			return "exists", tid
		}
		return "created", tid
	case "move":
		t := r.task(req)
		to, _ := req["to"].(string)
		if !contains(Columns, to) {
			panic(refused("unknown column"))
		}
		if to == "fixer" {
			panic(refused("the fixer column is retired: move the task to In Progress; " + r.o.Name +
				" plans it and an admin spins up the agent"))
		}
		r.move(t, to, by, "", false)
		return "moved", t.i("id")
	case "comment":
		t := r.task(req)
		body := strings.TrimFunc(cleanText(req["body"], maxComment+1), unicode.IsSpace)
		if body == "" || runes(body) > maxComment {
			panic(refused(fmt.Sprintf("comment must be 1 to %d characters", maxComment)))
		}
		res := r.must(`INSERT INTO comments (task_id, author, body, created_at, pending) VALUES (?,?,?,?,1)`, t.i("id"), by, body, r.now())
		lid, _ := res.LastInsertId()
		text := "[" + r.o.Name + " · " + by + "] " + body
		if t.s("tracker") == "gitlab" {
			text = body // the GitLab push names the author
		}
		r.queue(t.i("id"), "comment", object{{"body", text}, {"local", lid}, {"as", by}})
		r.event(t.i("id"), "comment", by, "Commented")
		return "commented", t.i("id")
	case "assign":
		t := r.task(req)
		var jname any
		name := by
		if t.s("tracker") == "gitlab" {
			name = r.glUser(gm, by)
			jname = name
		}
		r.must(`UPDATE tasks SET assignee=?, assignee_name=?, updated_at=? WHERE id=?`, name, by, r.now(), t.i("id"))
		r.queue(t.i("id"), "assign", object{{"user", by}, {"jira_name", jname}, {"as", by}})
		r.event(t.i("id"), "assigned", by, "Assigned to "+by)
		return "assigned", t.i("id")
	case "board-move":
		key, _ := req["key"].(string)
		col, isInt := asInt(req["col"])
		if !keyRe.MatchString(key) || !isInt || col < 0 || col >= 50 {
			panic(refused("bad board move"))
		}
		var tr any
		if req["transition"] != nil {
			s, ok := req["transition"].(string)
			if !ok || !isDigits(s) || len(s) >= 10 {
				panic(refused("bad transition"))
			}
			tr = s
		}
		r.must(`INSERT OR REPLACE INTO board_moves VALUES (?,?,?,?,?,?)`, rid, by, key, col, tr, r.now())
		return "queued", nil
	case "sync":
		if req["full"] == true && role == "admin" {
			r.setState("want_full", "1")
		} else {
			r.setState("want_pull", "1")
		}
		return "sync requested", nil
	case "settings":
		if role != "admin" {
			panic(refused("only admins can change board settings"))
		}
		v, ok := req["auto_incidents"].(bool)
		if !ok {
			panic(refused("bad setting"))
		}
		r.setState("auto_incidents", map[bool]string{true: "1", false: "0"}[v])
		r.event(nil, "settings", by, "Propose tasks for page-level incidents: "+map[bool]string{true: "on", false: "off"}[v])
		return "saved", nil
	}
	if what, ok := unsupported[kind]; ok {
		panic(refused(what + " requests are not handled by this board sync yet"))
	}
	panic(refused("unknown request kind"))
}

// safeHandle runs handle and turns a refusal or a database error into a result.
func (r *run) safeHandle(req map[string]any, rid string) (res string, tid any) {
	defer func() {
		if x := recover(); x != nil {
			r.rollback()
			switch e := x.(type) {
			case refused:
				res, tid = "refused: "+string(e), nil
			case dbErr:
				res, tid = "refused: database error ("+head(e.err.Error(), 80)+")", nil
			default:
				panic(x)
			}
		}
	}()
	res, tid = r.handle(req, rid)
	if err := r.commit(); err != nil {
		panic(dbErr{err})
	}
	return res, tid
}

func (r *run) processRequests() int {
	ents, err := os.ReadDir(r.o.ReqDir)
	if err != nil {
		return 0
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	n := 0
	for _, name := range names {
		path := filepath.Join(r.o.ReqDir, name)
		rid := ""
		if strings.HasSuffix(name, ".json") {
			rid = strings.TrimSuffix(name, ".json")
		}
		if !idRe.MatchString(rid) {
			if !strings.HasPrefix(name, ".") && !r.o.DryRun {
				os.Remove(path)
			}
			continue
		}
		req, rerr := readRequest(path)
		if req == nil {
			req = map[string]any{}
		}
		if !r.o.DryRun {
			if os.Remove(path) != nil {
				continue
			}
		}
		n++
		var result string
		var tid any
		if rerr != nil {
			result = "refused: " + rerr.Error()
		} else {
			result, tid = r.safeHandle(req, rid)
		}
		r.results[rid] = object{{"at", r.now()}, {"result", result}, {"task", tid}, {"kind", head(pyStr(orDefault(req["kind"], "")), 12)},
			{"by", head(pyStr(orDefault(req["by"], "")), 64)}}
		r.log().Info("request", "id", rid, "kind", pyStr(orDefault(req["kind"], "?")), "by", head(pyStr(orDefault(req["by"], "?")), 40), "result", result)
	}
	return n
}

func orDefault(v, d any) any {
	if v == nil {
		return d
	}
	return v
}
