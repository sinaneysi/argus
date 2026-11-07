package app

// Port of app/views_fixer.py: "Take action" on a develop / pre-release problem (admins only). The app writes
// settings-in/fix-requests/<id>.json; root's dispatcher re-checks everything. Production never gets a button.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"

	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var FixerEnvs = []string{"develop", "pre-release"}

const (
	FixerMaxPrompt = 16 * 1024
	FixerStaleRun  = 25 * 60
)

var (
	FixerIDRE    = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}$`)
	fixerAliasRE = regexp.MustCompile(`^(dev|pre)-[a-z0-9][a-z0-9-]*-fix$`)
)

var fixerSetupSteps = `<ol class="steps">
<li>On the operator workstation (root SSH aliases to ` + ARGUS_HOSTNAME + ` and the hosts), from a clone of
<code>` + ARGUS_REPO + `</code>: <code>admin/setup-fixer-access.sh --dry-run</code>, read it, then
<code>admin/setup-fixer-access.sh</code> (or <code>--host &lt;alias&gt;</code> for one host at a time).
It creates <code>argus-fix</code> on ` + ARGUS_HOSTNAME + ` and on the develop / pre-release hosts, with logged sudo.</li>
<li>On ` + ARGUS_HOSTNAME + `, as root, choose the environments Argus may fix:
<code>printf 'develop\n' &gt; /etc/argus/fix-scope &amp;&amp; chmod 0644 /etc/argus/fix-scope</code>
(add <code>pre-release</code> when wanted; <code>release</code> is never honoured).</li>
<li>Reload this page.</li></ol>`

// --- production markers (guardrails RELEASE, strict form) -------------------------------------------------
// Python uses lookaheads and Unicode \b/\d/\w, which RE2 lacks: each pattern is a RE2 candidate plus checks.

type fixerMarker struct {
	rx   *regexp.Regexp
	lead bool                             // \b before the match
	ok   func(text string, s, e int) bool // extra checks (lookaheads, trailing \b)
}

const fixerW = `[\p{L}\p{N}_]`

func fixerNoDigitAfter(text string, s, e int) bool {
	if e >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[e:])
	return !(r >= '0' && r <= '9' || isDigitRune(r))
}

func isDigitRune(r rune) bool { return py.IsDigit(string(r)) }

// fixerMarkers (the production markers) and fixerCompanyDomains are in site_defaults*.go (company data).

func fixerFindAll(mk fixerMarker, text string) []string {
	var out []string
	pos := 0
	for pos <= len(text) {
		loc := mk.rx.FindStringIndex(text[pos:])
		if loc == nil {
			break
		}
		s, e := pos+loc[0], pos+loc[1]
		if (!mk.lead || !fhWordBefore(text, s)) && (mk.ok == nil || mk.ok(text, s, e)) {
			out = append(out, text[s:e])
			if e == s {
				e++
			}
			pos = e
			continue
		}
		_, w := utf8.DecodeRuneInString(text[s:])
		if w == 0 {
			w = 1
		}
		pos = s + w
	}
	return out
}

// FixerProductionMarkers is production_markers(text).
func FixerProductionMarkers(text string) []string {
	var found []string
	add := func(m string) {
		for _, f := range found {
			if f == m {
				return
			}
		}
		found = append(found, m)
	}
	for _, mk := range fixerMarkers {
		if mk.rx == nil {
			for _, m := range fixerCompanyDomains(text) {
				add(m)
			}
			continue
		}
		for _, m := range fixerFindAll(mk, text) {
			add(m)
		}
	}
	if len(found) > 10 {
		found = found[:10]
	}
	return found
}

// --- state ---------------------------------------------------------------------------------------------------

func fixerScope() *py.OMap {
	return ReadJSONMap(filepath.Join(SET_OUT, "fix-scope.json"))
}

func fixerStatusRuns() *py.OMap {
	d := ReadJSONMap(filepath.Join(SET_OUT, "fix-status.json"))
	if r := py.AsMap(d.Get("runs")); r != nil {
		return r
	}
	return py.NewOMap()
}

func fixerFixRow(rid string) Row {
	r := Q("SELECT * FROM fix_runs WHERE id=?", rid)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func fixerRunning() []string {
	now := Now()
	var out []string
	runs := fixerStatusRuns()
	for _, rid := range runs.Keys() {
		st := py.AsMap(runs.Get(rid))
		if st.Get("result") == "started" && now-py.Num(py.Or(st.Get("started"), int64(0))) < FixerStaleRun && fixerFixRow(rid) == nil {
			out = append(out, rid)
		}
	}
	return out
}

// fixerProblem is get_problem(run, idx).
func fixerProblem(run, idx string) Row {
	if !(py.IsDigit(run) && py.IsDigit(idx)) {
		return nil
	}
	rn, _ := py.ParseInt(run)
	ix, _ := py.ParseInt(idx)
	r := Q("SELECT p.*, r.started_at FROM triage_problems p JOIN runs r ON r.id = p.run_id "+
		"WHERE p.run_id=? AND p.idx=?", rn, ix)
	if len(r) == 0 {
		return nil
	}
	p := Row{}
	for k, v := range r[0] {
		p[k] = v
	}
	if pr := FixHandoffProblem(r[0]).Prompt; pr != "" {
		p["fix_prompt"] = pr
	}
	a, b := I2S(p.I("run_id")), I2S(p.I("idx"))
	p["rca_id"] = nil
	p["back"] = "/incidents/problem?run=" + a + "&amp;idx=" + b
	p["back_label"] = "Problem"
	p["hidden"] = `<input type="hidden" name="run" value="` + a + `"><input type="hidden" name="idx" value="` + b + `">`
	p["review"] = "run=" + a + "&amp;idx=" + b
	return p
}

// fixerRca is get_rca(rid).
func fixerRca(rid string) Row {
	if !py.IsDigit(rid) || py.Len(rid) > 9 {
		return nil
	}
	n, _ := py.ParseInt(rid)
	rr := Q("SELECT r.*, i.env FROM rca_reports r LEFT JOIN incidents i ON i.id = r.incident_id WHERE r.id=?", n)
	if len(rr) == 0 || !rr[0].T("incident_id") {
		return nil
	}
	r := rr[0]
	h := FixHandoffRca(r, nil)
	if h.Prompt == "" {
		return nil
	}
	id := I2S(r.I("id"))
	return Row{"run_id": nil, "idx": nil, "rca_id": r.I("id"), "title": r.Get("title"), "environment": r.S("env"),
		"fix_prompt": h.Prompt, "back": "/rca/view?id=" + id, "back_label": "RCA report",
		"hidden": `<input type="hidden" name="rca" value="` + id + `">`, "review": "rca=" + id}
}

func fixerSource(get func(string) string) Row {
	if get("rca") != "" {
		return fixerRca(get("rca"))
	}
	return fixerProblem(get("run"), get("idx"))
}

func fixerStrList(v any) []string {
	var out []string
	for _, x := range py.AsList(v) {
		out = append(out, py.Str(x))
	}
	return out
}

// fixerAvailability is availability(env, sc).
func fixerAvailability(env string, sc *py.OMap) (bool, string) {
	if !In(env, FixerEnvs...) {
		return false, ""
	}
	if sc.Len() == 0 {
		return false, `<div class="banner warn">` + Icon("alert") + `<span>The fixer dispatcher has not ` +
			`published its state yet (argus-fixreq on ` + ARGUS_HOSTNAME + `).</span></div>`
	}
	if !py.Truthy(sc.Get("set_up")) {
		miss := strings.Join(fixerStrList(py.Or(sc.Get("missing"), []any{})), ", ")
		return false, `<div class="banner warn">` + Icon("alert") + `<span>Take action is not set up yet: the ` +
			`operator grants the access, Argus never does. Missing: ` + E(miss) + `</span></div>` +
			`<h3>Operator steps</h3>` + fixerSetupSteps
	}
	if py.Truthy(sc.Get("error")) {
		return false, StateBox(StFailed, "Take action is off: the fix scope file has a problem.", "Error: "+SafeErr(py.Str(sc.Get("error")))+". "+
			"Nothing was started. The operator corrects the scope file on "+ARGUS_HOSTNAME+".", "")
	}
	if !In(env, fixerStrList(py.Or(sc.Get("envs"), []any{}))...) {
		return false, `<p class="section-note">Take action is not enabled for ` +
			`<span class="tag">` + E(EnvLabel(env)) + `</span>: the operator has not listed it in ` +
			`<code>/etc/argus/fix-scope</code> on ` + ARGUS_HOSTNAME + `.</p>`
	}
	if !py.Truthy(py.AsMap(sc.Get("hosts")).Get(env)) {
		return false, Banner("No "+env+" host is configured for the fixer.", "fail")
	}
	return true, ""
}

// FixerAvailability is views_fixer.availability(env): (ok, html explaining why not).
func FixerAvailability(env string) (bool, string) { return fixerAvailability(env, fixerScope()) }

// FixerProblemActions is problem_actions(p).
func FixerProblemActions(c *Ctx, p Row) string {
	env := p.S("environment")
	if env == "release" {
		return `<span class="muted small">Argus never runs this. Copy the prompt above; production changes are run ` +
			`by a human, with the operator's approval per change.</span>`
	}
	if !In(env, FixerEnvs...) || !p.T("fix_prompt") {
		return ""
	}
	if c.Sess == nil || c.Sess.Role != "admin" {
		return ""
	}
	ok, why := FixerAvailability(env)
	if !ok {
		return `<h2>Take action</h2>` + why
	}
	if busy := fixerRunning(); len(busy) > 0 {
		return `<h2>Take action</h2><p class="section-note">A fix is running ` +
			`(<a href="/admin/fix/run?id=` + E(busy[0]) + `">` + E(busy[0]) + `</a>); one at a time.</p>`
	}
	review := ""
	if p.Has("review") {
		review = py.Str(p.Get("review"))
	} else {
		review = "run=" + I2S(p.I("run_id")) + "&amp;idx=" + I2S(p.I("idx"))
	}
	return `<h2>Take action</h2><p class="section-note">Argus can carry out this fix on ` +
		`<span class="tag">` + E(EnvLabel(env)) + `</span> after you review and edit the prompt.</p>` +
		`<p><a class="btn" href="/admin/fix/review?` + review + `">` + `Take action</a></p>`
}

// fixerPreamble is the fixed text the dispatcher puts before a prompt ("" until it is published).
func fixerPreamble() string {
	if data, err := os.ReadFile(filepath.Join(SET_OUT, "fix-preamble.md")); err == nil {
		s := strings.ReplaceAll(strings.ReplaceAll(py.ToValidUTF8(data), "\r\n", "\n"), "\r", "\n")
		return py.Head(s, 20000)
	}
	return ""
}

// FixerPostReview is post_review(form, sess): (code, html, location).
func FixerPostReview(c *Ctx, form py.QS, sess *Session) (int, string, string) {
	get := form.Get
	p := fixerSource(get)
	if p == nil || sess.Role != "admin" {
		return 404, "Not found", ""
	}
	prompt := strings.ReplaceAll(strings.ReplaceAll(get("prompt"), "\r\n", "\n"), "\r", "\n")
	chosen := form.List("host")
	env := p.S("environment")
	again := func(msg string) (int, string, string) {
		return 400, msg, ""
	}
	if !In(env, FixerEnvs...) {
		return again("Only develop and pre-release problems can be fixed by Argus.")
	}
	sc := fixerScope()
	if ok, _ := fixerAvailability(env, sc); !ok {
		return again("Take action is not available for " + env + ".")
	}
	if get("reviewed") != "1" {
		return again(`Tick "I reviewed this prompt" first.`)
	}
	if py.Strip(prompt) == "" {
		return again("The prompt is empty.")
	}
	if len(prompt) > FixerMaxPrompt {
		return again("The prompt is larger than " + Itoa(FixerMaxPrompt/1024) + " KB.")
	}
	if marks := FixerProductionMarkers(prompt); len(marks) > 0 {
		if len(marks) > 5 {
			marks = marks[:5]
		}
		return again("The prompt names production: " + strings.Join(marks, ", ") + ". Production changes are run by a human.")
	}
	allowed := map[string]bool{}
	for _, h := range fixerStrList(py.AsMap(sc.Get("hosts")).Get(env)) {
		allowed[h] = true
	}
	bad := len(chosen) == 0
	for _, h := range chosen {
		if !allowed[h] {
			bad = true
		}
	}
	if bad {
		return again("Pick at least one target host of this environment.")
	}
	if len(fixerRunning()) > 0 {
		return again("Another fix is running; one at a time.")
	}
	rid := time.Unix(NowInt(), 0).In(time.Local).Format("20060102150405") + "-" + auth.TokenHex(4)
	var hosts []any
	seen := map[string]bool{}
	for _, h := range chosen {
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	sum := sha256.Sum256([]byte(prompt))
	problem := py.NewOMap("run", p.Get("run_id"), "idx", p.Get("idx"), "title", py.Head(p.S("title"), 200))
	if py.Truthy(p.Get("rca_id")) {
		problem.Set("rca", p.Get("rca_id"))
	}
	if hosts == nil {
		hosts = []any{}
	}
	req := py.NewOMap("id", rid, "by", sess.Username, "at", NowInt(), "env", env, "hosts", hosts, "prompt", prompt,
		"reviewed", true, "prompt_sha256", hex.EncodeToString(sum[:]), "problem", problem)
	d := filepath.Join(SET_IN, "fix-requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(err)
	}
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(py.Dumps(req, -1))); err != nil {
		panic(err)
	}
	logf("fix requested by %s: %s %s %s", sess.Username, rid, env, strings.Join(chosen, ","))
	return 303, "", "/admin/fix/run?id=" + rid
}

func fixerJList(v any) []any {
	s, ok := v.(string)
	if !ok && v != nil {
		return nil
	}
	if s == "" {
		s = "[]"
	}
	parsed, err := py.ParseJSON([]byte(s))
	if err != nil {
		return nil
	}
	return py.AsList(parsed)
}

// fixerRunFiles is what is known of a fix run: its row, its dispatcher status, whether its request still waits. ok false: no such run.
func fixerRunFiles(rid string) (row Row, st *py.OMap, pending, ok bool) {
	if !FixerIDRE.MatchString(rid) {
		return nil, nil, false, false
	}
	row = fixerFixRow(rid)
	st = py.AsMap(fixerStatusRuns().Get(rid))
	if st == nil {
		st = py.NewOMap()
	}
	_, perr := os.Stat(filepath.Join(SET_IN, "fix-requests", rid+".json"))
	pending = perr == nil
	return row, st, pending, row != nil || st.Len() > 0 || pending
}
