package board

// chat.go: the task chat (the port of the reference task chat, ADR-0013 P2): one thread per task the agent works,
// one bounded read-only model turn per message (or per batch of messages posted while a turn waits).
//
//  1. The task page's composer writes settings-in/task-chat/<rid>.json {kind: "chat", key, text, by}. chatProcess
//     validates it again (an enabled admin, chat on, the agent works the task, 1..4000 characters, one post per 10 s,
//     a daily cap per user), stores it secret-scanned and queues one turn (a queued turn takes the new message too).
//  2. chatTick: one turn at a time, and none while the task's own intake or run is in flight. Claude off or the
//     egress guard: the turn is skipped with a line in the thread (the message stays). The task budget (intake +
//     chat + run), 30 turns per task a day and 100 a day; else the job file <jobs>/<key>.chat.json (the task, the
//     plan, the rolling summary, the last 20 messages; all untrusted) for the model runner.
//  3. chatIngest: the reply {reply, summary, replan, answers} is validated and secret-scanned; the reply joins the
//     thread, the summary replaces the rolling summary, the cost counts against the task. Answers to the plan's
//     questions and replan=true fill agent_inputs and send the task back to planning.
//
// A chat message never authorises a WRITE step. Not ported: the board-level "Ask" threads and proposed tasks from
// the chat (proposals are not in this version), Matrix messages in the thread.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/notify"
)

const (
	chatMaxText, chatMaxReply, chatMaxSummary, chatLastN  = 4000, 4000, 1500, 20
	chatPostGap                                           = 10
	chatTurnTimeout                                       = 7 * 60
	chatTurnsPerTaskDay, chatTurnsPerDay, chatPostsPerDay = 30, 100, 200
	chatPaused                                            = "Claude paused: your message is stored. The agent replies to the next message once Claude usage is on (Settings)."
	chatRedacted                                          = "This looked like a credential and was removed."
)

const chatSchema = `
CREATE TABLE IF NOT EXISTS task_chat (id INTEGER PRIMARY KEY, key TEXT NOT NULL, seq INTEGER NOT NULL,
  author_kind TEXT NOT NULL, author TEXT, kind TEXT NOT NULL, body TEXT NOT NULL, turn_id INTEGER, created INTEGER NOT NULL,
  UNIQUE(key, seq));
CREATE TABLE IF NOT EXISTS task_chat_turns (id INTEGER PRIMARY KEY, key TEXT NOT NULL, state TEXT NOT NULL,
  upto_seq INTEGER, requested_by TEXT, started INTEGER, finished INTEGER, cost REAL, run_id INTEGER, error TEXT);
CREATE INDEX IF NOT EXISTS task_chat_turns_state ON task_chat_turns(state, id);
CREATE TABLE IF NOT EXISTS task_chat_state (key TEXT PRIMARY KEY, summary TEXT, budget_usd REAL, spent_usd REAL,
  budget_raised_by TEXT, updated INTEGER);
`

func (r *run) chatEnsure() {
	for _, stmt := range strings.Split(chatSchema, ";") {
		if strings.TrimSpace(stmt) != "" {
			r.must(stmt)
		}
	}
}

func (r *run) dayStart() int64 {
	t := r.o.Now()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Unix()
}

func (r *run) chatAdd(key, authorKind, author, kind, body string, turn any) int64 {
	seq := r.one(`SELECT COALESCE(max(seq),0)+1 n FROM task_chat WHERE key=?`, key).i("n")
	var a any
	if author != "" {
		a = head(author, 64)
	}
	r.must(`INSERT INTO task_chat (key, seq, author_kind, author, kind, body, turn_id, created) VALUES (?,?,?,?,?,?,?,?)`,
		key, seq, authorKind, a, kind, head(body, chatMaxText), turn, r.now())
	return seq
}

func (r *run) scanText(text string) (string, bool) {
	out, hits := notify.Redact(text, r.o.Allow)
	return out, len(hits) > 0
}

// --- 1. requests from the app ----------------------------------------------------------------------------------

func (r *run) chatProcess() {
	dir := r.o.Agent.ChatReqDir
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	r.chatEnsure()
	r.commit()
	for _, name := range names {
		path := filepath.Join(dir, name)
		rid := strings.TrimSuffix(name, ".json")
		if !strings.HasSuffix(name, ".json") || !idRe.MatchString(rid) {
			if !strings.HasPrefix(name, ".") {
				os.Remove(path)
			}
			continue
		}
		req, rerr := readRequest(path)
		if os.Remove(path) != nil {
			continue
		}
		result := ""
		if rerr != nil {
			result = "refused: " + rerr.Error()
		} else {
			result = r.safeChatPost(req)
		}
		r.results[rid] = object{{"at", r.now()}, {"result", result}, {"task", nil}, {"kind", "chat"}, {"by", head(orStr(req["by"]), 64)}}
		r.log().Info("request", "id", rid, "kind", "chat", "by", head(orStr(req["by"]), 40), "result", result)
	}
}

func (r *run) safeChatPost(req map[string]any) (res string) {
	defer func() {
		if x := recover(); x != nil {
			r.rollback()
			switch e := x.(type) {
			case refused:
				res = "refused: " + string(e)
			case dbErr:
				res = "refused: database error (" + head(e.err.Error(), 80) + ")"
			default:
				panic(x)
			}
		}
	}()
	res = r.chatPost(req)
	if err := r.commit(); err != nil {
		panic(dbErr{err})
	}
	return res
}

func (r *run) chatPost(req map[string]any) string {
	by, key := orStr(req["by"]), orStr(req["key"])
	if orStr(req["kind"]) != "chat" || !userRe.MatchString(by) {
		panic(refused("bad request"))
	}
	role := r.userRole(by)
	if role == "" {
		panic(refused("unknown or disabled user"))
	}
	if role != "admin" {
		panic(refused("viewers read the task chat; admins post"))
	}
	if !r.agentSettings().Chat {
		panic(refused("the task chat is off (Admin -> Task agent)"))
	}
	if (!keyRe.MatchString(key) && !glKeyRe.MatchString(key)) || r.one(`SELECT 1 x FROM agent_tasks WHERE key=?`, key) == nil {
		panic(refused(r.o.Name + " is not working this task"))
	}
	raw, _ := req["text"].(string)
	text := strings.TrimSpace(cleanText(raw, chatMaxText+1))
	if text == "" {
		panic(refused("empty message"))
	}
	if runes(text) > chatMaxText {
		panic(refused(fmt.Sprintf("a message is limited to %d characters", chatMaxText)))
	}
	if last := r.one(`SELECT max(created) m FROM task_chat WHERE author_kind='user' AND author=?`, by); last != nil && !last.null("m") &&
		r.now()-last.i("m") < chatPostGap {
		panic(refused(fmt.Sprintf("one message per %d seconds", chatPostGap)))
	}
	if r.one(`SELECT count(*) n FROM task_chat WHERE author_kind='user' AND author=? AND created>=?`, by, r.dayStart()).i("n") >= chatPostsPerDay {
		panic(refused(fmt.Sprintf("daily limit of %d messages reached", chatPostsPerDay)))
	}
	text, hit := r.scanText(text)
	var tid int64
	if q := r.one(`SELECT id FROM task_chat_turns WHERE key=? AND state='queued' ORDER BY id LIMIT 1`, key); q != nil {
		tid = q.i("id")
	} else {
		res := r.must(`INSERT INTO task_chat_turns (key, state, requested_by) VALUES (?,?,?)`, key, "queued", by)
		tid, _ = res.LastInsertId()
	}
	seq := r.chatAdd(key, "user", by, "message", text, tid)
	if hit {
		r.chatAdd(key, "system", "", "message", chatRedacted, tid)
	}
	r.must(`UPDATE task_chat_turns SET upto_seq=? WHERE id=?`, seq, tid)
	if hit {
		return "posted (a credential was removed)"
	}
	return "posted"
}

// --- 2. the chat lane --------------------------------------------------------------------------------------------

func (r *run) turnsSince(start int64, key string) int64 {
	q := `SELECT count(*) n FROM task_chat_turns WHERE state IN ('running','done','failed') AND started>=?`
	if key != "" {
		return r.one(q+` AND key=?`, start, key).i("n")
	}
	return r.one(q, start).i("n")
}

func (r *run) closeTurn(t row, state, line, e string) {
	var ev any
	if e != "" {
		ev = e
	}
	r.must(`UPDATE task_chat_turns SET state=?, finished=?, error=? WHERE id=?`, state, r.now(), ev, t.i("id"))
	r.chatAdd(t.s("key"), "system", "", "message", line, t.i("id"))
}

func (r *run) chatTick() string {
	r.chatEnsure()
	r.chatIngest()
	for _, t := range r.rows(`SELECT * FROM task_chat_turns WHERE state='running'`) {
		if r.now()-t.i("started") > chatTurnTimeout {
			r.stopJob("chat", t.s("key"))
			r.closeTurn(t, "failed", "The agent did not reply in time. Send the message again.", "timeout")
		}
	}
	r.commit()
	if r.one(`SELECT 1 x FROM task_chat_turns WHERE state='running'`) != nil {
		return "chat busy"
	}
	for _, t := range r.rows(`SELECT * FROM task_chat_turns WHERE state='queued' ORDER BY id`) {
		key := t.s("key")
		a := r.one(`SELECT * FROM agent_tasks WHERE key=?`, key)
		if a == nil {
			r.closeTurn(t, "skipped", r.o.Name+" no longer works this task.", "")
			continue
		}
		if a.s("state") == "approved" || ((a.s("state") == "planning" || a.s("state") == "running") && !a.null("job_started")) {
			continue // answered after the task's own job ends
		}
		if !r.agentSettings().Chat {
			r.closeTurn(t, "skipped", "The task chat is off (Admin -> Task agent).", "")
			continue
		}
		if ok, why := r.o.Agent.Guard(); !ok {
			line := head(why, 300)
			if strings.Contains(why, "Claude usage is off") {
				line = chatPaused
			}
			r.closeTurn(t, "skipped", line, "")
			continue
		}
		if f64(a["spent_usd"]) >= r.o.Agent.TaskBudget {
			r.closeTurn(t, "over-budget", fmt.Sprintf("Task budget of $%.2f used (planning, chat and runs).", r.o.Agent.TaskBudget), "")
			continue
		}
		if r.turnsSince(r.dayStart(), key) >= chatTurnsPerTaskDay {
			r.closeTurn(t, "over-budget", fmt.Sprintf("Daily limit of %d chat replies for this task reached; the agent replies again tomorrow.", chatTurnsPerTaskDay), "")
			continue
		}
		if r.turnsSince(r.dayStart(), "") >= chatTurnsPerDay {
			r.closeTurn(t, "over-budget", fmt.Sprintf("The daily limit of %d chat replies is reached; the agent replies again tomorrow.", chatTurnsPerDay), "")
			continue
		}
		if err := r.writeJob(key+".chat.json", r.chatJob(a, t)); err != nil {
			r.closeTurn(t, "failed", "The agent could not start the reply. Send the message again.", "could not write the job file")
			r.commit()
			return "chat start failed"
		}
		r.must(`UPDATE task_chat_turns SET state='running', started=? WHERE id=?`, r.now(), t.i("id"))
		r.commit()
		return "chat " + key
	}
	r.commit()
	return "chat idle"
}

func (r *run) chatJob(a, turn row) map[string]any {
	key := turn.s("key")
	summary := ""
	if st := r.one(`SELECT summary FROM task_chat_state WHERE key=?`, key); st != nil {
		summary = st.s("summary")
	}
	prev := r.one(`SELECT COALESCE(max(upto_seq),0) m FROM task_chat_turns WHERE key=? AND state='done'`, key).i("m")
	ms := r.rows(`SELECT * FROM task_chat WHERE key=? AND seq<=? ORDER BY seq DESC LIMIT ?`, key, turn.i("upto_seq"), chatLastN)
	msgs := []any{}
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		msgs = append(msgs, map[string]any{"seq": m.i("seq"), "from": m.s("author_kind"), "author": m.s("author"), "kind": m.s("kind"),
			"text": m.s("body"), "new": m.i("seq") > prev && m.s("author_kind") != "argus"})
	}
	var plan map[string]any
	json.Unmarshal([]byte(a.s("plan")), &plan)
	task := map[string]any{}
	if id, ok := r.taskIDOf(key).(int64); ok {
		if t := r.one(`SELECT * FROM tasks WHERE id=?`, id); t != nil {
			tr := t.s("tracker")
			if tr == "" {
				tr = "jira"
			}
			task = map[string]any{"title": t.s("title"), "description": head(t.s("description"), 8000), "env": t.s("env"), "status": t.s("status"), "tracker": tr}
		}
	}
	_, ans, _ := r.agentInputs(key)
	qs := asList(plan["questions"])
	if qs == nil {
		qs = []any{}
	}
	return map[string]any{"phase": "chat", "board": false, "key": key, "task": task, "agent_state": a.s("state"),
		"plan_version": a.i("plan_version"), "plan": plan, "questions": qs, "answers": ans, "summary": summary,
		"messages": msgs, "turn": turn.i("id")}
}

// --- 3. results ------------------------------------------------------------------------------------------------

type chatReply struct {
	reply, summary string
	replan         bool
	answers        []any
}

func validateChat(d map[string]any, plan map[string]any) (chatReply, error) {
	if len(d) == 0 {
		return chatReply{}, fmt.Errorf("no output")
	}
	if e := orStr(d["_error"]); e != "" {
		return chatReply{}, fmt.Errorf("%s", e)
	}
	reply := strings.TrimSpace(strings.ReplaceAll(orStr(d["reply"]), "\x00", ""))
	if reply == "" {
		return chatReply{}, fmt.Errorf("empty reply")
	}
	qtext := map[string]string{}
	for _, q := range asList(plan["questions"]) {
		qtext[orStr(asMap(q)["id"])] = orStr(asMap(q)["text"])
	}
	var answers []any
	for i, x := range asList(d["answers"]) {
		if i == 5 {
			break
		}
		m := asMap(x)
		a, _ := m["answer"].(string)
		if t, ok := qtext[orStr(m["id"])]; ok && strings.TrimSpace(a) != "" {
			answers = append(answers, map[string]any{"q": t, "a": head(strings.TrimSpace(strings.ReplaceAll(a, "\x00", "")), 2000)})
		}
	}
	return chatReply{reply: head(reply, chatMaxReply), summary: head(strings.TrimSpace(strings.ReplaceAll(orStr(d["summary"]), "\x00", "")), chatMaxSummary),
		replan: d["replan"] == true, answers: answers}, nil
}

func (r *run) chatIngest() {
	out := r.o.Agent.OutDir
	for _, t := range r.rows(`SELECT * FROM task_chat_turns WHERE state='running'`) {
		key := t.s("key")
		base := filepath.Join(out, key+".chat")
		meta := readOut(base + ".meta.json")
		if meta == nil || int64(f64(meta["finished"])) < t.i("started") {
			continue
		}
		cost := f64(meta["cost_usd"])
		var runID any
		if rid := orStr(meta["run_id"]); runIDRe.MatchString(rid) {
			runID = rid
		}
		a := r.one(`SELECT * FROM agent_tasks WHERE key=?`, key)
		var plan map[string]any
		if a != nil {
			json.Unmarshal([]byte(a.s("plan")), &plan)
		}
		d := readOut(base + ".json")
		var res chatReply
		var err error
		me, limit := "", false
		if orStr(meta["skipped"]) != "" {
			err = fmt.Errorf("skipped")
		} else {
			if d == nil || d["_error"] != nil || len(d) == 0 {
				me, limit = r.modelError(meta, "chat")
			}
			if me != "" {
				err = fmt.Errorf("%s", me)
			} else {
				res, err = validateChat(d, plan)
			}
		}
		if err != nil {
			state, line := "failed", ""
			switch {
			case orStr(meta["skipped"]) != "":
				state, line = "skipped", chatPaused
			case limit:
				when := strings.TrimSpace(strings.TrimPrefix(me, agentLimitPrefix))
				line = "Claude usage limit reached"
				if strings.HasPrefix(when, "resets") {
					line += " (" + when + ")"
				}
				line += ". Send the message again after that."
			default:
				why := err.Error()
				if me != "" {
					_, why, _ = strings.Cut(me, ": ")
				}
				line = "The agent could not reply (" + head(why, 120) + "). Send the message again."
			}
			r.must(`UPDATE task_chat_turns SET state=?, finished=?, cost=?, run_id=?, error=? WHERE id=?`, state, r.now(), cost, runID, head(err.Error(), 300), t.i("id"))
			r.chatAdd(key, "system", "", "message", line, t.i("id"))
			r.must(`INSERT INTO task_chat_state (key, spent_usd, updated) VALUES (?,?,?) ON CONFLICT(key) DO
				UPDATE SET spent_usd=COALESCE(spent_usd,0)+excluded.spent_usd, updated=excluded.updated`, key, cost, r.now())
		} else {
			reply, _ := r.scanText(res.reply)
			summary, _ := r.scanText(res.summary)
			kind := "message"
			if len(res.answers) > 0 {
				kind = "answer"
			}
			r.chatAdd(key, "argus", r.actor(), kind, reply, t.i("id"))
			r.must(`UPDATE task_chat_turns SET state='done', finished=?, cost=?, run_id=? WHERE id=?`, r.now(), cost, runID, t.i("id"))
			r.must(`INSERT INTO task_chat_state (key, summary, spent_usd, updated) VALUES (?,?,?,?) ON CONFLICT(key) DO
				UPDATE SET summary=excluded.summary, spent_usd=COALESCE(spent_usd,0)+excluded.spent_usd, updated=excluded.updated`,
				key, summary, cost, r.now())
			if a != nil && (res.replan || len(res.answers) > 0) {
				r.chatReplan(a, t, res, summary)
			}
		}
		if a != nil && cost > 0 {
			r.must(`UPDATE agent_tasks SET spent_usd=COALESCE(spent_usd,0)+? WHERE key=?`, cost, key)
		}
		os.Remove(base + ".json")
		os.Remove(base + ".meta.json")
		os.Remove(filepath.Join(r.o.Agent.JobsDir, key+".chat.json"))
	}
	r.commit()
}

var nlRe = regexp.MustCompile(`\n{3,}`)

// chatReplan: the answers (and, for replan, a digest of the thread) go to agent_inputs; the intake makes the next plan.
func (r *run) chatReplan(a, turn row, res chatReply, summary string) {
	key := a.s("key")
	ins, oldAns, _ := r.agentInputs(key)
	var order []string
	ans := map[string]string{}
	for _, x := range append(oldAns, res.answers...) {
		q, v := orStr(asMap(x)["q"]), orStr(asMap(x)["a"])
		if _, seen := ans[q]; !seen {
			order = append(order, q)
		}
		ans[q] = v
	}
	out := []any{}
	for _, q := range order {
		if ans[q] != "" && len(out) < 20 {
			out = append(out, map[string]any{"q": q, "a": ans[q]})
		}
	}
	if res.replan {
		var users []string
		for _, m := range r.rows(`SELECT body FROM task_chat WHERE key=? AND author_kind='user' AND seq<=? ORDER BY seq DESC LIMIT 10`, key, turn.i("upto_seq")) {
			users = append(users, m.s("body"))
		}
		ins = head(nlRe.ReplaceAllString("From the task chat (summary):\n"+summary+"\n\nLatest messages, newest first:\n"+strings.Join(users, "\n---\n"), "\n\n"), 8000)
	}
	ab, _ := json.Marshal(out)
	by := turn.s("requested_by")
	if by == "" {
		by = "chat"
	}
	r.must(`INSERT OR REPLACE INTO agent_inputs VALUES (?,?,?,?,?,?)`, key, ins, string(ab), 0, by, r.now())
	r.must(`UPDATE agent_tasks SET state='planning', context_hash=NULL, error=NULL, updated=? WHERE key=? AND state NOT IN ('running','approved')`, r.now(), key)
	what := "answers"
	if res.replan {
		what = "instructions and answers"
	}
	r.alog(key, "answers", "Task chat: "+what+" saved; re-planning")
	r.chatAdd(key, "system", "", "message", "The agent plans again with this; the new plan version shows above.", turn.i("id"))
}

// chatStatus is the "chat" block of the agent status (no message text).
func (r *run) chatStatus(tick string) object {
	o := object{{"turns_today", r.turnsSince(r.dayStart(), "")}, {"cap_day", int64(chatTurnsPerDay)}, {"cap_task_day", int64(chatTurnsPerTaskDay)}}
	for _, x := range r.rows(`SELECT state, count(*) n FROM task_chat_turns WHERE state IN ('queued','running') GROUP BY state`) {
		o = append(o, kv{x.s("state"), x.i("n")})
	}
	return append(o, kv{"tick", tick})
}
