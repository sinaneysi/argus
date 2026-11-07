package app

// Port of app/views_backup.py: "New backup" (/backups/new), backup plans (/backups/plan), Coverage and plans on
// /backups, Admin -> Backup targets. The app never writes a plan and never reads a secret back: "Test connection"
// and the target keys go once into settings-in/backup-secrets/ (0600, a directory the app cannot list), everything
// else is a request file in settings-in/backup-requests/ for root's bin/backup_plans.py, which validates it again.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var backupTemplatesURL = GITLAB_URL + "/" + BACKUP_TEMPLATES_REPO

var (
	backupIDRe      = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}\n?$`)
	backupCoverID   = regexp.MustCompile(`^([1-4])-([0-9]{1,3})\n?$`)
	backupPlanName  = regexp.MustCompile(`^[0-9]{1,6}\.json\n?$`)
	backupDateRe    = regexp.MustCompile(`^20[0-9]{2}-[01][0-9]-[0-3][0-9]\n?$`)
	backupTargetReq = regexp.MustCompile(`^[0-9a-f]{16}$`)
	backupStepRe    = regexp.MustCompile(`^step=([^` + wsClass + `]+) result=([^` + wsClass + `]+)(.*)\n?$`)
)

// backupKeep is views_backup.KEEP.
var backupKeep = []string{"databases", "schedule", "cron", "target", "member", "name", "retention_days"}

func backupE(v any) string { return E(py.Str(v)) }

// BackupPlansDir is ARGUS_BACKUP_PLANS_DIR (old name OPS_BACKUP_PLANS); empty: backup-plans next to ops.db.
// Tests may change it.
var BackupPlansDir = env("ARGUS_BACKUP_PLANS_DIR", "")

func backupPlansDir() string {
	if v := BackupPlansDir; v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(DB), "backup-plans")
}

// BackupPlans is views_backup.plans().
func BackupPlans() []*py.OMap {
	d := filepath.Join(backupPlansDir(), "plans")
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	key := func(n string) int64 {
		head, _, _ := strings.Cut(n, ".")
		if py.IsDigit(head) {
			v, _ := bpAtoi(head)
			return v
		}
		return 0
	}
	sort.SliceStable(names, func(i, j int) bool { return key(names[i]) < key(names[j]) })
	var out []*py.OMap
	for _, n := range names {
		if backupPlanName.MatchString(n) {
			if p, ok := ReadJSON(filepath.Join(d, n), nil).(*py.OMap); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func backupGetPlan(pid string) *py.OMap {
	if !py.IsDigit(pid) || py.Len(pid) > 6 {
		return nil
	}
	n, _ := bpAtoi(pid)
	p, _ := ReadJSON(filepath.Join(backupPlansDir(), "plans", I2S(n)+".json"), nil).(*py.OMap)
	return p
}

func backupStatus() *py.OMap {
	if d, ok := ReadJSON(filepath.Join(SET_OUT, "backup-plans-status.json"), nil).(*py.OMap); ok {
		return d
	}
	return py.NewOMap()
}

func backupRef() BPRef {
	d := filepath.Join(backupPlansDir(), "ref")
	if bpIsFile(filepath.Join(d, "backup-discovery.json")) {
		return BPLoadRef(d)
	}
	return BPLoadRef("")
}

func backupWriteRequest(payload *py.OMap, sess *Session) (string, error) {
	rid := LocalTime(Now()).Format("20060102150405") + "-" + auth.TokenHex(4)
	d := filepath.Join(SET_IN, "backup-requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		return "", err
	}
	p := payload.Copy()
	p.Set("id", rid).Set("by", sess.Username).Set("at", NowInt())
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("backup %s requested by %s: %s", py.Str(p.Get("kind")), sess.Username, rid)
	return rid, nil
}

func backupRequestBanner(c *Ctx, qs py.QS) string {
	rid := qs.Get("req")
	if !backupIDRe.MatchString(rid) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "backup-requests", rid+".json")); err == nil {
		c.Refresh = true
		return Banner("Sent. Argus's root job validates it again and stores the plan; this page refreshes itself.", "ok")
	}
	resv := backupStatus().Map("requests").Get(rid)
	if !py.Truthy(resv) {
		c.Refresh = true
		return Banner("Sent. Waiting for the backup-plans job; this page refreshes itself.", "ok")
	}
	res := py.AsMap(resv)
	r := py.Str(res.GetD("result", ""))
	if strings.HasPrefix(r, "refused") {
		return Banner("Not saved: "+py.Slice(r, 9), "fail")
	}
	if py.Truthy(res.Get("plan")) {
		return `<div class="banner ok" role="status">` + Icon("check") + `<span>` + E(py.Capitalize(r)) + `. ` +
			`<a href="/backups/plan?id=` + I2S(py.I(res.Get("plan"))) + `">Open the plan</a>.</span></div>`
	}
	return Banner(py.Capitalize(r) + ".")
}

// --- small renderers ------------------------------------------------------------------------------------------

func backupPlanState(p *py.OMap) (string, string) {
	st := p.Get("status")
	mr := py.AsMap(py.Or(p.Get("mr"), py.NewOMap()))
	if st == "withdrawn" {
		return "warn", "withdrawn"
	}
	if mr.Get("state") == "merged" || st == "merged" {
		return "ok", "merged · monitored"
	}
	if mr.Get("state") == "opened" {
		return "warn", "MR open" + If(p.Get("env") == "release", " · proposal", "")
	}
	if mr.Get("state") == "error" {
		return "fail", "MR failed"
	}
	if mr.Get("state") == "closed" {
		return "fail", "MR closed"
	}
	if p.Get("env") != "release" {
		return "warn", "waiting for the MR"
	}
	return "warn", "proposal"
}

func backupBadge(kind, text string) string {
	return `<span class="badge ` + kind + `">` + E(text) + `</span>`
}

// BackupRestoreForSet is views_backup.restore_for_set(name, plist).
func BackupRestoreForSet(name string, plist []*py.OMap) []any {
	for _, p := range plist {
		if BPSetName(py.Str(p.GetD("bucket", "")), py.Str(p.GetD("prefix", ""))) == name ||
			"plan"+py.Str(p.Get("id"))+"_"+py.Str(p.Get("name")) == name {
			return py.AsList(p.Get("restore_tests"))
		}
	}
	return nil
}

// --- the Backups page: plans and coverage ---------------------------------------------------------------------

type backupItem struct{ group, sid, title, meta, risk string }

func backupMaps(v any) []*py.OMap {
	var out []*py.OMap
	for _, x := range py.AsList(v) {
		out = append(out, py.AsMap(x))
	}
	return out
}

func backupJoin(v any) string {
	var parts []string
	for _, x := range py.AsList(v) {
		parts = append(parts, py.Str(x))
	}
	return strings.Join(parts, ", ")
}

func backupCoverageItems(disc *py.OMap) []backupItem {
	var items []backupItem
	for _, d := range backupMaps(disc.GetD("defects", []any{})) {
		meta := py.Str(d.Get("where"))
		if py.Truthy(d.Get("issue")) {
			meta += " · " + py.Str(d.Get("issue"))
		}
		items = append(items, backupItem{"defect", "1-" + py.Str(d.Get("n")), "Defect " + py.Str(d.Get("n")) + ": " + py.Str(d.Get("text")), meta, py.Str(d.Get("risk"))})
	}
	for _, g := range backupMaps(disc.GetD("gaps", []any{})) {
		meta := py.Str(g.Get("engine")) + " · " + py.Str(g.Get("env")) + " · " + py.Str(g.Get("why"))
		if py.Truthy(g.Get("issue")) {
			meta += " · " + py.Str(g.Get("issue"))
		}
		items = append(items, backupItem{"gap", "2-" + py.Str(g.Get("n")), py.Str(g.Get("what")), meta, "high"})
	}
	sets := backupMaps(disc.GetD("sets", []any{}))
	k := 0
	for _, s := range sets {
		if s.Get("encrypted") == "yes" {
			continue
		}
		k++
		enc := py.Str(s.Get("encrypted"))
		if s.Get("encrypted") == "no" {
			enc = "not"
		}
		meta := enc + " encrypted · " + backupJoin(s.Get("suffixes")) + " · " + py.F(py.Num(s.Get("total_gb")), 0) + " GB · family " + py.Str(s.Get("family"))
		items = append(items, backupItem{"unencrypted", "3-" + Itoa(k), py.Str(s.Get("set")), meta, "high"})
	}
	k = 0
	for _, s := range sets {
		if py.Truthy(s.Get("script_known")) {
			continue
		}
		k++
		meta := py.Str(s.Get("objects")) + " objects · " + py.Str(s.Get("per_day")) + "/day · " + py.F(py.Num(s.Get("total_gb")), 0) + " GB"
		items = append(items, backupItem{"noscript", "4-" + Itoa(k), py.Str(s.Get("set")), meta, "medium"})
	}
	return items
}

// BackupPrefill is views_backup.prefill(sid): board task values for a plan or a Coverage finding, or nil.
func BackupPrefill(sid string) *py.OMap {
	if py.IsDigit(sid) {
		p := backupGetPlan(sid)
		if p == nil {
			return nil
		}
		prod := p.Get("env") == "release"
		prio, fix := "Medium", p.GetD("fix_prompt", "")
		if prod {
			prio, fix = "High", ""
		}
		files := py.AsMap(py.Or(p.Get("files"), py.NewOMap()))
		return py.NewOMap("title", "Install backup "+py.Str(p.Get("name"))+" on "+py.Str(p.Get("server")), "env", p.GetD("env", ""),
			"host", p.GetD("server", ""), "priority", prio, "description", "Backup plan #"+py.Str(p.Get("id"))+".",
			"evidence", py.Head(py.Str(files.GetD("PLAN.md", "")), 30000), "fix_prompt", fix,
			"url", "/backups/plan?id="+I2S(py.I(p.Get("id"))), "what", "backup plan #"+I2S(py.I(p.Get("id"))))
	}
	if !backupCoverID.MatchString(sid) {
		return nil
	}
	var item *backupItem
	for _, i := range backupCoverageItems(backupRef().Discovery) {
		if i.sid == sid {
			it := i
			item = &it
			break
		}
	}
	if item == nil {
		return nil
	}
	words := map[string][2]string{
		"defect": {"Fix backup defect", "The discovery found this defect in the real backup scripts. Replace the " +
			"affected jobs with " + BACKUP_TEMPLATES_REPO + " (Backups → New backup) and run a restore drill."},
		"gap": {"Add a backup for", "This database has no backup. Decide the recovery point with the operator, " +
			"then create a plan with Backups → New backup."},
		"unencrypted": {"Encrypt backup set", "This set is stored without encryption" + defIssueRef(defIssueUnencrypted) + ". Move its job to the " +
			"templates with ENCRYPT=age."},
		"noscript": {"Find the script of backup set", "No script was found for this set. Find the host and the job, " +
			"then migrate it to the templates."}}[item.group]
	return py.NewOMap("title", py.Head(words[0]+": "+item.title, 200), "env", "release", "host", "",
		"priority", If(item.risk == "high", "High", "Medium"), "description", words[1],
		"evidence", item.title+"\n"+item.meta+"\n\nSource: Argus Backups → Coverage (discovery facts).", "fix_prompt", "",
		"url", "/backups", "what", "backup coverage "+item.group+" "+sid)
}

// --- New backup: four fields ------------------------------------------------------------------------------------

func backupDrafts() *py.OMap {
	d, _ := ReadJSON(filepath.Join(SET_OUT, "backup-drafts.json"), nil).(*py.OMap)
	return py.AsMap(py.Or(d.Get("drafts"), py.NewOMap()))
}

func backupTargetsDoc() *py.OMap {
	if d, ok := ReadJSON(filepath.Join(SET_OUT, "backup-targets.json"), nil).(*py.OMap); ok {
		return d
	}
	return py.NewOMap()
}

func backupTargets() []*py.OMap {
	var out []*py.OMap
	for _, t := range py.AsList(backupTargetsDoc().Get("targets")) {
		if m := py.AsMap(t); m != nil && py.Truthy(m.Get("name")) {
			out = append(out, m)
		}
	}
	return out
}

// backupDrop is views_backup.drop(sess, payload): one 0600 request with a secret for root; nothing of it is logged.
func backupDrop(sess *Session, payload *py.OMap) (string, error) {
	d := filepath.Join(SET_IN, "backup-secrets")
	rid := auth.TokenHex(8)
	name := sess.Username + "-" + rid + ".json"
	tmp := filepath.Join(d, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	p := payload.Copy()
	p.Set("user", sess.Username).Set("at", NowInt())
	if _, err := f.WriteString(py.Dumps(p, -1)); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(d, name)); err != nil {
		return "", err
	}
	logf("backup %s requested by %s: %s", py.Str(payload.Get("kind")), sess.Username, rid)
	return rid, nil
}

func backupFormValues(form py.QS) map[string]string {
	v := map[string]string{}
	for _, k := range append(append([]string{}, backupKeep...), "draft") {
		v[k] = py.Head(py.Strip(form.Get(k)), 200)
	}
	return v
}

func backupKeepQS(v map[string]string, extra ...string) string {
	var pairs [][2]string
	for _, k := range backupKeep {
		if v[k] != "" {
			pairs = append(pairs, [2]string{k, v[k]})
		}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] != "" {
			pairs = append(pairs, [2]string{extra[i], extra[i+1]})
		}
	}
	return py.Urlencode(pairs)
}

func backupEnvIn(envs any, env string) bool {
	switch x := envs.(type) {
	case []any:
		for _, e := range x {
			if e == env {
				return true
			}
		}
	case string:
		return strings.Contains(x, env)
	}
	return false
}

func backupTargetOpts(tl []*py.OMap, cur, env string) string {
	if len(tl) == 0 {
		return `<option value="">No target yet</option>`
	}
	if cur == "" {
		var fit []*py.OMap
		for _, t := range tl {
			if env == "" || !py.Truthy(t.Get("envs")) || backupEnvIn(t.Get("envs"), env) {
				fit = append(fit, t)
			}
		}
		if len(fit) == 1 {
			cur = py.Str(fit[0].Get("name"))
		}
	}
	var b strings.Builder
	if cur == "" {
		b.WriteString(`<option value="">Choose a target</option>`)
	}
	for _, t := range tl {
		test := py.AsMap(py.Or(t.Get("test"), py.NewOMap()))
		wo := "untested"
		if strings.HasPrefix(py.Str(test.GetD("delete", "")), "denied") {
			wo = "write-only"
		} else if py.Truthy(t.Get("provision")) {
			wo = "per-job users"
		} else if py.Truthy(t.Get("can_delete")) {
			wo = "keys can delete"
		}
		envs := backupJoin(t.Get("envs"))
		if envs == "" {
			envs = "all environments"
		}
		b.WriteString(`<option value="` + backupE(t.Get("name")) + `"` + If(t.Get("name") == cur, " selected", "") + `>` + backupE(t.Get("name")) + ` · ` +
			backupE(t.GetD("bucket", "")) + `/` + backupE(t.GetD("prefix", "")) + ` · ` + E(wo) + ` · ` + E(envs) + `</option>`)
	}
	return b.String()
}

func backupTruthyD(d *py.OMap) bool { return d != nil && d.Len() > 0 }

func backupNow() time.Time {
	n := Now()
	sec := int64(n)
	return time.Unix(sec, int64((n-float64(sec))*1e9)).In(BPTZ)
}

func backupCouldNotSave(c *Ctx, path string, err error) (int, string) {
	logf("save failed on %s: %v", path, err)
	return 500, "Could not save"
}

func backupWith(v map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, x := range v {
		out[k] = x
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// backupNewAnswer is what Save, Test or Preview decides: a page to go to, or the form again with errors.
type backupNewAnswer struct {
	redirect string
	errs     []string
	values   map[string]string
	err      error // the request file could not be written
}

// backupDecideNew validates the form and drops the request; the caller has checked the admin role.
func backupDecideNew(form py.QS, sess *Session) backupNewAnswer {
	action := form.Get("action")
	v, errs := BSClean(form)
	keep := backupFormValues(form)
	if len(errs) > 0 {
		return backupNewAnswer{errs: errs, values: keep}
	}
	if action == "preview" && v["uri"] == "" {
		return backupNewAnswer{values: keep}
	}
	if In(action, "test", "retest", "preview") {
		uri := v["uri"]
		if uri == "" && action == "retest" {
			return backupNewAnswer{errs: []string{"Paste the connection string again to test it again (Argus does not keep it)."}, values: backupWith(keep, "draft", "")}
		}
		parsed, err := BUParse(uri)
		if err != nil {
			return backupNewAnswer{errs: []string{"Connection string: " + err.Error()}, values: backupWith(keep, "draft", "")}
		}
		vms := Q("SELECT name, env, ips FROM vms WHERE gone=0 ORDER BY lower(name)")
		r := backupRef()
		var vips *py.OMap
		if vd, ok := ReadJSON(filepath.Join(r.Dir, "backup-vips.json"), nil).(*py.OMap); ok {
			vips = py.AsMap(vd.GetD("vips", py.NewOMap()))
		}
		res := BSResolve(parsed, vms, vips, nil)
		if res.Error != "" {
			return backupNewAnswer{errs: []string{res.Error}, values: backupWith(keep, "draft", "")}
		}
		member := ""
		for _, m := range res.Members {
			if py.Str(m.Get("name")) == keep["member"] {
				member = keep["member"]
			}
		}
		if member == "" {
			member = BSDefaultMember(res, nil)
		}
		draft := auth.TokenHex(8)
		if _, err := backupDrop(sess, py.NewOMap("kind", "test", "draft", draft, "uri", uri, "sha", BUFingerprint(uri), "member", member)); err != nil {
			return backupNewAnswer{err: err}
		}
		return backupNewAnswer{redirect: "/backups/new?" + backupKeepQS(backupWith(keep, "member", member), "draft", draft)}
	}
	if action == "save" {
		draft := v["draft"]
		var d *py.OMap
		if BSDraftRE.MatchString(draft) {
			d = py.AsMap(backupDrafts().Get(draft))
		}
		if !backupTruthyD(d) || d.Get("user") != sess.Username || !(d.Get("state") == "ok" || d.Get("state") == "deferred") {
			return backupNewAnswer{errs: []string{"Test the connection first: Save unlocks when the test passes."}, values: keep}
		}
		sha, hasSha := v["sha"]
		shaBad := d.Get("sha") != nil
		if hasSha {
			shaBad = d.Get("sha") != sha
		}
		if !hmacEq(v["token"], py.Str(d.GetD("token", ""))) || shaBad {
			return backupNewAnswer{errs: []string{"This form's connection string is not the one that was tested: test it again."}, values: backupWith(keep, "draft", "")}
		}
		clean := py.NewOMap()
		for _, k := range append(append([]string{}, backupKeep...), "draft", "token", "sha") {
			if v[k] != "" {
				clean.Set(k, v[k])
			}
		}
		clean.Set("member", d.GetD("member", ""))
		rid, err := backupWriteRequest(py.NewOMap("kind", "simple", "form", clean), sess)
		if err != nil {
			return backupNewAnswer{err: err}
		}
		return backupNewAnswer{redirect: "/backups/new?req=" + rid}
	}
	return backupNewAnswer{values: keep}
}

// --- plan page --------------------------------------------------------------------------------------------------

// backupStripInstall is re.sub(r"(?ms)^## Install\n.*?(?=^## )", "", s).
func backupStripInstall(s string) string {
	lineStart := func(k int) bool { return k == 0 || s[k-1] == '\n' }
	var b strings.Builder
	i := 0
	for {
		start, end := -1, -1
		for p := i; p < len(s) && start < 0; p++ {
			if !lineStart(p) || !strings.HasPrefix(s[p:], "## Install\n") {
				continue
			}
			for k := p + len("## Install\n"); k < len(s); k++ {
				if lineStart(k) && strings.HasPrefix(s[k:], "## ") {
					start, end = p, k
					break
				}
			}
		}
		if start < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		b.WriteString(s[i:start])
		i = end
	}
}

func backupPostInstall(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	pid := form.Get("plan")
	p := backupGetPlan(pid)
	if p == nil {
		return 404, "No such plan"
	}
	if p.Get("kind") != "simple" || !(p.Get("env") == "develop" || p.Get("env") == "pre-release") {
		return 400, "Install now is for develop and pre-release"
	}
	n, _ := bpAtoi(pid)
	rid, err := backupWriteRequest(py.NewOMap("kind", "install", "plan", n), sess)
	if err != nil {
		return backupCouldNotSave(c, "/backups/install", err)
	}
	return 303, "/backups/plan?id=" + I2S(n) + "&req=" + rid + "#install"
}

func backupPostRestore(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := func(k string) string { return py.Strip(form.Get(k)) }
	pid := get("plan")
	if backupGetPlan(pid) == nil {
		return 404, "No such plan"
	}
	if !In(get("result"), "ok", "partial", "failed") || !backupDateRe.MatchString(get("date")) {
		return 400, "Result ok/partial/failed and a date YYYY-MM-DD"
	}
	if BPSecretish(get("object")) || BPSecretish(get("notes")) {
		return 400, "That looks like a secret"
	}
	n, _ := bpAtoi(pid)
	rid, err := backupWriteRequest(py.NewOMap("kind", "restore", "plan", n, "date", get("date"), "result", get("result"),
		"object", py.Head(get("object"), 300), "notes", py.Head(get("notes"), 1000)), sess)
	if err != nil {
		return backupCouldNotSave(c, "/backups/restore", err)
	}
	return 303, "/backups/plan?id=" + I2S(n) + "&req=" + rid + "#restore"
}

// --- Admin -> Backup targets ------------------------------------------------------------------------------------

func backupTargetState(t *py.OMap) string {
	r := py.AsMap(py.Or(t.Get("test"), py.NewOMap()))
	if r.Len() == 0 {
		return backupBadge("warn", "not tested")
	}
	if r.Get("result") != "ok" {
		return backupBadge("fail", "test failed")
	}
	if py.Truthy(t.Get("provision")) {
		return backupBadge("ok", "per-job users")
	}
	if strings.HasPrefix(py.OrS(r.Get("delete"), ""), "denied") {
		return backupBadge("ok", "write-only")
	}
	return backupBadge("warn", "keys can delete")
}

func backupTargetBanner(c *Ctx, qs py.QS) string {
	rid := qs.Get("req")
	if !backupTargetReq.MatchString(rid) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "backup-secrets", c.Sess.Username+"-"+rid+".json")); err == nil {
		c.Refresh = true
		return Banner("Sent to root on "+ARGUS_HOSTNAME+"; testing the target. This page refreshes itself.", "ok")
	}
	rv := backupStatus().Map("requests").Get(rid)
	if !py.Truthy(rv) {
		c.Refresh = true
		return Banner("Waiting for the backup-plans job; this page refreshes itself.", "ok")
	}
	res := py.Str(py.AsMap(rv).GetD("result", ""))
	return Banner(strings.ToUpper(py.Head(res, 1))+py.Slice(res, 1)+".", If(strings.HasPrefix(res, "refused"), "fail", "ok"))
}

func backupPostTargets(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := func(k string) string { return py.Strip(form.Get(k)) }
	action, name := get("action"), get("name")
	rejected := func(field, msg string) (int, string) {
		c.Rej = Reject("target-add", field, msg, py.QS{})
		return 400, msg
	}
	if !BSTargetRE.MatchString(name) {
		return rejected("name", "Name: 2-31 lower-case letters, digits and dashes.")
	}
	var rid string
	var err error
	switch action {
	case "add":
		var envs []any
		for _, x := range form.List("envs") {
			if In(x, BPEnvs...) {
				envs = append(envs, x)
			}
		}
		if envs == nil {
			envs = []any{}
		}
		payload := py.NewOMap("kind", "target-add", "name", name, "type", get("type"), "endpoint", get("endpoint"), "bucket", get("bucket"),
			"prefix", get("prefix"), "access_key", get("access_key"), "secret_key", get("secret_key"),
			"provision", get("provision") == "yes", "retention_days", get("retention_days"),
			"envs", envs, "ca_pem", py.Head(form.Get("ca_pem"), 20000))
		// the same checks root makes (bin/backup_exec.py target-add), answered here with the entries kept
		ret := get("retention_days")
		if ep := strings.TrimRight(get("endpoint"), "/"); !backupEndpointRE.MatchString(ep + "/") {
			return rejected("endpoint", "Endpoint: https://host[:port], no path.")
		}
		if !backupBucketRE.MatchString(get("bucket")) {
			return rejected("bucket", "Bucket: 3-63 lower-case letters, digits, dots and dashes.")
		}
		if get("access_key") == "" || get("secret_key") == "" {
			return rejected("access_key", "The access key and the secret key are required.")
		}
		if !backupKeyRE.MatchString(get("access_key")) {
			return rejected("access_key", "Access key: 3-128 letters, digits and + / = . _ -")
		}
		if !backupKeyRE.MatchString(get("secret_key")) || len(get("secret_key")) < 8 {
			return rejected("secret_key", "Secret key: 8-128 letters, digits and + / = . _ -")
		}
		if n, ok := inRange(ret, 1, 400); ret != "" && (!ok || n < 1) {
			return rejected("retention_days", "Default retention must be between 1 and 400 days.")
		}
		rid, err = backupDrop(sess, payload)
	case "test", "remove":
		rid, err = backupDrop(sess, py.NewOMap("kind", "target-"+action, "name", name))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return backupCouldNotSave(c, "/admin/backup-targets", err)
	}
	return 303, "/admin/backup-targets?req=" + rid
}

// BackupPost dispatches the backup POSTs by path: (303, location) or (code, html).
func BackupPost(c *Ctx, path string, form py.QS, sess *Session) (int, string) {
	switch path {
	case "/backups/restore":
		return backupPostRestore(c, form, sess)
	case "/backups/install":
		return backupPostInstall(c, form, sess)
	case "/admin/backup-targets":
		return backupPostTargets(c, form, sess)
	}
	return 404, "Not found"
}
