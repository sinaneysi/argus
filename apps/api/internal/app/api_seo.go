package app

// api_seo.go: GET /api/v1/seo and POST /api/v1/seo/send, the Marketing → SEO page. Search health as an operations
// team sees it, read from the evidence database (ARGUS_DB) that external SEO jobs fill; Argus does not collect it:
//
//	seo_snapshots   Googlebot requests per edge and host every 15 min, with the same window 1 and 7 days earlier
//	seo_errors      Googlebot 5xx of the newest hour, per status, backend and first path segment
//	gsc_daily       Search Console clicks, impressions and position per day
//	gsc_urls        Search Console URL Inspection of the key URLs
//	gsc_sitemaps    Search Console sitemap state
//	seo_suggestions rule-based suggestions per day; seo_summary the model's short summary of them
//
// Every table is optional: a missing one is an empty section. "Send" drops one request file in
// ARGUS_SETTINGS_IN/seo-send/<id>.json ({"by", "at"}); the job that watches that folder posts the newest suggestions to
// the chat room. The API never talks to the chat itself.

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const seoChartDays = 7

// seoLevel: FAIL at a 5xx share of 5 % or a p95 3x the 7-day p95, WARN at 1 % or 2x.
func seoLevel(now, week Row) string {
	if now == nil || now.I("total") == 0 {
		return "OK"
	}
	share := float64(now.I("s5xx")) / float64(now.I("total"))
	ratio := 1.0
	if week != nil && week.F("p95") > 0 {
		ratio = now.F("p95") / week.F("p95")
	}
	switch {
	case share >= 0.05 || ratio >= 3:
		return "FAIL"
	case share >= 0.01 || ratio >= 2:
		return "WARN"
	}
	return "OK"
}

func seoPill(level string) apiPill {
	return apiPill{Text: level, Kind: map[string]string{"OK": "ok", "WARN": "warn", "FAIL": "fail", "INFO": "info"}[level]}
}

func seoPct(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) + " %" }
func seoSec(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) + " s" }
func seoNum(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }
func seoPos(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// seoWindow is one window of an edge row: requests, 5xx share and p95; nil when the window has no row.
func seoWindow(r Row) map[string]any {
	if r == nil {
		return nil
	}
	pct := "0 %"
	if t := r.I("total"); t > 0 {
		pct = seoPct(100 * float64(r.I("s5xx")) / float64(t))
	}
	p95 := "-"
	if !r.IsNull("p95") {
		p95 = seoSec(r.F("p95"))
	}
	return map[string]any{"requests": r.I("total"), "share": pct, "p95": p95}
}

// seoChart is a line of (ts, value) with its figures, in the shape of the backup size chart; nil under 2 points.
func seoChart(pts []Pt, f func(float64) string) map[string]any {
	xy := [][2]float64{}
	for _, p := range pts {
		if !p.Nil {
			xy = append(xy, [2]float64{p.T, p.V})
		}
	}
	if len(xy) < 2 {
		return nil
	}
	mn, mx := xy[0][1], xy[0][1]
	for _, p := range xy {
		mn, mx = min(mn, p[1]), max(mx, p[1])
	}
	return map[string]any{"points": xy, "latest": f(xy[len(xy)-1][1]), "min": f(mn), "max": f(mx), "low": mn, "high": mx,
		"from": int64(xy[0][0]), "to": int64(xy[len(xy)-1][0])}
}

func seoUnescape(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

// apiSeoJSON is GET /api/v1/seo.
func apiSeoJSON(qs py.QS, _ *Session) (any, *apiErr) {
	sent := qs.Get("sent") == "1"
	out := map[string]any{"empty": true, "refresh": map[string]any{"seconds": 60}, "banners": []apiBanner{}}
	if sent {
		out["refresh"] = nil // keep the note readable
		out["banners"] = []apiBanner{{Text: "Sent. The suggestions reach the chat room within a minute.", Kind: "ok"}}
	}
	out["suggestions"], out["gsc"] = seoSuggestions(), seoGSC()
	last := Q("SELECT max(ts) AS ts FROM seo_snapshots")
	if len(last) == 0 || last[0].IsNull("ts") {
		out["summary"] = seoSummary(0, nil)
		return out, nil
	}
	ts := last[0].I("ts")
	type key struct{ edge, host string }
	by := map[key]map[int64]Row{}
	var order []key
	for _, r := range Q("SELECT * FROM seo_snapshots WHERE ts=? ORDER BY host, edge, offset_d", ts) {
		k := key{r.S("edge"), r.S("host")}
		if by[k] == nil {
			by[k] = map[int64]Row{}
			order = append(order, k)
		}
		by[k][r.I("offset_d")] = r
	}
	edges, pairs, worst := []map[string]any{}, [][2]Row{}, "OK"
	rank := map[string]int{"OK": 0, "WARN": 1, "FAIL": 2}
	for _, k := range order {
		now, day, week := by[k][0], by[k][1], by[k][7]
		pairs = append(pairs, [2]Row{now, week})
		lv := seoLevel(now, week)
		if rank[lv] > rank[worst] {
			worst = lv
		}
		ver, hit := "-", "-"
		if now != nil && now.I("total") > 0 {
			ver = I2S(100*now.I("verified")/now.I("total")) + " %"
			if !now.IsNull("hits") {
				hit = I2S(100*now.I("hits")/now.I("total")) + " %"
			}
		}
		edges = append(edges, map[string]any{"level": seoPill(lv), "edge": k.edge, "host": k.host, "cache": now != nil && !now.IsNull("hits"),
			"now": seoWindow(now), "verified": ver, "hits": hit, "day": seoWindow(day), "week": seoWindow(week)})
	}
	// 7-day trend over every edge and host: the 5xx share and the worst edge's p95, one point per snapshot.
	var p5, p95 []Pt
	for _, r := range Q(`SELECT ts, sum(total) AS t, sum(s5xx) AS f, max(p95) AS p FROM seo_snapshots
		WHERE offset_d=0 AND ts>=? GROUP BY ts ORDER BY ts`, ts-seoChartDays*86400) {
		share := 0.0
		if r.I("t") > 0 {
			share = 100 * float64(r.I("f")) / float64(r.I("t"))
		}
		p5 = append(p5, Pt{T: r.F("ts"), V: share})
		p95 = append(p95, Pt{T: r.F("ts"), V: r.F("p"), Nil: r.IsNull("p")})
	}
	out["empty"], out["at"], out["level"], out["edges"] = false, ts, seoPill(worst), edges
	out["charts"] = map[string]any{"share": seoChart(p5, seoPct), "p95": seoChart(p95, seoSec)}
	out["summary"] = seoSummary(ts, pairs)
	return out, nil
}

// seoGSC is the Search Console part: daily clicks, impressions and position, and the index state of the key URLs.
func seoGSC() map[string]any {
	days := Q("SELECT date, clicks, impressions, position FROM gsc_daily ORDER BY date DESC LIMIT 35")
	if len(days) == 0 {
		return map[string]any{"empty": true}
	}
	var cl, im, po []Pt
	for i := len(days) - 1; i >= 0; i-- {
		d := days[i]
		t, err := time.ParseInLocation("2006-01-02", d.S("date"), time.Local)
		if err != nil {
			continue
		}
		ts := float64(t.Unix())
		cl = append(cl, Pt{T: ts, V: d.F("clicks")})
		im = append(im, Pt{T: ts, V: d.F("impressions")})
		po = append(po, Pt{T: ts, V: d.F("position")})
	}
	urls := []map[string]any{}
	for _, u := range Q("SELECT * FROM gsc_urls WHERE ts=(SELECT max(ts) FROM gsc_urls) ORDER BY verdict<>'PASS' DESC, url") {
		lv := "OK"
		if u.S("verdict") != "PASS" {
			lv = "WARN"
		}
		urls = append(urls, map[string]any{"level": seoPill(lv), "url": seoUnescape(u.S("url")), "coverage": u.S("coverage"),
			"lastCrawl": u.S("last_crawl"), "crawledAs": u.S("crawled_as")})
	}
	return map[string]any{"empty": false, "clicks": seoChart(cl, seoNum), "impressions": seoChart(im, seoNum),
		"position": seoChart(po, seoPos), "urls": urls}
}

type seoFinding struct {
	Level    apiPill `json:"level"`
	What     string  `json:"what"`
	Evidence string  `json:"evidence"`
	Action   string  `json:"action"`
	rank     int
}

// seoSummary lists the problems an operations team can fix, built only from stored facts (Googlebot per edge, the
// newest hour of Googlebot 5xx per backend, URL index state, sitemaps, Search Console freshness). Worst first.
func seoSummary(ts int64, edges [][2]Row) map[string]any {
	var fs []seoFinding
	add := func(lv, what, ev, act string) {
		fs = append(fs, seoFinding{seoPill(lv), what, ev, act, map[string]int{"FAIL": 0, "WARN": 1, "INFO": 2}[lv]})
	}
	// 1. Googlebot 5xx per backend in the newest hour
	if ts > 0 {
		for _, r := range Q(`SELECT status, upstream, prefix, n, alias FROM seo_errors WHERE ts=? ORDER BY n DESC LIMIT 5`, ts) {
			lv := "WARN"
			if r.I("n") >= 20 {
				lv = "FAIL"
			}
			who := r.S("upstream")
			if r.S("alias") != "" {
				who += " (" + r.S("alias") + ")"
			}
			act := map[string]string{
				"503": "503 comes from the backend or an edge limit. Check the service on " + who + ", its health, and the edge's rate and connection limits for " + r.S("prefix") + ". Google crawls less after repeated 503s.",
				"502": "502 means the edge got no valid answer. Check that the port on " + who + " listens, and look for restarts or out-of-memory kills.",
				"504": "504 means the backend is too slow. Check the latency and the slow calls behind " + who + ". Add capacity or cache this path.",
				"500": "500 is an application error. Read the service log behind " + who + " and open a task for its owner.",
			}[r.S("status")]
			if act == "" {
				act = "Check the backend " + who + " and the edge error log for " + r.S("prefix") + "."
			}
			add(lv, "Googlebot gets "+r.S("status")+" on "+r.S("prefix"), I2S(r.I("n"))+" in the last hour from "+who, act)
		}
	}
	// 2. Googlebot latency against 7 days ago, per edge
	for _, e := range edges {
		now, week := e[0], e[1]
		if now == nil || now.I("total") == 0 || week == nil || week.F("p95") <= 0 || now.F("p95") < 2*week.F("p95") {
			continue
		}
		add("WARN", "Googlebot is slower on "+now.S("edge"), "p95 "+seoSec(now.F("p95"))+" now, "+seoSec(week.F("p95"))+" 7 days ago",
			"Check the backend latency on the edge and the recent deploys on the CI/CD page. Slow pages lower the crawl rate.")
	}
	// 3. key URLs Google does not index; only a failed fetch or a robots block is operations work
	for _, u := range Q(`SELECT url, verdict, coverage, fetch_state, robots FROM gsc_urls WHERE ts=(SELECT max(ts) FROM gsc_urls)
		AND verdict<>'PASS' ORDER BY url LIMIT 5`) {
		lv, act := "INFO", "Not an operations problem. The content team owns it (noindex tag, internal links, crawl priority)."
		fetch := u.S("fetch_state")
		switch {
		case u.S("robots") == "DISALLOWED":
			lv, act = "WARN", "robots.txt blocks it. Fix the Disallow rule where the edge or the app serves the file."
		case fetch != "" && fetch != "SUCCESSFUL" && fetch != "PAGE_FETCH_STATE_UNSPECIFIED":
			lv, act = "WARN", "Google could not fetch it ("+fetch+"). Check the status, the redirects and the firewall or CDN rules for this URL."
		}
		add(lv, "Not indexed: "+seoUnescape(u.S("url")), u.S("coverage"), act)
	}
	// 4. sitemaps Google cannot read or reports problems on
	for _, m := range Q(`SELECT path, errors, warnings, last_downloaded FROM gsc_sitemaps WHERE errors>0 OR warnings>0 ORDER BY errors DESC`) {
		lv := "INFO"
		if m.I("errors") > 0 {
			lv = "WARN"
		}
		add(lv, "Sitemap with issues: "+m.S("path"), I2S(m.I("errors"))+" errors, "+I2S(m.I("warnings"))+" warnings, last read "+m.S("last_downloaded"),
			"Fetch it as Google does: status, gzip, content type, under 50 MB and 50,000 URLs. Check that it lists only URLs that answer 200.")
	}
	// 5. stale Search Console data: the collector or its proxy is down
	if d := Q(`SELECT max(fetched) AS f FROM gsc_daily`); len(d) > 0 && !d[0].IsNull("f") && Now()-d[0].F("f") > 13*3600 {
		add("WARN", "Search Console data is stale", "last read "+Ago(d[0].I("f")), "Check the Search Console job and the proxy it uses.")
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].rank < fs[j].rank })
	fix := 0
	for _, f := range fs {
		if f.Level.Text != "INFO" {
			fix++
		}
	}
	if fs == nil {
		fs = []seoFinding{}
	}
	return map[string]any{"findings": fs, "fix": fix}
}

var seoRuleNames = [][2]string{{"falling", "Clicks falling"}, {"ctr", "Low CTR: rewrite the title or description"},
	{"striking", "Close to the top 3"}, {"bounce", "High bounce from Google"}}

// seoSuggestions is the marketing part: the newest day's rule-based suggestions, the model's summary, and Send.
func seoSuggestions() map[string]any {
	day := Q("SELECT max(day) AS d FROM seo_suggestions")
	if len(day) == 0 || day[0].IsNull("d") {
		return map[string]any{"empty": true}
	}
	d := day[0].S("d")
	groups := []map[string]any{}
	for _, rn := range seoRuleNames {
		rows := []map[string]any{}
		for _, r := range Q("SELECT page, title, evidence, action FROM seo_suggestions WHERE day=? AND rule=? ORDER BY rank LIMIT 5", d, rn[0]) {
			rows = append(rows, map[string]any{"title": r.S("title"), "page": seoUnescape(r.S("page")), "evidence": r.S("evidence"), "action": r.S("action")})
		}
		if len(rows) > 0 {
			groups = append(groups, map[string]any{"key": rn[0], "title": rn[1], "rows": rows})
		}
	}
	lines := []string{}
	if sm := Q("SELECT text FROM seo_summary WHERE day=? AND text<>''", d); len(sm) > 0 {
		for _, l := range strings.Split(sm[0].S("text"), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	return map[string]any{"empty": false, "day": d, "groups": groups, "summary": lines}
}

// seoSend is POST /api/v1/seo/send: one request file in ARGUS_SETTINGS_IN/seo-send.
func seoSend(sess *Session) (any, *apiErr) {
	if len(Q("SELECT 1 FROM seo_suggestions LIMIT 1")) == 0 {
		return nil, &apiErr{400, "No suggestions to send yet."}
	}
	d := filepath.Join(SET_IN, "seo-send")
	if err := os.MkdirAll(d, 0o777); err != nil {
		logf("seo send: %v", err)
		return nil, &apiErr{500, "Not sent."}
	}
	rid := time.Now().Format("20060102150405") + "-" + auth.TokenHex(4)
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(body)); err != nil {
		logf("seo send: %v", err)
		return nil, &apiErr{500, "Not sent."}
	}
	logf("seo suggestions send requested by %s: %s", sess.Username, rid)
	return map[string]any{"ok": true, "redirect": "/seo?sent=1"}, nil
}
