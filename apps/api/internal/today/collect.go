package today

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// Cand is one candidate item of the plan.
type Cand struct {
	Ref, Kind, Area, Env, Title, Do, Why, Link, LinkLabel string
	Prompt, Close                                         any // string or nil
	Members                                               []string
	Sig                                                   string
	Details                                               []Detail
	Urgency, Impact, Confidence                           float64
	Units                                                 int
	Size                                                  string
}

// Detail is one line under an item.
type Detail struct{ Text, Link string }

var (
	sevU     = map[string]float64{"page": 9, "critical": 9, "high": 7, "medium": 4, "warning": 4, "low": 2, "info": 1}
	envI     = map[string]float64{"release": 3.0, "production": 3.0, "pre-release": 1.5, "staging": 1.5, "develop": 1.0, "dev": 1.0}
	envShort = map[string]string{"release": "prod", "production": "prod", "pre-release": "pre", "staging": "pre", "develop": "dev", "dev": "dev"}
	family   = map[string]string{"http-5xx": "5xx errors", "http-4xx": "4xx errors", "memory": "out of memory", "disk": "disk filling",
		"down": "down", "cpu": "CPU high", "latency": "slow responses", "cve": "vulnerability"}
)

func getF(m map[string]float64, k string, d float64) float64 {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}

type citem struct {
	urgency, impact, confidence float64
	members                     []string
	sig                         string
	details                     []Detail
	prompt, close               any
	units                       int
	size, linkLabel             string
}

func mk(ref, kind, area, env string, title any, do, why string, link string, x citem) Cand {
	if x.confidence == 0 {
		x.confidence = 0.9
	}
	if x.members == nil {
		x.members = []string{ref}
	}
	if x.units == 0 {
		x.units = 1
	}
	if x.linkLabel == "" {
		x.linkLabel = "Open"
	}
	return Cand{Ref: ref, Kind: kind, Area: area, Env: env, Title: words(title, 10), Do: one(do, 140), Why: one(why, 140), Link: link,
		LinkLabel: x.linkLabel, Prompt: x.prompt, Close: x.close, Members: sortedSet(x.members), Sig: x.sig, Details: x.details,
		Urgency: x.urgency, Impact: x.impact, Confidence: x.confidence, Units: x.units, Size: x.size}
}

type cctx struct {
	now       int64
	con       DB
	bcon      DB
	cfg       *Config
	operator  string
	covered   map[string]bool
	diskHosts []string
}

func (x *cctx) hasDiskHost(h string) bool {
	for _, d := range x.diskHosts {
		if d == h {
			return true
		}
	}
	return false
}

func lazyOf(v any) *py.OMap {
	s, ok := v.(string)
	if !ok || s == "" {
		return py.NewOMap()
	}
	d, err := py.ParseJSON([]byte(s))
	if m, ok := d.(*py.OMap); err == nil && ok {
		return m
	}
	return py.NewOMap()
}

func (o *Options) cProblems(ctx context.Context, x *cctx) []Cand {
	var out []Cand
	for _, r := range soft(ctx, x.con, "SELECT * FROM problems WHERE status NOT IN ('resolved','closed') AND merged_into IS NULL") {
		var inc []string
		for _, l := range soft(ctx, x.con, "SELECT ref_id FROM problem_links WHERE problem_id=? AND ref_type='incident' AND state='accepted'", r.Get("id")) {
			inc = append(inc, py.Str(l.At(0)))
		}
		for _, i := range inc {
			x.covered[i] = true
		}
		st := py.Str(r.Get("status"))
		known := (st == "known_error" || st == "fix_in_progress") && (truthy(r.Get("fix_options")) || truthy(r.Get("fix_prompt")))
		sev := getF(sevU, strOr(r.Get("severity")), 4)
		env := strOr(r.Get("env"))
		imp := getF(envI, env, 1.5) + b2f(truthy(r.Get("customer_facing"))) + min(1.0, log2(float64(1+len(inc)))/2)
		lz := lazyOf(r.Get("lazy"))
		id := py.Str(r.Get("id"))
		title := "P-" + id + " " + py.Str(r.Get("title"))
		var do, kind string
		if known {
			do = "Apply the recommended fix with its backup and rollback"
			if truthy(lz.Get("fix")) {
				do += ": " + py.Str(lz.Get("fix"))
			} else {
				do += "."
			}
			kind = "problem_fix"
		} else {
			do = "Read the Lazy card; confirm the root cause or move it to known error."
			kind = "problem_review"
		}
		why := fmt.Sprintf("%s on %s, explains %s", or2(r.Get("severity"), "no severity"), or2(env, "unknown env"), plural(len(inc), "incident"))
		conf := 0.8
		if py.Str(r.Get("root_cause_label")) == "proven" {
			conf = 1.0
		}
		link := "/problems/view?id=" + id
		out = append(out, mk("problem:"+id, kind, "problems", env, title, do, why, link, citem{urgency: sev, impact: imp, confidence: conf,
			sig: sigOf(r.Get("status"), r.Get("severity")), close: link, members: []string{"problem:" + id}}))
	}
	return out
}

func or2(v any, d string) string {
	if truthy(v) {
		return py.Str(v)
	}
	return d
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (o *Options) cIncidents(ctx context.Context, x *cctx) []Cand {
	var out []Cand
	now := x.now
	linked := map[string]bool{}
	for _, r := range soft(ctx, x.con, "SELECT incident_id FROM cicd_findings WHERE status='open' AND incident_id IS NOT NULL") {
		linked[py.Str(r.At(0))] = true
	}
	for _, r := range soft(ctx, x.con, "SELECT * FROM incidents WHERE status IN ('open','acknowledged') ORDER BY id") {
		sid := py.Str(r.Get("id"))
		fam := r.Get("family")
		if py.Str(fam) == "cve" && fam != nil || x.covered[sid] || linked[sid] {
			continue
		}
		subj := strings.ToLower(strOr(r.Get("subject")))
		if py.Str(fam) == "disk" && fam != nil {
			skip := false
			for _, h := range x.diskHosts {
				if subj == strings.ToLower(h) {
					skip = true
				}
			}
			if skip {
				continue
			}
		}
		lz := lazyOf(r.Get("lazy"))
		env := strOr(r.Get("env"))
		sev := strOr(r.Get("severity"))
		u := getF(sevU, sev, 3)
		if ls := r.Get("last_seen"); truthy(ls) && now-i64(ls) < 6*3600 {
			u++
		}
		if py.Str(r.Get("status")) == "acknowledged" {
			u -= 2
		}
		hub1 := isNum(r.Get("hub_tier")) && num(r.Get("hub_tier")) == 1
		imp := getF(envI, env, 1.5) + b2f(truthy(r.Get("customer_facing"))) + b2f(hub1)
		fs := strOr(fam)
		fl, ok := family[fs]
		if !ok {
			fl = fs
		}
		what := py.Str(or(r.Get("subject"), r.Get("title"))) + " " + fl
		var kind, do string
		var conf float64
		if lz.Len() > 0 {
			kind = "incident_review"
			if truthy(lz.Get("fix")) {
				do = "Read the Lazy card; then take the fix: " + py.Str(lz.Get("fix"))
			} else {
				do = "Read the Lazy card; then acknowledge or resolve it."
			}
			conf = 1.0
			if strings.Contains(strOr(lz.Get("why")), "probable") {
				conf = 0.8
			}
		} else {
			kind, do, conf = "incident_triage", "Open it and press Investigate deeply; read the RCA when it lands.", 0.6
		}
		bits := []string{or2(sev, "no severity")}
		if env == "release" {
			bits = append(bits, "production")
		} else {
			bits = append(bits, or2(env, "unknown env"))
		}
		if truthy(r.Get("customer_facing")) {
			bits = append(bits, "customer-facing")
		}
		if hub1 {
			bits = append(bits, strings.TrimSpace("hub "+strOr(r.Get("hub_service"))))
		}
		bits = append(bits, py.Str(or(r.Get("occurrences"), int64(1)))+"x in "+ago(r.Get("first_seen"), now))
		link, label := "/incidents/view?id="+sid, "Open"
		if lz.Len() > 0 {
			link += "#lazy-reading"
			label = "Lazy card"
		}
		var prompt any
		if truthy(r.Get("fix_prompt")) {
			prompt = "/rca/prompt.md?incident=" + sid
		}
		out = append(out, mk("incident:"+sid, kind, "incidents", env, what, do, strings.Join(bits, ", "), link, citem{urgency: u, impact: imp,
			confidence: conf, sig: sigOf(sev, r.Get("status"), lz.Len() > 0), prompt: prompt, close: "/incidents/view?id=" + sid, linkLabel: label}))
	}
	return out
}

func (o *Options) cQA(ctx context.Context, x *cctx) []Cand {
	rows := soft(ctx, x.con, "SELECT id, title FROM qa_issues WHERE actionability='needs_info' AND coalesce(last_ts, created_at, 0) >= ? "+
		"ORDER BY last_ts DESC", x.now-14*86400)
	if len(rows) == 0 {
		return nil
	}
	n := len(rows)
	var mem []string
	var det []Detail
	for i, r := range rows {
		mem = append(mem, "qa:"+py.Str(r.At(0)))
		if i < 5 {
			det = append(det, Detail{one(r.At(1), 90), "/qa?state=needs_info"})
		}
	}
	return []Cand{mk("qa:needs_info", "qa_needs_info", "qa", "", "Chase "+plural(n, "QA report")+" that need info",
		"Open QA reports, Needs info; answer or ping the reporter in the thread.",
		strconv.Itoa(n)+" waiting for the reporter; without the missing facts nobody can act", "/qa?state=needs_info",
		citem{urgency: 3, impact: 1.5, confidence: 0.9, units: n, members: mem, details: det})}
}

func (o *Options) cProposals(ctx context.Context, x *cctx) []Cand {
	now := x.now
	rows := soft(ctx, x.bcon, "SELECT id, title, env, severity, sev_rank, created_at FROM proposals WHERE state='pending' OR "+
		"(state='snoozed' AND coalesce(snooze_until,0) < ?) ORDER BY sev_rank DESC, created_at, id", now)
	if len(rows) == 0 {
		return nil
	}
	bn := int(num(x.cfg.def("proposals_batch", int64(10))))
	batch := rows
	if bn >= 0 && bn < len(rows) {
		batch = rows[:bn]
	}
	n, total := len(batch), len(rows)
	hi, prod := false, false
	for _, r := range batch {
		s := strOr(r.Get("severity"))
		hi = hi || s == "critical" || s == "high"
		prod = prod || py.Str(r.Get("env")) == "release" && r.Get("env") != nil
	}
	oldest := i64(rows[0].Get("created_at"))
	for _, r := range rows {
		oldest = min(oldest, i64(r.Get("created_at")))
	}
	u := 4 + b2f(hi) + b2f(now-oldest > 3*86400)
	more := ""
	if total > n {
		more = fmt.Sprintf("; %d more after these", total-n)
	}
	env, imp := "", 1.5
	if prod {
		env, imp = "release", 2.5
	}
	var mem []string
	var det []Detail
	for i, r := range batch {
		mem = append(mem, "proposal:"+py.Str(r.Get("id")))
		if i < 5 {
			det = append(det, Detail{one(r.Get("title"), 90), "/proposals"})
		}
	}
	return []Cand{mk("proposals", "proposals", "proposals", env, "Decide "+plural(n, "proposed task"),
		fmt.Sprintf("Open Proposed tasks; accept, snooze or reject the top %d.", n),
		fmt.Sprintf("%d waiting, oldest %s; nothing reaches Jira without you%s", total, ago(oldest, now), more), "/proposals",
		citem{urgency: u, impact: imp, confidence: 1.0, units: n, members: mem, details: det})}
}

func taskLink(tid any) string {
	if truthy(tid) {
		return "/board/task?id=" + py.Str(tid)
	}
	return "/board"
}

func (o *Options) cAgent(ctx context.Context, x *cctx) []Cand {
	rows := soft(ctx, x.bcon, "SELECT a.key, a.state, t.id tid, t.title FROM agent_tasks a LEFT JOIN tasks t ON t.jira_key=a.key "+
		"WHERE a.state IN ('waiting','blocked') ORDER BY a.updated")
	if len(rows) == 0 {
		return nil
	}
	n := len(rows)
	var ks []string
	for i, r := range rows {
		if i < 4 {
			ks = append(ks, py.Str(r.Get("key")))
		}
	}
	keys := strings.Join(ks, ", ")
	if n > 4 {
		keys += fmt.Sprintf(" +%d", n-4)
	}
	var mem []string
	var det []Detail
	for _, r := range rows {
		mem = append(mem, "agent:"+py.Str(r.Get("key")))
		det = append(det, Detail{py.Str(r.Get("key")) + " " + one(strOr(r.Get("title")), 80) + " (" + py.Str(r.Get("state")) + ")", taskLink(r.Get("tid"))})
	}
	return []Cand{mk("agent:questions", "agent_questions", "board", "", "Answer the task agent on "+plural(n, "task"),
		"Open each task; answer the questions or approve the plan ("+keys+").", "the agent is blocked until you answer",
		taskLink(rows[0].Get("tid")), citem{urgency: 6, impact: 2, confidence: 1.0, units: n, members: mem, details: det})}
}

func (o *Options) cBoard(ctx context.Context, x *cctx) []Cand {
	if x.operator == "" {
		return nil
	}
	raw := o.Statuses
	if raw == "" {
		raw = "In Progress"
	}
	var st []any
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			st = append(st, s)
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(st)), ",")
	var out []Cand
	args := append([]any{x.operator}, st...)
	for _, r := range soft(ctx, x.bcon, "SELECT id, jira_key, title, priority, env, jira_status, updated_at FROM tasks WHERE gone=0 AND "+
		"status != 'done' AND assignee=? AND jira_status IN ("+marks+") ORDER BY id", args...) {
		pr := strings.ToLower(strOr(r.Get("priority")))
		u := 5 + map[string]float64{"highest": 2, "high": 1, "low": -1, "lowest": -2}[pr]
		why := py.Str(r.Get("jira_status")) + " and assigned to you"
		if truthy(r.Get("priority")) {
			why += ", priority " + py.Str(r.Get("priority"))
		}
		env := strOr(r.Get("env"))
		out = append(out, mk("board:"+py.Str(or(r.Get("jira_key"), r.Get("id"))), "board_task", "board", env,
			strOr(r.Get("jira_key"))+" "+py.Str(r.Get("title")), "Spend one focused session on it; update Jira before you stop.", why,
			"/board/task?id="+py.Str(r.Get("id")), citem{urgency: u, impact: getF(envI, env, 2.0), confidence: 0.8,
				sig: sigOf(r.Get("jira_status"), r.Get("priority"))}))
	}
	return out
}

func firstProposal(rows []Row) string {
	for _, r := range rows {
		s, _ := r.Get("proposals").(string)
		if s == "" {
			s = "[]"
		}
		v, err := py.ParseJSON([]byte(s))
		if err != nil {
			continue
		}
		ps, _ := v.([]any)
		for _, p := range ps {
			t := p
			if m, ok := p.(*py.OMap); ok {
				t = m.Get("text")
			}
			if truthy(t) {
				return one(t, 140)
			}
		}
	}
	return ""
}

func orS(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type group struct {
	key  string
	rows []Row
}

type groups struct {
	order []string
	m     map[string]*group
}

func (g *groups) add(k string, r Row) {
	if g.m == nil {
		g.m = map[string]*group{}
	}
	if g.m[k] == nil {
		g.m[k] = &group{key: k}
		g.order = append(g.order, k)
	}
	g.m[k].rows = append(g.m[k].rows, r)
}

func members(prefix string, rows []Row, col string) []string {
	var out []string
	for _, r := range rows {
		out = append(out, prefix+py.Str(r.Get(col)))
	}
	return out
}

func (o *Options) cCicd(ctx context.Context, x *cctx) []Cand {
	var out []Cand
	now := x.now
	var byProj, stuck, cls groups
	var survey, drift []Row
	key := func(v any) string { return py.Str(v) }
	for _, r := range soft(ctx, x.con, "SELECT * FROM cicd_findings WHERE status='open'") {
		k := py.Str(r.Get("kind"))
		env := py.Str(r.Get("env"))
		kl := r.Get("klass")
		okKl := kl == nil || (py.Str(kl) != "flaky" && py.Str(kl) != "canceled")
		switch {
		case (k == "failure" || k == "deploy") && env == "release" && okKl:
			byProj.add(key(r.Get("project")), r)
		case k == "stuck":
			stuck.add(key(r.Get("project")), r)
		case (k == "failure" || k == "deploy") && (env == "pre-release" || env == "develop") && okKl:
			cls.add(env+"\x00"+orS(strOr(kl), "unknown"), r)
		case k == "survey" && (py.Str(r.Get("severity")) == "critical" || py.Str(r.Get("severity")) == "high"):
			survey = append(survey, r)
		case k == "branch" && env == "release" && py.Str(r.Get("severity")) == "high":
			drift = append(drift, r)
		}
	}
	for _, pk := range byProj.order {
		rs := byProj.m[pk].rows
		sort.SliceStable(rs, func(i, j int) bool { return i64(rs[i].Get("id")) < i64(rs[j].Get("id")) })
		var kl []string
		first := int64(-1)
		for _, r := range rs {
			kl = append(kl, orS(strOr(r.Get("klass")), "unknown"))
			fs := now
			if truthy(r.Get("first_seen")) {
				fs = i64(r.Get("first_seen"))
			}
			if first < 0 || fs < first {
				first = fs
			}
		}
		kl = sortedSet(kl)
		conf := 0.9
		if len(kl) == 1 && kl[0] == "unknown" {
			conf = 0.7
		}
		proj := rs[0].Get("project")
		out = append(out, mk("cicd:release:"+key(proj), "cicd_release", "cicd", "release", "Unblock "+shortRepo(proj)+" release pipeline",
			orS(firstProposal(rs), "Open the finding; fix the cause, then re-run the pipeline yourself."),
			fmt.Sprintf("release blocked: %s (%s) since %s", plural(len(rs), "failing job"), strings.Join(kl, ", "), ago(first, now)),
			"/cicd/finding?id="+py.Str(rs[0].Get("id")), citem{urgency: 8, impact: 3, confidence: conf, members: members("cicd:", rs, "id")}))
	}
	for _, pk := range stuck.order {
		rs := stuck.m[pk].rows
		env := strOr(rs[0].Get("env"))
		proj := rs[0].Get("project")
		u := 5.0
		if env == "release" {
			u = 8
		}
		out = append(out, mk("cicd:stuck:"+key(proj), "cicd_stuck", "cicd", env, "Free the stuck deploy lock of "+shortRepo(proj),
			orS(firstProposal(rs), "Cancel the older pipeline that holds the lock (only while its prod deploy is manual)."),
			"deploys of "+shortRepo(proj)+" wait behind it", "/cicd/finding?id="+py.Str(rs[0].Get("id")),
			citem{urgency: u, impact: getF(envI, env, 1.5), members: members("cicd:", rs, "id")}))
	}
	for _, ck := range cls.order {
		rs := cls.m[ck].rows
		env, kl, _ := strings.Cut(ck, "\x00")
		var repos []string
		for _, r := range rs {
			repos = append(repos, shortRepo(r.Get("project")))
		}
		repos = sortedSet(repos)
		in := strings.Join(repos[:min(3, len(repos))], ", ")
		if len(repos) > 3 {
			in += fmt.Sprintf(" +%d", len(repos)-3)
		}
		u := 2.0
		if env == "pre-release" {
			u = 4
		}
		size := ""
		if len(repos) == 1 {
			size = "S"
		} else if len(repos) > 5 {
			size = "L"
		}
		out = append(out, mk("cicd:class:"+env+":"+kl, "cicd_class", "cicd", env, fmt.Sprintf("Fix %s failures on %s (%s)", kl, env, plural(len(repos), "repo")),
			orS(firstProposal(rs), fmt.Sprintf("Fix the shared %s cause once; start with %s.", kl, repos[0])),
			plural(len(rs), "failing job")+" in "+in, "/cicd/failures", citem{urgency: u,
				impact: getF(envI, env, 1.0) * (1 + min(1.0, log2(float64(len(repos)))/3)), confidence: 0.8, members: members("cicd:", rs, "id"), size: size}))
	}
	var high []Row
	for _, r := range survey {
		if py.Str(r.Get("severity")) == "critical" {
			out = append(out, mk("cicd:survey:"+py.Str(or(r.Get("rule"), r.Get("id"))), "cicd_security", "cicd", orS(strOr(r.Get("env")), "release"),
				words(r.Get("title"), 10), orS(firstProposal([]Row{r}), "Open the finding; apply the proposed change with the operator's OK."),
				"critical CI/CD security finding ("+one(r.Get("project"), 60)+")", "/cicd/finding?id="+py.Str(r.Get("id")),
				citem{urgency: 6, impact: 4, confidence: 0.9, members: []string{"cicd:" + py.Str(r.Get("id"))}}))
		}
	}
	for _, r := range survey {
		if py.Str(r.Get("severity")) == "high" {
			high = append(high, r)
		}
	}
	if len(high) > 0 {
		var det []Detail
		for i, r := range high {
			if i < 5 {
				det = append(det, Detail{one(r.Get("title"), 90), "/cicd/finding?id=" + py.Str(r.Get("id"))})
			}
		}
		out = append(out, mk("cicd:survey:high", "cicd_security", "cicd", "release", "Review "+plural(len(high), "high CI/CD security finding"),
			"Open the audit's security section; pick one finding and propose its fix.", "unprotected branches and variables reach production",
			"/cicd/audit#security", citem{urgency: 4, impact: 3.5, confidence: 0.9, members: members("cicd:", high, "id"), details: det}))
	}
	if len(drift) > 0 {
		out = append(out, mk("cicd:drift:release", "cicd_class", "cicd", "release", "Reconcile "+plural(len(drift), "release branch", "release branches")+" that drifted",
			"Open Branches & variables; merge or reset the drifted release branches.", "release differs from what was tested", "/cicd/audit",
			citem{urgency: 3, impact: 2.5, confidence: 0.9, members: members("cicd:", drift, "id")}))
	}
	nr := soft(ctx, x.con, "SELECT path FROM cicd_readiness r WHERE checked_at = (SELECT max(checked_at) FROM cicd_readiness x "+
		"WHERE x.path=r.path) AND verdict='not_ready' GROUP BY path")
	if len(nr) > 0 {
		var mem []string
		for _, r := range nr {
			mem = append(mem, "ready:"+py.Str(r.At(0)))
		}
		out = append(out, mk("cicd:readiness", "cicd_readiness", "cicd", "", plural(len(nr), "repo")+" not CI/CD ready",
			"Open CI/CD; send the top blocker's fix prompt to one repo's team.", "they cannot deploy through the standard pipeline", "/cicd",
			citem{urgency: 2, impact: 2, confidence: 0.9, members: mem, size: "S"}))
	}
	return out
}

func cveLink(k any) string { return "/vulns?group=" + quote(py.Str(k)) }

// quote is Python's urllib.parse.quote(s, safe=""): everything but letters, digits and _.-~ percent-encoded.
func quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func cvePrompt(fid any) string { return "/rca/prompt.md?cve=" + py.Str(fid) }

func (o *Options) cCVE(ctx context.Context, x *cctx) []Cand {
	var out []Cand
	now := x.now
	rows := soft(ctx, x.con, "SELECT group_key, kind, component_name, env_max_class env, max(kev) kev, min(priority) pr, max(n_targets) t, "+
		"count(*) n, min(sla_due) sla, min(id) fid, max(nofix) nf, min(nofix) mnf FROM cve_findings WHERE state='open' "+
		"AND (priority='P1' OR kev=1) GROUP BY group_key")
	reboot := map[string][]string{}
	for _, e := range soft(ctx, x.con, "SELECT DISTINCT env FROM cve_hosts") {
		var hs []string
		for _, h := range soft(ctx, x.con, "SELECT name FROM cve_hosts WHERE reboot_required=1 AND env=?", e.At(0)) {
			hs = append(hs, py.Str(h.At(0)))
		}
		reboot[py.Str(e.At(0))] = hs
	}
	type rh struct {
		r     Row
		hosts []string
	}
	var packages, deps, nonprod []Row
	var rebootNP []rh
	tOrN := func(r Row) any { return or(r.Get("t"), r.Get("n")) }
	for _, r := range rows {
		prod := py.Str(r.Get("env")) == "production"
		late := truthy(r.Get("sla")) && i64(r.Get("sla")) < now
		lateS := ""
		if late {
			lateS = ", past the SLA"
		}
		if isNum(r.Get("mnf")) && num(r.Get("mnf")) == 1 {
			continue
		}
		kev := truthy(r.Get("kev"))
		kind := py.Str(r.Get("kind"))
		if kind == "kernel" {
			env := "develop"
			if prod {
				env = "release"
			} else if py.Str(r.Get("env")) == "staging" {
				env = "pre-release"
			}
			hosts := reboot[env]
			nh := len(hosts)
			if nh == 0 {
				nh = int(num(or(r.Get("t"), int64(0))))
			}
			if prod {
				why := "P1 kernel CVEs"
				u := 6.0
				if kev {
					why, u = "known-exploited kernel CVEs", 9
				}
				mem := []string{}
				for _, h := range hosts {
					mem = append(mem, "cvehost:"+h)
				}
				if len(mem) == 0 {
					mem = []string{py.Str(r.Get("group_key"))}
				}
				var det []Detail
				if len(hosts) > 0 {
					t := strings.Join(hosts[:min(12, len(hosts))], ", ")
					if len(hosts) > 12 {
						t += fmt.Sprintf(" +%d", len(hosts)-12)
					}
					det = []Detail{{t, cveLink(r.Get("group_key"))}}
				}
				out = append(out, mk("cve:reboot:production", "reboot_wave", "vulns", "release",
					"Plan the production kernel reboot wave ("+plural(nh, "host")+")",
					"Pick a window and the first five hosts; use the reboot prompt, one host at a time.",
					why+" on "+plural(nh, "production host")+lateS, cveLink(r.Get("group_key")),
					citem{urgency: u, impact: 3 + min(2.0, log2(float64(1+nh))/3), confidence: 0.9, prompt: cvePrompt(r.Get("fid")), members: mem, details: det}))
			} else {
				rebootNP = append(rebootNP, rh{r, hosts})
			}
			continue
		}
		if kev {
			k, env, lab, u, imp := "cve_nonprod", "develop", "KEV, non-prod", 4.0, 1.2
			if prod {
				k, env, lab, u, imp = "cve_upgrade", "release", "known exploited", 9, 3.5
			}
			out = append(out, mk("cve:group:"+py.Str(r.Get("group_key")), k, "vulns", env,
				"Upgrade "+py.Str(r.Get("component_name"))+" ("+lab+")", "Read the group's fix card; upgrade one target first, with backup and rollback.",
				"known-exploited, "+plural(int(num(tOrN(r))), "target")+lateS, cveLink(r.Get("group_key")),
				citem{urgency: u, impact: imp, confidence: 0.9, prompt: cvePrompt(r.Get("fid")), members: []string{py.Str(r.Get("group_key"))}}))
			continue
		}
		switch {
		case !prod:
			nonprod = append(nonprod, r)
		case kind == "package" || kind == "os" || kind == "base-image":
			packages = append(packages, r)
		case kind == "npm" || kind == "pypi" || kind == "maven" || kind == "go" || kind == "composer" || kind == "nuget":
			deps = append(deps, r)
		default:
			out = append(out, mk("cve:group:"+py.Str(r.Get("group_key")), "cve_upgrade", "vulns", "release",
				"Upgrade "+py.Str(r.Get("component_name"))+" on production", "Read the fix card; plan the upgrade with backup and rollback.",
				"P1 on "+plural(int(num(tOrN(r))), "target")+lateS, cveLink(r.Get("group_key")),
				citem{urgency: 6 + b2f(late), impact: 3 + min(1.5, log2(1+num(or(r.Get("t"), int64(1))))/3), confidence: 0.85,
					prompt: cvePrompt(r.Get("fid")), members: []string{py.Str(r.Get("group_key"))}}))
		}
	}
	byT := func(rs []Row) []Row {
		ws := append([]Row(nil), rs...)
		sort.SliceStable(ws, func(i, j int) bool { return num(or(ws[i].Get("t"), int64(0))) > num(or(ws[j].Get("t"), int64(0))) })
		return ws
	}
	names := func(ws []Row, n int) string {
		var s []string
		for i, r := range ws {
			if i < n {
				s = append(s, py.Str(r.Get("component_name")))
			}
		}
		return strings.Join(s, ", ")
	}
	if len(packages) > 0 {
		ws := byT(packages)
		tgt := 0
		var det []Detail
		for i, r := range ws {
			tgt += int(num(or(r.Get("t"), int64(0))))
			if i < 6 {
				det = append(det, Detail{py.Str(r.Get("component_name")) + " (" + py.Str(tOrN(r)) + " targets)", cveLink(r.Get("group_key"))})
			}
		}
		out = append(out, mk("cve:wave:packages", "patch_wave", "vulns", "release", "Plan the production package patch wave ("+plural(len(ws), "package")+")",
			"Pick a window; patch "+names(ws, 4)+" first, host group by host group.",
			fmt.Sprintf("P1 fixes available for %s on %d targets", plural(len(ws), "package"), tgt), "/vulns",
			citem{urgency: 6, impact: 3 + min(2.0, log2(float64(1+tgt))/4), confidence: 0.85, prompt: cvePrompt(ws[0].Get("fid")),
				members: members("", ws, "group_key"), details: det}))
	}
	if len(deps) > 0 {
		ws := byT(deps)
		var det []Detail
		for i, r := range ws {
			if i < 6 {
				det = append(det, Detail{py.Str(r.Get("component_name")), cveLink(r.Get("group_key"))})
			}
		}
		out = append(out, mk("cve:wave:deps", "cicd_class", "vulns", "release", "Bump "+plural(len(ws), "vulnerable library", "vulnerable libraries")+" in app repos",
			"Send the fix prompts as MRs to the owning repos; start with "+names(ws, 3)+".",
			"P1 fixes exist; they ship with the next release, no window needed", "/vulns",
			citem{urgency: 4, impact: 3, confidence: 0.85, prompt: cvePrompt(ws[0].Get("fid")), members: members("", ws, "group_key"), size: "L", details: det}))
	}
	if len(rebootNP) > 0 {
		var hs []string
		kev := false
		var gks []string
		for _, x := range rebootNP {
			hs = append(hs, x.hosts...)
			kev = kev || truthy(x.r.Get("kev"))
			gks = append(gks, py.Str(x.r.Get("group_key")))
		}
		hs = sortedSet(hs)
		mem := []string{}
		for _, h := range hs {
			mem = append(mem, "cvehost:"+h)
		}
		if len(mem) == 0 {
			mem = gks
		}
		n := len(hs)
		if n == 0 {
			n = len(rebootNP)
		}
		why, u := "P1 kernel CVEs", 2.0
		if kev {
			why, u = "known-exploited kernel CVEs", 4
		}
		out = append(out, mk("cve:reboot:nonprod", "cve_nonprod", "vulns", "pre-release", "Reboot "+plural(n, "develop/pre-release host")+" on the new kernel",
			"Hand the reboot to the fixer, one host at a time; check each comes back.", why+"; rehearses the production wave",
			cveLink(rebootNP[0].r.Get("group_key")), citem{urgency: u, impact: 1.5, confidence: 0.9, prompt: cvePrompt(rebootNP[0].r.Get("fid")), members: mem}))
	}
	if len(nonprod) > 0 {
		out = append(out, mk("cve:wave:nonprod", "cve_nonprod", "vulns", "develop", "Patch "+plural(len(nonprod), "P1 component")+" on develop/pre-release",
			"Hand the group prompts to the fixer; no production approval needed.", "cheap to fix, keeps the list honest", "/vulns",
			citem{urgency: 2, impact: 1.2, confidence: 0.85, members: members("", nonprod, "group_key")}))
	}
	return out
}

var trailingNum = regexp.MustCompile(`-?\d+$`)

func gb(v any) string {
	if !isNum(v) {
		if s, ok := v.(string); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				v = f
			} else {
				return "?"
			}
		} else {
			return "?"
		}
	}
	f := num(v)
	if f >= 1e9 {
		return py.F(f/1e9, 1) + " GB"
	}
	return py.F(f/1e6, 0) + " MB"
}

func (o *Options) cCapacity(ctx context.Context, x *cctx) []Cand {
	doc := py.ReadJSONMap(filepath.Join(o.StateDir, "capacity.json"))
	var g struct {
		order []string
		m     map[string][]*py.OMap
		key   map[string][3]string
	}
	g.m, g.key = map[string][]*py.OMap{}, map[string][3]string{}
	for _, fv := range doc.List("filesystems") {
		f, ok := fv.(*py.OMap)
		if !ok {
			continue
		}
		d := f.Get("days")
		lv := py.Str(f.Get("level"))
		if !isNum(d) || !(lv == "critical" || lv == "warn" || lv == "high") || f.Get("level") == nil || num(d) > 14 {
			continue
		}
		stem := trailingNum.ReplaceAllString(strOr(f.Get("host")), "")
		k3 := [3]string{strOr(f.Get("env")), stem, strOr(f.Get("mount"))}
		k := strings.Join(k3[:], "\x00")
		if _, ok := g.m[k]; !ok {
			g.order = append(g.order, k)
			g.key[k] = k3
		}
		g.m[k] = append(g.m[k], f)
	}
	var out []Cand
	for _, k := range g.order {
		fs := g.m[k]
		env, stem, mount := g.key[k][0], g.key[k][1], g.key[k][2]
		sort.SliceStable(fs, func(i, j int) bool { return num(fs[i].Get("days")) < num(fs[j].Get("days")) })
		dv := fs[0].Get("days")
		d := num(dv)
		var hosts []string
		var lower []any
		for _, f := range fs {
			hosts = append(hosts, py.Str(f.Get("host")))
			lower = append(lower, strings.ToLower(py.Str(f.Get("host"))))
		}
		x.diskHosts = append(x.diskHosts, hosts...)
		u := 6.0
		if d <= 3 {
			u = 10
		} else if d <= 7 {
			u = 8
		}
		ds := py.G(d, 6)
		title := fmt.Sprintf("Free %s on %s (full in %s d)", mount, hosts[0], ds)
		if len(fs) > 1 {
			title = fmt.Sprintf("Free %s on %s (~%s d)", mount, plural(len(fs), stem+" host"), ds)
		}
		link := "/capacity"
		if len(fs) == 1 && truthy(fs[0].Get("vm_id")) {
			link = "/inventory/view?id=" + py.Str(fs[0].Get("vm_id"))
		}
		var inc []string
		for _, r := range soft(ctx, x.con, "SELECT id FROM incidents WHERE status IN ('open','acknowledged') AND family='disk' "+
			"AND lower(subject) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(lower)), ",")+")", lower...) {
			inc = append(inc, py.Str(r.At(0)))
		}
		free := "?"
		if fs[0].Has("free_pct") {
			free = py.Str(fs[0].Get("free_pct"))
		}
		why := fmt.Sprintf("%s %% free, grows %s/day", free, gb(fs[0].Get("loss_per_day")))
		var cl any
		if len(inc) > 0 {
			why += "; incident #" + inc[0] + " open"
			cl = "/incidents/view?id=" + inc[0]
		}
		var mem []string
		for _, h := range hosts {
			mem = append(mem, "fs:"+h+":"+mount)
		}
		size := ""
		if len(fs) > 2 {
			size = "L"
		}
		out = append(out, mk("disk:"+env+":"+stem+":"+mount, "disk_full", "capacity", env, title,
			"Open the storage analysis; clean or grow the disk, note the rollback.", why, link,
			citem{urgency: u, impact: getF(envI, env, 1.0) + min(1.0, log2(float64(len(fs)))/2), confidence: 0.9, members: mem,
				sig: sigOf(int64(d)), close: cl, size: size}))
	}
	return out
}

// fingerprint and overrideValid are the "Mark as OK" rules of the backups page (internal/app OverrideValid).
func fingerprint(newest, size any) string {
	sz := ""
	if size != nil && size != "" {
		switch v := size.(type) {
		case int64:
			sz = strconv.FormatInt(v, 10)
		case float64:
			sz = strconv.FormatInt(int64(v), 10)
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				sz = strconv.FormatInt(n, 10)
			}
		}
	}
	return strOr(newest) + "|" + sz
}

func overrideValid(ov Row, status, fp string, okSince bool, now int64) bool {
	switch {
	case truthy(ov.Get("ended_at")), now >= i64(ov.Get("expires")), status == "OK",
		py.Str(ov.Get("scope")) == "until-ok" && okSince, status != py.Str(ov.Get("check_kind")),
		py.Str(ov.Get("scope")) == "until-changed" && fp != strOr(ov.Get("snapshot")):
		return false
	}
	return true
}

func (o *Options) cBackups(ctx context.Context, x *cctx) []Cand {
	run := soft(ctx, x.con, "SELECT id FROM runs WHERE task='backup-check' ORDER BY started_at DESC LIMIT 1")
	if len(run) == 0 {
		return nil
	}
	rows := soft(ctx, x.con, "SELECT name, status, newest, size FROM backup_results WHERE run_id=?", run[0].At(0))
	badBy := map[string]Row{}
	for _, r := range rows {
		if py.Str(r.Get("status")) != "OK" {
			badBy[py.Str(r.Get("name"))] = r
		}
	}
	acc := map[string]bool{}
	if len(badBy) > 0 {
		for _, ov := range soft(ctx, x.con, "SELECT * FROM backup_overrides WHERE ended_at IS NULL AND expires > ? ORDER BY id", x.now) {
			r, ok := badBy[py.Str(ov.Get("set_name"))]
			if !ok {
				continue
			}
			okSince := len(soft(ctx, x.con, "SELECT 1 FROM backup_results b JOIN runs r ON r.id=b.run_id WHERE r.task='backup-check' "+
				"AND b.name=? AND b.status='OK' AND r.started_at > ? LIMIT 1", ov.Get("set_name"), ov.Get("at"))) > 0
			if overrideValid(ov, py.Str(r.Get("status")), fingerprint(r.Get("newest"), r.Get("size")), okSince, x.now) {
				acc[py.Str(ov.Get("set_name"))] = true
			}
		}
	}
	var bad []Row
	for _, r := range rows {
		if py.Str(r.Get("status")) != "OK" && !acc[py.Str(r.Get("name"))] {
			bad = append(bad, r)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	var parts []string
	for i, r := range bad {
		if i < 3 {
			parts = append(parts, py.Str(r.Get("name"))+" ("+py.Str(r.Get("status"))+")")
		}
	}
	n := len(bad)
	return []Cand{mk("backups:failing", "backup_failing", "backups", "release", "Fix "+plural(n, "failing backup set"),
		"Open Backups; fix the first failing set and run backup-check again.", "a failing backup is a restore you cannot do: "+strings.Join(parts, ", "),
		"/backups", citem{urgency: 7, impact: 3, confidence: 0.9, units: n, members: members("backup:", bad, "name")})}
}

var devLabel = regexp.MustCompile(`(^|[.*])dev\.`)

func (o *Options) cCerts(ctx context.Context, x *cctx) []Cand {
	rows := soft(ctx, x.con, "SELECT gid, label, status, days_left, n_names FROM cert_groups WHERE run_id = (SELECT id FROM runs WHERE "+
		"task='cert-check' ORDER BY started_at DESC LIMIT 1) AND kind='cert' AND status IN ('FAIL','WARN') AND "+
		"days_left BETWEEN 0 AND 30 ORDER BY days_left")
	if len(rows) == 0 {
		return nil
	}
	d := i64(rows[0].Get("days_left"))
	names := 0
	env := "develop"
	var mem []string
	var det []Detail
	for _, r := range rows {
		names += int(num(or(r.Get("n_names"), int64(1))))
		if !devLabel.MatchString(strOr(r.Get("label"))) {
			env = "release"
		}
		mem = append(mem, "cert:"+py.Str(or(r.Get("gid"), r.Get("label"))))
		det = append(det, Detail{fmt.Sprintf("%s: %s days, %s names", py.Str(r.Get("label")), py.Str(r.Get("days_left")), py.Str(r.Get("n_names"))), "/certs"})
	}
	title := fmt.Sprintf("Renew %s (%d days left)", py.Str(rows[0].Get("label")), d)
	if len(rows) > 1 {
		title = fmt.Sprintf("Renew %s (first in %d days)", plural(len(rows), "certificate"), d)
	}
	u := 3.0
	switch {
	case d <= 7:
		u = 10
	case d <= 14:
		u = 8
	case d <= 21:
		u = 5
	}
	return []Cand{mk("certs:expiring", "cert_renew", "certs", env, title,
		"Check why certbot did not renew on the edges; renew and reload nginx, soonest first.",
		fmt.Sprintf("%s expires in %d days; %d names in all", py.Str(rows[0].Get("label")), d, names), "/certs",
		citem{urgency: u, impact: 2 + min(3.0, log2(float64(1+names))/2), confidence: 0.95, units: len(rows), members: mem,
			sig: sigOf(floorDiv(d, 7), int64(len(rows))), details: det})}
}

// cCreds reads credentials.json of the credential expiry job: the items it marks for Today (expired, rejected or
// within ARGUS_CRED_TODAY_DAYS), absent ones left out.
func (o *Options) cCreds(ctx context.Context, x *cctx) []Cand {
	var items []*py.OMap
	for _, v := range py.ReadJSONMap(o.CredFile).List("items") {
		m, ok := v.(*py.OMap)
		if !ok || !truthy(m.Get("today")) || m.Get("status") == nil || py.Str(m.Get("status")) == "absent" {
			continue
		}
		items = append(items, m)
	}
	dl := func(m *py.OMap) float64 {
		if isNum(m.Get("days_left")) {
			return num(m.Get("days_left"))
		}
		return -1
	}
	sort.SliceStable(items, func(i, j int) bool {
		if dl(items[i]) != dl(items[j]) {
			return dl(items[i]) < dl(items[j])
		}
		return py.Str(items[i].Get("name")) < py.Str(items[j].Get("name"))
	})
	var out []Cand
	for _, r := range items {
		d, st := r.Get("days_left"), py.Str(r.Get("status"))
		gone := st == "expired" || st == "rejected"
		var why string
		if gone {
			w := "expired"
			if st == "rejected" {
				w = "rejected by the service"
			}
			why = py.Str(r.Get("name")) + " is " + w
		} else {
			why = "expires " + py.Str(r.Get("expires_at")) + " (" + py.Str(d) + " days left)"
		}
		dn := i64(or(d, int64(0)))
		u := 5.0
		if gone {
			u = 10
		} else if dn <= 7 {
			u = 8
		}
		out = append(out, mk("cred:"+py.Str(r.Get("id")), "cred_rotate", "argus", "", "Rotate "+py.Str(r.Get("name")),
			"Open Settings → Credentials, follow How to rotate, store the new value, Check now.", why+"; used by "+py.Str(r.Get("used_by")),
			"/admin/credentials", citem{urgency: u, impact: 3, confidence: 1.0, sig: sigOf(st, floorDiv(dn, 7)), linkLabel: "Credentials"}))
	}
	return out
}

func (o *Options) cSelfcheck(ctx context.Context, x *cctx) []Cand {
	var bad []*py.OMap
	fail := false
	for _, v := range py.ReadJSONMap(o.SelfcheckFile).List("checks") {
		c, ok := v.(*py.OMap)
		if !ok || c.Get("status") == nil || py.Str(c.Get("status")) == "OK" && isStr(c.Get("status")) || py.Str(c.Get("group")) == "credentials" && isStr(c.Get("group")) {
			continue
		}
		bad = append(bad, c)
		fail = fail || py.Str(c.Get("status")) == "FAIL"
	}
	if len(bad) == 0 {
		return nil
	}
	var names, ws, mem []string
	for i, c := range bad {
		if i < 2 {
			names = append(names, py.Str(c.GetD("name", "?")))
			ws = append(ws, one(py.Str(c.Get("name"))+": "+py.Str(c.GetD("detail", "")), 60))
		}
		mem = append(mem, "self:"+py.Str(c.Get("name")))
	}
	state, u := "warning", 3.0
	if fail {
		state, u = "failing", 6
	}
	return []Cand{mk("selfcheck", "selfcheck", "argus", "", "Repair "+plural(len(bad), o.app()+" part")+" ("+strings.Join(names, ", ")+")",
		"Open "+o.app()+" health on the Overview; restart or fix the failing part.",
		o.app()+"'s own checks are "+state+": "+strings.Join(ws, "; "), "/", citem{urgency: u, impact: 2, confidence: 0.9, members: mem})}
}

func isStr(v any) bool { _, ok := v.(string); return ok }

func (o *Options) cIdeas(ctx context.Context, x *cctx) []Cand {
	rows := soft(ctx, x.con, "SELECT id, title FROM ideas WHERE status='new' ORDER BY created_at, id")
	if len(rows) == 0 {
		return nil
	}
	bn := int(num(x.cfg.def("ideas_batch", int64(5))))
	batch := rows
	if bn >= 0 && bn < len(rows) {
		batch = rows[:bn]
	}
	return []Cand{mk("ideas", "ideas", "ideas", "", "Triage "+plural(len(batch), "new idea"), "Open Ideas; explore, accept or reject each one with a reason.",
		fmt.Sprintf("%d new from the advisers", len(rows)), "/ideas", citem{urgency: 1.5, impact: 1.5, confidence: 1.0, units: len(batch),
			members: members("idea:", batch, "id")})}
}

func (o *Options) decisionsDoc() *py.OMap {
	if o.DecisionsFile == "" {
		return py.NewOMap()
	}
	return py.ReadJSONMap(o.DecisionsFile)
}

func (o *Options) cDecisions(ctx context.Context, x *cctx) []Cand {
	var out []Cand
	for _, v := range o.decisionsDoc().List("decisions") {
		d, ok := v.(*py.OMap)
		if !ok || py.Str(d.GetD("status", "open")) != "open" || !truthy(d.Get("plan")) {
			continue
		}
		kind := "decision"
		if k := py.Str(d.Get("kind")); isStr(d.Get("kind")) && (k == "decision" || k == "decision_token") {
			kind = k
		}
		link := "/today#waiting"
		if truthy(d.Get("link")) {
			link = py.Str(d.Get("link"))
		}
		out = append(out, mk("decision:"+py.Str(d.Get("id")), kind, "decisions", "", d.GetD("title", "Decision"), "Decide it, or say what you need first.",
			one(d.GetD("detail", ""), 120), link, citem{urgency: 3, impact: 2, confidence: 1.0}))
	}
	return out
}

func count1(ctx context.Context, d DB, q string) int64 {
	r := soft(ctx, d, q)
	if len(r) == 0 {
		return 0
	}
	return i64(r[0].At(0))
}

func (o *Options) decisionsList(ctx context.Context) ([]any, any) {
	doc := o.decisionsDoc()
	live := map[string]int64{
		"proposals_pending": count1(ctx, o.Board, "SELECT count(*) FROM proposals WHERE state='pending'"),
		"ideas_new":         count1(ctx, o.DB, "SELECT count(*) FROM ideas WHERE status='new'"),
		"agent_waiting":     count1(ctx, o.Board, "SELECT count(*) FROM agent_tasks WHERE state IN ('waiting','blocked')"),
	}
	plan := py.ReadJSON(filepath.Join(o.StateDir, "problems-backfill-plan.json"), nil)
	if m, ok := plan.(*py.OMap); ok {
		live["problems_backfill"] = int64(len(m.List("problems")))
	} else {
		live["problems_backfill"] = 0
	}
	out := []any{}
	for _, v := range doc.List("decisions") {
		d, ok := v.(*py.OMap)
		if !ok || py.Str(d.GetD("status", "open")) != "open" {
			continue
		}
		n := d.Get("items")
		detail := d.GetD("detail", "")
		c := d.Get("count")
		if cs, ok := c.(string); ok && cs == "queues" {
			detail = fmt.Sprintf("%d proposed tasks, %d new ideas, %d task-agent questions, your Matrix ID.", live["proposals_pending"], live["ideas_new"], live["agent_waiting"])
			n = live["proposals_pending"] + live["ideas_new"] + live["agent_waiting"]
		} else if cs, ok := c.(string); ok {
			if x, ok := live[cs]; ok {
				n = x
			}
		}
		var cnt any
		switch n.(type) {
		case int64, bool:
			cnt = n
		}
		link := ""
		if truthy(d.Get("link")) {
			link = py.Str(d.Get("link"))
		}
		out = append(out, py.NewOMap("id", py.Str(d.GetD("id", "")), "group", d.GetD("group", ""), "title", d.GetD("title", ""),
			"detail", one(detail, 300), "link", link, "count", cnt))
	}
	return out, doc.GetD("updated", "")
}

// Collect is every candidate, one per source reference (the first wins).
func (o *Options) Collect(ctx context.Context, con DB, cfg *Config, now int64) []Cand {
	x := &cctx{now: now, con: con, bcon: o.Board, cfg: cfg, operator: o.Operator, covered: map[string]bool{}}
	var out []Cand
	for _, f := range []func(context.Context, *cctx) []Cand{o.cProblems, o.cCapacity, o.cCicd, o.cIncidents, o.cCVE, o.cQA, o.cProposals,
		o.cAgent, o.cBoard, o.cBackups, o.cCerts, o.cCreds, o.cSelfcheck, o.cIdeas, o.cDecisions} {
		out = append(out, f(ctx, x)...)
	}
	seen := map[string]bool{}
	var uniq []Cand
	for _, c := range out {
		if !seen[c.Ref] {
			seen[c.Ref] = true
			uniq = append(uniq, c)
		}
	}
	return uniq
}
