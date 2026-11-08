package googlebot

// collect.go: the collector. Every run reads the finished buckets of the active interval that are not stored yet
// (the newest 2 hours), one transaction per bucket and edge group; a failed read is a gap row (ok=0 and the error),
// never zeros, and a stored bucket is never read again, so a rerun cannot count twice. The interval adapts itself
// (1, 5 or 10 minutes) to the run's cost. Then the detector runs.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sinaneysi/argus/internal/migrate"
)

var intervals = []int64{60, 300, 600} // allowed bucket widths, seconds

const (
	catchup      = 2 * 3600 // collect looks back this far for missing buckets
	slack        = 120      // wait this long after a bucket ends (log shipping delay)
	keepAggDays  = 400
	keepURLDays  = 90
	keepEvents   = 30
	defaultWidth = 300
)

// Options configure the job.
type Options struct {
	DB         string // googlebot.db
	Groups     []Group
	Classes    []Class
	Loki       *Loki
	Ranges     func(ctx context.Context) (Nets, string) // Google's crawler ranges and their date
	Loc        *time.Location
	TZLabel    string // the zone's name in notices
	Width      int64  // the interval of a new database (60, 300 or 600)
	StoreLimit int64  // bytes: above it the per-URL rows stop and an alert opens (0: no limit)
	SiteHost   string // the site host of the sitemaps (crawl waste and coverage)
	Sitemap    string // the sitemap index URL
	Fetch      func(ctx context.Context, url string) (io.ReadCloser, error)
	Notify     func(ctx context.Context, text string) error // nil: no notices
	Pause      time.Duration                                // between backfill buckets and sitemap reads
	Now        func() time.Time
	Log        io.Writer
}

func (o *Options) now() int64 {
	if o.Now != nil {
		return o.Now().Unix()
	}
	return time.Now().Unix()
}

func (o *Options) logf(f string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, f+"\n", a...)
	}
}

func (o *Options) loc() *time.Location {
	if o.Loc == nil {
		return time.UTC
	}
	return o.Loc
}

// Open migrates googlebot.db (creating it when missing) and opens it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := migrate.ApplyFile(ctx, path, migrate.Googlebot); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(60000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func state(db *sql.DB, k, def string) string {
	var v string
	if db.QueryRow("SELECT v FROM gw_state WHERE k=?", k).Scan(&v) != nil {
		return def
	}
	return v
}

func stateInt(db *sql.DB, k string, def int64) int64 {
	n, err := strconv.ParseInt(state(db, k, ""), 10, 64)
	if err != nil {
		return def
	}
	return n
}

func setState(db execer, k string, v any) error {
	_, err := db.Exec("INSERT OR REPLACE INTO gw_state VALUES (?,?)", k, fmt.Sprint(v))
	return err
}

func (o *Options) width(db *sql.DB) int64 {
	def := o.Width
	if def == 0 {
		def = defaultWidth
	}
	return stateInt(db, "gw_interval", def)
}

func (o *Options) overLimit() bool {
	if o.StoreLimit <= 0 {
		return false
	}
	fi, err := os.Stat(o.DB)
	return err == nil && fi.Size() > o.StoreLimit
}

// runBucket reads one bucket for every edge group not stored yet; each group in its own transaction.
func (o *Options) runBucket(ctx context.Context, db *sql.DB, ts, width int64, nets Nets, st *Stats) error {
	if f := stateInt(db, "gw_first_day", -1); f < 0 || f > ts {
		if err := setState(db, "gw_first_day", ts); err != nil {
			return err
		}
	}
	done := map[string]bool{}
	rows, err := db.Query("SELECT edge FROM gw_buckets WHERE ts=? AND ok=1", ts)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e string
		rows.Scan(&e)
		done[e] = true
	}
	rows.Close()
	for _, g := range o.Groups {
		if done[g.Label] { // stored earlier: reading it again would count it twice
			continue
		}
		start, b0 := time.Now(), st.Bytes
		a := &Aggregator{Cache: g.Parser == "cache", Nets: nets, Classes: o.Classes, Loc: o.loc()}
		err := o.Loki.Lines(ctx, g, ts, ts+width, st, func(ls []Line) {
			for _, l := range ls {
				a.Add(l)
			}
		})
		if err == nil {
			if o.overLimit() { // keep the aggregates (small), stop the per-URL growth; the detector says so
				a.DropURLs()
			}
			var note any
			if a.Bad > 0 {
				note = fmt.Sprintf("%d malformed line(s)", a.Bad)
			}
			err = inTx(db, func(tx *sql.Tx) error {
				if err := a.Store(tx, ts, width, g.Label); err != nil {
					return err
				}
				_, err := tx.Exec("INSERT OR REPLACE INTO gw_buckets VALUES (?,?,?,1,?,?,?,?,?)", ts, width, g.Label, a.Lines,
					st.Bytes-b0, time.Since(start).Milliseconds(), note, o.now())
				return err
			})
			if err == nil {
				st.Lines += int64(a.Lines)
				continue
			}
		}
		// any failure is a recorded gap for this bucket and group
		if _, e := db.Exec("INSERT OR REPLACE INTO gw_buckets VALUES (?,?,?,0,NULL,NULL,?,?,?)", ts, width, g.Label,
			time.Since(start).Milliseconds(), cutS(err.Error(), 200), o.now()); e != nil {
			return e
		}
		st.Failed++
	}
	return nil
}

func inTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// todo is every bucket start in [first, last] that some group has not stored yet.
func (o *Options) todo(db *sql.DB, first, last, width int64) ([]int64, error) {
	have := map[[2]string]bool{}
	rows, err := db.Query("SELECT ts, edge FROM gw_buckets WHERE ok=1 AND ts>=? AND ts<=?", first, last)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ts int64
		var e string
		rows.Scan(&ts, &e)
		have[[2]string{strconv.FormatInt(ts, 10), e}] = true
	}
	rows.Close()
	var out []int64
	for t := first; t <= last; t += width {
		for _, g := range o.Groups {
			if !have[[2]string{strconv.FormatInt(t, 10), g.Label}] {
				out = append(out, t)
				break
			}
		}
	}
	return out, nil
}

// adapt steps the interval up when a run used more than half of it or Loki refused every read, and down after 24 h
// within a fifth of it.
func adapt(db *sql.DB, took float64, width, now int64, limited bool) (int64, error) {
	i := 1
	for k, w := range intervals {
		if w == width {
			i = k
		}
	}
	nw := width
	switch {
	case (took > 0.5*float64(width) || limited) && i < len(intervals)-1:
		nw = intervals[i+1]
		setState(db, "gw_ok_since", now)
	case took <= 0.2*float64(width):
		since := stateInt(db, "gw_ok_since", now)
		if i > 0 && now-since >= 86400 && stateInt(db, "gw_floor", intervals[0]) <= intervals[i-1] {
			nw = intervals[i-1]
			setState(db, "gw_ok_since", now)
		}
	default:
		setState(db, "gw_ok_since", now)
	}
	if nw != width {
		setState(db, "gw_interval", nw)
		d, _ := json.Marshal(map[string]any{"from": width, "to": nw, "took_s": round(took, 1)})
		if _, err := db.Exec("INSERT INTO gw_events VALUES (?,?,?)", now, "interval", string(d)); err != nil {
			return nw, err
		}
	}
	return nw, nil
}

func (o *Options) prune(db *sql.DB, now int64) error {
	old := Day(now-keepURLDays*86400, o.loc())
	for _, q := range []struct {
		sql string
		arg any
	}{
		{"DELETE FROM gw_agg WHERE ts<?", now - keepAggDays*86400}, {"DELETE FROM gw_buckets WHERE ts<?", now - keepAggDays*86400},
		{"DELETE FROM gw_errors WHERE ts<?", now - keepAggDays*86400},
		{"DELETE FROM gw_url_day WHERE day<?", old}, {"DELETE FROM gw_url_var WHERE day<?", old},
		{"DELETE FROM gw_events WHERE ts<?", now - keepEvents*86400},
	} {
		if _, err := db.Exec(q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}

// Result is one collect run.
type Result struct {
	Buckets  int
	Stats    Stats
	Width    int64
	NewWidth int64
	Findings []Finding
}

// AllFailed: every read of the run failed (the job reports an error).
func (r Result) AllFailed(groups int) bool {
	return r.Buckets > 0 && r.Stats.Failed == int64(r.Buckets*groups)
}

// Collect is one run: every missing bucket of the last 2 hours, then the detector.
func (o *Options) Collect(ctx context.Context) (Result, error) {
	db, err := Open(ctx, o.DB)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	now := o.now()
	width := o.width(db)
	if state(db, "gw_ok_since", "") == "" {
		setState(db, "gw_ok_since", now)
	}
	last := now - slack
	last -= last%width + width // the newest bucket that has fully ended
	first := last - catchup + width
	first -= first % width
	nets, ver := o.Ranges(ctx)
	setState(db, "gw_ranges", ver)
	list, err := o.todo(db, first, last, width)
	if err != nil {
		return Result{}, err
	}
	res := Result{Buckets: len(list), Width: width}
	start := time.Now()
	for i, t := range list {
		if ctx.Err() != nil {
			break
		}
		if err := o.runBucket(ctx, db, t, width, nets, &res.Stats); err != nil {
			return res, err
		}
		if time.Since(start).Seconds() > 0.8*float64(width) && i != len(list)-1 {
			break // leave the rest for the next run instead of overlapping
		}
	}
	took := time.Since(start).Seconds()
	ev, _ := json.Marshal(map[string]any{"bytes": res.Stats.Bytes, "queries": res.Stats.Queries, "lines": res.Stats.Lines,
		"failed": res.Stats.Failed, "took_s": round(took, 2), "buckets": len(list), "width": width})
	if _, err := db.Exec("INSERT INTO gw_events VALUES (?,?,?)", now, "run", string(ev)); err != nil {
		return res, err
	}
	if res.NewWidth, err = adapt(db, took, width, now, res.Stats.Failed > 0 && res.Stats.Lines == 0); err != nil {
		return res, err
	}
	if err := o.prune(db, now); err != nil {
		return res, err
	}
	o.logf("googlebot: %d bucket(s) x %d group(s), %d lines, %d Loki queries, %.1f MB, %.1fs, %d failed, interval %ds%s",
		len(list), len(o.Groups), res.Stats.Lines, res.Stats.Queries, float64(res.Stats.Bytes)/1e6, took, res.Stats.Failed, width,
		map[bool]string{true: fmt.Sprintf(" -> %ds", res.NewWidth), false: ""}[res.NewWidth != width])
	res.Findings, err = o.detect(ctx, db, now, true)
	return res, err
}

// Backfill reads an old range of days (in the zone, inclusive), without the detector and without notices.
func (o *Options) Backfill(ctx context.Context, d0, d1 string) (Result, error) {
	db, err := Open(ctx, o.DB)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	f, err1 := Midnight(d0, o.loc())
	l, err2 := Midnight(d1, o.loc())
	if err1 != nil || err2 != nil || l < f {
		return Result{}, fmt.Errorf("want two days YYYY-MM-DD, the first not after the second")
	}
	width := o.width(db)
	last := min(l+86400-width, o.now()-slack-width)
	nets, ver := o.Ranges(ctx)
	list, err := o.todo(db, f-f%width, last, width)
	if err != nil {
		return Result{}, err
	}
	res := Result{Buckets: len(list), Width: width, NewWidth: width}
	start := time.Now()
	for _, t := range list {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if err := o.runBucket(ctx, db, t, width, nets, &res.Stats); err != nil {
			return res, err
		}
		if o.Pause > 0 {
			time.Sleep(o.Pause) // keep the Loki load low
		}
		if next := t + width; Day(next, o.loc()) != Day(t, o.loc()) {
			o.logf("  %s done, %d lines, %d failed, %.0fs", Day(t, o.loc()), res.Stats.Lines, res.Stats.Failed, time.Since(start).Seconds())
		}
	}
	o.logf("backfill %s..%s: %d bucket(s), %d lines, %d queries, %.1f MB, %d failed, %.0fs, ranges %s", d0, d1, len(list),
		res.Stats.Lines, res.Stats.Queries, float64(res.Stats.Bytes)/1e6, res.Stats.Failed, time.Since(start).Seconds(), ver)
	return res, nil
}
