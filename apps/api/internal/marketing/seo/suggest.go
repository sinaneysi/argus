package seo

// suggest.go: the daily SEO suggestions (the Marketing part of the SEO page), rule-based, from Search Console per query
// and page (28 days of final data; the last 7 days against the 21 before) and, when a GA4 property is set, its organic
// landing pages. Each rule turns facts into one suggestion with the numbers that prove it; one set per day in
// seo_suggestions. The model's short summary of them (seo_summary) waits for the shared model runtime.
//
//	ctr       many impressions, a CTR far under what that position usually gets  -> rewrite the title or description
//	striking  position 4-15 with real impressions                                 -> internal links, content
//	falling   clicks of the last 7 days fell against the 21 days before           -> changes, index state
//	bounce    (GA4) an organic landing page with a high bounce rate               -> speed, content match

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/google"
)

const (
	minImpr        = 2000 // 28-day impressions before a query+page counts
	topN           = 15   // suggestions kept per rule
	maxRows        = 100000
	minCTRStriking = 0.002 // under this the query does not match the page's intent
	keepDays       = 90
)

// ctrCurve is the typical organic CTR by position (a rounded industry curve).
var ctrCurve = map[int]float64{1: 0.28, 2: 0.15, 3: 0.10, 4: 0.07, 5: 0.05, 6: 0.04, 7: 0.03, 8: 0.025, 9: 0.02, 10: 0.018}

func expectedCTR(pos float64) float64 {
	p := int(math.RoundToEven(pos))
	p = max(1, min(10, p))
	if v, ok := ctrCurve[p]; ok {
		return v
	}
	return 0.015
}

// Brand is the brand words (| separated): a brand query has many pages and a low CTR is expected there.
type Brand []string

// ParseBrand reads "name|other name".
func ParseBrand(s string) Brand {
	var b Brand
	for _, x := range strings.Split(s, "|") {
		if x = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(x)), " ", ""); x != "" {
			b = append(b, x)
		}
	}
	return b
}

func (b Brand) has(q string) bool {
	ql := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(q), "‌", ""), " ", "")
	for _, x := range b {
		if strings.Contains(ql, x) {
			return true
		}
	}
	return false
}

// Suggestion is one row of seo_suggestions.
type Suggestion struct {
	Rule                                 string
	Rank                                 int
	Page, Query, Title, Evidence, Action string
	Score                                float64
}

type cand struct {
	score       float64
	page, query string
	ev          string
}

// sortCands is Python's sorted(list of tuples, reverse=True): by score, then page, then query, all descending.
func sortCands(c []cand) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].score != c[j].score {
			return c[i].score > c[j].score
		}
		if c[i].page != c[j].page {
			return c[i].page > c[j].page
		}
		if c[i].query != c[j].query {
			return c[i].query > c[j].query
		}
		return c[i].ev > c[j].ev
	})
	if len(c) > topN {
		c = c[:topN]
	}
}

func short(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	s := p.EscapedPath()
	if p.RawQuery != "" {
		s += "?" + p.RawQuery
	}
	if d, err := url.PathUnescape(s); err == nil {
		s = d
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	if s == "" {
		return "/"
	}
	return s
}

func pyPct0(v float64) string { return fmt.Sprintf("%.0f", v) }

// Rules turns the rows into suggestions. qp: query+page rows (28 days); recent / older: page rows of the last 7 days
// and of the 21 days before; ga: GA4 organic landing pages (may be empty).
func Rules(qp, recent, older []google.Row, ga []google.GAPage, brand Brand) []Suggestion {
	var out []Suggestion
	best := map[string]string{}
	bestClicks := map[string]float64{}
	for _, r := range qp {
		q, page := r.Keys[0], r.Keys[1]
		if c, ok := bestClicks[q]; !ok || r.Clicks > c {
			best[q], bestClicks[q] = page, r.Clicks
		}
	}
	var c []cand
	for _, r := range qp {
		q, page := r.Keys[0], r.Keys[1]
		if r.Impressions < minImpr || r.Position > 10 || brand.has(q) || best[q] != page || r.CTR < minCTRStriking {
			continue
		}
		exp := expectedCTR(r.Position)
		if r.CTR < exp/2 {
			lost := r.Impressions * (exp - r.CTR)
			c = append(c, cand{lost, page, q, fmt.Sprintf("%d impressions, CTR %.1f%% at position %.1f (usual ~%s%%): ~%d clicks/28 d missed",
				int64(r.Impressions), r.CTR*100, r.Position, pyPct0(exp*100), int64(lost))})
		}
	}
	sortCands(c)
	for i, x := range c[:min(len(c), topN)] {
		out = append(out, Suggestion{"ctr", i, x.page, x.query, "Low CTR for «" + x.query + "»", x.ev,
			"Rewrite the title and meta description to match this query; add price/stock/offer if true.", x.score})
	}
	var st []cand
	for _, r := range qp {
		q, page := r.Keys[0], r.Keys[1]
		if r.Impressions >= minImpr && r.Position >= 4 && r.Position <= 15 && !brand.has(q) && best[q] == page && r.CTR >= minCTRStriking {
			if gain := r.Impressions * (expectedCTR(3) - r.CTR); gain > 0 {
				st = append(st, cand{gain, page, q, fmt.Sprintf("position %.1f, %d impressions, %d clicks: top 3 would add ~%d clicks/28 d",
					r.Position, int64(r.Impressions), int64(r.Clicks), int64(gain))})
			}
		}
	}
	sortCands(st)
	for i, x := range st[:min(len(st), topN)] {
		out = append(out, Suggestion{"striking", i, x.page, x.query, "Close to top 3: «" + x.query + "»", x.ev,
			"Add internal links from strong pages, improve the content for this query, check page speed.", x.score})
	}
	old := map[string]float64{}
	var oldOrder []string
	for _, r := range older {
		if _, ok := old[r.Keys[0]]; !ok {
			oldOrder = append(oldOrder, r.Keys[0])
		}
		old[r.Keys[0]] = r.Clicks / 21.0
	}
	var fa []cand
	seen := map[string]bool{}
	for _, r := range recent {
		page, now := r.Keys[0], r.Clicks/7.0
		seen[page] = true
		before := old[page]
		if before >= 50 && now < before*0.7 {
			fa = append(fa, cand{before - now, page, "", fmt.Sprintf("%s clicks/day now vs %s before (%s%%)", pyPct0(now), pyPct0(before), pyPct0((now/before-1)*100))})
		}
	}
	for _, page := range oldOrder {
		if before := old[page]; before >= 50 && !seen[page] {
			fa = append(fa, cand{before, page, "", fmt.Sprintf("0 clicks in the last 7 days vs %s/day before", pyPct0(before))})
		}
	}
	sortCands(fa)
	for i, x := range fa[:min(len(fa), topN)] {
		out = append(out, Suggestion{"falling", i, x.page, "", "Clicks falling: " + short(x.page), x.ev,
			"Check recent changes (deploy, title, redirects), its index state and 5xx on /seo; compare competitors.", x.score})
	}
	type bc struct {
		s float64
		g google.GAPage
	}
	var bo []bc
	for _, g := range ga {
		if g.Sessions >= 500 && g.Bounce >= 0.6 && g.Page != "(not set)" && g.Page != "" {
			bo = append(bo, bc{float64(g.Sessions) * g.Bounce, g})
		}
	}
	sort.SliceStable(bo, func(i, j int) bool { return bo[i].s > bo[j].s })
	for i, x := range bo[:min(len(bo), topN)] {
		p := []rune(x.g.Page)
		out = append(out, Suggestion{"bounce", i, x.g.Page, "", "High bounce from Google: " + string(p[:min(len(p), 100)]),
			fmt.Sprintf("%d organic sessions, bounce %s%%, %d key events (28 d)", x.g.Sessions, pyPct0(x.g.Bounce*100), int64(x.g.Events)),
			"Check the page matches the search intent, its load time (p95 on /seo) and the first screen.", x.s})
	}
	return out
}

// SuggestOptions configure the daily suggestions.
type SuggestOptions struct {
	Property   string // the Search Console property
	GAProperty string // GA4 property id ("" = skip the bounce rule)
	Brand      Brand
	Today      time.Time
}

// Suggest fetches the rows, applies the rules and stores today's set (replacing an earlier run of the same day).
// The second value says why the GA4 rule was skipped ("" when it ran or is not configured).
func Suggest(ctx context.Context, db *sql.DB, cl *google.Client, o SuggestOptions) ([]Suggestion, string, error) {
	end := o.Today.AddDate(0, 0, -3) // final data only
	d := func(t time.Time) string { return t.Format("2006-01-02") }
	qp, err := cl.SearchAnalytics(ctx, o.Property, d(end.AddDate(0, 0, -27)), d(end), []string{"query", "page"}, maxRows)
	if err != nil {
		return nil, "", err
	}
	recent, err := cl.SearchAnalytics(ctx, o.Property, d(end.AddDate(0, 0, -6)), d(end), []string{"page"}, maxRows)
	if err != nil {
		return nil, "", err
	}
	older, err := cl.SearchAnalytics(ctx, o.Property, d(end.AddDate(0, 0, -27)), d(end.AddDate(0, 0, -7)), []string{"page"}, maxRows)
	if err != nil {
		return nil, "", err
	}
	var ga []google.GAPage
	note := ""
	if o.GAProperty != "" {
		if ga, err = cl.OrganicLandingPages(ctx, o.GAProperty); err != nil { // GA is optional: the Search Console rules still run
			note = "GA4 skipped: " + strings.SplitN(err.Error(), ":", 2)[0]
			ga = nil
		}
	}
	sug := Rules(qp, recent, older, ga, o.Brand)
	day := d(o.Today)
	tx, err := db.Begin()
	if err != nil {
		return nil, note, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM seo_suggestions WHERE day=?", day); err != nil {
		return nil, note, err
	}
	for _, s := range sug {
		if _, err := tx.Exec("INSERT INTO seo_suggestions VALUES (?,?,?,?,?,?,?,?,?)", day, s.Rule, s.Rank, s.Page, s.Query, s.Title, s.Evidence, s.Action, s.Score); err != nil {
			return nil, note, err
		}
	}
	if _, err := tx.Exec("DELETE FROM seo_suggestions WHERE day < ?", d(o.Today.AddDate(0, 0, -keepDays))); err != nil {
		return nil, note, err
	}
	return sug, note, tx.Commit()
}

// HasDay says whether the day's set is stored.
func HasDay(db *sql.DB, day string) bool {
	var n int
	db.QueryRow("SELECT count(*) FROM seo_suggestions WHERE day=?", day).Scan(&n)
	return n > 0
}

var labels = [][2]string{{"falling", "Clicks falling"}, {"ctr", "Low CTR"}, {"striking", "Close to top 3"}, {"bounce", "High bounce (GA4)"}}

// Message is the chat text of the newest set (top 3 per rule, the summary first when there is one); "" without a set.
func Message(db *sql.DB) string {
	var day sql.NullString
	db.QueryRow("SELECT max(day) FROM seo_suggestions").Scan(&day)
	if !day.Valid {
		return ""
	}
	lines := []string{fmt.Sprintf("SEO suggestions %s (top %d per type, full list on /seo):", day.String, 3)}
	var sm string
	if db.QueryRow("SELECT text FROM seo_summary WHERE day=? AND text<>''", day.String).Scan(&sm) == nil && sm != "" {
		r := []rune(sm)
		lines = append(lines, "Top actions (AI summary of the facts below):", string(r[:min(len(r), 1500)]), "")
	}
	cut := func(s string, n int) string {
		r := []rune(s)
		return string(r[:min(len(r), n)])
	}
	for _, l := range labels {
		rows, err := db.Query("SELECT title, evidence FROM seo_suggestions WHERE day=? AND rule=? ORDER BY rank LIMIT 3", day.String, l[0])
		if err != nil {
			continue
		}
		var part []string
		for rows.Next() {
			var t, e string
			rows.Scan(&t, &e)
			part = append(part, "- "+cut(t, 90)+": "+cut(e, 140))
		}
		rows.Close()
		if len(part) > 0 {
			lines = append(lines, l[1]+":")
			lines = append(lines, part...)
		}
	}
	return strings.Join(lines, "\n")
}
