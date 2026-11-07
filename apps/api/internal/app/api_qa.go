package app

// The Work section's Conversations (/qa) and Ask Argus (/board/ask) as JSON for the Next.js pages (ADR-0018). Same
// queries and states as views_qa.go; the page's chain, badges and summaries come from its helpers.

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiChainOf(items []string) [][]apiLink {
	out := [][]apiLink{}
	for _, x := range items {
		out = append(out, apiSegs(x))
	}
	return out
}

// apiQaMsg is one message, quoted as received; "kind" says what a message without text was.
func apiQaMsg(m Row) map[string]any {
	body := qaShownBody(m.Get("body"))
	out := map[string]any{"id": m.I("id"), "at": py.I(m.Get("ts")), "sender": qaSenderName(m), "mxid": m.S("sender"), "text": body, "edited": m.T("edit_of"),
		"long": py.Len(body) > qaClamp || strings.Count(body, "\n") >= 2, "reconstructed": m.T("reconstructed"), "reason": m.S("reason"),
		"disp": apiAgentBadge(qaDispBadge(m.Get("disposition"), m.Get("incident_id"))), "room": py.OrS(m.Get("room_name"), MATRIX_QA_ROOM_LABEL)}
	if py.Strip(body) == "" {
		kind := strings.ReplaceAll(m.S("msgtype"), "m.", "")
		out["noText"] = "(" + py.OrS(kind, "empty") + ", no text)"
	}
	if l := apiSafeHref(QAPermalink(m.Get("room_id"), m.Get("event_id"))); l != "" {
		out["link"] = l
	}
	return out
}

func apiQaTabs(cur string, nIssues, nMsgs any) []map[string]any {
	return []map[string]any{{"label": "Issues", "href": "/qa", "current": cur == "issues", "count": nIssues},
		{"label": "All messages", "href": "/qa?view=messages", "current": cur == "messages", "count": nMsgs}}
}

// apiQaJSON is GET /api/v1/qa: the issues, or every message with view=messages (or an old ?disp= link).
func apiQaJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/qa"}
	view := qs.Get("view")
	if view == "messages" || (view != "issues" && (qs.Has("disp") || qs.Has("sender") || qs.Has("has"))) {
		return apiQaMessages(qs), nil
	}
	return apiQaIssues(c, qs), nil
}

func apiQaMessages(qs py.QS) map[string]any {
	if !qaReady() {
		return map[string]any{"view": "messages", "ready": false, "tabs": apiQaTabs("messages", nil, nil), "status": []string{}, "empty": "No " + MATRIX_QA_ROOM_LABEL + " message recorded yet. They appear after the next chat-triage run."}
	}
	today := qaToday()
	l := newListQ("/qa", qs, []string{"view", "q", "disp", "sender", "has", "from", "to"}, map[string]string{}, func(k, v string) bool {
		switch k {
		case "view":
			return v == "messages"
		case "disp":
			_, ok := qaDispSQL[v]
			return ok
		case "sender":
			return len(v) <= 255 && !strings.ContainsAny(v, "<>\"\x00\n")
		case "has":
			return v == "yes" || v == "no"
		case "from", "to":
			return v == "today" || qaDateRE.MatchString(v)
		}
		return false
	})
	l.tabs = []string{"view"}
	l.v["view"] = "messages"
	day := func(k string) string {
		if l.get(k) == "today" {
			return today
		}
		return l.get(k)
	}
	frm, to := day("from"), day("to")
	where := func(skip string) (string, []any) {
		w, args := []string{"source='qa'"}, []any{}
		if skip != "disp" && l.get("disp") != "" {
			w = append(w, qaDispSQL[l.get("disp")])
		}
		if skip != "sender" && l.get("sender") != "" {
			w, args = append(w, "sender=?"), append(args, l.get("sender"))
		}
		if skip != "has" && l.get("has") != "" {
			w = append(w, If(l.get("has") == "yes", "incident_id IS NOT NULL", "incident_id IS NULL"))
		}
		if v, ok := qaDayStart(frm, false); frm != "" && ok {
			w, args = append(w, "ts >= ?"), append(args, v)
		}
		if v, ok := qaDayStart(to, true); to != "" && ok {
			w, args = append(w, "ts < ?"), append(args, v)
		}
		cs, ca := queueLike(l.words(), []string{"body", "sender_name", "sender"})
		return strings.Join(append(w, cs...), " AND "), append(args, ca...)
	}
	w, args := where("")
	matched := 0
	if r := Q("SELECT count(*) n FROM chat_messages WHERE "+w, args...); len(r) > 0 {
		matched = int(r[0].I("n"))
	}
	from, _ := listPage(&l, matched)
	ch := qaNewChain()
	rows := []map[string]any{}
	for _, m := range Q("SELECT * FROM chat_messages WHERE "+w+" ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?", append(args, l.per(), from)...) {
		x := apiQaMsg(m)
		x["chain"] = apiChainOf(qaChainItems(m, ch, true))
		rows = append(rows, x)
	}
	t0, _ := qaDayStart(today, false)
	cnt := map[string]int64{}
	for _, r := range Q("SELECT disposition, count(*) n FROM chat_messages WHERE source='qa' AND ts >= ? GROUP BY disposition", t0) {
		cnt[py.OrS(r.Get("disposition"), "unclassified")] = r.I("n")
	}
	var nToday int64
	for _, v := range cnt {
		nToday += v
	}
	known, unr := "already open", "images, files"
	if cnt["duplicate"] != 0 {
		known = I2S(cnt["duplicate"]) + " duplicate"
	}
	if cnt["unclassified"] != 0 {
		unr = I2S(cnt["unclassified"]) + " unclassified"
	}
	tile := func(lbl string, v int64, s, k string, hot bool) apiTile {
		return apiTile{Label: lbl, Figure: I2S(v), Sub: s, Href: "/qa?view=messages&disp=" + k + "&from=today&to=today", Hot: hot}
	}
	tiles := []apiTile{tile("Problem", cnt["problem"], "became an incident or joined one", "problem", cnt["problem"] > 0),
		tile("Known incident", cnt["known"]+cnt["duplicate"], known, "known", false), tile("Not a problem", cnt["not_a_problem"], "chat, thanks, tests", "not_a_problem", false),
		tile("Suspicious", cnt["suspicious"], "tried to steer the agent", "suspicious", false), tile("Unread", cnt["unreadable"]+cnt["unclassified"], unr, "unread", false)}
	status := []string{I2S(nToday) + " message" + If(nToday == 1, "", "s") + " today", I2S(cnt["problem"]) + " problem" + If(cnt["problem"] == 1, "", "s")}
	if last := LatestRun("chat-triage"); last != nil {
		status = append(status, "last triage "+Ago(last.Get("started_at")))
	} else {
		status = append(status, "no triage run yet")
	}
	if n := Q("SELECT max(ts) t FROM chat_messages WHERE source='qa'"); len(n) > 0 && n[0].T("t") {
		status = append(status, "newest message "+Ago(n[0].Get("t")))
	} else {
		status = append(status, "no message yet")
	}
	dw, dargs := where("disp")
	dispN := map[string]int{}
	for _, r := range Q("SELECT disposition d, count(*) n FROM chat_messages WHERE "+dw+" GROUP BY d", dargs...) {
		dispN[qaDispOf(r.Get("d"))] += int(r.I("n"))
	}
	sw, sargs := where("sender")
	var senders [][2]string
	sendN := map[string]int{}
	for _, r := range Q("SELECT sender, max(sender_name) name, count(*) n FROM chat_messages WHERE "+sw+" GROUP BY sender ORDER BY n DESC LIMIT 12", sargs...) {
		senders = append(senders, [2]string{r.Str("sender"), py.OrS(py.Or(r.Get("name"), r.Get("sender")), "")})
		sendN[r.Str("sender")] = int(r.I("n"))
	}
	if cur := l.get("sender"); cur != "" && sendN[cur] == 0 {
		senders = append(senders, [2]string{cur, cur})
	}
	hw, hargs := where("has")
	hasN := map[string]int{}
	for _, r := range Q("SELECT CASE WHEN incident_id IS NULL THEN 'no' ELSE 'yes' END h, count(*) n FROM chat_messages WHERE "+hw+" GROUP BY h", hargs...) {
		hasN[r.Str("h")] = int(r.I("n"))
	}
	nQA := 0
	if r := Q("SELECT count(*) n FROM chat_messages WHERE source='qa'"); len(r) > 0 {
		nQA = int(r[0].I("n"))
	}
	list := apiListOf(l, matched, nQA, "messages", nil)
	list["hidden"] = map[string]string{"view": "messages"}
	return map[string]any{"view": "messages", "ready": true, "tabs": apiQaTabs("messages", nil, int64(nQA)), "tiles": tiles, "status": status, "rows": rows, "list": list,
		"facets": []apiFacet{apiFacetOf("disp", "Disposition", l.get("disp"), qaFilterDisp[1:], dispN), apiFacetOf("sender", "Sender", l.get("sender"), senders, sendN),
			apiFacetOf("has", "Incident", l.get("has"), [][2]string{{"yes", "Linked to an incident"}, {"no", "No incident"}}, hasN)},
		"dates": map[string]string{"from": frm, "to": to}, "room": MATRIX_QA_ROOM_LABEL}
}

func apiProbTag(p Row) any {
	if p == nil {
		return nil
	}
	return map[string]any{"id": p.I("id"), "label": probPID(p.I("id")), "title": probWord[p.S("status")] + ": " + p.S("title")}
}

// apiQaAsk is qaQuestionHTML as data: what Argus asked the reporter and whether it was answered.
func apiQaAsk(i Row, state string) any {
	st := i.S("ask_status")
	if st == "" || st == "before" || (st == "skipped" && i.Get("actionability") != "needs_info") {
		return nil
	}
	if st == "skipped" {
		if state != "resolved" {
			return map[string]any{"note": "Not asked: " + i.S("ask_note")}
		}
		return nil
	}
	out := map[string]any{"head": map[string]string{"sent": "Argus asked " + Ago(i.Get("asked_at")) + " in " + MATRIX_QA_ROOM_LABEL, "dry_run": "Dry run: this question was not sent",
		"off": "Not sent: asking is off in Settings", "failed": "Not sent: Matrix send failed"}[st], "text": i.S("question_text")}
	if st == "sent" {
		if l := apiSafeHref(QAPermalink(i.Get("room_id"), i.Get("question_event"))); l != "" {
			out["link"] = l
		}
	}
	switch {
	case i.T("answered_at"):
		out["answered"] = "Answered " + Ago(i.Get("answered_at"))
	case state == "no_answer":
		out["tail"] = "No answer in 24 h; no further pings"
	case st == "sent":
		out["tail"] = "Waiting for the reporter"
	}
	return out
}

func apiQaIssueRow(i, inc Row, state string, msgs []Row, ch *qaChain, prob Row) map[string]any {
	span := When(i.Get("first_ts"))
	if i.T("last_ts") && When(i.Get("last_ts")) != span {
		span += " – " + When(i.Get("last_ts"))
	}
	who := qaReporters(msgs)
	extra := ""
	if len(who) > 3 {
		extra = " +" + Itoa(len(who)-3)
		who = who[:3]
	}
	missing := []string{}
	if state == "needs_info" || state == "no_answer" {
		if v, err := py.ParseJSON([]byte(py.OrS(i.Get("missing"), "[]"))); err == nil {
			switch x := v.(type) {
			case []any:
				for _, s := range x {
					missing = append(missing, strings.ReplaceAll(py.Str(s), "_", " "))
				}
			case *py.OMap:
				for _, k := range x.Keys() {
					missing = append(missing, strings.ReplaceAll(k, "_", " "))
				}
			}
		}
	}
	shown := msgs
	if len(shown) > 60 {
		shown = shown[:60]
	}
	mm := []map[string]any{}
	for _, m := range shown {
		x := apiQaMsg(m)
		x["disp"] = apiAgentBadge(qaDispBadge(m.Get("disposition"), nil))
		mm = append(mm, x)
	}
	m := Row{"run_id": i.Get("last_run_id"), "problem_idx": i.Get("problem_idx"), "task": "chat-triage", "incident_id": i.Get("incident_id"), "event_id": nil}
	chain := qaChainItems(m, ch, false)
	seen := map[int64]bool{}
	for _, x := range msgs {
		if x.T("event_id") {
			if ts := ch.matrixRequest(x.Get("event_id")); len(ts) > 0 && !seen[ts[0].I("id")] {
				seen[ts[0].I("id")] = true
				chain = append(chain, qaTaskLink(ts[0], "matrix-request"))
			}
		}
	}
	return map[string]any{"id": i.I("id"), "title": py.OrS(i.Get("title"), "(untitled)"), "state": apiAgentBadge(qaStateBadge(state, i.Get("incident_id"))), "env": i.S("environment"),
		"severity": Text(IncidentsSevText(i.Get("severity"))), "confirmed": i.T("confirmed"), "suspicious": i.T("suspicious"), "reconstructed": i.T("reconstructed"),
		"who": strings.Join(who, ", ") + extra, "span": span, "missing": missing, "lazy": apiLazy(qaIssueLazy(i, inc)), "chain": apiChainOf(chain), "ask": apiQaAsk(i, state),
		"messages": mm, "count": len(msgs), "older": max(0, len(msgs)-60), "problem": apiProbTag(prob)}
}

func apiQaIssues(c *Ctx, qs py.QS) map[string]any {
	if !qaIssuesReady() {
		return map[string]any{"view": "issues", "ready": false, "tabs": apiQaTabs("issues", nil, nil), "status": []string{},
			"empty": "Issues appear after the next chat-triage run (every 30 min), or after the backfill on " + ARGUS_HOSTNAME + ". Every message is under All messages."}
	}
	now := Now()
	since := int64(now - qaWindowDays*86400)
	l := newListQ("/qa", qs, []string{"q", "state", "env"}, map[string]string{"state": "open"}, func(k, v string) bool {
		if k == "state" {
			for _, f := range qaStateFilters {
				if f[0] == v {
					return true
				}
			}
			return false
		}
		return len(v) <= 40 && proposalLabelRe.MatchString(v)
	})
	cur := l.get("state")
	ch := qaNewChain()
	var allx []qaIssue
	for _, i := range Q("SELECT * FROM qa_issues WHERE COALESCE(last_ts, created_at) >= ? ORDER BY COALESCE(last_ts, created_at) DESC, id DESC LIMIT 500", since) {
		var inc Row
		if i.T("incident_id") {
			inc = ch.incident(i.I("incident_id"))
		}
		allx = append(allx, qaIssue{i, inc, qaIssueState(i, inc, now)})
	}
	words := l.words()
	searched := func(x qaIssue) bool {
		return listMatch(words, x.i.S("title"), x.i.S("reporter_name"), x.i.S("reporter"), "QA-"+I2S(x.i.I("id")), x.i.S("environment"))
	}
	pick := func(f, s string) bool {
		switch f {
		case "open":
			return s != "resolved"
		case "actionable":
			return s == "actionable"
		case "needs_info":
			return s == "needs_info" || s == "no_answer"
		case "resolved":
			return s == "resolved"
		}
		return true
	}
	counts, stateN, envN := map[string]int{}, map[string]int{}, map[string]int{}
	envOK := func(x qaIssue) bool { return l.get("env") == "" || x.i.S("environment") == l.get("env") }
	var shown []qaIssue
	for _, x := range allx {
		for _, f := range qaStateFilters {
			if pick(f[0], x.st) {
				counts[f[0]]++
				if searched(x) && envOK(x) {
					stateN[f[0]]++
				}
			}
		}
		if searched(x) && pick(cur, x.st) {
			envN[x.i.S("environment")]++
			if envOK(x) {
				shown = append(shown, x)
			}
		}
	}
	order := map[string]int{"actionable": 0, "needs_info": 1, "no_answer": 2, "resolved": 3}
	sort.SliceStable(shown, func(a, b int) bool {
		oa, ok := order[shown[a].st]
		if !ok {
			oa = 9
		}
		ob, ok := order[shown[b].st]
		if !ok {
			ob = 9
		}
		if oa != ob {
			return oa < ob
		}
		ta := py.Num(py.Or(py.Or(shown[a].i.Get("last_ts"), shown[a].i.Get("created_at")), int64(0)))
		tb := py.Num(py.Or(py.Or(shown[b].i.Get("last_ts"), shown[b].i.Get("created_at")), int64(0)))
		return ta > tb
	})
	today, _ := qaDayStart(qaToday(), false)
	actToday := 0
	for _, x := range allx {
		if x.st == "actionable" && py.Num(py.Or(x.i.Get("actionable_at"), int64(0))) >= float64(today) {
			actToday++
		}
	}
	notIssues := Q("SELECT * FROM chat_messages WHERE "+qaNotIssueSQL+" AND ts >= ? ORDER BY ts DESC, id DESC LIMIT 200", since)
	notToday := 0
	kinds := py.NewOMap()
	for _, m := range notIssues {
		if py.Num(py.Or(m.Get("ts"), int64(0))) >= float64(today) {
			notToday++
		}
		d, _ := m.Get("disposition").(string)
		lk, ok := qaDisp[d]
		if !ok {
			lk = [2]string{"Unclassified", ""}
		}
		k := strings.ToLower(lk[0])
		kinds.Set(k, py.I(kinds.GetD(k, int64(0)))+1)
	}
	cfg := QAAskSettings(ReadJSON(filepath.Join(SET_IN, "settings.json"), py.NewOMap()))
	ask := "on"
	if !py.Truthy(cfg.Get("enabled")) {
		ask = "off"
	} else if py.Truthy(cfg.Get("dry_run")) {
		ask = "dry run"
	}
	nNeed := counts["needs_info"]
	status := []string{Itoa(counts["open"]) + " open issue" + If(counts["open"] == 1, "", "s"), Itoa(nNeed) + " need" + If(nNeed == 1, "s", "") + " info"}
	if last := LatestRun("chat-triage"); last != nil {
		status = append(status, "last triage "+Ago(last.Get("started_at")))
	} else {
		status = append(status, "no triage run yet")
	}
	status = append(status, "questions "+ask)
	tiles := []apiTile{{Label: "Open issues", Figure: Itoa(counts["open"]), Sub: "actionable or waiting for detail", Href: "/qa?state=open", Hot: counts["open"] > 0},
		{Label: "Needs info", Figure: Itoa(nNeed), Sub: "Argus asked the reporter", Href: "/qa?state=needs_info"},
		{Label: "Actionable", Figure: Itoa(counts["actionable"]), Sub: Itoa(actToday) + " new today", Href: "/qa?state=actionable"},
		{Label: "Not issues today", Figure: Itoa(notToday), Sub: "greetings, chat, thanks", Href: "/qa?view=messages&from=today&to=today"}}
	state := apiFacet{Key: "state", Label: "State", Value: cur, Options: []apiFacetOpt{}}
	for _, f := range qaStateFilters {
		state.Options = append(state.Options, apiFacetOpt{f[0], f[1], int64(stateN[f[0]])})
	}
	var envs [][2]string
	for e := range envN {
		if e != "" {
			envs = append(envs, [2]string{e, e})
		}
	}
	sort.Slice(envs, func(a, b int) bool { return envs[a][0] < envs[b][0] })
	noun := map[string]string{"open": "open issues", "actionable": "actionable issues", "needs_info": "issues needing info", "resolved": "resolved issues", "all": "issues"}[cur]
	var qmap, imap map[string]Row
	if ProblemsOn() {
		qmap, imap = probPrimaryMap("qa_issue"), probPrimaryMap("incident")
	}
	from, to := listPage(&l, len(shown))
	rows := []map[string]any{}
	for _, x := range shown[from:to] {
		var prob Row
		if qmap != nil {
			prob = qmap[I2S(x.i.I("id"))]
			if prob == nil && x.i.T("incident_id") {
				prob = imap[I2S(x.i.I("incident_id"))]
			}
		}
		rows = append(rows, apiQaIssueRow(x.i, x.inc, x.st, Q("SELECT * FROM chat_messages WHERE issue_id=? ORDER BY ts, id", x.i.I("id")), ch, prob))
	}
	empty := map[string]string{"open": "No open QA issue. Reports that are problems appear here after each chat-triage run.", "actionable": "No actionable QA issue.",
		"needs_info": "No QA issue is waiting for detail.", "resolved": "No resolved QA issue in the last 30 days.", "all": "No QA issue in the last 30 days."}[cur]
	var nm any
	if n := Q("SELECT count(*) n FROM chat_messages WHERE source='qa'"); len(n) > 0 {
		nm = n[0].Get("n")
	}
	out := map[string]any{"view": "issues", "ready": true, "tabs": apiQaTabs("issues", counts["open"], nm), "tiles": tiles, "status": status, "rows": rows,
		"list": apiListOf(l, len(shown), counts[cur], noun, nil), "facets": []apiFacet{state, apiFacetOf("env", "Environment", l.get("env"), envs, envN)}, "empty": empty, "room": MATRIX_QA_ROOM_LABEL, "bulk": nil}
	if len(notIssues) > 0 {
		keys := append([]string(nil), kinds.Keys()...)
		sort.SliceStable(keys, func(a, b int) bool { return py.I(kinds.Get(keys[a])) > py.I(kinds.Get(keys[b])) })
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, I2S(py.I(kinds.Get(k)))+" "+k)
		}
		ni := []map[string]any{}
		for k, m := range notIssues {
			if k >= 15 {
				break
			}
			x := apiQaMsg(m)
			ni = append(ni, x)
		}
		out["notIssues"] = map[string]any{"count": len(notIssues), "note": strings.Join(parts, ", "), "rows": ni}
	}
	if ProblemBulkOn(c) {
		ps := []apiOption{}
		for _, r := range probOpenRows("", 50) {
			ps = append(ps, apiOption{I2S(r.I("id")), "P-" + I2S(r.I("id")) + " · " + FirstLine(r.S("title"), 60)})
		}
		out["bulk"] = map[string]any{"problems": ps, "back": probBack(c, qs)}
	}
	return out
}

// apiBoardAskJSON is GET /api/v1/board/ask: one thread with ?key=new-<id>, else the recent threads and a new composer.
// Viewers read; operators and admins post. The chat is built before the banner, so a send never shows a pause link.
func apiBoardAskJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: boardPathOf("/board/ask", qs)}
	out := map[string]any{"on": AgentChatOn(), "return": boardPathOf("/board/ask", qs), "chat": nil, "threads": []map[string]any{}, "thread": false}
	if !AgentChatOn() {
		out["notices"] = apiNotices(BoardRequestBanner(c, qs))
		out["refresh"] = apiRefresh(c)
		return out, nil
	}
	if key := qs.Get("key"); key != "" {
		if !agentAskKeyRe.MatchString(key) {
			return nil, &apiErr{404, "no such thread"}
		}
		out["thread"], out["chat"] = true, apiChat(c, key, sess)
	} else {
		out["chat"] = apiChat(c, "new", sess)
		threads := []map[string]any{}
		for _, r := range BoardBQ(`SELECT c.key, max(c.created) last, count(*) n, (SELECT author FROM task_chat f WHERE f.key=c.key AND f.seq=1) who,
			(SELECT body FROM task_chat f WHERE f.key=c.key AND f.seq=1) first FROM task_chat c WHERE c.key LIKE 'new-%' GROUP BY c.key ORDER BY last DESC LIMIT 30`) {
			k := r.Str("key")
			if !agentAskKeyRe.MatchString(k) {
				continue
			}
			first := r.Str("first")
			if py.Len(first) > 120 {
				first = string([]rune(first)[:120]) + "…"
			}
			threads = append(threads, map[string]any{"href": "/board/ask?key=" + k, "first": first, "who": py.OrS(r.Get("who"), "someone"), "count": py.I(r.Get("n")), "at": py.I(r.Get("last"))})
		}
		out["threads"] = threads
	}
	out["notices"] = apiNotices(BoardRequestBanner(c, qs))
	out["refresh"] = apiRefresh(c)
	return out, nil
}
