package googlebot

// seocheck.go: the SEO page's Googlebot section, derived from googlebot.db (no second Loki read). Every 15 minutes,
// for the newest complete 15-minute window and the same window 1 and 7 days earlier, one seo_snapshots row per edge
// host and site host (the path class "blog" gets its own row, host + "/blog": a slower backend must not look like a
// slowdown of the site): requests with a Googlebot user agent from Google's ranges or not (internal subrequests are not
// counted), how many came from Google's ranges, the status classes, p50 and p95 from the merged histogram (seconds),
// and the cache HITs on a cache edge. seo_errors gets the newest hour of 5xx per status, backend and first path
// segment (top 10), with the backend's alias and location from the service map when it knows the address.
//
// Differences from the earlier job, which queried Loki for this: the verified share uses Google's full range lists
// (not one /19); p50/p95 come from the histogram (within about 10 %); every Google crawler line counts whose user agent
// says Googlebot, on every edge group.

import (
	"database/sql"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

const (
	seoWindow    = 900  // seconds per snapshot
	seoErrWindow = 3600 // the newest hour of 5xx
	seoKeepDays  = 90
)

// SEOOptions configure the snapshot.
type SEOOptions struct {
	Primary    string   // the primary edge group: a window counts once every bucket of it is stored
	Hosts      []string // site hosts kept (empty: every host)
	ServiceMap string   // {"backends": {"addr:port": [{"alias", "location", "domain"}]}}; optional
	SiteHost   string   // a backend reference whose domain ends in this site's domain is preferred
}

type seoRow struct {
	total, verified, s2, s3, s4, s5 int64
	hits                            int64
	cache                           bool
	hist                            Hist
}

func (s SEOOptions) hostOK(h string) bool {
	if len(s.Hosts) == 0 {
		return true
	}
	h = strings.TrimSuffix(h, "/blog")
	for _, x := range s.Hosts {
		if strings.EqualFold(strings.TrimSpace(x), h) {
			return true
		}
	}
	return false
}

func window(g *sql.DB, s SEOOptions, t0, t1 int64) (map[[2]string]*seoRow, error) {
	rows, err := g.Query(`SELECT edge, host, who, pclass, upstream, req, s2xx, s3xx, s4xx, s5xx, hist, cache FROM gw_agg
		WHERE ts>=? AND ts<? AND who IN ('real','fake') AND crawler IN ('smartphone','desktop','image','video')`, t0, t1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]*seoRow{}
	for rows.Next() {
		var edge, host, who, pc, up, hist, cache string
		var req, s2, s3, s4, s5 int64
		if err := rows.Scan(&edge, &host, &who, &pc, &up, &req, &s2, &s3, &s4, &s5, &hist, &cache); err != nil {
			return nil, err
		}
		if pc == "blog" {
			host += "/blog"
		}
		if !s.hostOK(host) {
			continue
		}
		k := [2]string{edge, host}
		r := out[k]
		if r == nil {
			r = &seoRow{hist: Hist{}}
			out[k] = r
		}
		r.total += req
		if who == "real" {
			r.verified += req
		}
		r.s2, r.s3, r.s4, r.s5 = r.s2+s2, r.s3+s3, r.s4+s4, r.s5+s5
		r.hist.Merge(ParseHist(hist))
		if up == "cache" {
			r.cache = true
			var c map[string]int64
			json.Unmarshal([]byte(cache), &c)
			r.hits += c["HIT"]
		}
	}
	return out, rows.Err()
}

func coveredBy(g *sql.DB, group string, t0, t1 int64) bool {
	var n sql.NullInt64
	g.QueryRow("SELECT sum(width) FROM gw_buckets WHERE edge=? AND ok=1 AND ts>=? AND ts<?", group, t0, t1).Scan(&n)
	return n.Int64 >= t1-t0
}

// backendOf is (alias, location) of a backend address from the service map ("" when unknown).
func (s SEOOptions) backendOf(m map[string][]map[string]string, up string) (string, string) {
	first := strings.TrimSpace(strings.Split(up, ",")[0])
	refs := m[first]
	dom := strings.TrimPrefix(s.SiteHost, "www.")
	var pick []map[string]string
	for _, r := range refs {
		if dom != "" && strings.HasSuffix(r["domain"], dom) {
			pick = append(pick, r)
		}
	}
	if len(pick) == 0 {
		pick = refs
	}
	if len(pick) == 0 {
		return "", ""
	}
	return pick[0]["alias"], pick[0]["location"]
}

// SEOSnapshot writes the newest window to ops (seo_snapshots, seo_errors); 0 when there is nothing new.
func SEOSnapshot(g, ops *sql.DB, s SEOOptions, now int64) (int64, error) {
	var end sql.NullInt64
	g.QueryRow("SELECT max(ts+width) FROM gw_buckets WHERE edge=? AND ok=1", s.Primary).Scan(&end)
	if !end.Valid {
		return 0, nil
	}
	t := end.Int64 - end.Int64%seoWindow
	if !coveredBy(g, s.Primary, t-seoWindow, t) {
		return 0, nil
	}
	var have sql.NullInt64
	ops.QueryRow("SELECT max(ts) FROM seo_snapshots").Scan(&have)
	if have.Valid && have.Int64 >= t {
		return 0, nil
	}
	snaps := map[int64]map[[2]string]*seoRow{}
	for _, d := range []int64{0, 1, 7} {
		w, err := window(g, s, t-d*86400-seoWindow, t-d*86400)
		if err != nil {
			return 0, err
		}
		snaps[d] = w
	}
	svc := map[string][]map[string]string{}
	if s.ServiceMap != "" {
		if b, err := os.ReadFile(s.ServiceMap); err == nil {
			var m struct {
				Backends map[string][]map[string]string `json:"backends"`
			}
			if json.Unmarshal(b, &m) == nil {
				svc = m.Backends
			}
		}
	}
	type errRow struct {
		status, up, prefix string
		n                  int64
	}
	var errs []errRow
	rows, err := g.Query(`SELECT status, upstream, prefix, sum(n) FROM gw_errors WHERE ts>=? AND ts<? GROUP BY 1,2,3`, t-seoErrWindow, t)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var e errRow
		rows.Scan(&e.status, &e.up, &e.prefix, &e.n)
		errs = append(errs, e)
	}
	rows.Close()
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].n != errs[j].n {
			return errs[i].n > errs[j].n
		}
		return errs[i].status+errs[i].up+errs[i].prefix < errs[j].status+errs[j].up+errs[j].prefix
	})
	if len(errs) > 10 {
		errs = errs[:10]
	}
	tx, err := ops.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for d, w := range snaps {
		for k, r := range w {
			var p50, p95, hits any
			if v, ok := r.hist.Quantile(0.5); ok {
				p50 = round(v/1000, 3)
			}
			if v, ok := r.hist.Quantile(0.95); ok {
				p95 = round(v/1000, 3)
			}
			if r.cache {
				hits = r.hits
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO seo_snapshots (ts, edge, host, offset_d, total, verified, s2xx, s3xx, s4xx, s5xx,
				p50, p95, hits) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, t, k[0], k[1], d, r.total, r.verified, r.s2, r.s3, r.s4, r.s5, p50, p95, hits); err != nil {
				return 0, err
			}
		}
	}
	for _, e := range errs {
		alias, loc := s.backendOf(svc, e.up)
		if _, err := tx.Exec("INSERT INTO seo_errors VALUES (?,?,?,?,?,?,?)", t, e.status, e.up, e.prefix, e.n, alias, loc); err != nil {
			return 0, err
		}
	}
	for _, q := range []string{"DELETE FROM seo_snapshots WHERE ts < ?", "DELETE FROM seo_errors WHERE ts < ?"} {
		if _, err := tx.Exec(q, now-seoKeepDays*86400); err != nil {
			return 0, err
		}
	}
	return t, tx.Commit()
}
