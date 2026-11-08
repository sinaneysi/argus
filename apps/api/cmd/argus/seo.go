package main

// seo.go: the search jobs of the marketing module (internal/marketing/seo) in the worker and in argus marketing:
//
//	gsc          every 6 hours: Search Console daily totals, the key URLs' index state, the sitemaps (ARGUS_DB)
//	seo-suggest  once a day: the rule-based suggestions of the SEO page
//	seo-send     within seconds of the SEO page's Send: the newest suggestions to the notify room
//
// The Google data source (Settings -> Data sources, type google) holds the service-account key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/marketing/google"
	"github.com/sinaneysi/argus/internal/marketing/seo"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/worker"
)

// googleClient is the client of the first Google data source.
func googleClient(c *config.Config) (*google.Client, error) {
	e := firstSource(c, "google")
	if e == nil {
		return nil, errors.New("no Google data source: connect Google Search Console in Settings -> Data sources")
	}
	so := sourcesOptions(c)
	k, err := google.ParseKey(so.Credential(e))
	if err != nil {
		return nil, fmt.Errorf("google/%s: %w", e.Name, err)
	}
	return &google.Client{Key: k, Base: e.URL, AllowHTTP: sources.AllowHTTP}, nil
}

func tzNow(c *config.Config) time.Time {
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc)
}

// gscRun is one Search Console read.
func gscRun(ctx context.Context, c *config.Config, out io.Writer) error {
	cl, err := googleClient(c)
	if err != nil {
		return err
	}
	prop := c.Values["ARGUS_GSC_SITE"].Raw
	if prop == "" {
		return errors.New("ARGUS_GSC_SITE is not set (sc-domain:example.com or https://www.example.com/)")
	}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	r, err := seo.Collect(ctx, db, cl, prop, seo.URLs(c.Values["ARGUS_GSC_URLS_FILE"].Raw, seo.Home(prop)), tzNow(c), out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gsc %s: %d days (newest %s), %d URLs inspected, %d not PASS, %d sitemaps\n", prop, r.Days, r.Newest, r.Inspected, len(r.NotPass), r.Sitemaps)
	return nil
}

// suggestRun stores today's suggestions; force false: only when today has none yet.
func suggestRun(ctx context.Context, c *config.Config, force bool, out io.Writer) error {
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	now := tzNow(c)
	if !force && seo.HasDay(db, now.Format("2006-01-02")) {
		return nil
	}
	cl, err := googleClient(c)
	if err != nil {
		return err
	}
	o := seo.SuggestOptions{Property: c.Values["ARGUS_GSC_SITE"].Raw, GAProperty: c.Values["ARGUS_GA_PROPERTY"].Raw,
		Brand: seo.ParseBrand(c.Values["ARGUS_SEO_BRAND"].Raw), Today: now}
	sug, note, err := seo.Suggest(ctx, db, cl, o)
	if err != nil {
		return err
	}
	by := map[string]int{}
	for _, s := range sug {
		by[s.Rule]++
	}
	fmt.Fprintf(out, "seo suggestions %s: %d %v%s\n", now.Format("2006-01-02"), len(sug), by, map[bool]string{true: " (" + note + ")", false: ""}[note != ""])
	// TODO(P3a model runtime): the daily summary (seo_summary) through internal/model with style.Prepend.
	return nil
}

// seoSendRun consumes the Send requests of the SEO page first (a failed send is never retried in a loop), then posts
// the newest suggestions once for all of them.
func seoSendRun(ctx context.Context, c *config.Config, out io.Writer) error {
	dir := filepath.Join(c.SettingsIn, "seo-send")
	names, _ := os.ReadDir(dir)
	var who []string
	n := 0
	for _, e := range names {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		var r struct {
			By string `json:"by"`
		}
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &r) == nil && r.By != "" {
			who = append(who, cutRunes(r.By, 40))
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("seo send: cannot remove %s: %w", e.Name(), err)
		}
		n++
	}
	if n == 0 {
		return nil
	}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	text := seo.Message(db)
	db.Close()
	if text == "" {
		return nil
	}
	sort.Strings(who)
	uniq := []string{}
	for i, w := range who {
		if i == 0 || w != who[i-1] {
			uniq = append(uniq, w)
		}
	}
	if len(uniq) == 0 {
		uniq = []string{"?"}
	}
	text += "\n(sent from /seo by " + strings.Join(uniq, ", ") + ")"
	notify.SetAppName(c.DisplayName)
	msg := notify.Formatter{Base: c.PublicURL}.Text("seo", text, "")
	err = senderFrom(c).Send(ctx, msg, "notify:seo")
	if errors.Is(err, notify.ErrNotSent) {
		return errors.New("seo send: no notice channel is configured")
	}
	if err == nil {
		fmt.Fprintf(out, "seo suggestions sent to the notify room (%d request(s))\n", n)
	}
	return err
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// seoJobs are the search jobs of the module.
func seoJobs(c *config.Config, det needs.Detector, out io.Writer) []worker.Job {
	ready := func(job string, site bool) func() (bool, string) {
		return func() (bool, string) {
			if !marketingOn(c) {
				return false, "the marketing module is off (Settings -> General, or ARGUS_MODULES=marketing)"
			}
			if miss := det.Now().Missing(needs.Jobs[job]); len(miss) > 0 {
				return false, needs.Reason(miss)
			}
			if site && c.Values["ARGUS_GSC_SITE"].Raw == "" {
				return false, "not configured: set ARGUS_GSC_SITE (the Search Console property)"
			}
			return true, ""
		}
	}
	return []worker.Job{
		{Name: "gsc", Every: 6 * time.Hour, Sources: needs.Jobs["gsc"], Ready: ready("gsc", true),
			Run: func(ctx context.Context) error { return gscRun(ctx, c, out) }},
		{Name: "seo-suggest", Every: time.Hour, Sources: needs.Jobs["seo-suggest"], Ready: ready("seo-suggest", true),
			Run: func(ctx context.Context) error { return suggestRun(ctx, c, false, out) }},
		{Name: "seo-send", Every: applyEvery, Ready: ready("seo-send", false),
			Run: pendingJob(filepath.Join(c.SettingsIn, "seo-send"), func() error { return seoSendRun(context.Background(), c, out) })},
	}
}
