package estate

// hygiene.go: the alert hygiene report (the earlier `hygiene.py collect`). No model, nothing is changed: each finding
// carries a suggested rule change for a person to make.
//
// Inputs: alerts-firing.json (the firing and pending alerts of every site, written by `argus checks alerts`),
// watch-stats.json (optional: per-day counters per "site|alertname|severity" from an alert-room watcher) and, per
// Prometheus site, the targets down for the whole window (max_over_time(up[7d]) == 0, GET only).
//
// Findings:
//
//	test      a test or routing alert still firing (Test*Alert, RoutingTest, label test="true")
//	stale     firing for more than 7 days (never_resolved: and not resolved once in the counted window)
//	dead      a scrape target down for the whole window
//	flapping  fires and resolves again and again (3+ a day, resolves at least 60 % of the time)
//	noisy     a warning or info alert firing 3+ times a day that nobody acts on
//
// Writes hygiene.json (the page /hygiene and the Ideas workspace read it).

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	hygStaleDays  = 7
	hygWindowDays = 7
	hygFlapPerDay = 3
	hygNoisyDay   = 3
	hygDeadQuery  = "max_over_time(up[7d]) == 0"
)

var (
	hygTestRe = regexp.MustCompile(`(?i)^(test|routingtest)|selftest|test.?alert\n?\z`)
	hygDownRe = regexp.MustCompile(`(?i)down|exporter`)
)

func hygISO(v any) (float64, bool) {
	s := py.Head(py.Str(v), 19)
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return float64(t.Unix()), true
		}
	}
	return 0, false
}

func hygIsTest(a *py.OMap) bool {
	labels := a.Map("labels")
	if labels == nil {
		labels = py.NewOMap()
	}
	return hygTestRe.MatchString(py.OrS(a.Get("alertname"), "")) || strings.ToLower(py.Str(labels.GetD("test", ""))) == "true"
}

func hygSuggestion(kind string, a *py.OMap) string {
	switch kind {
	case "test":
		return "Delete this test rule (or give it a 1 h `for:` and a silence); a test alert firing for days teaches the team to ignore pages."
	case "dead":
		return "Remove the target from the scrape config if the host is gone, else fix the exporter; keep one PrometheusExporterDown per target, not a page."
	case "flapping":
		return "Add `for: 10m` (or a longer one) and hysteresis (resolve below a lower threshold) to the rule."
	case "noisy":
		return "Raise the threshold, add `for:`, or route this warning to the digest only; nobody acts on it today."
	case "never":
		return "Never resolved in the counted window. " + hygSuggestion("stale", a)
	}
	if a != nil && hygDownRe.MatchString(py.OrS(a.Get("alertname"), "")) {
		return "Firing for more than a week: remove the dead target or fix the service; add an owner and a silence meanwhile."
	}
	return "Firing for more than a week: fix the cause or change the rule; a page-level alert should never stay firing."
}

// hygNum is a counter value the reference adds up (int, float, and a bool as an int); ok false for anything else.
func hygNum(v any) (any, bool) {
	switch x := v.(type) {
	case int64, float64:
		return x, true
	case bool:
		if x {
			return int64(1), true
		}
		return int64(0), true
	}
	return nil, false
}

func hygAdd(a, b any) any {
	ai, aok := a.(int64)
	bi, bok := b.(int64)
	if aok && bok {
		return ai + bi
	}
	return py.Num(a) + py.Num(b)
}

// hygWindow sums the counters per "site|alertname|severity" over the last days days; the days seen (at least 1).
func hygWindow(stats *py.OMap, now int64, days int) (*py.OMap, int) {
	start := time.Unix(now-int64(days-1)*86400, 0).UTC().Format("2006-01-02")
	out := py.NewOMap()
	seen := map[string]bool{}
	ds := stats.Map("days")
	if ds == nil {
		return out, 1
	}
	for _, day := range ds.Keys() {
		rows := ds.Map(day)
		if day < start || rows == nil {
			continue
		}
		seen[day] = true
		for _, key := range rows.Keys() {
			c := rows.Map(key)
			if c == nil {
				continue
			}
			agg := out.Map(key)
			if agg == nil {
				agg = py.NewOMap()
				out.Set(key, agg)
			}
			for _, k := range c.Keys() {
				if v, ok := hygNum(c.Get(k)); ok {
					agg.Set(k, hygAdd(agg.GetD(k, int64(0)), v))
				}
			}
		}
	}
	return out, max(1, len(seen))
}

func hygRound1(f float64) float64 { return round(f, 1) }

// HygieneAnalyse is the reference's analyse(): the findings of a snapshot, the counters and the dead targets
// (site -> label sets), sorted by kind and then by age or rate.
func HygieneAnalyse(snapshot, stats *py.OMap, dead [][2]any, now int64) []*py.OMap {
	if snapshot == nil {
		snapshot = py.NewOMap()
	}
	if stats == nil {
		stats = py.NewOMap()
	}
	var out []*py.OMap
	counted, ndays := hygWindow(stats, now, hygWindowDays)
	resolved := map[string]bool{}
	for _, k := range counted.Keys() {
		if py.Truthy(counted.Map(k).Get("resolved")) {
			if parts := strings.Split(k, "|"); len(parts) > 1 {
				resolved[parts[1]] = true
			}
		}
	}
	for _, dv := range py.AsList(snapshot.GetD("dcs", []any{})) {
		d := py.AsMap(dv)
		if d == nil {
			continue
		}
		dc := py.OrS(d.Get("dc"), "?")
		for _, av := range py.AsList(d.Get("alerts")) {
			a := py.AsMap(av)
			if a == nil || a.Get("state") != "firing" {
				continue
			}
			sinceF, ok := hygISO(a.Get("activeAt"))
			var since, age any
			ageD := 0.0
			if ok {
				since = sinceF
			}
			if ok && sinceF != 0 {
				ageD = (float64(now) - sinceF) / 86400
				if ageD != 0 {
					age = hygRound1(ageD)
				}
			}
			base := func() *py.OMap {
				return py.NewOMap("dc", dc, "alertname", a.Get("alertname"), "severity", a.Get("severity"), "instance", a.Get("instance"),
					"since", since, "age_days", age)
			}
			if hygIsTest(a) {
				out = append(out, base().Set("kind", "test").Set("suggestion", hygSuggestion("test", a)))
			} else if ok && sinceF != 0 && ageD > hygStaleDays {
				kind := "stale"
				if !resolved[py.Str(a.Get("alertname"))] && ndays >= 2 {
					kind = "never"
				}
				out = append(out, base().Set("kind", "stale").Set("never_resolved", kind == "never").Set("suggestion", hygSuggestion(kind, a)))
			}
		}
	}
	for _, x := range dead {
		for _, m := range x[1].([]map[string]string) {
			var inst, job any
			if v, ok := m["instance"]; ok {
				inst = v
			}
			if v, ok := m["job"]; ok {
				job = v
			}
			out = append(out, py.NewOMap("dc", x[0], "kind", "dead", "alertname", "up == 0", "severity", "", "instance", inst, "job", job,
				"age_days", int64(hygWindowDays), "suggestion", hygSuggestion("dead", nil)))
		}
	}
	for _, key := range counted.Keys() {
		parts := strings.Split(key, "|")
		if len(parts) != 3 {
			continue
		}
		c := counted.Map(key)
		fired, res := c.GetD("firing", int64(0)), c.GetD("resolved", int64(0))
		perDay := py.Num(fired) / float64(ndays)
		base := py.NewOMap("dc", strings.ToLower(parts[0]), "alertname", parts[1], "severity", parts[2], "fired", fired, "resolved", res,
			"per_day", hygRound1(perDay), "window_days", int64(ndays))
		if hygTestRe.MatchString(parts[1]) {
			continue
		}
		sev := parts[2]
		if (py.Truthy(c.GetD("flapping", int64(0))) || perDay >= hygFlapPerDay) && py.Num(res) >= 0.6*py.Num(fired) && py.Num(fired) >= float64(2*ndays) {
			out = append(out, base.Set("kind", "flapping").Set("suggestion", hygSuggestion("flapping", nil)))
		} else if (sev == "warning" || sev == "info" || sev == "none" || sev == "unknown") && perDay >= hygNoisyDay {
			out = append(out, base.Set("kind", "noisy").Set("suggestion", hygSuggestion("noisy", nil)))
		}
	}
	order := map[string]int{"test": 0, "stale": 1, "dead": 2, "flapping": 3, "noisy": 4}
	rank := func(f *py.OMap) int {
		if r, ok := order[f.S("kind")]; ok {
			return r
		}
		return 9
	}
	size := func(f *py.OMap) float64 {
		if v := f.Get("age_days"); py.Truthy(v) {
			return py.Num(v)
		}
		return py.Num(f.Get("per_day"))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return -size(out[i]) < -size(out[j])
	})
	return out
}

// Hygiene writes hygiene.json and returns the summary line.
func (o *Options) Hygiene(ctx context.Context) (string, error) {
	if len(o.Sites) == 0 {
		return "", ErrNoSite
	}
	now := o.now().Unix()
	errs := []any{}
	var dead [][2]any
	for _, s := range o.Sites {
		before := o.Client.Errors
		var ms []map[string]string
		for _, x := range o.Client.Instant(ctx, s, hygDeadQuery) {
			ms = append(ms, x.Metric)
		}
		if o.Client.Errors > before {
			errs = append(errs, py.Head(s.Name+" up: the query failed", 200))
			continue
		}
		dead = append(dead, [2]any{s.Name, ms})
	}
	snap := readJSONMap(filepath.Join(o.StateDir, "alerts-firing.json"))
	stats := readJSONMap(filepath.Join(o.StateDir, "watch-stats.json"))
	findings := HygieneAnalyse(snap, stats, dead, now)
	counts := py.NewOMap()
	list := make([]any, 0, len(findings))
	for _, f := range findings {
		counts.Set(f.S("kind"), py.I(counts.GetD(f.S("kind"), int64(0)))+1)
		list = append(list, f)
	}
	days := []string{}
	if stats != nil && stats.Map("days") != nil {
		days = append(days, stats.Map("days").Keys()...)
	}
	sort.Strings(days)
	days = days[max(0, len(days)-hygWindowDays):]
	sd := make([]any, len(days))
	for i, d := range days {
		sd[i] = d
	}
	doc := py.NewOMap("generated_at", now, "window_days", int64(hygWindowDays), "stale_days", int64(hygStaleDays), "counts", counts,
		"stats_days", sd, "errors", errs, "findings", list)
	if err := writeAtomic(filepath.Join(o.StateDir, "hygiene.json"), []byte(py.Dumps(doc, 1))); err != nil {
		return "", err
	}
	var parts []string
	for _, k := range counts.Keys() {
		parts = append(parts, fmt.Sprintf("%d %s", py.I(counts.Get(k)), k))
	}
	line := strings.Join(parts, ", ")
	if line == "" {
		line = "nothing"
	}
	return fmt.Sprintf("hygiene: %s; %d errors", line, len(errs)), nil
}
