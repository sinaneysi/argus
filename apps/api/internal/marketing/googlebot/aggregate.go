package googlebot

// aggregate.go: one bucket's lines of one edge group -> the rows of gw_agg, gw_url_day, gw_url_var, gw_url and
// gw_errors. Lines are added one at a time (Add), so a bucket is read in chunks and never held whole.

import (
	"database/sql"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Line is one access line from Loki: the edge host label, its time and the tab-joined fields
// ip, status, request time, body bytes, server name, upstream, cache status, request URI, user agent.
type Line struct {
	Label string
	TS    int64
	Text  string
}

type aggKey struct{ label, host, who, crawler, pclass, up string }

// AggRow is one gw_agg row's counters.
type AggRow struct {
	Req    int64
	Status map[string]int64
	LatSum float64
	Ge10s  int64
	Bytes  int64
	Hist   Hist
	Cache  map[string]int64
}

type urlKey struct{ day, host, url string }

type urlRow struct {
	pclass                                  string
	hits, smartphone, desktop, image, other int64
	s2xx, s3xx, s4xx, s5xx, bytes, cacheHit int64
	latSum, latMax                          float64
	firstTS, lastTS                         int64
}

type lastKey struct{ host, url string }

type lastRow struct {
	pclass string
	ts     int64
	status string
	ts200  int64
}

type errKey struct{ status, upstream, prefix string }

// Aggregator collects one bucket of one edge group.
type Aggregator struct {
	Cache   bool // the group is a cache edge (its upstream kind is "cache")
	Nets    Nets
	Classes []Class
	Loc     *time.Location // the day of a per-URL row is the calendar day here

	agg   map[aggKey]*AggRow
	urls  map[urlKey]*urlRow
	vars  map[[4]string]bool
	last  map[lastKey]*lastRow
	errs  map[errKey]int64
	Lines int
	Bad   int
	order []aggKey
}

var statusRe = regexp.MustCompile(`^[1-5][0-9][0-9]$`)

func (a *Aggregator) init() {
	if a.agg == nil {
		a.agg, a.urls, a.vars, a.last, a.errs = map[aggKey]*AggRow{}, map[urlKey]*urlRow{}, map[[4]string]bool{}, map[lastKey]*lastRow{}, map[errKey]int64{}
	}
}

// Day is the calendar day of ts in loc (YYYY-MM-DD).
func Day(ts int64, loc *time.Location) string { return time.Unix(ts, 0).In(loc).Format("2006-01-02") }

// Midnight is the start of a calendar day in loc.
func Midnight(day string, loc *time.Location) (int64, error) {
	t, err := time.ParseInLocation("2006-01-02", day, loc)
	return t.Unix(), err
}

// Add classifies one line; a malformed line only counts in Bad.
func (a *Aggregator) Add(l Line) {
	a.init()
	a.Lines++
	f := strings.Split(l.Text, "\t")
	if len(f) != 9 {
		a.Bad++
		return
	}
	ip, st, rt, bb, sn, up, cache, ru, ua := f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7], f[8]
	sec := 0.0
	if rt != "" && rt != "-" {
		v, err := strconv.ParseFloat(strings.TrimSpace(rt), 64)
		if err != nil {
			a.Bad++
			return
		}
		sec = v
	}
	var nbytes int64
	if bb != "" {
		v, err := strconv.ParseInt(strings.TrimSpace(bb), 10, 64)
		if err != nil {
			a.Bad++
			return
		}
		nbytes = v
	}
	if !statusRe.MatchString(st) {
		a.Bad++
		return
	}
	ua = runeCut(unjson(ua), 300)
	who := whoIs(ip, a.Nets)
	cr := Crawler(ua)
	host := sn
	if host == "" {
		host = "-"
	}
	host = runeCut(strings.ToLower(host), 80)
	u, qh := Normalize(unjson(ru))
	pc := PClass(u, a.Classes)
	first, _, _ := strings.Cut(up, ",")
	uk := upKind(strings.TrimSpace(first), a.Cache)
	cache = strings.ToUpper(cache)
	if cache == "" || cache == "-" || !cacheStates[cache] {
		cache = "none"
	}
	k := aggKey{l.Label, host, who, cr, pc, uk}
	r := a.agg[k]
	if r == nil {
		r = &AggRow{Status: map[string]int64{}, Hist: Hist{}, Cache: map[string]int64{}}
		a.agg[k] = r
		a.order = append(a.order, k)
	}
	r.Req++
	for _, c := range scode(st) {
		r.Status[c]++
	}
	r.LatSum += sec
	if sec >= 10 {
		r.Ge10s++
	}
	r.Bytes += nbytes
	r.Hist[strconv.Itoa(HBin(sec*1000))]++
	r.Cache[cache]++
	if st[0] == '5' && googlebotUA(cr) {
		a.errs[errKey{st, runeCut(up, 60), errPrefix(ru)}]++
	}
	if who != "real" {
		return
	}
	day := Day(l.TS, a.Loc)
	ukey := urlKey{day, host, u}
	ur := a.urls[ukey]
	if ur == nil {
		ur = &urlRow{pclass: pc, firstTS: l.TS, lastTS: l.TS}
		a.urls[ukey] = ur
	}
	ur.hits++
	switch cr {
	case "smartphone":
		ur.smartphone++
	case "desktop":
		ur.desktop++
	case "image":
		ur.image++
	default:
		ur.other++
	}
	switch st[0] {
	case '2':
		ur.s2xx++
	case '3':
		ur.s3xx++
	case '4':
		ur.s4xx++
	case '5':
		ur.s5xx++
	}
	ur.bytes += nbytes
	ur.latSum += sec
	ur.latMax = math.Max(ur.latMax, sec)
	if cache == "HIT" {
		ur.cacheHit++
	}
	ur.firstTS, ur.lastTS = min(ur.firstTS, l.TS), max(ur.lastTS, l.TS)
	if qh != "" {
		a.vars[[4]string{day, host, u, qh}] = true
	}
	lk := lastKey{host, u}
	prev := a.last[lk]
	switch {
	case prev == nil || l.TS >= prev.ts:
		ok200 := int64(0)
		if st == "200" {
			ok200 = l.TS
		}
		if prev != nil {
			ok200 = max(ok200, prev.ts200)
		}
		a.last[lk] = &lastRow{pc, l.TS, st, ok200}
	case st == "200":
		prev.ts200 = max(prev.ts200, l.TS)
	}
}

// googlebotUA: the crawler types whose user agent says Googlebot (what the SEO page counts).
func googlebotUA(cr string) bool {
	return cr == "smartphone" || cr == "desktop" || cr == "image" || cr == "video"
}

// Counts is (gw_agg rows, per-URL rows, query variants, URLs with a last crawl).
func (a *Aggregator) Counts() (int, int, int, int) {
	return len(a.agg), len(a.urls), len(a.vars), len(a.last)
}

// DropURLs forgets the per-URL rows (the database is over its size limit; the aggregates go on).
func (a *Aggregator) DropURLs() {
	a.urls, a.vars, a.last = map[urlKey]*urlRow{}, map[[4]string]bool{}, map[lastKey]*lastRow{}
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(x*p) / p
}

// execer is a transaction or a database.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Store writes the bucket's rows (inside the caller's transaction).
func (a *Aggregator) Store(tx execer, ts, width int64, group string) error {
	a.init()
	cols := "ts, width, edge, host, who, crawler, pclass, upstream, req, " + strings.Join(Status, ", ") + ", lat_sum, ge10s, bytes, hist, cache"
	ins := "INSERT INTO gw_agg (" + cols + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", 9+len(Status)+5), ",") + ")"
	for _, k := range a.order {
		r := a.agg[k]
		args := []any{ts, width, k.label, k.host, k.who, k.crawler, k.pclass, k.up, r.Req}
		for _, s := range Status {
			args = append(args, r.Status[s])
		}
		args = append(args, round(r.LatSum, 3), r.Ge10s, r.Bytes, compact(r.Hist), compact(r.Cache))
		if _, err := tx.Exec(ins, args...); err != nil {
			return err
		}
	}
	ucols := []string{"hits", "smartphone", "desktop", "image", "other", "s2xx", "s3xx", "s4xx", "s5xx", "bytes", "lat_sum", "cache_hit"}
	var set []string
	for _, c := range ucols {
		set = append(set, c+"="+c+"+excluded."+c)
	}
	uq := "INSERT INTO gw_url_day (day, host, url, pclass, " + strings.Join(ucols, ", ") + ", lat_max, first_ts, last_ts) VALUES (?,?,?,?," +
		strings.TrimSuffix(strings.Repeat("?,", len(ucols)), ",") + ",?,?,?) ON CONFLICT(day, host, url) DO UPDATE SET " + strings.Join(set, ", ") +
		", lat_max=max(lat_max, excluded.lat_max), first_ts=min(first_ts, excluded.first_ts), last_ts=max(last_ts, excluded.last_ts)"
	for _, k := range sortedURLKeys(a.urls) {
		v := a.urls[k]
		if _, err := tx.Exec(uq, k.day, k.host, k.url, v.pclass, v.hits, v.smartphone, v.desktop, v.image, v.other, v.s2xx, v.s3xx, v.s4xx,
			v.s5xx, v.bytes, v.latSum, v.cacheHit, v.latMax, v.firstTS, v.lastTS); err != nil {
			return err
		}
	}
	for k := range a.vars {
		if _, err := tx.Exec("INSERT OR IGNORE INTO gw_url_var VALUES (?,?,?,?)", k[0], k[1], k[2], k[3]); err != nil {
			return err
		}
	}
	lq := "INSERT INTO gw_url VALUES (?,?,?,?,?,?) ON CONFLICT(host, url) DO UPDATE SET pclass=excluded.pclass, " +
		"last_status=CASE WHEN excluded.last_ts>=coalesce(last_ts,0) THEN excluded.last_status ELSE last_status END, " +
		"last_ts=max(coalesce(last_ts,0), excluded.last_ts), last_200_ts=max(coalesce(last_200_ts,0), excluded.last_200_ts)"
	for k, v := range a.last {
		var ok200 any
		if v.ts200 != 0 {
			ok200 = v.ts200
		}
		if _, err := tx.Exec(lq, k.host, k.url, v.pclass, v.ts, v.status, ok200); err != nil {
			return err
		}
	}
	for k, n := range a.errs {
		if _, err := tx.Exec("INSERT INTO gw_errors VALUES (?,?,?,?,?,?,?)", ts, width, group, k.status, k.upstream, k.prefix, n); err != nil {
			return err
		}
	}
	return nil
}

func sortedURLKeys(m map[urlKey]*urlRow) []urlKey {
	out := make([]urlKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		if out[i].host != out[j].host {
			return out[i].host < out[j].host
		}
		return out[i].url < out[j].url
	})
	return out
}
