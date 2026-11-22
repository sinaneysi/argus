// Package seo is the marketing module's search jobs: the Search Console data for the SEO page (gsc_daily, gsc_urls,
// gsc_sitemaps) and the daily rule-based suggestions (seo_suggestions), read through a Google service-account data
// source (internal/marketing/google). Read-only: the key needs Restricted access to the property and the read-only
// scopes only.
package seo

// gsc.go: every 6 hours, the daily totals of the last 35 days (Search Console adds a day 2-3 days late, so recent days
// are read again every run), the index state of the key URLs (at most 50) and the submitted sitemaps.

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/google"
)

const (
	gscDays     = 35
	gscKeepDays = 400
	gscURLKeep  = 90 * 86400
	maxURLs     = 50
)

// URLs reads the key URL file (one URL per line, # comments; at most 50). Missing: fallback alone.
func URLs(path, fallback string) []string {
	f, err := os.Open(path)
	if err != nil {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() && len(out) < maxURLs {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// Home is the property's home page: a URL-prefix property itself, a domain property's www host.
func Home(property string) string {
	if strings.HasPrefix(property, "sc-domain:") {
		return "https://www." + strings.TrimPrefix(property, "sc-domain:") + "/"
	}
	return property
}

// GSCResult is one run.
type GSCResult struct {
	Days      int
	Newest    string
	Inspected int
	NotPass   []string
	Sitemaps  int
}

func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(x*p) / p
}

// Collect reads Search Console and stores what the SEO page shows. A URL whose inspection fails is skipped (logged).
func Collect(ctx context.Context, db *sql.DB, cl *google.Client, property string, urls []string, now time.Time, log io.Writer) (GSCResult, error) {
	var res GSCResult
	end := now.Format("2006-01-02")
	days, err := cl.SearchAnalytics(ctx, property, now.AddDate(0, 0, -gscDays).Format("2006-01-02"), end, []string{"date"}, 25000)
	if err != nil {
		return res, err
	}
	type state struct {
		u string
		s google.Inspection
	}
	var states []state
	for _, u := range urls {
		s, err := cl.Inspect(ctx, property, u)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if log != nil {
				fmt.Fprintf(log, "inspect failed for %s: %s\n", u, cut(err.Error(), 120))
			}
			continue
		}
		states = append(states, state{u, s})
	}
	maps, err := cl.Sitemaps(ctx, property)
	if err != nil {
		return res, err
	}
	t := now.Unix()
	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	for _, d := range days {
		if len(d.Keys) == 0 {
			continue
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO gsc_daily VALUES (?,?,?,?,?,?)", d.Keys[0], int64(d.Clicks), int64(d.Impressions),
			round(d.CTR, 5), round(d.Position, 2), t); err != nil {
			return res, err
		}
		res.Days++
		res.Newest = d.Keys[0]
	}
	for _, s := range states {
		if _, err := tx.Exec("INSERT OR REPLACE INTO gsc_urls VALUES (?,?,?,?,?,?,?,?)", t, s.u, nz(s.s.Verdict), nz(s.s.Coverage),
			nz(s.s.LastCrawl), nz(s.s.CrawledAs), nz(s.s.Robots), nz(s.s.FetchState)); err != nil {
			return res, err
		}
		res.Inspected++
		if s.s.Verdict != "PASS" {
			res.NotPass = append(res.NotPass, s.u)
		}
	}
	if _, err := tx.Exec("DELETE FROM gsc_sitemaps"); err != nil {
		return res, err
	}
	for _, m := range maps {
		var sub int64
		for _, c := range m.Contents {
			if c.Type == "web" {
				sub += google.Num(c.Submitted)
			}
		}
		pending := 0
		if m.Pending {
			pending = 1
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO gsc_sitemaps VALUES (?,?,?,?,?,?,?)", m.Path, m.LastDownloaded, pending,
			google.Num(m.Errors), google.Num(m.Warnings), sub, t); err != nil {
			return res, err
		}
		res.Sitemaps++
	}
	if _, err := tx.Exec("DELETE FROM gsc_urls WHERE ts < ?", t-gscURLKeep); err != nil {
		return res, err
	}
	if _, err := tx.Exec("DELETE FROM gsc_daily WHERE date < ?", now.AddDate(0, 0, -gscKeepDays).Format("2006-01-02")); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
