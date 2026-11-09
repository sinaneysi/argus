package googlebot

// detect.go: the detector. It runs after every collect on complete hours only and skips an hour with a gap. A
// finding opens one gw_alerts row per rule and key, updates it while it holds and resolves it when it is gone; a
// notice goes out once when an alert opens and once when it resolves (information alerts: only when they open).
//
// Baseline = the median of the same hour of the week over the previous 4 weeks; with less history, the same hour of
// the previous 7 days; with fewer than 3 values, no baseline and no alert.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// Rules are the thresholds.
var Rules = struct {
	DropRatio                    float64
	DropHours                    int
	P50X, P95X, ErrPct, BlockPct float64
	ClassDrop, ClassStable       float64
	MinReq                       int64
	FakeX                        float64
	RangesDays                   float64
	GapIntervals                 int64
	WastePts                     float64
}{DropRatio: 0.6, DropHours: 2, P50X: 3, P95X: 2, ErrPct: 0.5, BlockPct: 0.5, ClassDrop: 0.5, ClassStable: 0.2, MinReq: 200,
	FakeX: 3, RangesDays: 3, GapIntervals: 2, WastePts: 10}

// Finding is one detector result. It is stored (and cached per day) as the JSON array
// [rule, key, severity, observed, baseline, detail].
type Finding struct {
	Rule, Key, Severity string
	Observed, Baseline  *float64
	Detail              map[string]any
}

func (f Finding) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{f.Rule, f.Key, f.Severity, f.Observed, f.Baseline, f.Detail})
}

func (f *Finding) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 6 {
		return fmt.Errorf("bad finding")
	}
	json.Unmarshal(a[0], &f.Rule)
	json.Unmarshal(a[1], &f.Key)
	json.Unmarshal(a[2], &f.Severity)
	json.Unmarshal(a[3], &f.Observed)
	json.Unmarshal(a[4], &f.Baseline)
	json.Unmarshal(a[5], &f.Detail)
	return nil
}

func fp(v float64) *float64 { return &v }

// hourAgg is one hour's [requests, 5xx, 403+429, histogram] per key.
type hourAgg struct {
	req, s5, s43 int64
	hist         Hist
}

type evaluator struct {
	db      *sql.DB
	primary string
	cache   map[string]map[string]*hourAgg
	covered map[int64]bool
}

func (e *evaluator) isCovered(h int64) bool {
	if v, ok := e.covered[h]; ok {
		return v
	}
	var n sql.NullInt64
	e.db.QueryRow("SELECT sum(width) FROM gw_buckets WHERE edge=? AND ok=1 AND ts>=? AND ts<?", e.primary, h, h+3600).Scan(&n)
	e.covered[h] = n.Int64 >= 3600
	return e.covered[h]
}

// tot is the hour's aggregates of who by column (nil when the hour is not complete).
func (e *evaluator) tot(h int64, by, who string) map[string]*hourAgg {
	key := fmt.Sprint(h, by, who)
	if v, ok := e.cache[key]; ok {
		return v
	}
	if !e.isCovered(h) {
		e.cache[key] = nil
		return nil
	}
	col := map[string]string{"pclass": "pclass", "crawler": "crawler", "edge": "edge", "": "''"}[by]
	out := map[string]*hourAgg{}
	rows, err := e.db.Query("SELECT "+col+", req, s5xx, s403 + s429, hist FROM gw_agg WHERE who=? AND ts>=? AND ts<?", who, h, h+3600)
	if err == nil {
		for rows.Next() {
			var k, hist string
			var req, s5, s43 int64
			rows.Scan(&k, &req, &s5, &s43, &hist)
			a := out[k]
			if a == nil {
				a = &hourAgg{hist: Hist{}}
				out[k] = a
			}
			a.req += req
			a.s5 += s5
			a.s43 += s43
			a.hist.Merge(ParseHist(hist))
		}
		rows.Close()
	}
	e.cache[key] = out
	return out
}

func (e *evaluator) req(h int64, k, by, who string) (float64, bool) {
	t := e.tot(h, by, who)
	if t == nil {
		return 0, false
	}
	if a := t[k]; a != nil {
		return float64(a.req), true
	}
	return 0, true
}

func median(vals []float64) float64 {
	sort.Float64s(vals)
	m := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[m]
	}
	return (vals[m-1] + vals[m]) / 2
}

// baseline is the median of fn over the same hour of the 4 previous weeks, else of the 7 previous days.
func baseline(hour int64, fn func(int64) (float64, bool)) (float64, bool) {
	var vals []float64
	for k := int64(1); k <= 4; k++ {
		if v, ok := fn(hour - k*7*86400); ok {
			vals = append(vals, v)
		}
	}
	if len(vals) < 2 {
		vals = nil
		for k := int64(1); k <= 7; k++ {
			if v, ok := fn(hour - k*86400); ok {
				vals = append(vals, v)
			}
		}
	}
	if len(vals) < 3 {
		return 0, false
	}
	return median(vals), true
}

func roundEven(x float64) float64 { return math.RoundToEven(x) }

// Evaluate is the findings now (complete hours only). daily: also the once-a-day rules.
func (o *Options) Evaluate(db *sql.DB, now int64, daily bool) []Finding {
	e := &evaluator{db: db, primary: o.Groups[0].Label, cache: map[string]map[string]*hourAgg{}, covered: map[int64]bool{}}
	var out []Finding
	hEnd := now - now%3600 // the current hour is incomplete
	var hours []int64
	for k := Rules.DropHours; k > 0; k-- {
		hours = append(hours, hEnd-3600*int64(k))
	}
	all := func(x int64) (float64, bool) { return e.req(x, "", "", "real") }
	// 1. real-Googlebot requests per hour below 60 % of the baseline for 2 consecutive hours
	drop, det := true, [][]any{}
	var lastO, lastB float64
	for _, h := range hours {
		o, ok := all(h)
		b, okb := baseline(h, all)
		if !ok || !okb || b == 0 || !(o < Rules.DropRatio*b) {
			drop = false
		}
		det = append(det, []any{h, int64(o), roundEven(b)})
		lastO, lastB = o, b
	}
	if drop {
		out = append(out, Finding{"request_drop", "all", "warning", fp(lastO), fp(lastB),
			map[string]any{"hours": det, "ratio": round(lastO/lastB, 2)}})
	}
	// 2. latency: p50 over 3x or p95 over 2x the baseline (the last complete hour, merged histogram)
	h := hours[len(hours)-1]
	t := e.tot(h, "", "real")
	if a := t[""]; len(t) > 0 && a != nil && a.req >= Rules.MinReq {
		p50, _ := a.hist.Quantile(0.5)
		p95, _ := a.hist.Quantile(0.95)
		q := func(p float64) func(int64) (float64, bool) {
			return func(x int64) (float64, bool) {
				y := e.tot(x, "", "real")
				if len(y) == 0 || y[""] == nil {
					return 0, false
				}
				return y[""].hist.Quantile(p)
			}
		}
		b50, ok50 := baseline(h, q(0.5))
		b95, ok95 := baseline(h, q(0.95))
		d := map[string]any{"hour": h, "p50": p50, "p95": p95, "base_p50": nullable(b50, ok50), "base_p95": nullable(b95, ok95)}
		if ok50 && b50 != 0 && p50 > Rules.P50X*math.Max(b50, 1) {
			out = append(out, Finding{"latency", "p50", "warning", fp(p50), fp(b50), d})
		}
		if ok95 && b95 != 0 && p95 > Rules.P95X*math.Max(b95, 1) {
			out = append(out, Finding{"latency", "p95", "warning", fp(p95), fp(b95), d})
		}
		// 3. errors and blocks to real Googlebot
		n := float64(a.req)
		if 100*float64(a.s5)/n > Rules.ErrPct {
			out = append(out, Finding{"5xx", "all", "warning", fp(round(100*float64(a.s5)/n, 2)), fp(Rules.ErrPct),
				map[string]any{"hour": h, "req": a.req, "s5xx": a.s5}})
		}
		if 100*float64(a.s43)/n > Rules.BlockPct {
			out = append(out, Finding{"403_429", "all", "warning", fp(round(100*float64(a.s43)/n, 2)), fp(Rules.BlockPct),
				map[string]any{"hour": h, "req": a.req, "s403_429": a.s43}})
		}
	}
	// 4. one path class below half its baseline while the others stay within 20 %
	if tc := e.tot(h, "pclass", "real"); len(tc) > 0 {
		ratios := map[string]float64{}
		for k, v := range tc {
			k := k
			if b, ok := baseline(h, func(x int64) (float64, bool) { return e.req(x, k, "pclass", "real") }); ok && b != 0 && b >= 50 {
				ratios[k] = float64(v.req) / b
			}
		}
		keys := make([]string, 0, len(ratios))
		for k := range ratios {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r, stable, others := ratios[k], true, map[string]any{}
			for _, kk := range keys {
				if kk != k {
					others[kk] = round(ratios[kk], 2)
					if math.Abs(1-ratios[kk]) > Rules.ClassStable {
						stable = false
					}
				}
			}
			if r < 1-Rules.ClassDrop && len(others) > 0 && stable {
				out = append(out, Finding{"class_drop", k, "warning", fp(round(r, 2)), fp(1), map[string]any{"hour": h, "others": others}})
			}
		}
	}
	// 5. fake-Googlebot spike (information only)
	fake := func(x int64) (float64, bool) { return e.req(x, "", "", "fake") }
	if f, ok := fake(h); ok {
		if bf, okb := baseline(h, fake); okb && bf != 0 && f > Rules.FakeX*bf && f >= float64(Rules.MinReq) {
			out = append(out, Finding{"fake_spike", "all", "info", fp(f), fp(bf), map[string]any{"hour": h}})
		}
	}
	// 6. the daily rules (crawl waste), computed once a day. Not in a replay: gw_url holds today's state.
	if daily {
		out = append(out, o.dailyFindings(db, now)...)
	}
	// 7. data quality: the range file's age, the database's size, collector gaps
	ver := state(db, "gw_ranges", "")
	age := 999.0
	if len(ver) >= 10 {
		if d, err := time.Parse("2006-01-02", ver[:10]); err == nil {
			age = float64(now-d.Unix()) / 86400
		}
	}
	if o.overLimit() {
		fi, _ := os.Stat(o.DB)
		out = append(out, Finding{"storage_limit", "googlebot.db", "warning", fp(round(float64(fi.Size())/1e9, 2)),
			fp(round(float64(o.StoreLimit)/1e9, 2)), map[string]any{"note": "per-URL rows are no longer written"}})
	}
	if age > Rules.RangesDays {
		out = append(out, Finding{"ranges_stale", "google", "warning", fp(round(age, 1)), fp(Rules.RangesDays), map[string]any{"version": ver}})
	}
	width := o.width(db)
	rows, err := db.Query("SELECT edge, count(*) FROM gw_buckets WHERE ok=0 AND ts>=? GROUP BY edge ORDER BY edge", now-3600)
	if err == nil {
		for rows.Next() {
			var edge string
			var n int64
			rows.Scan(&edge, &n)
			if n > Rules.GapIntervals {
				out = append(out, Finding{"collector_gap", edge, "warning", fp(float64(n)), fp(float64(Rules.GapIntervals)),
					map[string]any{"last_hour_gaps": n, "interval_s": width}})
			}
		}
		rows.Close()
	}
	return out
}

func nullable(v float64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// WasteRows is the real-Googlebot crawl of one day on the site host and its waste groups.
func WasteRows(db *sql.DB, day, host string) (int64, [][2]any) {
	one := func(q string) int64 {
		var n sql.NullFloat64
		db.QueryRow(q, day, host).Scan(&n)
		return int64(n.Float64)
	}
	tot := one("SELECT sum(hits) FROM gw_url_day WHERE day=? AND host=?")
	parts := [][2]string{
		{"not in a sitemap", "SELECT sum(d.hits) FROM gw_url_day d WHERE d.day=? AND d.host=? AND d.pclass IN " +
			"('pdp','plp','brand','brand_category','facet','landing','static') AND d.url NOT LIKE '%?%' " +
			"AND NOT EXISTS (SELECT 1 FROM gw_sitemap s WHERE s.url=d.url)"},
		{"query-string variants", "SELECT sum(hits) FROM gw_url_day WHERE day=? AND host=? AND url LIKE '%?%'"},
		{"redirects (3xx)", "SELECT sum(s3xx) FROM gw_url_day WHERE day=? AND host=?"},
		{"4xx", "SELECT sum(s4xx) FROM gw_url_day WHERE day=? AND host=?"},
		{"5xx", "SELECT sum(s5xx) FROM gw_url_day WHERE day=? AND host=?"},
		{"'undefined' / script junk", "SELECT sum(hits) FROM gw_url_day WHERE day=? AND host=? " +
			"AND (url LIKE '%undefined%' OR url LIKE '%<script%')"},
	}
	var out [][2]any
	for _, p := range parts {
		out = append(out, [2]any{p[0], one(p[1])})
	}
	return tot, out
}

func wasteShare(tot int64, parts [][2]any) float64 {
	var v, r int64
	for _, p := range parts {
		switch p[0] {
		case "query-string variants":
			v = p[1].(int64)
		case "redirects (3xx)":
			r = p[1].(int64)
		}
	}
	return 100 * float64(v+r) / float64(tot)
}

// dailyFindings are the once-a-day rules, cached in gw_state per day.
func (o *Options) dailyFindings(db *sql.DB, now int64) []Finding {
	loc := o.loc()
	day := Day(now, loc)
	key := "gw_daily:" + day
	if c := state(db, key, ""); c != "" {
		var out []Finding
		if json.Unmarshal([]byte(c), &out) == nil {
			return out
		}
	}
	out := []Finding{}
	yday := Day(now-86400, loc)
	if tot, parts := WasteRows(db, yday, o.SiteHost); tot >= Rules.MinReq {
		share := wasteShare(tot, parts)
		var base []float64
		for k := int64(2); k <= 8; k++ {
			if t2, p2 := WasteRows(db, Day(now-k*86400, loc), o.SiteHost); t2 >= Rules.MinReq {
				base = append(base, wasteShare(t2, p2))
			}
		}
		sort.Float64s(base)
		if len(base) >= 3 && share > base[len(base)/2]+Rules.WastePts {
			out = append(out, Finding{"crawl_waste", "variants+redirects", "info", fp(round(share, 1)), fp(round(base[len(base)/2], 1)),
				map[string]any{"day": yday}})
		}
	}
	db.Exec("DELETE FROM gw_state WHERE k LIKE 'gw_daily:%' AND k<?", "gw_daily:"+Day(now-3*86400, loc))
	b, _ := json.Marshal(out)
	setState(db, key, string(b))
	return out
}

// detect evaluates, stores the alerts and sends the notices.
func (o *Options) detect(ctx context.Context, db *sql.DB, now int64, notify bool) ([]Finding, error) {
	found := o.Evaluate(db, now, true)
	keys := map[[2]string]bool{}
	err := inTx(db, func(tx *sql.Tx) error {
		for _, f := range found {
			keys[[2]string{f.Rule, f.Key}] = true
			det, _ := json.Marshal(f.Detail)
			var id int64
			if tx.QueryRow("SELECT id FROM gw_alerts WHERE rule=? AND key=? AND resolved_ts IS NULL", f.Rule, f.Key).Scan(&id) == nil {
				if _, err := tx.Exec("UPDATE gw_alerts SET last_ts=?, observed=?, baseline=?, detail=? WHERE id=?", now, f.Observed, f.Baseline, string(det), id); err != nil {
					return err
				}
			} else if _, err := tx.Exec("INSERT INTO gw_alerts (rule, key, severity, first_ts, last_ts, observed, baseline, detail) VALUES (?,?,?,?,?,?,?,?)",
				f.Rule, f.Key, f.Severity, now, now, f.Observed, f.Baseline, string(det)); err != nil {
				return err
			}
		}
		rows, err := tx.Query("SELECT id, rule, key FROM gw_alerts WHERE resolved_ts IS NULL")
		if err != nil {
			return err
		}
		var gone []int64
		for rows.Next() {
			var id int64
			var r, k string
			rows.Scan(&id, &r, &k)
			if !keys[[2]string{r, k}] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		for _, id := range gone {
			if _, err := tx.Exec("UPDATE gw_alerts SET resolved_ts=? WHERE id=?", now, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return found, err
	}
	if notify {
		o.send(ctx, db)
	}
	return found, nil
}

// Detect is the detector alone (argus marketing googlebot detect).
func (o *Options) Detect(ctx context.Context) ([]Finding, error) {
	db, err := Open(ctx, o.DB)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return o.detect(ctx, db, o.now(), true)
}

// send posts the notice of every alert that opened or resolved since the last run.
func (o *Options) send(ctx context.Context, db *sql.DB) {
	if o.Notify == nil {
		return
	}
	type row struct {
		id, first      int64
		rule, key, sev string
		obs, base      sql.NullFloat64
		resolved       sql.NullInt64
	}
	var rs []row
	rows, err := db.Query("SELECT id, rule, key, severity, observed, baseline, first_ts, resolved_ts FROM gw_alerts " +
		"WHERE (notified=0) OR (notified=1 AND resolved_ts IS NOT NULL) ORDER BY id")
	if err != nil {
		return
	}
	for rows.Next() {
		var r row
		rows.Scan(&r.id, &r.rule, &r.key, &r.sev, &r.obs, &r.base, &r.first, &r.resolved)
		rs = append(rs, r)
	}
	rows.Close()
	for _, r := range rs {
		res := r.resolved.Valid
		if r.sev == "info" && res {
			db.Exec("UPDATE gw_alerts SET notified=2 WHERE id=?", r.id)
			continue
		}
		var ob, ba *float64
		if r.obs.Valid {
			ob = &r.obs.Float64
		}
		if r.base.Valid {
			ba = &r.base.Float64
		}
		text := Message(r.rule, r.key, r.sev, ob, ba, r.first, res, o.loc(), o.TZLabel)
		if err := o.Notify(ctx, text); err != nil {
			o.logf("googlebot: notice not sent: %s", cutS(err.Error(), 150))
			continue
		}
		n := 1
		if res {
			n = 2
		}
		db.Exec("UPDATE gw_alerts SET notified=? WHERE id=?", n, r.id)
	}
}

var messages = map[string]string{
	"request_drop":  "Googlebot requests/hour below 60%% of baseline: %s vs %s",
	"latency":       "Googlebot %s latency %s ms vs baseline %s ms",
	"5xx":           "5xx to Googlebot %s%% (limit %s%%)",
	"403_429":       "403/429 to Googlebot %s%% (limit %s%%)",
	"class_drop":    "path class drop: ratio %s vs %s",
	"fake_spike":    "fake-Googlebot spike: %s req/h vs %s",
	"ranges_stale":  "Google IP range file %s days old (limit %s)",
	"collector_gap": "googlebot-watch gaps in the last hour: %s (limit %s)",
	"storage_limit": "googlebot.db is %s GB (limit %s GB): per-URL tracking stopped",
	"crawl_waste":   "crawl waste (variants + redirects) %s%% vs baseline %s%%",
}

func fmtNum(v *float64) string {
	if v == nil {
		return "-"
	}
	if *v == math.Trunc(*v) && !math.IsInf(*v, 0) {
		return fmt.Sprintf("%d", int64(*v))
	}
	return fmt.Sprintf("%.2f", *v)
}

// Message is the notice text, built from the structured record only.
func Message(rule, key, sev string, obs, base *float64, first int64, resolved bool, loc *time.Location, tzLabel string) string {
	what := rule + " %s vs %s"
	if m, ok := messages[rule]; ok {
		what = m
	}
	var body string
	if rule == "latency" {
		body = fmt.Sprintf(what, key, fmtNum(obs), fmtNum(base))
	} else {
		body = fmt.Sprintf(what, fmtNum(obs), fmtNum(base))
	}
	head := strings.ToUpper(sev)
	if resolved {
		head = "RESOLVED"
	}
	if loc == nil {
		loc = time.UTC
	}
	if tzLabel == "" {
		tzLabel = loc.String()
	}
	return fmt.Sprintf("%s googlebot-watch [%s] %s (%s): %s, since %s", head, rule, key, tzLabel, body,
		time.Unix(first, 0).In(loc).Format("01-02 15:04"))
}
