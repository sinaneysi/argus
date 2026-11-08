package cicd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/notify"
)

// HostEnv maps host names (or addresses) that match Re to an environment (the guardrail "stage never reaches
// production" and the environment of a deploy found only in the engine project).
type HostEnv struct {
	Env string
	Re  *regexp.Regexp
}

// Options configure one run.
type Options struct {
	DB         *sql.DB
	GitLab     *GitLab
	Groups     []string // GitLab groups; empty: every project the token is a member of
	EnginePath string   // the shared CI engine project (path); empty: a project with a .gitlab-ci.yml is watched
	EngineID   int64    // its id (its deploy pipelines and failed jobs)
	// DeployBridge is the bridge job that triggers the deploy pipeline; DeployJobNames the deploy jobs in it.
	DeployBridge   string
	DeployJobNames []string
	InventoryFile  string // the Ansible inventory in the engine project ("" none)
	InventoryRef   string
	HostEnvs       []HostEnv
	StuckMinutes   int
	RoundRobin     int    // projects polled per cycle besides the hot ones
	StateDir       string // cicd-status.json, heartbeat-cicd-<task>
	PublicURL      string // links in messages
	MetaFile       string // the CI variables metadata (ci-vars-meta); "" none
	Incidents      *incidents.Options
	Notify         func(notify.Message) error // nil: no messages
	Now            func() time.Time
	Log            io.Writer
}

// Watcher is one run of scan, watch or audit.
type Watcher struct {
	Opt     Options
	tx      *sql.Tx
	now     int64
	New     []int64
	Counts  map[string]int
	ctx     context.Context
	incRe   *regexp.Regexp
	stuckSt []string
}

var stuckStates = []string{"created", "pending", "waiting_for_resource", "preparing"}
var openStates = append(append([]string{}, stuckStates...), "running", "scheduled")
var sevOf = map[string]string{"release": "high", "pre-release": "medium", "develop": "low"}

// New starts a run; Close commits it.
func New(ctx context.Context, o Options) (*Watcher, error) {
	if o.DB == nil || o.GitLab == nil {
		return nil, errors.New("cicd: no database or GitLab")
	}
	if o.DeployBridge == "" {
		o.DeployBridge = "trigger_deploy"
	}
	if len(o.DeployJobNames) == 0 {
		o.DeployJobNames = []string{"deploy_systemd", "deploy"}
	}
	if o.StuckMinutes <= 0 {
		o.StuckMinutes = 10
	}
	if o.RoundRobin <= 0 {
		o.RoundRobin = 15
	}
	if o.InventoryRef == "" {
		o.InventoryRef = "main"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	w := &Watcher{Opt: o, now: o.Now().Unix(), Counts: map[string]int{}, ctx: ctx}
	if o.EnginePath != "" {
		w.incRe = regexp.MustCompile(`project:\s*['"]?` + regexp.QuoteMeta(o.EnginePath) + `['"]?`)
	}
	return w, w.begin()
}

func (w *Watcher) begin() error {
	tx, err := w.Opt.DB.Begin()
	w.tx = tx
	return err
}

func (w *Watcher) commit() error {
	if err := w.tx.Commit(); err != nil {
		return err
	}
	return w.begin()
}

// Close commits what the run wrote.
func (w *Watcher) Close() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx = nil
	return err
}

func (w *Watcher) bump(k string, n int) { w.Counts[k] += n }

func (w *Watcher) exec(q string, args ...any) error {
	_, err := w.tx.Exec(q, args...)
	return err
}

// --- CI file and inventory parsing (pure) ------------------------------------------------------------------

var (
	refRe    = regexp.MustCompile(`\bref:\s*['"]?([A-Za-z0-9._/-]+)`)
	fileRe   = regexp.MustCompile(`\bfile:\s*['"]?([A-Za-z0-9._/-]+)`)
	targetRe = regexp.MustCompile(`(?m)^\s*(?:TARGET_HOSTS|DEPLOY_TARGET):\s*['"]?([A-Za-z0-9._-]+)`)
)

// Include is one include of the engine project: its ref and file.
type Include struct{ Ref, File string }

// ParseCI is every include of the engine project (matched by incRe) and TARGET_HOSTS/DEPLOY_TARGET if the file sets it.
func ParseCI(text string, incRe *regexp.Regexp) ([]Include, string) {
	lines := splitLines(text)
	var incs []Include
	for i, l := range lines {
		if incRe == nil || !incRe.MatchString(l) {
			continue
		}
		ref, f := "", ""
		for _, l2 := range lines[max(0, i-4):min(len(lines), i+6)] {
			if m := refRe.FindStringSubmatch(l2); m != nil && ref == "" {
				ref = m[1]
			}
			if m := fileRe.FindStringSubmatch(l2); m != nil && f == "" {
				f = m[1]
			}
		}
		incs = append(incs, Include{orS(ref, "HEAD"), orS(f, "?")})
	}
	target := ""
	if m := targetRe.FindStringSubmatch(text); m != nil {
		target = m[1]
	}
	return incs, target
}

// KindOf is the project kind from its included engine files.
func KindOf(files []string) string {
	s := strings.Join(files, " ")
	switch {
	case strings.Contains(s, "app.gitlab-ci.yml"):
		return "app"
	case strings.Contains(s, "package.gitlab-ci.yml"):
		return "package"
	case len(files) > 0:
		return "other"
	}
	return "none"
}

// Inventory is an Ansible inventory: hosts per group (children expanded) and host addresses.
type Inventory struct {
	Groups map[string][]string `json:"groups"`
	IPs    map[string]string   `json:"ips"`
	At     int64               `json:"at"`
}

var (
	invHeadRe = regexp.MustCompile(`^\[([A-Za-z0-9._-]+)(:children|:vars)?\]$`)
	invIPRe   = regexp.MustCompile(`ansible_host=([0-9.]+)`)
	recapRe   = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9][A-Za-z0-9._-]+)\s*:\s*ok=`)
)

// ParseInventory reads an INI inventory.
func ParseInventory(text string) Inventory {
	inv := Inventory{Groups: map[string][]string{}, IPs: map[string]string{}}
	children := map[string][]string{}
	var order []string
	cur, isChildren := "", false
	for _, raw := range splitLines(text) {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		if m := invHeadRe.FindStringSubmatch(line); m != nil {
			cur, isChildren = m[1], m[2] == ":children"
			switch {
			case m[2] == ":vars":
				cur = ""
			case isChildren:
				if _, ok := children[cur]; !ok {
					children[cur] = []string{}
					order = append(order, cur)
				}
			default:
				if _, ok := inv.Groups[cur]; !ok {
					inv.Groups[cur] = []string{}
				}
			}
			continue
		}
		if cur == "" {
			continue
		}
		name := strings.Fields(line)[0]
		if isChildren {
			children[cur] = append(children[cur], name)
			continue
		}
		inv.Groups[cur] = append(inv.Groups[cur], name)
		if m := invIPRe.FindStringSubmatch(line); m != nil {
			inv.IPs[name] = m[1]
		}
	}
	for _, g := range order {
		seen := map[string]bool{}
		for _, h := range inv.Groups[g] {
			seen[h] = true
		}
		for _, k := range children[g] {
			for _, h := range inv.Groups[k] {
				if !seen[h] {
					inv.Groups[g] = append(inv.Groups[g], h)
					seen[h] = true
				}
			}
		}
	}
	return inv
}

// RecapHosts are the hosts of an Ansible PLAY RECAP, sorted.
func RecapHosts(trace string) []string {
	set := map[string]bool{}
	for _, m := range recapRe.FindAllStringSubmatch(trace, -1) {
		set[m[1]] = true
	}
	var out []string
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// HostEnvOf is the environment of a host by the configured patterns ("" when none matches).
func HostEnvOf(host string, envs []HostEnv) string {
	h := strings.ToLower(host)
	for _, e := range envs {
		if e.Re.MatchString(h) {
			return e.Env
		}
	}
	return ""
}

// TargetGroup is the inventory group whose members are exactly these hosts.
func TargetGroup(hosts []string, inv Inventory) string {
	hs := lowerSorted(hosts)
	if len(hs) == 0 {
		return ""
	}
	var names []string
	for g := range inv.Groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		if strings.Join(lowerSorted(inv.Groups[g]), "\n") == strings.Join(hs, "\n") {
			return g
		}
	}
	return ""
}

func lowerSorted(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strings.ToLower(x)
	}
	sort.Strings(out)
	return out
}

// UnitSlug is the service unit slug of a project path (group/repo -> group-repo).
func UnitSlug(path string) string {
	return strings.ToLower(strings.ReplaceAll(strings.Trim(path, "/"), "/", "-"))
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// --- lock watchdog (pure) ----------------------------------------------------------------------------------

// Pipe is a pipeline as the lock watchdog sees it.
type Pipe struct {
	ID           int64
	Ref, Status  string
	BridgeStatus string
	ProdDeploy   string
	ProjectID    int64
	CreatedAt    int64
}

// LockPlan is who holds the deploy lock and what to propose.
type LockPlan struct {
	Holder    *Pipe
	Ahead     []Pipe
	Proposals []Proposal
	Summary   string
}

// PlanLock: cancel the OLDER pipeline (ahead in the queue or holding the deploy bridge); a release pipeline only
// while it holds the lock and its production deploy is still manual.
func (w *Watcher) PlanLock(waiting Pipe, others []Pipe, project string) LockPlan {
	var pid any = waiting.ProjectID
	if waiting.ProjectID == 0 {
		pid = project
	}
	sorted := append([]Pipe{}, others...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var plan LockPlan
	for i := range sorted {
		o := sorted[i]
		if plan.Holder == nil && o.ID != waiting.ID && o.BridgeStatus == "running" {
			h := o
			plan.Holder = &h
		}
		if o.ID < waiting.ID && o.Status == "waiting_for_resource" {
			plan.Ahead = append(plan.Ahead, o)
		}
	}
	for i, o := range plan.Ahead {
		if i >= 5 {
			break
		}
		pr := w.proposal("cancel_pipeline", pid, EnvOf(o.Ref), o.ID,
			fmt.Sprintf("older pipeline %d (%s) is queued ahead of %d on %s", o.ID, o.Ref, waiting.ID, w.Opt.DeployBridge), nil)
		pr.Text = fmt.Sprintf("Cancel the older queued pipeline %d on %s.", o.ID, o.Ref)
		plan.Proposals = append(plan.Proposals, pr)
	}
	if h := plan.Holder; h != nil {
		env := EnvOf(h.Ref)
		reason := fmt.Sprintf("pipeline %d (%s) holds %s", h.ID, h.Ref, w.Opt.DeployBridge)
		if env == "release" {
			reason += "; its prod deploy is " + orS(h.ProdDeploy, "unknown")
		}
		pr := w.proposal("cancel_pipeline", pid, env, h.ID, reason, func(p *Proposal) { p.HoldsLock, p.ProdDeploy = true, h.ProdDeploy })
		if h.ID >= waiting.ID {
			pr.Allowed, pr.Rule = false, "the holder is newer than the waiting pipeline: wait, do not cancel"
		}
		pr.Text = fmt.Sprintf("Cancel pipeline %d on %s, which holds the deploy lock", h.ID, h.Ref)
		if env == "release" && pr.Allowed {
			pr.Text += " (allowed: its production deploy is still manual)"
		}
		if !pr.Allowed {
			pr.Text += " — needs the operator: " + pr.Rule
		}
		plan.Proposals = append(plan.Proposals, pr)
		plan.Summary = fmt.Sprintf("held by pipeline %d (%s)", h.ID, h.Ref)
	} else {
		pr := w.proposal("none", pid, EnvOf(waiting.Ref), nil, "no visible holder: a read-only token cannot read resource_groups (403)", nil)
		pr.Text = "No pipeline of this project visibly holds the lock (phantom holder, or the holder is in the CI engine project). " +
			"The operator clears it: Settings → CI/CD → resource group, or the Rails console Ci::ResourceGroup."
		plan.Proposals = append(plan.Proposals, pr)
		plan.Summary = "no visible holder (phantom or in the CI engine project)"
	}
	if len(plan.Ahead) > 0 {
		plan.Summary += fmt.Sprintf("; %d older pipeline(s) queued ahead", len(plan.Ahead))
	}
	return plan
}

// --- projects ------------------------------------------------------------------------------------------------

func (w *Watcher) listProjects(activeAfter int64, maxPages int) ([]map[string]any, error) {
	q := func() map[string]string {
		m := map[string]string{"archived": "false", "simple": "true", "order_by": "last_activity_at", "sort": "desc"}
		if activeAfter > 0 {
			m["last_activity_after"] = ISO(activeAfter)
		}
		return m
	}
	var out []map[string]any
	if len(w.Opt.Groups) == 0 {
		v := Q("membership", "true")
		for k, x := range q() {
			v.Set(k, x)
		}
		ps, err := w.Opt.GitLab.All(w.ctx, "projects", maxPages, v)
		out = append(out, ps...)
		if err != nil {
			return out, err
		}
	}
	for _, g := range w.Opt.Groups {
		v := Q("include_subgroups", "true")
		for k, x := range q() {
			v.Set(k, x)
		}
		ps, err := w.Opt.GitLab.All(w.ctx, "groups/"+Enc(g)+"/projects", maxPages, v)
		out = append(out, ps...)
		if err != nil {
			return out, err
		}
	}
	var keep []map[string]any
	for _, p := range out {
		if toI(p["id"]) != 0 && str(p["path_with_namespace"]) != "" {
			keep = append(keep, p)
		}
	}
	return keep, nil
}

// Scan refreshes the estate: projects whose activity changed, or that were scanned more than a day (watched) or a
// week (others) ago. Budget-bound; the next run continues where this one stopped.
func (w *Watcher) Scan(force bool) (int, error) {
	projs, err := w.listProjects(0, 10)
	if err != nil && !errors.Is(err, ErrBudget) {
		return 0, err
	}
	for _, p := range projs {
		path := str(p["path_with_namespace"])
		grp := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			grp = path[:i]
		}
		if err := w.exec("INSERT INTO cicd_projects (id, path, grp, default_branch, last_activity) VALUES (?,?,?,?,?) "+
			"ON CONFLICT(id) DO UPDATE SET path=excluded.path, grp=excluded.grp, default_branch=excluded.default_branch, "+
			"last_activity=excluded.last_activity", toI(p["id"]), path, grp, nullS(str(p["default_branch"])), nullS(str(p["last_activity_at"]))); err != nil {
			return 0, err
		}
	}
	if err := w.commit(); err != nil {
		return 0, err
	}
	rs, err := rows(w.tx, "SELECT * FROM cicd_projects ORDER BY COALESCE(scanned_at, 0)")
	if err != nil {
		return 0, err
	}
	done := 0
	for _, r := range rs {
		age := w.now - r.I("scanned_at")
		limit := int64(7 * 86400)
		if r.I("consumer") == 1 {
			limit = 86400
		}
		due := force || r.Null("scanned_at") || r.I("scanned_at") == 0 || age > limit || TS(r.S("last_activity")) > r.I("scanned_at")
		if !due {
			continue
		}
		if err := w.scanProject(r); err != nil {
			if errors.Is(err, ErrBudget) {
				break
			}
			return done, err
		}
		done++
		if err := w.commit(); err != nil {
			return done, err
		}
	}
	if _, err := w.refreshInventory(); err != nil && !errors.Is(err, ErrBudget) {
		return done, err
	}
	w.bump("scanned", done)
	return done, w.commit()
}

func nullS(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (w *Watcher) scanProject(r Row) error {
	pid := r.I("id")
	names := append([]string{}, Envs...)
	if d := r.S("default_branch"); d != "" && !inList(d, names) {
		names = append(names, d)
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	brs, err := w.Opt.GitLab.All(w.ctx, fmt.Sprintf("projects/%d/repository/branches", pid), 1,
		Q("regex", "^("+strings.Join(quoted, "|")+")$"))
	if err != nil {
		return err
	}
	have := map[string]map[string]any{}
	for _, b := range brs {
		have[str(b["name"])] = b
	}
	includes, targets := map[string][]string{}, map[string]string{}
	var files []string
	hasCI := false
	var incOrder []string
	for _, n := range names {
		b, ok := have[n]
		c, _ := b["commit"].(map[string]any)
		if err := w.exec("INSERT INTO cicd_branches (project_id, branch, present, sha, committed_at, title) VALUES (?,?,?,?,?,?) "+
			"ON CONFLICT(project_id, branch) DO UPDATE SET present=excluded.present, sha=excluded.sha, "+
			"committed_at=excluded.committed_at, title=excluded.title",
			pid, n, b2i(ok), nullS(str(c["id"])), nullI(TS(str(c["committed_date"]))), runeHead(str(c["title"]), 200)); err != nil {
			return err
		}
		if !ok {
			continue
		}
		text, found, err := w.Opt.GitLab.FileRaw(w.ctx, pid, ".gitlab-ci.yml", n)
		if err != nil {
			return err
		}
		hasCI = hasCI || found
		incs, target := ParseCI(text, w.incRe)
		if len(incs) > 0 {
			includes[n] = []string{incs[0].Ref, incs[0].File}
			incOrder = append(incOrder, n)
			for _, i := range incs {
				files = append(files, i.File)
			}
		}
		if target != "" {
			targets[n] = target
		}
	}
	kind, consumer := KindOf(files), len(includes) > 0
	if w.incRe == nil { // no engine project configured: every project with a CI file is watched as an app
		consumer = hasCI
		kind = map[bool]string{true: "app", false: "none"}[hasCI]
	}
	refSet := map[string]bool{}
	for _, v := range includes {
		refSet[v[0]] = true
	}
	var refs []string
	for k := range refSet {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	mainFile := ""
	if v, ok := includes[r.S("default_branch")]; ok {
		mainFile = v[1]
	} else if len(incOrder) > 0 {
		mainFile = includes[incOrder[0]][1]
	}
	var prot any
	if consumer {
		pb, err := w.Opt.GitLab.Get(w.ctx, fmt.Sprintf("projects/%d/protected_branches", pid), Q("per_page", "50"))
		if err != nil {
			return err
		}
		if list := maps(pb); pb != nil {
			m := map[string]any{}
			for _, b := range list {
				m[str(b["name"])] = map[string]any{"push": maxLevel(b["push_access_levels"]), "dev_push": devPush(b["push_access_levels"]),
					"merge": maxLevel(b["merge_access_levels"])}
			}
			s, _ := pyDumps(m)
			prot = s
		}
	}
	incJ, _ := pyDumps(includes)
	tgtJ, _ := pyDumps(targets)
	return w.exec("UPDATE cicd_projects SET kind=?, consumer=?, include_ref=?, include_file=?, includes=?, ci_target=?, "+
		"protected=?, scanned_at=? WHERE id=?", kind, b2i(consumer), nullS(strings.Join(refs, ",")), nullS(mainFile), incJ, tgtJ,
		prot, w.now, pid)
}

func maxLevel(v any) int64 {
	var m int64
	for _, a := range maps(v) {
		if l := toI(a["access_level"]); l > m {
			m = l
		}
	}
	return m
}

func devPush(v any) bool {
	for _, a := range maps(v) {
		l := toI(a["access_level"])
		if a["access_level"] == nil {
			l = 99
		}
		if l <= 30 {
			return true
		}
	}
	return false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullI(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func (w *Watcher) refreshInventory() (Inventory, error) {
	var inv Inventory
	ok := GetState(w.tx, "inventory", &inv)
	if ok && w.now-inv.At < 86400 || w.Opt.InventoryFile == "" || w.Opt.EngineID == 0 {
		return inv, nil
	}
	text, found, err := w.Opt.GitLab.FileRaw(w.ctx, w.Opt.EngineID, w.Opt.InventoryFile, w.Opt.InventoryRef)
	if err != nil || !found || text == "" {
		return inv, err
	}
	inv = ParseInventory(text)
	inv.At = w.now
	return inv, SetState(w.tx, "inventory", inv)
}

// --- the watcher ---------------------------------------------------------------------------------------------

func (w *Watcher) consumers() (map[int64]Row, []int64, error) {
	rs, err := rows(w.tx, "SELECT * FROM cicd_projects WHERE consumer=1 AND COALESCE(archived,0)=0")
	if err != nil {
		return nil, nil, err
	}
	m := map[int64]Row{}
	var ids []int64
	for _, r := range rs {
		m[r.I("id")] = r
		ids = append(ids, r.I("id"))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return m, ids, nil
}

// Watch polls the hot projects (active in the last 3 h, unfinished pipelines, and a round-robin share), triages new
// failures, follows the engine project's failed jobs, the stuck pipelines and the deploy hosts.
func (w *Watcher) Watch(backfillDays int) error {
	cons, order, err := w.consumers()
	if err != nil {
		return err
	}
	if len(cons) == 0 {
		fmt.Fprintln(w.Opt.Log, "cicd watch: no watched projects yet (the scan finds them)")
	}
	var hot []int64
	if backfillDays > 0 {
		hot = order
	} else {
		ps, err := w.listProjects(w.now-3*3600, 1)
		if err != nil && !errors.Is(err, ErrBudget) {
			return err
		}
		for _, p := range ps {
			if _, ok := cons[toI(p["id"])]; ok {
				hot = append(hot, toI(p["id"]))
			}
		}
		args := []any{}
		for _, s := range openStates {
			args = append(args, s)
		}
		args = append(args, w.Opt.EngineID, w.now-6*3600)
		rs, err := rows(w.tx, "SELECT DISTINCT project_id FROM cicd_pipelines WHERE status IN ("+qs(len(openStates))+
			") AND project_id != ? AND created_at > ?", args...)
		if err != nil {
			return err
		}
		for _, r := range rs {
			hot = append(hot, r.I("project_id"))
		}
		if len(order) > 0 {
			var pos int
			GetState(w.tx, "rr", &pos)
			n := min(w.Opt.RoundRobin, len(order))
			for i := 0; i < n; i++ {
				hot = append(hot, order[(pos+i)%len(order)])
			}
			if err := SetState(w.tx, "rr", (pos+n)%len(order)); err != nil {
				return err
			}
		}
	}
	seen := map[int64]bool{}
	for _, pid := range hot {
		p, ok := cons[pid]
		if seen[pid] || !ok {
			continue
		}
		seen[pid] = true
		if err := w.pollProject(p, backfillDays); err != nil {
			if errors.Is(err, ErrBudget) {
				break
			}
			return err
		}
		if err := w.commit(); err != nil {
			return err
		}
	}
	for _, stage := range []func() error{w.triagePending, w.pollEngine, w.stuck, w.fillHosts} {
		if err := stage(); err != nil {
			if errors.Is(err, ErrBudget) {
				break
			}
			return err
		}
		if err := w.commit(); err != nil {
			return err
		}
	}
	return w.commit()
}

func qs(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func (w *Watcher) pollProject(p Row, backfillDays int) error {
	var since int64
	pages, per := 1, "50"
	if backfillDays > 0 {
		since, pages, per = w.now-int64(backfillDays)*86400, 5, "100"
	} else {
		since = TS(p.S("poll_cursor"))
		if since == 0 {
			since = w.now - 86400
		}
		since -= 600
	}
	pls, err := w.Opt.GitLab.All(w.ctx, fmt.Sprintf("projects/%d/pipelines", p.I("id")), pages,
		Q("updated_after", ISO(since), "order_by", "updated_at", "sort", "asc", "per_page", per))
	if err != nil {
		return err
	}
	cursor := p.S("poll_cursor")
	for _, pl := range pls {
		if u := str(pl["updated_at"]); u > cursor {
			cursor = u
		}
		env := EnvOf(str(pl["ref"]))
		if env == "" {
			continue
		}
		if err := w.recordPipeline(p, pl, env); err != nil {
			return err
		}
	}
	return w.exec("UPDATE cicd_projects SET poll_cursor=?, polled_at=? WHERE id=?", orS(cursor, ISO(w.now-600)), w.now, p.I("id"))
}

func (w *Watcher) recordPipeline(p Row, pl map[string]any, env string) error {
	id, pid := toI(pl["id"]), p.I("id")
	old, err := one(w.tx, "SELECT * FROM cicd_pipelines WHERE id=?", id)
	if err != nil {
		return err
	}
	st := str(pl["status"])
	if err := w.exec("INSERT INTO cicd_pipelines (id, project_id, ref, env, sha, status, source, created_at, updated_at, seen_at) "+
		"VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at, "+
		"prev_status=CASE WHEN cicd_pipelines.status!=excluded.status THEN cicd_pipelines.status ELSE cicd_pipelines.prev_status END",
		id, pid, str(pl["ref"]), env, nullS(str(pl["sha"])), st, nullS(str(pl["source"])), nullI(TS(str(pl["created_at"]))),
		nullI(TS(str(pl["updated_at"]))), w.now); err != nil {
		return err
	}
	if old != nil && old.S("status") == st {
		return nil
	}
	if old == nil { // a pipeline exists for this ref again: the inferred "push started no pipeline" is over
		if _, err := ResolveFindings(w.tx, "key=?", []any{fmt.Sprintf("variable|%d|%s|no-pipeline", pid, env)},
			fmt.Sprintf("pipeline %d ran", id), w.now); err != nil {
			return err
		}
	}
	w.bump("pipelines", 1)
	ref := str(pl["ref"])
	switch st {
	case "failed", "canceled":
		if err := w.exec("UPDATE cicd_pipelines SET triaged=0 WHERE id=?", id); err != nil {
			return err
		}
	case "success":
		note := fmt.Sprintf("pipeline %d on %s passed", id, ref)
		if old != nil && old.S("status") == "failed" {
			if err := w.exec("UPDATE cicd_findings SET klass='flaky', rule='passed-on-retry' WHERE kind='failure' AND pipeline_id=? AND status='open'", id); err != nil {
				return err
			}
			note += " on retry (flaky)"
		}
		rs, err := ResolveFindings(w.tx, "kind='failure' AND project_id=? AND ref=?", []any{pid, ref}, note, w.now)
		if err != nil {
			return err
		}
		if err := w.resolveIncidents(rs, note); err != nil {
			return err
		}
		if err := w.detectDeploy(p, pl, env); err != nil {
			return err
		}
	}
	if !inList(st, stuckStates) {
		rs, err := ResolveFindings(w.tx, "kind='stuck' AND pipeline_id=?", []any{id}, "pipeline is now "+st, w.now)
		if err != nil {
			return err
		}
		return w.resolveIncidents(rs, fmt.Sprintf("pipeline %d is now %s", id, st))
	}
	return nil
}

func (w *Watcher) resolveIncidents(rs []Row, note string) error {
	for _, r := range rs {
		iid := r.I("incident_id")
		if iid == 0 {
			continue
		}
		still, err := one(w.tx, "SELECT 1 x FROM cicd_findings WHERE incident_id=? AND status='open'", iid)
		if err != nil {
			return err
		}
		inc, err := one(w.tx, "SELECT status FROM incidents WHERE id=?", iid)
		if err != nil {
			return err
		}
		if still != nil || inc == nil || inc.S("status") == "resolved" {
			continue
		}
		if err := w.exec("UPDATE incidents SET status='resolved', resolved_at=?, resolved_by='argus', resolve_note=? WHERE id=?",
			w.now, runeHead(note, 300), iid); err != nil {
			return err
		}
		if err := w.exec("INSERT INTO incident_events (incident_id, at, kind, actor, text) VALUES (?,?,?,?,?)",
			iid, w.now, "auto-resolve", "argus", runeHead(note, 300)); err != nil {
			return err
		}
	}
	return nil
}

// --- triage --------------------------------------------------------------------------------------------------

func (w *Watcher) triagePending() error {
	rs, err := rows(w.tx, "SELECT pl.*, p.path FROM cicd_pipelines pl JOIN cicd_projects p ON p.id=pl.project_id "+
		"WHERE pl.triaged=0 AND pl.status IN ('failed','canceled') ORDER BY "+
		"CASE pl.env WHEN 'release' THEN 0 WHEN 'pre-release' THEN 1 ELSE 2 END, pl.id DESC LIMIT 40")
	if err != nil {
		return err
	}
	for _, pl := range rs {
		if err := w.triagePipeline(pl); err != nil {
			return err
		}
		if err := w.exec("UPDATE cicd_pipelines SET triaged=1 WHERE id=?", pl.I("id")); err != nil {
			return err
		}
		if err := w.commit(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) commitInfo(pid int64, sha string) (string, string, string, error) {
	c, err := w.Opt.GitLab.Obj(w.ctx, fmt.Sprintf("projects/%d/repository/commits/%s", pid, sha), nil)
	if err != nil {
		return "", "", "", err
	}
	mrs, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/repository/commits/%s/merge_requests", pid, sha), nil)
	if err != nil {
		return "", "", "", err
	}
	mr := ""
	for _, m := range mrs {
		if toI(m["iid"]) != 0 {
			mr = fmt.Sprintf("!%d", toI(m["iid"]))
			break
		}
	}
	return runeHead(str(c["author_name"]), 60), mr, runeHead(str(c["title"]), 200), nil
}

type target struct {
	project any
	path    string
	job     map[string]any
}

func (w *Watcher) engine(ds map[string]any) any {
	if p := toI(ds["project_id"]); p != 0 {
		return p
	}
	return w.Opt.EngineID
}

func (w *Watcher) triagePipeline(pl Row) error {
	pid, path, plID := pl.I("project_id"), pl.S("path"), pl.I("id")
	jobs, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/jobs", pid, plID),
		map[string][]string{"per_page": {"50"}, "scope[]": {"failed", "canceled"}})
	if err != nil {
		return err
	}
	bridges, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/bridges", pid, plID), Q("per_page", "20"))
	if err != nil {
		return err
	}
	if err := w.inferExecutor(pid, jobs, bridges); err != nil {
		return err
	}
	var targets []target
	for _, b := range bridges {
		bst := str(b["status"])
		if bst != "failed" && bst != "canceled" {
			continue
		}
		ds, _ := b["downstream_pipeline"].(map[string]any)
		if bst == "failed" && toI(ds["id"]) != 0 {
			if err := w.exec("UPDATE cicd_pipelines SET downstream_id=?, deploy_status=? WHERE id=?", toI(ds["id"]), bst, plID); err != nil {
				return err
			}
			ep := w.engine(ds)
			dj, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%v/pipelines/%d/jobs", ep, toI(ds["id"])),
				map[string][]string{"per_page": {"50"}, "scope[]": {"failed"}})
			if err != nil {
				return err
			}
			for _, j := range dj {
				if af, _ := j["allow_failure"].(bool); !af {
					targets = append(targets, target{ep, path, j})
				}
			}
			if len(dj) == 0 {
				all, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%v/pipelines/%d/jobs", ep, toI(ds["id"])), Q("per_page", "50"))
				if err != nil {
					return err
				}
				var canc map[string]any
				for _, j := range all {
					if str(j["status"]) == "canceled" {
						canc = j
						break
					}
				}
				if canc != nil {
					targets = append(targets, target{ep, path, canc})
				} else {
					bb := map[string]any{}
					for k, v := range b {
						bb[k] = v
					}
					if str(bb["failure_reason"]) == "" {
						bb["failure_reason"] = "downstream_failed_no_job"
					}
					targets = append(targets, target{pid, path, bb})
				}
			}
		} else {
			targets = append(targets, target{pid, path, b})
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return toI(jobs[i]["id"]) < toI(jobs[j]["id"]) })
	for _, j := range jobs {
		if af, _ := j["allow_failure"].(bool); af {
			continue
		}
		if s := str(j["status"]); s != "failed" && s != "canceled" {
			continue
		}
		targets = append(targets, target{pid, path, j})
	}
	if len(targets) == 0 && pl.S("status") == "canceled" {
		return nil
	}
	author, mr, _, err := w.commitInfo(pid, pl.S("sha"))
	if err != nil {
		return err
	}
	for i, t := range targets {
		if i >= 2 {
			break
		}
		j := jobOf(t.job)
		trace := ""
		if j.Status != "canceled" && !j.Downstream {
			if trace, err = w.Opt.GitLab.TraceTail(w.ctx, t.project, j.ID); err != nil {
				return err
			}
		}
		c := Classify(trace, j, t.path)
		if err := w.addFailure(pl, t.path, j, c, trace, author, mr, w.isEngine(fmt.Sprint(t.project))); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) inferExecutor(pid int64, jobs, bridges []map[string]any) error {
	names := map[string]bool{}
	for _, j := range append(append([]map[string]any{}, jobs...), bridges...) {
		names[str(j["name"])] = true
	}
	ex := ""
	switch {
	case names["prepare_systemd"]:
		ex = "systemd"
	case names["build"] && names[w.Opt.DeployBridge]:
		ex = "docker"
	case names["publish"]:
		ex = "package"
	}
	if ex == "" {
		return nil
	}
	return w.exec("UPDATE cicd_projects SET executor=? WHERE id=?", ex, pid)
}

func (w *Watcher) addFailure(pl Row, path string, j Job, c Class, trace, author, mr string, inEngine bool) error {
	env, ref := pl.S("env"), pl.S("ref")
	if c.Klass == "canceled" || c.Rule == "canceled-waiting" { // canceled by a person: counted, not a finding
		w.bump("class:canceled", 1)
		return nil
	}
	f := F{"project_id": pl.I("project_id"), "project": path, "env": env, "ref": ref, "job_id": j.ID, "job_name": j.Name,
		"pipeline_id": pl.I("id"), "author": author}
	props := w.Proposals(c, f)
	where := ""
	if inEngine {
		where = " in the CI engine project"
	}
	sev := orS(sevOf[env], "low")
	if c.Klass == "flaky" {
		sev = "info"
	}
	evidence := c.Line
	if trace != "" {
		evidence = Excerpt(trace, c.Line)
	}
	f["severity"], f["klass"], f["rule"] = sev, c.Klass, c.Rule
	f["title"] = fmt.Sprintf("%s%s failed on %s: %s", j.Name, where, ref, Label[c.Klass])
	f["detail"] = strings.TrimSpace(c.Why + ". " + c.Line)
	f["evidence"], f["proposals"], f["sha"], f["mr"], f["llm"] = evidence, props, pl.S("sha"), mr, c.LLM
	key := fmt.Sprintf("failure|%d|%s|%s|%s", pl.I("project_id"), ref, j.Name, c.Signature)
	fid, isNew, reopened, err := UpsertFinding(w.tx, "failure", key, w.now, f, true)
	if err != nil {
		return err
	}
	w.bump("class:"+c.Klass, 1)
	if _, err := w.linkIncident(fid, path, ref, env, "ci-"+c.Klass); err != nil {
		return err
	}
	if isNew || reopened {
		w.New = append(w.New, fid)
	}
	return nil
}

// linkIncident: only production gets an incident; develop and pre-release failures stay CI/CD findings.
func (w *Watcher) linkIncident(fid int64, path, ref, env, family string) (int64, error) {
	if env != "release" || w.Opt.Incidents == nil {
		return 0, nil
	}
	f, err := one(w.tx, "SELECT * FROM cicd_findings WHERE id=?", fid)
	if err != nil || f == nil {
		return 0, err
	}
	if f.S("kind") != "failure" {
		family = "ci-" + f.S("kind")
	}
	subject := runeHead(strings.ToLower(path+"@"+ref), 120)
	iid, err := w.Opt.Incidents.Attach(w.tx, incidents.Finding{Task: "cicd", At: w.now, Env: env, Severity: f.S("severity"),
		Title: runeHead(fmt.Sprintf("CI/CD %s (%s): %s", path, ref, f.S("title")), 300),
		Text:  runeHead(f.S("title")+". "+f.S("detail"), 2000)}, []incidents.Cand{{DC: "CICD", Subject: subject, Family: family}})
	if err != nil || iid == 0 {
		return 0, err
	}
	return iid, w.exec("UPDATE cicd_findings SET incident_id=? WHERE id=?", iid, fid)
}

// --- the CI engine project -------------------------------------------------------------------------------------

// pollEngine: failed jobs in the engine project whose upstream was not triaged (a deploy played later). The app is
// found from the log (its unit slug) and the environment from the hosts.
func (w *Watcher) pollEngine() error {
	if w.Opt.EngineID == 0 {
		return nil
	}
	jobs, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/jobs", w.Opt.EngineID),
		map[string][]string{"per_page": {"20"}, "scope[]": {"failed"}})
	if err != nil {
		return err
	}
	var seenL []int64
	GetState(w.tx, "dep_jobs", &seenL)
	seen := map[int64]bool{}
	for _, x := range seenL {
		seen[x] = true
	}
	first := len(seen) == 0
	for _, jm := range jobs {
		j := jobOf(jm)
		if seen[j.ID] {
			continue
		}
		seen[j.ID] = true
		plm, _ := jm["pipeline"].(map[string]any)
		ds := toI(plm["id"])
		have, err := one(w.tx, "SELECT 1 x FROM cicd_findings WHERE job_id=?", j.ID)
		if err != nil {
			return err
		}
		if first || have != nil {
			continue
		}
		up, err := one(w.tx, "SELECT pl.*, p.path FROM cicd_pipelines pl JOIN cicd_projects p ON p.id=pl.project_id WHERE pl.downstream_id=?", ds)
		if err != nil {
			return err
		}
		trace, err := w.Opt.GitLab.TraceTail(w.ctx, w.Opt.EngineID, j.ID)
		if err != nil {
			return err
		}
		if up == nil {
			if up, err = w.guessUpstream(trace, ds); err != nil {
				return err
			}
		}
		c := Classify(trace, j, up.S("path"))
		if err := w.addFailure(up, up.S("path"), j, c, trace, j.Username, "", true); err != nil {
			return err
		}
	}
	var all []int64
	for x := range seen {
		all = append(all, x)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	if len(all) > 300 {
		all = all[len(all)-300:]
	}
	return SetState(w.tx, "dep_jobs", all)
}

func (w *Watcher) guessUpstream(trace string, ds int64) (Row, error) {
	env := ""
	for _, h := range RecapHosts(trace) {
		if env = HostEnvOf(h, w.Opt.HostEnvs); env != "" {
			break
		}
	}
	env = orS(env, "release")
	path, pid := orS(w.Opt.EnginePath, "ci-engine"), w.Opt.EngineID
	rs, err := rows(w.tx, "SELECT id, path FROM cicd_projects WHERE consumer=1 ORDER BY length(path) DESC")
	if err != nil {
		return nil, err
	}
	lt := strings.ToLower(trace)
	for _, r := range rs {
		if strings.Contains(lt, UnitSlug(r.S("path"))) {
			path, pid = r.S("path"), r.I("id")
			break
		}
	}
	return Row{"id": ds, "project_id": pid, "path": path, "env": env, "ref": env, "sha": ""}, nil
}

// --- deploy events -----------------------------------------------------------------------------------------------

func (w *Watcher) bridge(pid, plID int64) (map[string]any, error) {
	br, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/bridges", pid, plID), Q("per_page", "20"))
	if err != nil {
		return nil, err
	}
	for _, b := range br {
		if str(b["name"]) == w.Opt.DeployBridge {
			return b, nil
		}
	}
	return nil, nil
}

func (w *Watcher) detectDeploy(p Row, pl map[string]any, env string) error {
	plID := toI(pl["id"])
	b, err := w.bridge(p.I("id"), plID)
	if err != nil || b == nil {
		return err
	}
	dsm, _ := b["downstream_pipeline"].(map[string]any)
	ds := toI(dsm["id"])
	if err := w.exec("UPDATE cicd_pipelines SET deploy_status=?, downstream_id=? WHERE id=?", str(b["status"]), nullI(ds), plID); err != nil {
		return err
	}
	if str(b["status"]) != "success" {
		return nil
	}
	at := TS(str(b["finished_at"]))
	if at == 0 {
		at = TS(str(pl["updated_at"]))
	}
	if at == 0 {
		at = w.now
	}
	prev, err := one(w.tx, "SELECT sha FROM cicd_deploys WHERE project_id=? AND env=? AND at<? ORDER BY at DESC LIMIT 1", p.I("id"), env, at)
	if err != nil {
		return err
	}
	var prevSha any
	if prev != nil {
		prevSha = nullS(prev.S("sha"))
	}
	author := ""
	if u, ok := b["user"].(map[string]any); ok {
		author = str(u["username"])
	}
	fresh := w.now-at < 1800
	vs := "not-run"
	if fresh {
		vs = "pending"
	}
	if err := w.exec("INSERT OR IGNORE INTO cicd_deploys (project_id, project, env, sha, prev_sha, pipeline_id, downstream_id, at, "+
		"author, unit, verify_state) VALUES (?,?,?,?,?,?,?,?,?,?,?)", p.I("id"), p.S("path"), env, nullS(str(pl["sha"])), prevSha,
		plID, nullI(ds), at, author, UnitSlug(p.S("path")), vs); err != nil {
		return err
	}
	w.bump("deploys", 1)
	if !fresh {
		return nil
	}
	d, err := one(w.tx, "SELECT * FROM cicd_deploys WHERE pipeline_id=?", plID)
	if err != nil {
		return err
	}
	return w.deployHosts(d)
}

func (w *Watcher) deployHosts(d Row) error {
	if d == nil || d.I("downstream_id") == 0 || w.Opt.EngineID == 0 {
		return nil
	}
	jobs, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/jobs", w.Opt.EngineID, d.I("downstream_id")), Q("per_page", "20"))
	if err != nil {
		return err
	}
	hosts := []string{}
	for _, j := range jobs {
		if inList(str(j["name"]), w.Opt.DeployJobNames) && str(j["status"]) == "success" {
			trace, err := w.Opt.GitLab.TraceTail(w.ctx, w.Opt.EngineID, toI(j["id"]))
			if err != nil {
				return err
			}
			hosts = append(hosts, RecapHosts(trace)...)
			break
		}
	}
	var inv Inventory
	GetState(w.tx, "inventory", &inv)
	hj, _ := pyDumps(hosts)
	group := TargetGroup(hosts, inv)
	if err := w.exec("UPDATE cicd_deploys SET hosts=?, target_group=? WHERE id=?", hj, group, d.I("id")); err != nil {
		return err
	}
	env := d.S("env")
	for _, h := range hosts { // stage must never reach production
		if HostEnvOf(h, w.Opt.HostEnvs) != "release" || (env != "develop" && env != "pre-release") {
			continue
		}
		pr := w.proposal("set_variable", d.I("project_id"), env, "TARGET_HOSTS", "point TARGET_HOSTS at the env's own inventory group", nil)
		pr.Text = fmt.Sprintf("Operator: set TARGET_HOSTS@%s to the %s group, then redeploy.", env, env)
		fid, isNew, _, err := UpsertFinding(w.tx, "variable", fmt.Sprintf("variable|%d|%s|prod-target", d.I("project_id"), env), w.now, F{
			"project_id": d.I("project_id"), "project": d.S("project"), "env": env, "ref": env, "severity": "high",
			"klass": "variable", "rule": "guardrail-4-target", "title": fmt.Sprintf("%s deploy reached production host %s", env, h),
			"detail":    fmt.Sprintf("TARGET_HOSTS@%s names a production inventory group (%s): stage must never reach production.", env, orS(group, "?")),
			"proposals": []Proposal{pr}, "pipeline_id": d.I("pipeline_id"), "sha": d.S("sha"), "author": d.S("author")}, true)
		if err != nil {
			return err
		}
		if isNew {
			w.New = append(w.New, fid)
		}
	}
	return nil
}

func (w *Watcher) fillHosts() error {
	rs, err := rows(w.tx, "SELECT d.* FROM cicd_deploys d WHERE d.hosts IS NULL AND d.downstream_id IS NOT NULL AND d.id IN "+
		"(SELECT max(id) FROM cicd_deploys GROUP BY project_id, env) ORDER BY d.at DESC LIMIT 8")
	if err != nil {
		return err
	}
	for _, d := range rs {
		if err := w.deployHosts(d); err != nil {
			return err
		}
	}
	return nil
}

// --- lock / stuck watchdog -----------------------------------------------------------------------------------------

func (w *Watcher) stuck() error {
	args := []any{}
	for _, s := range stuckStates {
		args = append(args, s)
	}
	args = append(args, w.now-int64(w.Opt.StuckMinutes)*60)
	rs, err := rows(w.tx, "SELECT pl.*, p.path FROM cicd_pipelines pl JOIN cicd_projects p ON p.id=pl.project_id WHERE pl.status IN ("+
		qs(len(stuckStates))+") AND pl.created_at < ? ORDER BY pl.created_at LIMIT 6", args...)
	if err != nil {
		return err
	}
	for _, wr := range rs {
		pid, id := wr.I("project_id"), wr.I("id")
		fresh, err := w.Opt.GitLab.Obj(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d", pid, id), nil)
		if err != nil {
			return err
		}
		st := orS(str(fresh["status"]), wr.S("status"))
		if !inList(st, stuckStates) {
			if err := w.exec("UPDATE cicd_pipelines SET status=?, updated_at=? WHERE id=?", st, nullI(TS(str(fresh["updated_at"]))), id); err != nil {
				return err
			}
			if _, err := ResolveFindings(w.tx, "kind='stuck' AND pipeline_id=?", []any{id}, "now "+st, w.now); err != nil {
				return err
			}
			continue
		}
		var others []Pipe
		for _, s := range []string{"running", "waiting_for_resource"} {
			ls, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines", pid), Q("status", s, "per_page", "10", "order_by", "id", "sort", "asc"))
			if err != nil {
				return err
			}
			for _, o := range ls {
				others = append(others, Pipe{ID: toI(o["id"]), Ref: str(o["ref"]), Status: s})
			}
		}
		n := 0
		for i := range others {
			o := &others[i]
			if o.Status != "running" || o.ID == id || n >= 3 {
				continue
			}
			n++
			b, err := w.bridge(pid, o.ID)
			if err != nil {
				return err
			}
			o.BridgeStatus = str(b["status"])
			dsm, _ := b["downstream_pipeline"].(map[string]any)
			if ds := toI(dsm["id"]); o.BridgeStatus == "running" && ds != 0 && o.Ref == "release" && w.Opt.EngineID != 0 {
				dj, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/jobs", w.Opt.EngineID, ds), Q("per_page", "20"))
				if err != nil {
					return err
				}
				for _, x := range dj {
					if inList(str(x["name"]), w.Opt.DeployJobNames) {
						o.ProdDeploy = str(x["status"])
						break
					}
				}
			}
		}
		plan := w.PlanLock(Pipe{ID: id, Ref: wr.S("ref"), Status: wr.S("status"), ProjectID: pid, CreatedAt: wr.I("created_at")}, others, wr.S("path"))
		created := wr.I("created_at")
		if created == 0 {
			created = w.now
		}
		mins := (w.now - created) / 60
		env := wr.S("env")
		sev := orS(sevOf[env], "low")
		if env == "release" || (plan.Holder != nil && plan.Holder.Ref == "release") {
			sev = "high"
		}
		klass, detail := "runner", fmt.Sprintf("No runner picked it up for %d min (status %s).", mins, st)
		props := plan.Proposals
		if st == "waiting_for_resource" {
			klass, detail = "lock", "Lock: "+plan.Summary+"."
		} else {
			pr := w.proposal("none", pid, env, nil, "runner", nil)
			pr.Text = "Check the runners (online, tags, concurrency) and the job's tags."
			props = []Proposal{pr}
		}
		fid, isNew, reopened, err := UpsertFinding(w.tx, "stuck", fmt.Sprintf("stuck|%d", id), w.now, F{"project_id": pid,
			"project": wr.S("path"), "env": env, "ref": wr.S("ref"), "severity": sev, "klass": klass, "rule": st,
			"title": fmt.Sprintf("pipeline %d %s for %d min on %s", id, st, mins, wr.S("ref")), "detail": detail,
			"proposals": props, "pipeline_id": id, "sha": wr.S("sha")}, false)
		if err != nil {
			return err
		}
		if isNew || reopened {
			w.New = append(w.New, fid)
			if _, err := w.linkIncident(fid, wr.S("path"), wr.S("ref"), env, "ci-lock"); err != nil {
				return err
			}
		}
		w.bump("stuck", 1)
	}
	return nil
}

// --- status and messages -------------------------------------------------------------------------------------------

// WriteStatus records one task's run in cicd-status.json (the pages' "watched N min ago") and its heartbeat.
func (w *Watcher) WriteStatus(task string, ok bool, note string) error {
	if w.Opt.StateDir == "" {
		return nil
	}
	path := filepath.Join(w.Opt.StateDir, "cicd-status.json")
	st := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &st)
	}
	status := "OK"
	if !ok {
		status = "WARN"
	}
	vars := "inferred (needs the CI variables metadata of ci-vars-meta)"
	if w.Opt.MetaFile != "" {
		if _, err := os.Stat(w.Opt.MetaFile); err == nil {
			vars = "full (ci-vars-meta)"
		}
	}
	st[task] = map[string]any{"at": w.Opt.Now().Unix(), "status": status, "calls": w.Opt.GitLab.Calls, "budget": w.Opt.GitLab.Budget,
		"errors": w.Opt.GitLab.Errors, "counts": w.Counts, "note": runeHead(note, 200),
		"write": "disabled (Argus is read-only: every fix is a proposal)", "vars": vars}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.Opt.StateDir, "heartbeat-cicd-"+task), []byte(fmt.Sprint(w.Opt.Now().Unix())), 0o644)
}

// NotifyFindings posts the new findings: release failures that block (deploy, health, ssh, lock) and stuck
// pipelines as FAIL/WARN at once (at most 6 a run), everything else as INFO. Each finding is posted once.
func (w *Watcher) NotifyFindings(ids []int64, send bool) (int, error) {
	sent, seen := 0, map[int64]bool{}
	for i, fid := range ids {
		if i >= 30 || seen[fid] {
			continue
		}
		seen[fid] = true
		f, err := one(w.tx, "SELECT * FROM cicd_findings WHERE id=?", fid)
		if err != nil {
			return sent, err
		}
		if f == nil || f.I("notified") != 0 || f.S("status") != "open" {
			continue
		}
		kind := f.S("kind")
		blocking := kind == "stuck" || kind == "deploy" || (kind == "failure" && (inList(f.S("job_name"), DeployJobs) ||
			inList(f.S("klass"), []string{"health", "deploy", "ssh", "lock"})))
		rel := f.S("env") == "release"
		sev := "info"
		switch {
		case rel && blocking:
			sev = "page"
		case rel || kind == "stuck" || kind == "deploy":
			sev = "warn"
		}
		prompt := sev == "page" || rel || kind == "stuck" || kind == "deploy"
		if prompt && sent >= 6 {
			prompt, sev = false, "warn"
		}
		var props []Proposal
		json.Unmarshal([]byte(f.S("proposals")), &props)
		facts := []string{"project: " + orS(f.S("project"), "-"), "env: " + orS(f.S("env"), "-")}
		if f.S("author") != "" {
			facts = append(facts, "author: "+strings.TrimSpace(f.S("author")+" "+f.S("mr")))
		}
		if len(props) > 0 {
			facts = append(facts, "proposal: "+runeHead(props[0].Text, 140))
		}
		level := map[string]string{"page": "FAIL", "warn": "WARN", "info": "INFO"}[sev]
		msg := notify.Message{Level: level, Title: "CI/CD: " + f.S("title"), Summary: f.S("detail"), Facts: facts,
			Link: strings.TrimRight(w.Opt.PublicURL, "/") + fmt.Sprintf("/cicd/finding?id=%d", fid), LinkText: "Open finding"}
		if err := w.exec("UPDATE cicd_findings SET notified=1 WHERE id=?", fid); err != nil {
			return sent, err
		}
		if !send || w.Opt.Notify == nil {
			continue
		}
		if err := w.Opt.Notify(msg); err != nil && !errors.Is(err, notify.ErrNotSent) {
			fmt.Fprintf(w.Opt.Log, "warning: CI/CD message for finding %d not sent: %v\n", fid, err)
		}
		if prompt {
			sent++
		}
	}
	return sent, w.commit()
}
