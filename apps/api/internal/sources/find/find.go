// Package find is `argus sources find` (ADR-0017 P3): which data source instance to read for a service or a question.
// Deterministic, no model.
//
//  1. The service: an exact service name (group/repo), else the names in the text that the catalogue's own rules
//     resolve (repository name with prefixes and suffixes dropped, group-repo, a service domain from services.json).
//     Longer phrases win over their words. Generic and everyday words never match on their own: "search" or "order"
//     names a service only as the whole input or next to "service", "repo", "project" or "app". Several services are
//     all reported (up to 3), none is guessed.
//  2. Targets per type from source_routes: confirmed first, then proposed with confidence >= MinConf (0.6); ties broken
//     by stale last, then source, kind, locator. Rejected routes and instances not enabled for the agent are never
//     returned. Policy: one confirmed target -> use it; several -> the top 2-3 and say which; none -> a capped fan-out.
//  3. Fan-out (only for a --type asked and one resolved service): hard limits (instances, calls, seconds, output), GET
//     only through the discovery client (pinned address, no redirects, capped answers); the report says "searched N of
//     M instances" and which limit stopped it. Release instances are scanned only for a release question and only
//     when root verified them read-only. Hits go through the catalogue matcher.
//  4. Learning (feeds P4): a fan-out hit for a known service is recorded as a proposed route, origin learned,
//     confidence at most 0.7, with the run id; never confirmed; an existing row (any status) is never touched.
//  5. Audit: one JSON line per find: time, a hash of the question, the service, the chosen targets, the fan-out
//     counts and limits hit. Never the question text or a secret.
package find

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
)

// --- limits ----------------------------------------------------------------------------------------------------------

// Limits are the configurable caps (ARGUS_SOURCES_FIND_*, ARGUS_SOURCES_FANOUT_*), clamped to sane bounds.
type Limits struct {
	MinConf      float64 `json:"min_conf"`
	Top          int     `json:"top"`
	MaxInstances int     `json:"max_instances"`
	MaxCalls     int     `json:"max_calls"`
	MaxSeconds   int     `json:"max_seconds"`
	MaxOutput    int     `json:"max_output"`
}

// LimitKeys are the configuration keys, in Limits order.
var LimitKeys = []string{"ARGUS_SOURCES_FIND_MIN_CONF", "ARGUS_SOURCES_FIND_TOP", "ARGUS_SOURCES_FANOUT_MAX_INSTANCES",
	"ARGUS_SOURCES_FANOUT_MAX_CALLS", "ARGUS_SOURCES_FANOUT_MAX_SECONDS", "ARGUS_SOURCES_FANOUT_MAX_OUTPUT"}

// DefaultLimits are the defaults.
func DefaultLimits() Limits {
	return Limits{MinConf: 0.6, Top: 3, MaxInstances: 5, MaxCalls: 20, MaxSeconds: 60, MaxOutput: 6000}
}

// LimitsFrom reads the limits with get (a configuration lookup; "" means the default), clamped.
func LimitsFrom(get func(key string) string) Limits {
	d := DefaultLimits()
	num := func(k string, def, lo, hi int) int {
		n, err := strconv.Atoi(strings.TrimSpace(get(k)))
		if err != nil {
			return def
		}
		return min(hi, max(lo, n))
	}
	l := d
	if f, err := strconv.ParseFloat(strings.TrimSpace(get(LimitKeys[0])), 64); err == nil && f == f {
		l.MinConf = min(1, max(0, f))
	}
	l.Top = num(LimitKeys[1], d.Top, 1, 3)
	l.MaxInstances = num(LimitKeys[2], d.MaxInstances, 1, 20)
	l.MaxCalls = num(LimitKeys[3], d.MaxCalls, 1, 200)
	l.MaxSeconds = num(LimitKeys[4], d.MaxSeconds, 5, 300)
	l.MaxOutput = num(LimitKeys[5], d.MaxOutput, 1000, 50000)
	return l
}

// --- types -----------------------------------------------------------------------------------------------------------

// TypeDef is a type the agent asks for: the registry type and the catalogue kinds.
type TypeDef struct {
	Source string
	Kinds  []string
}

// Types the agent asks for.
var Types = map[string]TypeDef{
	"gitlab":     {"gitlab", []string{"project"}},
	"k8s":        {"kubernetes", []string{"workload", "service", "ingress", "namespace"}},
	"loki":       {"grafana", []string{"loki"}},
	"prometheus": {"grafana", []string{"prom-job"}},
	"sentry":     {"sentry", []string{"project"}},
	"es":         {"elasticsearch", []string{"index"}},
}

// Order is the order types are reported in.
var Order = []string{"gitlab", "k8s", "loki", "prometheus", "sentry", "es"}

var aliases = map[string]string{"kubernetes": "k8s", "kube": "k8s", "prom": "prometheus", "elasticsearch": "es",
	"opensearch": "es", "logs": "loki"}

// Canon is the type name for t or one of its aliases ("", false when unknown).
func Canon(t string) (string, bool) {
	t = strings.ToLower(strings.TrimSpace(t))
	if a, ok := aliases[t]; ok {
		t = a
	}
	_, ok := Types[t]
	if !ok {
		return "", false
	}
	return t, true
}

func hasKind(td TypeDef, kind string) bool {
	for _, k := range td.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// --- 1. the service --------------------------------------------------------------------------------------------------

// common: words that never name a service on their own in a question (everyday and ops words), on top of the
// catalogue's Generic names. They still match as the whole input ("search") or next to a cue ("the search service").
var common = wordSet(`
a an and are as at be been but by can could did do does down error errors fail failed failing fails find for from
get give has have how i in into is it its last latest list logs log look me my new no not now of off on or our out
please repo repos repository project projects scan show slow some status the their them there this to today up us
was we what when where which who why will with yesterday code commit commits deploy deployed deploys pipeline
pipelines branch branches merge request requests issue issues alert alerts incident incidents metric metrics grafana
loki prometheus gitlab sentry kubernetes k8s cluster pod pods namespace elastic index indices dashboard check
read search order orders user users cart auth mail email account accounts product products price prices
report reports file files config setting settings data time timeout latency memory cpu disk restart restarts
release production prod develop stage staging dev pre pre-release live`)

var (
	cues         = wordSet("service services repo repository project app application svc microservice")
	releaseWords = wordSet("release production prod")
	wordRe       = regexp.MustCompile(`[a-z0-9][a-z0-9._/-]*[a-z0-9]|[a-z0-9]`)
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// QHash is the 12-hex hash of a question (what the audit keeps instead of the text).
func QHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:12]
}

// AboutRelease: the question is about the release environment (release, production, prod, main-*).
func AboutRelease(text string) bool {
	low := strings.ToLower(text)
	for _, w := range wordRe.FindAllString(low, -1) {
		if releaseWords[w] {
			return true
		}
	}
	return strings.Contains(low, "main-")
}

// Resolution is the services found in a text and why.
type Resolution struct {
	Services  []string
	How       map[string]string
	Ambiguous bool
}

// ResolveService finds the service(s) a service name or a question is about.
func ResolveService(text string, svcs *catalogue.Services) Resolution {
	low := strings.ToLower(strings.TrimSpace(text))
	names := map[string]string{}
	for _, n := range svcs.Names {
		names[strings.ToLower(n)] = n
	}
	if n, ok := names[low]; ok && low != "" {
		return Resolution{Services: []string{n}, How: map[string]string{n: "exact service name " + n}}
	}
	words := wordRe.FindAllString(low, -1)
	whole := len(words) == 1
	var found []string
	how := map[string]string{}
	used := map[int]bool{}
	take := func(svc, why string, span ...int) {
		if _, ok := how[svc]; !ok {
			found = append(found, svc)
			how[svc] = why
		}
		for _, i := range span {
			used[i] = true
		}
	}
	strip := func(w string) string { return strings.Trim(w, "./-") }
	// group/repo or a domain written in the text
	for i, w := range words {
		w2 := strip(w)
		if n, ok := names[w2]; ok {
			take(n, "exact service name "+n, i)
			continue
		}
		if d := svcs.Domain(w2); len(d) == 1 {
			take(d[0], w2+" is a domain of the service (services.json)", i)
		}
	}
	// phrases, longest first ("order payment ts" -> order-payment-ts before payment)
	for size := 3; size >= 1; size-- {
	span:
		for i := 0; i+size <= len(words); i++ {
			idx := make([]int, 0, size)
			parts := make([]string, 0, size)
			for j := i; j < i+size; j++ {
				if used[j] {
					continue span
				}
				p := strip(words[j])
				if strings.Contains(p, "/") {
					continue span
				}
				idx = append(idx, j)
				parts = append(parts, p)
			}
			if size == 1 {
				p := parts[0]
				if len(p) < 3 || catalogue.Generic[p] {
					continue
				}
				cued := (i > 0 && cues[strip(words[i-1])]) || (i+1 < len(words) && cues[strip(words[i+1])])
				if common[p] && !(whole || cued) {
					continue
				}
			} else {
				all := true
				for _, p := range parts {
					all = all && (common[p] || catalogue.Generic[p])
				}
				if all {
					continue // "search api", "order logs": everyday words together are not a name
				}
			}
			cand := strings.Join(parts, "-")
			hit, h := svcs.Resolve(cand)
			if len(hit) == 0 {
				continue
			}
			if len(hit) > 1 {
				sort.Strings(hit)
				if len(hit) > 3 {
					hit = hit[:3]
				}
				for _, svc := range hit {
					take(svc, cand+" fits several services", idx...)
				}
				continue
			}
			svc := hit[0]
			repo := svc[strings.LastIndex(svc, "/")+1:]
			why := map[string]string{"full": cand + " matches the service's group and repository",
				"exact-name": cand + " matches the repository name " + repo,
				"name":       cand + " matches " + repo + " once prefixes and suffixes are dropped"}[h]
			take(svc, why, idx...)
		}
	}
	r := Resolution{Services: found, How: how, Ambiguous: len(found) > 1}
	if len(r.Services) > 3 {
		r.Services = r.Services[:3]
	}
	if r.Services == nil {
		r.Services = []string{}
	}
	return r
}

// --- 2. targets from the catalogue -----------------------------------------------------------------------------------

// Instance is one registry entry as the router sees it.
type Instance struct {
	Type, Name, Env, Status string
	Enabled                 bool // enabled for the agent (purpose agent, root enabled it)
}

// Instances are the data sources: ARGUS_DB data_sources when it has rows, else the registry entries.
func Instances(db catalogue.DB, entries map[string]*sources.Entry) map[string]Instance {
	out := map[string]Instance{}
	if db != nil {
		if q, err := db.Query(`SELECT key, type, name, COALESCE(env,''), COALESCE(status,''), COALESCE(enabled,0),
  COALESCE(purpose,'') FROM data_sources`); err == nil {
			for q.Next() {
				var k, t, n, env, st, purpose string
				var en int
				if q.Scan(&k, &t, &n, &env, &st, &en, &purpose) == nil {
					out[k] = Instance{t, n, env, st, en != 0 && purpose == "agent"}
				}
			}
			q.Close()
		}
	}
	if len(out) > 0 {
		return out
	}
	for k, e := range entries {
		if e != nil && strings.Contains(k, "/") {
			t, n, _ := strings.Cut(k, "/")
			out[k] = Instance{t, n, e.Env, e.Status, e.Enabled && e.Purpose == "agent"}
		}
	}
	return out
}

// ToolHint is how the agent reads a target with the read tools Argus has (only Kubernetes, `argus tools kube`).
func ToolHint(typ, source, locator string) string {
	name := source[strings.Index(source, "/")+1:]
	if typ == "k8s" {
		ns, _, _ := strings.Cut(locator, "/")
		return "argus tools kube " + name + " get pods -n " + ns
	}
	return "(" + source + ": no read tool in Argus yet; give the locator to a person)"
}

// Target is one route the agent should read.
type Target struct {
	Source     string  `json:"source"`
	Kind       string  `json:"kind"`
	Locator    string  `json:"locator"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
	Origin     string  `json:"origin"`
	Stale      bool    `json:"stale"`
	Env        string  `json:"env"`
	Tool       string  `json:"tool"`
}

// Hit is one thing a fan-out found for the service.
type Hit struct {
	Source     string  `json:"source"`
	Kind       string  `json:"kind"`
	Locator    string  `json:"locator"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Rule       string  `json:"rule"`
	Why        string  `json:"why"`
	Env        string  `json:"env"`
	Tool       string  `json:"tool"`
}

// Fanout is the honest report of one fan-out.
type Fanout struct {
	Type           string   `json:"type"`
	Instances      int      `json:"instances"`
	Searched       []string `json:"searched"`
	Partial        []string `json:"partial"`
	SkippedRelease []string `json:"skipped_release"`
	NotSearched    []string `json:"not_searched"`
	LimitsHit      []string `json:"limits_hit"`
	Errors         []string `json:"errors"`
	Hits           []Hit    `json:"hits"`
}

// TypeResult is the decision for one type of one service.
type TypeResult struct {
	Decision string   `json:"decision"` // use | several | proposed | none
	Note     string   `json:"note"`
	Targets  []Target `json:"targets"`
	Skipped  []string `json:"skipped"`
	Fanout   *Fanout  `json:"fanout"`
}

// ServiceResult is one service's types.
type ServiceResult struct {
	Service string                 `json:"service"`
	Types   map[string]*TypeResult `json:"types"`
}

// Result is one find.
type Result struct {
	QueryHash       string            `json:"query_hash"`
	Services        []ServiceResult   `json:"services"`
	ReleaseQuestion bool              `json:"release_question"`
	Limits          Limits            `json:"limits"`
	How             map[string]string `json:"how"`
	Learned         int               `json:"learned"`
	Types           []string          `json:"types"`
	Note            string            `json:"note,omitempty"`
}

func tie(rows []catalogue.Route, n int) string {
	if len(rows) > n && rows[n-1].Status == rows[n].Status && rows[n-1].Confidence == rows[n].Confidence {
		return fmt.Sprintf(" (tie at %.2f with %d more: broken by source/kind/locator order)", rows[n].Confidence, len(rows)-n)
	}
	return ""
}

func less(a, b catalogue.Route) bool {
	if (a.Status == "confirmed") != (b.Status == "confirmed") {
		return a.Status == "confirmed"
	}
	if a.Stale != b.Stale {
		return !a.Stale
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Locator < b.Locator
}

// Targets ranks the catalogue's routes of one service per type.
func Targets(db catalogue.DB, service string, want []string, inst map[string]Instance, lim Limits) map[string]*TypeResult {
	var rows []catalogue.Route
	if db != nil {
		rows = catalogue.Routes(db, "service=? AND status != 'rejected'", service)
	}
	out := map[string]*TypeResult{}
	for _, t := range want {
		td := Types[t]
		var conf, prop []catalogue.Route
		skipped := map[string]bool{}
		below := 0
		for _, r := range rows {
			st := inst[r.Source].Type
			if st == "" {
				st, _, _ = strings.Cut(r.Source, "/")
			}
			if st != td.Source || !hasKind(td, r.Kind) {
				continue
			}
			if i, ok := inst[r.Source]; !ok || !i.Enabled {
				skipped[r.Source+" (not enabled for the agent)"] = true
				continue
			}
			switch {
			case r.Status == "confirmed":
				conf = append(conf, r)
			case r.Status == "proposed" && r.Confidence >= lim.MinConf:
				prop = append(prop, r)
			case r.Status == "proposed":
				below++
			}
		}
		sort.SliceStable(conf, func(i, j int) bool { return less(conf[i], conf[j]) })
		sort.SliceStable(prop, func(i, j int) bool { return less(prop[i], prop[j]) })
		d := &TypeResult{Targets: []Target{}, Skipped: []string{}}
		var use []catalogue.Route
		switch {
		case len(conf) == 1:
			d.Decision, use = "use", conf
		case len(conf) > 1:
			use = conf[:min(lim.Top, len(conf))]
			d.Decision = "several"
			d.Note = fmt.Sprintf("%d confirmed: using the top %d", len(conf), len(use)) + tie(conf, len(use))
		case len(prop) > 0:
			use = prop[:min(lim.Top, len(prop))]
			d.Decision = "proposed"
			d.Note = fmt.Sprintf("no confirmed route; %d proposed at >= %.2f: using the top %d (leads: verify before you conclude)",
				len(prop), lim.MinConf, len(use)) + tie(prop, len(use))
		default:
			d.Decision = "none"
			d.Note = fmt.Sprintf("no confirmed route and no proposed one at >= %.2f", lim.MinConf)
			if below > 0 {
				d.Note += fmt.Sprintf(" (%d below)", below)
			}
		}
		for s := range skipped {
			d.Skipped = append(d.Skipped, s)
		}
		sort.Strings(d.Skipped)
		if len(d.Skipped) > 5 {
			d.Skipped = d.Skipped[:5]
		}
		for _, r := range use {
			d.Targets = append(d.Targets, Target{Source: r.Source, Kind: r.Kind, Locator: r.Locator, Status: r.Status,
				Confidence: r.Confidence, Origin: r.Origin, Stale: r.Stale, Env: inst[r.Source].Env,
				Tool: ToolHint(t, r.Source, r.Locator)})
		}
		out[t] = d
	}
	return out
}

// --- 3. fan-out ------------------------------------------------------------------------------------------------------

// Prober looks into one source for the search terms within calls and the time left (discover.Options.Probe).
type Prober func(typ string, e *sources.Entry, terms []string, calls int, left time.Duration) ([]discover.Item, int, error)

// SearchTerms are the names a fan-out searches for (3+ characters, at most 2).
func SearchTerms(service string, svcs *catalogue.Services) []string {
	repo := service[strings.LastIndex(service, "/")+1:]
	var out []string
	for _, t := range []string{catalogue.Simple(repo), svcs.Norm(repo)} {
		if len(t) >= 3 && (len(out) == 0 || out[0] != t) {
			out = append(out, t)
		}
	}
	return out
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// FanOut searches the agent-enabled instances of one type for one service.
func FanOut(typ, service string, svcs *catalogue.Services, lim Limits, releaseOK bool, entries map[string]*sources.Entry,
	probe Prober, clock func() time.Time) *Fanout {
	td := Types[typ]
	var all []string
	el := discover.Eligible(entries)
	for k, e := range el {
		if e.Type == td.Source {
			all = append(all, k)
		}
	}
	sort.Strings(all)
	rep := &Fanout{Type: typ, Instances: len(all), Searched: []string{}, Partial: []string{}, SkippedRelease: []string{},
		NotSearched: []string{}, LimitsHit: []string{}, Errors: []string{}, Hits: []Hit{}}
	var todo []string
	for _, k := range all {
		e := el[k]
		if e.Env == "release" && !(releaseOK && e.Status == "readonly") {
			rep.SkippedRelease = append(rep.SkippedRelease, k)
		} else {
			todo = append(todo, k)
		}
	}
	sort.SliceStable(todo, func(i, j int) bool { return el[todo[i]].Env != "release" && el[todo[j]].Env == "release" })
	terms := SearchTerms(service, svcs)
	if len(terms) == 0 {
		rep.Errors = append(rep.Errors, "no searchable name (3+ characters)")
		return rep
	}
	callsLeft := lim.MaxCalls
	deadline := clock().Add(time.Duration(lim.MaxSeconds) * time.Second)
	for n, k := range todo {
		if n >= lim.MaxInstances {
			rep.NotSearched = append(rep.NotSearched, todo[n:]...)
			rep.LimitsHit = append(rep.LimitsHit, fmt.Sprintf("max instances %d", lim.MaxInstances))
			break
		}
		if callsLeft <= 0 || deadline.Sub(clock()) <= time.Second {
			rep.NotSearched = append(rep.NotSearched, todo[n:]...)
			if callsLeft <= 0 {
				rep.LimitsHit = append(rep.LimitsHit, fmt.Sprintf("max calls %d", lim.MaxCalls))
			} else {
				rep.LimitsHit = append(rep.LimitsHit, fmt.Sprintf("max seconds %d", lim.MaxSeconds))
			}
			break
		}
		e := el[k]
		items, used, err := probe(typ, e, terms, callsLeft, deadline.Sub(clock()))
		callsLeft -= used
		switch {
		case err != nil && discover.IsBudget(err):
			rep.Partial = append(rep.Partial, k)
			rep.LimitsHit = append(rep.LimitsHit, err.Error())
		case err != nil:
			rep.Errors = append(rep.Errors, k+": "+cut(sources.Mask(err.Error()), 160))
			continue
		default:
			rep.Searched = append(rep.Searched, k)
		}
		for _, it := range items {
			if !hasKind(td, it.Kind) {
				continue
			}
			if typ != "gitlab" && !anyIn(terms, catalogue.Simple(it.Key)) {
				continue
			}
			ms, _ := svcs.MatchItem(td.Source, it.Kind, it.Key, it.Attrs)
			for _, m := range ms {
				if m.Service != service {
					continue
				}
				loc, label := catalogue.Locator(it.Kind, it.Key, it.Attrs)
				rep.Hits = append(rep.Hits, Hit{Source: k, Kind: it.Kind, Locator: loc, Label: label, Confidence: m.Confidence,
					Rule: m.Rule, Why: m.Why, Env: e.Env, Tool: ToolHint(typ, k, loc)})
			}
		}
	}
	rep.LimitsHit = dedupe(rep.LimitsHit)
	return rep
}

func anyIn(terms []string, s string) bool {
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// --- 4. learning -----------------------------------------------------------------------------------------------------

// Learn records fan-out hits as proposed routes, origin learned, confidence at most 0.7. It never confirms and never
// touches an existing row of any status. Returns how many were added.
func Learn(db catalogue.DB, service string, hits []Hit, runID string, at int64) (int, error) {
	if err := catalogue.Migrate(db); err != nil {
		return 0, err
	}
	n := 0
	for _, h := range hits {
		var id int64
		if db.QueryRow("SELECT id FROM source_routes WHERE service=? AND source=? AND kind=? AND locator=?",
			service, h.Source, h.Kind, h.Locator).Scan(&id) == nil {
			continue
		}
		why := cut(fmt.Sprintf("run %s: fan-out found it (%s)", runID, h.Why), 300)
		rule := cut("learned-"+h.Rule, 60)
		res, err := db.Exec(`INSERT INTO source_routes (service, source, kind, locator, label, confidence, origin, status, rule,
  evidence, evidence_hash, stale, created_at, updated_at, updated_by) VALUES (?,?,?,?,?,?,'learned','proposed',?,?,?,0,?,?,'sources-find')`,
			service, h.Source, h.Kind, h.Locator, h.Label, min(0.7, h.Confidence), rule, why, catalogue.EvidenceHash(rule, why), at, at)
		if err != nil {
			return n, err
		}
		rid, _ := res.LastInsertId()
		b, _ := json.Marshal(map[string]any{"run": runID, "source": h.Source, "kind": h.Kind})
		db.Exec("INSERT INTO source_route_audit (at, by_user, action, route_id, service, detail) VALUES (?,?,?,?,?,?)",
			at, "sources-find", "learned", rid, service, string(b))
		n++
	}
	return n, nil
}

// --- the command -----------------------------------------------------------------------------------------------------

// Options say where everything is. Probe nil means no fan-out (the web process); Learn needs a writable DB.
type Options struct {
	DB        catalogue.DB              // ARGUS_DB (nil: no catalogue)
	Services  *catalogue.Services       // the service catalogue
	Entries   map[string]*sources.Entry // the registry as root published it (sources-status.json)
	Limits    Limits
	Probe     Prober
	Learn     bool
	RunID     string
	AuditPath string // "" : no audit line
	NoFanout  string // why there is no fan-out here (the API: "fan-out runs in the agent")
	Now       func() time.Time
	Clock     func() time.Time // the fan-out deadline's clock (default Now)
}

// Find routes one service name or question. want: the types asked for (none: every type, and no fan-out); release:
// the caller says it is a release question; fan: fan-out allowed.
func Find(text string, want []string, release, fan bool, o Options) Result {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	clock := o.Clock
	if clock == nil {
		clock = now
	}
	explicit := len(want) > 0
	asked := map[string]bool{}
	for _, t := range want {
		asked[t] = true
	}
	var types []string
	for _, t := range Order {
		if !explicit || asked[t] {
			types = append(types, t)
		}
	}
	svcs := o.Services
	if svcs == nil {
		svcs = catalogue.NewServices(nil, nil)
	}
	rel := release || AboutRelease(text)
	res := ResolveService(text, svcs)
	out := Result{QueryHash: QHash(text), Services: []ServiceResult{}, ReleaseQuestion: rel, Limits: o.Limits, How: res.How,
		Types: types, Note: o.NoFanout}
	inst := Instances(o.DB, o.Entries)
	for _, svc := range res.Services {
		per := Targets(o.DB, svc, types, inst, o.Limits)
		if o.DB == nil {
			for _, d := range per {
				d.Note = "no catalogue"
			}
		}
		for _, t := range types {
			d := per[t]
			if d.Decision != "none" || len(res.Services) != 1 {
				continue
			}
			if o.Probe == nil && o.NoFanout != "" && explicit {
				d.Note += "; " + o.NoFanout
			}
			// fan out only for a type the caller asked for, and only for one service (never for an ambiguous one)
			if !fan || !explicit || o.Probe == nil {
				continue
			}
			rep := FanOut(t, svc, svcs, o.Limits, rel, o.Entries, o.Probe, clock)
			d.Fanout = rep
			if len(rep.Hits) > 0 && o.Learn && o.DB != nil && isName(svcs, svc) {
				n, err := Learn(o.DB, svc, rep.Hits, orManual(o.RunID), now().Unix())
				out.Learned += n
				if err != nil {
					rep.Errors = append(rep.Errors, cut("learned routes not recorded: "+err.Error(), 160))
				}
			}
		}
		out.Services = append(out.Services, ServiceResult{Service: svc, Types: per})
	}
	if o.AuditPath != "" {
		auditLine(o.AuditPath, out, res.Services, o.RunID, now().Unix())
	}
	return out
}

func isName(svcs *catalogue.Services, svc string) bool {
	for _, n := range svcs.Names {
		if n == svc {
			return true
		}
	}
	return false
}

func orManual(run string) string {
	if run == "" {
		return "manual"
	}
	return run
}

// --- 5. audit --------------------------------------------------------------------------------------------------------

// AuditRecord is the audit line of one find: never the question text, never a secret.
func AuditRecord(out Result, services []string, runID string, at int64) map[string]any {
	chosen := []string{}
	fan := map[string]any{}
	for _, s := range out.Services {
		for _, t := range Order {
			d := s.Types[t]
			if d == nil {
				continue
			}
			for _, x := range d.Targets {
				chosen = append(chosen, t+":"+x.Source+":"+x.Status)
			}
			if f := d.Fanout; f != nil {
				fan[s.Service+":"+t] = map[string]any{"searched": len(f.Searched), "of": f.Instances, "hits": len(f.Hits),
					"limits": f.LimitsHit, "skipped_release": len(f.SkippedRelease)}
			}
		}
	}
	return map[string]any{"at": at, "q": out.QueryHash, "service": services, "types": out.Types, "run": runID,
		"release": out.ReleaseQuestion, "learned": out.Learned, "chosen": chosen, "fanout": fan}
}

func auditLine(path string, out Result, services []string, runID string, at int64) {
	b, err := json.Marshal(AuditRecord(out, services, runID, at))
	if err != nil {
		return
	}
	if len(b) > 2000 {
		b = b[:2000]
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// --- text ------------------------------------------------------------------------------------------------------------

// Render is the text the agent reads, capped at Limits.MaxOutput bytes.
func Render(out Result, w io.Writer) {
	var lines []string
	lim := out.Limits
	if len(out.Services) == 0 {
		lines = append(lines, "# no service found in the text (an exact service name, a repository name, group-repo or one of "+
			"its domains). Try a service name: argus sources routes <service>; argus sources list")
	}
	if len(out.Services) > 1 {
		var n []string
		for _, s := range out.Services {
			n = append(n, s.Service)
		}
		lines = append(lines, "# several services fit; nothing is guessed: "+strings.Join(n, ", "))
	}
	rq := ""
	if out.ReleaseQuestion {
		rq = " - this is one"
	}
	lines = append(lines, fmt.Sprintf("# policy: one confirmed target -> use it; several -> the top %d (say which); none -> "+
		"fan-out (max %d instances, %d calls, %d s; release instances only for a release question%s)",
		lim.Top, lim.MaxInstances, lim.MaxCalls, lim.MaxSeconds, rq))
	for _, s := range out.Services {
		lines = append(lines, fmt.Sprintf("## %s: %s", s.Service, out.How[s.Service]))
		for _, t := range Order {
			d := s.Types[t]
			if d == nil {
				continue
			}
			lines = append(lines, t+"\t"+d.Decision+"\t"+d.Note)
			for i, x := range d.Targets {
				extra := ""
				if x.Stale {
					extra += " stale"
				}
				if x.Env == "release" {
					extra += " env=release"
				}
				lines = append(lines, fmt.Sprintf("  %d\t%s\t%s\t%s\t%.2f\t%s%s\t%s", i+1, x.Status, x.Source, x.Locator,
					x.Confidence, x.Origin, extra, x.Tool))
			}
			if len(d.Skipped) > 0 {
				lines = append(lines, "  skipped: "+strings.Join(d.Skipped, ", "))
			}
			f := d.Fanout
			if f == nil {
				continue
			}
			tot := len(f.Searched) + len(f.Partial)
			bits := []string{fmt.Sprintf("fan-out: searched %d of %d instances", len(f.Searched), f.Instances)}
			if len(f.Partial) > 0 {
				bits = append(bits, fmt.Sprintf("%d partly (%s)", len(f.Partial), strings.Join(f.Partial, ", ")))
			}
			if len(f.SkippedRelease) > 0 {
				bits = append(bits, fmt.Sprintf("%d release skipped (%s)", len(f.SkippedRelease), strings.Join(f.SkippedRelease, ", ")))
			}
			if len(f.NotSearched) > 0 {
				ns := f.NotSearched
				if len(ns) > 5 {
					ns = ns[:5]
				}
				bits = append(bits, fmt.Sprintf("%d not searched (%s)", len(f.NotSearched), strings.Join(ns, ", ")))
			}
			if len(f.LimitsHit) > 0 {
				bits = append(bits, "limits hit: "+strings.Join(f.LimitsHit, "; "))
			}
			if len(f.Errors) > 0 {
				es := f.Errors
				if len(es) > 3 {
					es = es[:3]
				}
				bits = append(bits, "errors: "+strings.Join(es, "; "))
			}
			line := "  " + strings.Join(bits, "; ")
			if tot == 0 {
				line += " - nothing was read"
			}
			lines = append(lines, line)
			for i, h := range f.Hits {
				if i >= lim.Top*2 {
					break
				}
				lines = append(lines, fmt.Sprintf("  found %d\t%s\t%s\t%.2f\t%s\t%s", i+1, h.Source, h.Locator, h.Confidence, h.Why, h.Tool))
			}
			if len(f.Hits) == 0 && tot > 0 {
				lines = append(lines, "  found nothing in the instances searched (say so; do not claim the others were checked)")
			}
		}
	}
	if out.Learned > 0 {
		lines = append(lines, fmt.Sprintf("# %d learned route(s) proposed for review (Settings -> Data sources -> Catalogue)", out.Learned))
	}
	body := strings.Join(lines, "\n")
	if c := lim.MaxOutput; c > 0 && len(body) > c {
		body = strings.ToValidUTF8(body[:c], "") + fmt.Sprintf("\n# output capped at %d bytes", c)
	}
	fmt.Fprintln(w, body)
}
