package app

// Port of app/views_qa.py: QA issues and messages (/qa), the incident Source block and source filter, and the
// run detail page (/runs/view). Every message body is untrusted chat text: escaped, dir="auto", never markdown.

import (
	"crypto/sha256"
	"encoding/hex"

	"regexp"

	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

var qaDisp = map[string][2]string{"problem": {"Problem", "fail"}, "known": {"Known incident", "warn"},
	"duplicate": {"Duplicate", "warn"}, "not_a_problem": {"Not a problem", "ok"}, "suspicious": {"Suspicious", "warn"},
	"unreadable": {"Unread", ""}, "unclassified": {"Unclassified", ""}, "answer": {"Answer", ""}, "alert": {"Alert", ""}}

var qaFilterDisp = [][2]string{{"", "All"}, {"problem", "Problem"}, {"known", "Known incident or duplicate"},
	{"not_a_problem", "Not a problem"}, {"suspicious", "Suspicious"}, {"unread", "Unread or unclassified"}}

// qaDispOf is the Disposition facet a stored disposition counts under (the same groups as qaDispSQL).
func qaDispOf(d any) string {
	switch v, _ := d.(string); v {
	case "problem", "not_a_problem", "suspicious":
		return v
	case "known", "duplicate":
		return "known"
	case "unreadable", "unclassified", "":
		return "unread"
	}
	return ""
}

var qaDispSQL = map[string]string{"problem": "disposition='problem'", "known": "disposition IN ('known','duplicate')",
	"not_a_problem": "disposition='not_a_problem'", "suspicious": "disposition='suspicious'",
	"unread": "(disposition IN ('unreadable','unclassified') OR disposition IS NULL)"}

// QASourceLabels is SOURCE_LABELS; QASources is its key order.
var QASourceLabels = map[string]string{"qa": MATRIX_QA_ROOM_LABEL, "kuma": "Kuma", "alertmanager": "Alertmanager", "cicd": "CI/CD",
	"matrix": "Matrix request", "security": "Security", "auto": "Argus auto"}
var QASources = []string{"qa", "kuma", "alertmanager", "cicd", "matrix", "security", "auto"}

// Python's `$` also matches before a final "\n": the trailing \n? keeps that.
var (
	qaDateRE  = regexp.MustCompile(`^\p{Nd}{4}-\p{Nd}{2}-\p{Nd}{2}\n?$`)
	qaEventRE = regexp.MustCompile(`^\$[A-Za-z0-9_\-+/=.:]{1,250}\n?$`)
	qaRoomRE  = regexp.MustCompile(`^![A-Za-z0-9_\-+/=.]{1,200}:[A-Za-z0-9.\-]{1,200}(:[0-9]{1,5})?\n?$`)
)

const qaClamp = 140

func qaReady() bool {
	return len(Q("SELECT 1 FROM sqlite_master WHERE type='table' AND name='chat_messages'")) > 0
}

// QAPermalink is permalink(room_id, event_id).
func QAPermalink(room, event any) string {
	r, e := py.OrS(room, ""), py.OrS(event, "")
	if !qaRoomRE.MatchString(r) || !qaEventRE.MatchString(e) {
		return ""
	}
	q := func(s string) string { return py.Quote(s, "!$:@.-_~=") }
	_, via, _ := strings.Cut(r, ":")
	return "https://matrix.to/#/" + q(r) + "/" + q(e) + "?via=" + q(via)
}

func qaDispBadge(d any, incidentID any) string {
	ds, _ := d.(string)
	lk, ok := qaDisp[ds]
	if !ok {
		lk = [2]string{"Unclassified", ""}
	}
	label, kind := lk[0], lk[1]
	if ds == "problem" && py.Truthy(incidentID) {
		label = "Problem → incident #" + I2S(py.I(incidentID))
	} else if (ds == "known" || ds == "duplicate") && py.Truthy(incidentID) {
		label = label + " #" + I2S(py.I(incidentID))
	}
	return `<span class="badge` + If(kind != "", " "+kind, "") + `">` + E(label) + `</span>`
}

// qaShownBody is shown_body(body).
func qaShownBody(v any) string {
	body := py.OrS(v, "")
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "> ") {
		i := 0
		for i < len(lines) && strings.HasPrefix(lines[i], ">") {
			i++
		}
		if rest := py.Strip(strings.Join(lines[i:], "\n")); rest != "" {
			return rest
		}
	}
	return strings.Trim(body, "\n")
}

func qaSenderName(m Row) string {
	return py.OrS(py.Or(m.Get("sender_name"), m.Get("sender")), "?")
}

func qaSenderCellInline(m Row) string {
	return `<b dir="auto">` + E(qaSenderName(m)) + `</b> <span class="mono dim small qmxid" title="` + E(m.S("sender")) + `">` +
		E(m.S("sender")) + `</span>`
}

// --- the chain -----------------------------------------------------------------------------------------

type qaChain struct {
	inc   map[int64]Row
	rca   map[int64][]Row
	tasks map[int64][]Row
	fix   map[int64][]Row
	mx    map[string][]Row
	ho    map[[2]int64]string
}

func qaNewChain() *qaChain {
	return &qaChain{inc: map[int64]Row{}, rca: map[int64][]Row{}, tasks: map[int64][]Row{}, fix: map[int64][]Row{},
		mx: map[string][]Row{}, ho: map[[2]int64]string{}}
}

func (ch *qaChain) incident(iid int64) Row {
	if r, ok := ch.inc[iid]; ok {
		return r
	}
	r := Q("SELECT * FROM incidents WHERE id=?", iid)
	var x Row
	if len(r) > 0 {
		x = r[0]
	}
	ch.inc[iid] = x
	return x
}

func (ch *qaChain) rcas(iid int64) []Row {
	if r, ok := ch.rca[iid]; ok {
		return r
	}
	r := Q("SELECT id, title, status FROM rca_reports WHERE incident_id=? ORDER BY created DESC", iid)
	ch.rca[iid] = r
	return r
}

func (ch *qaChain) jira(iid int64) []Row {
	if r, ok := ch.tasks[iid]; ok {
		return r
	}
	r := BoardBQ("SELECT id, jira_key, labels, title FROM tasks WHERE source_kind='incident' AND source_id=? AND gone=0", I2S(iid))
	ch.tasks[iid] = r
	return r
}

func (ch *qaChain) fixes(iid int64) []Row {
	if r, ok := ch.fix[iid]; ok {
		return r
	}
	r := Q("SELECT f.id, f.status, f.outcome FROM fix_runs f JOIN incident_events e ON "+
		"e.run_id=f.problem_run AND e.idx=f.problem_idx WHERE e.incident_id=? AND e.kind='finding'"+
		" GROUP BY f.id ORDER BY f.requested_at DESC", iid)
	ch.fix[iid] = r
	return r
}

func (ch *qaChain) handoff(run, idx int64) string {
	k := [2]int64{run, idx}
	if s, ok := ch.ho[k]; ok {
		return s
	}
	r := Q("SELECT * FROM triage_problems WHERE run_id=? AND idx=?", run, idx)
	s := "missing"
	if len(r) > 0 {
		s = FixHandoffProblem(r[0]).State
	}
	ch.ho[k] = s
	return s
}

func (ch *qaChain) matrixRequest(eventID any) []Row {
	key := py.Str(eventID)
	if r, ok := ch.mx[key]; ok {
		return r
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])[:12]
	r := BoardBQ("SELECT id, jira_key, title FROM tasks WHERE source_kind='matrix' AND source_id=? AND gone=0", h)
	ch.mx[key] = r
	return r
}

func qaTaskLink(t Row, labelKind string) string {
	key := t.S("jira_key")
	href := "/board/task?id=" + I2S(t.I("id"))
	if key != "" {
		href = "/board/task?key=" + py.Quote(key)
	}
	text := key
	if text == "" {
		text = "task " + I2S(t.I("id"))
	}
	return `<a href="` + href + `" title="` + E(labelKind) + `: ` + E(t.S("title")) + `">` + E(text) + `</a>`
}

func qaChainItems(m Row, ch *qaChain, withRun bool) []string {
	var items []string
	if withRun && m.T("run_id") {
		items = append(items, `<a href="/runs/view?id=`+I2S(m.I("run_id"))+`" title="the `+E(py.OrS(m.Get("task"), "triage"))+` run that `+
			`handled it">Run `+I2S(m.I("run_id"))+`</a>`)
	}
	if m.Get("problem_idx") != nil && m.T("run_id") && py.OrS(m.Get("task"), "chat-triage") == "chat-triage" {
		pr := "/incidents/problem?run=" + I2S(m.I("run_id")) + "&amp;idx=" + I2S(m.I("problem_idx"))
		items = append(items, `<a href="`+pr+`">`+FindingWord("Problem")+`</a>`)
		if ch.handoff(m.I("run_id"), m.I("problem_idx")) != "missing" {
			items = append(items, `<a href="`+pr+`#how-to-fix" `+
				`title="Argus's fix options and the prompt for a write-access agent">How to fix</a>`)
		}
	}
	iidV := m.Get("incident_id")
	var inc Row
	if py.Truthy(iidV) {
		inc = ch.incident(py.I(iidV))
	}
	if inc != nil {
		iid := py.I(iidV)
		st := py.OrS(inc.Get("status"), "?")
		items = append(items, `<a href="/incidents/view?id=`+I2S(iid)+`" title="`+E(inc.S("title"))+` (`+E(st)+`)">`+
			`Incident #`+I2S(iid)+`</a> <span class="dim small">`+E(st)+`</span>`)
		if rcas := ch.rcas(iid); len(rcas) > 0 {
			more := ""
			if len(rcas) > 1 {
				more = ` <span class="dim small">+` + Itoa(len(rcas)-1) + `</span>`
			}
			items = append(items, `<a href="/rca/view?id=`+I2S(rcas[0].I("id"))+`" title="`+E(rcas[0].S("title"))+`">RCA</a>`+more)
		}
		for i, t := range ch.jira(iid) {
			if i >= 2 {
				break
			}
			kind := "Jira task"
			if strings.Contains(t.S("labels"), "qa-report") {
				kind = "qa-report"
			}
			items = append(items, qaTaskLink(t, kind))
		}
		if fx := ch.fixes(iid); len(fx) > 0 {
			f0 := fx[0]
			more := ""
			if len(fx) > 1 {
				more = ` <span class="dim small">+` + Itoa(len(fx)-1) + `</span>`
			}
			items = append(items, `<a href="/admin/fix/run?id=`+E(f0.Str("id"))+`" title="fix run `+E(f0.Str("id"))+`: `+
				E(py.OrS(py.Or(f0.Get("outcome"), f0.Get("status")), ""))+`">Fix run</a>`+more)
		}
		var room any = ""
		if inc.Has("thread_room") {
			room = inc.Get("thread_room")
		}
		if link := QAPermalink(room, inc.Get("thread_root")); link != "" {
			items = append(items, `<a href="`+E(link)+`" rel="noopener noreferrer" title="the incident thread in the notify room">`+
				`Matrix thread</a>`)
		}
	} else if py.Truthy(iidV) {
		items = append(items, `<span class="dim">incident #`+I2S(py.I(iidV))+` (gone)</span>`)
	}
	if m.T("event_id") {
		if ts := ch.matrixRequest(m.Get("event_id")); len(ts) > 0 {
			items = append(items, qaTaskLink(ts[0], "matrix-request"))
		}
	}
	return items
}

func qaMetaLine(m Row, room bool) string {
	link := QAPermalink(m.Get("room_id"), m.Get("event_id"))
	bits := []string{`<time>` + When(m.Get("ts")) + `</time>`}
	if room {
		lbl := m.S("room_name")
		if lbl == "" {
			src := m.S("source")
			if l, ok := QASourceLabels[src]; ok {
				lbl = l
			} else {
				lbl = src
			}
		}
		bits = append(bits, E(lbl))
	}
	if m.T("reconstructed") {
		bits = append(bits, `<span class="tag" title="rebuilt from the stored fetch files and run summaries">reconstructed</span>`)
	}
	if link != "" {
		bits = append(bits, `<a href="`+E(link)+`" rel="noopener noreferrer">Open in Matrix</a>`)
	}
	for i := range bits {
		bits[i] = "<span>" + bits[i] + "</span>"
	}
	return strings.Join(bits, `<span class="dot-sep"></span>`)
}

// qaDayStart is _day_start(s, end): local midnight of YYYY-MM-DD (+1 day), ok=false when it does not parse.
func qaDayStart(s string, end bool) (int64, bool) {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return 0, false
	}
	v := t.Unix()
	if end {
		v += 86400
	}
	return v, true
}

func qaToday() string { return time.Unix(NowInt(), 0).In(time.Local).Format("2006-01-02") }

// --- QA issues ----------------------------------------------------------------------------------------

const (
	qaNoAnswerS   = 24 * 3600
	qaWindowDays  = 30
	qaNotIssueSQL = "source='qa' AND issue_id IS NULL AND COALESCE(disposition, '') NOT IN " +
		"('problem', 'known', 'duplicate', 'answer')"
)

var qaStates = map[string][2]string{"actionable": {"Actionable", "fail"}, "needs_info": {"Needs info", "warn"},
	"no_answer": {"Needs info, no answer", "warn"}, "resolved": {"Resolved", "ok"}}
var qaStateFilters = [][2]string{{"open", "Open"}, {"actionable", "Actionable"}, {"needs_info", "Needs info"},
	{"resolved", "Resolved"}, {"all", "All"}}

func qaIssuesReady() bool {
	return qaReady() && len(Q("SELECT 1 FROM sqlite_master WHERE type='table' AND name='qa_issues'")) > 0
}

func qaIssueState(i, inc Row, now float64) string {
	if inc != nil && inc.Get("status") == "resolved" {
		return "resolved"
	}
	if i.Get("actionability") == "actionable" || i.T("incident_id") {
		return "actionable"
	}
	since := py.Num(py.Or(py.Or(py.Or(i.Get("asked_at"), i.Get("last_ts")), i.Get("created_at")), now))
	if !i.T("answered_at") && now-since > qaNoAnswerS {
		return "no_answer"
	}
	return "needs_info"
}

func qaStateBadge(state string, incidentID any) string {
	lk, ok := qaStates[state]
	if !ok {
		lk = [2]string{"Needs info", "warn"}
	}
	label := lk[0]
	if state == "actionable" && py.Truthy(incidentID) {
		label = "Actionable → incident #" + I2S(py.I(incidentID))
	} else if state == "resolved" && py.Truthy(incidentID) {
		label = "Resolved · incident #" + I2S(py.I(incidentID))
	}
	return `<span class="badge ` + lk[1] + `">` + E(label) + `</span>`
}

func qaReporters(msgs []Row) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range msgs {
		k := m.Str("sender")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, py.OrS(py.Or(m.Get("sender_name"), m.Get("sender")), "None"))
	}
	return out
}

// qaIssueLazy is views_qa.issue_lazy(i, inc).
func qaIssueLazy(i, inc Row) (Lazy, bool) {
	if inc != nil {
		return LazyIncident(inc)
	}
	if i.Get("actionability") != "actionable" || i.Get("last_run_id") == nil || i.Get("problem_idx") == nil {
		return Lazy{}, false
	}
	p := Q("SELECT * FROM triage_problems WHERE run_id=? AND idx=?", i.I("last_run_id"), i.I("problem_idx"))
	if len(p) == 0 {
		return Lazy{}, false
	}
	return LazyRow(p[0], "rca")
}

type qaIssue struct {
	i, inc Row
	st     string
}

// --- incident: Source block and the source filter -------------------------------------------------------

// QAIncSources is inc_sources(r).
func QAIncSources(r Row) []string {
	var keys []string
	if r.Has("sources") {
		if s, ok := r.Get("sources").(string); ok && s != "" {
			if v, err := py.ParseJSON([]byte(s)); err == nil {
				var items []string
				switch x := v.(type) {
				case []any:
					for _, k := range x {
						if ks, ok := k.(string); ok {
							items = append(items, ks)
						}
					}
				case *py.OMap:
					items = x.Keys()
				case string:
					for _, ch := range x {
						items = append(items, string(ch))
					}
				}
				for _, k := range items {
					if _, ok := QASourceLabels[k]; ok {
						keys = append(keys, k)
					}
				}
			}
		}
	}
	if len(keys) > 0 {
		return keys
	}
	switch strings.ToUpper(py.OrS(r.Get("dc"), "")) {
	case "CICD":
		return []string{"cicd"}
	case "KUMA":
		return []string{"kuma"}
	}
	if IsObsSite(py.OrS(r.Get("dc"), "")) {
		return []string{"alertmanager"}
	}
	return []string{"qa"}
}

// QASourceTags is source_tags(r).
func QASourceTags(r Row) string {
	var b strings.Builder
	for _, k := range QAIncSources(r) {
		b.WriteString(`<span class="tag src src-` + k + `">` + E(QASourceLabels[k]) + `</span>`)
	}
	return b.String()
}

// QASourceBlock is source_block(r).
func QASourceBlock(r Row) string {
	tags := QASourceTags(r)
	var msgs []Row
	if qaReady() {
		msgs = Q("SELECT * FROM chat_messages WHERE incident_id=? ORDER BY (source='qa') DESC, ts DESC LIMIT 50", r.I("id"))
	}
	if len(msgs) > 0 {
		var items []string
		for _, m := range msgs {
			q := E(qaShownBody(m.Get("body")))
			if q == "" {
				q = `<span class="dim">(no text)</span>`
			}
			items = append(items, `<li><blockquote dir="auto">`+q+
				`</blockquote><div class="qmeta"><span>`+qaSenderCellInline(m)+`</span>`+
				`<span class="dot-sep"></span>`+qaMetaLine(m, true)+`<span class="dot-sep"></span>`+
				`<span>`+qaDispBadge(m.Get("disposition"), nil)+`</span></div></li>`)
		}
		n := len(items)
		if n > 3 {
			n = 3
		}
		first := strings.Join(items[:n], "")
		rest := ""
		if len(items) > 3 {
			rest = Fold(Itoa(len(items)-3)+" more messages", `<ul class="quotes">`+strings.Join(items[3:], "")+`</ul>`, -1, false, "")
		}
		return `<section class="srcblock"><h2>Source <span class="count">` + Itoa(len(msgs)) + `</span></h2>` +
			`<div class="srctags">` + tags + `</div><ul class="quotes">` + first + `</ul>` + rest + `</section>`
	}
	summ := Q("SELECT DISTINCT p.sources FROM incident_events e JOIN triage_problems p ON p.run_id=e.run_id AND "+
		"p.idx=e.idx WHERE e.incident_id=? AND e.kind='finding' LIMIT 10", r.I("id"))
	var lines []string
	seen := map[string]bool{}
	for _, s := range summ {
		v, err := py.ParseJSON([]byte(py.OrS(s.Get("sources"), "[]")))
		if err != nil {
			continue
		}
		var xs []string
		switch x := v.(type) {
		case []any:
			for _, y := range x {
				xs = append(xs, py.Str(y))
			}
		case *py.OMap:
			xs = x.Keys()
		case string:
			for _, ch := range x {
				xs = append(xs, string(ch))
			}
		}
		for _, y := range xs {
			if !seen[y] {
				seen[y] = true
				lines = append(lines, y)
			}
		}
	}
	if len(lines) > 6 {
		lines = lines[:6]
	}
	var inner string
	if len(lines) > 0 {
		var b strings.Builder
		for _, x := range lines {
			b.WriteString(`<li dir="auto">` + E(x) + `</li>`)
		}
		inner = `<p class="section-note">No original message is recorded for this incident (it predates message ` +
			`tracking, or came from an alert). The sources as Argus summarised them:</p><ul class="list">` + b.String() + "</ul>"
	} else {
		var names []string
		for _, k := range QAIncSources(r) {
			names = append(names, QASourceLabels[k])
		}
		inner = `<p class="section-note">No chat message behind this incident: it was opened by ` +
			E(strings.Join(names, " and ")) + `. The timeline lists each finding.</p>`
	}
	return `<section class="srcblock"><h2>Source</h2><div class="srctags">` + tags + `</div>` + inner + `</section>`
}

// --- run detail page ------------------------------------------------------------------------------------
