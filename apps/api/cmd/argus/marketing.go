package main

// marketing.go: argus marketing, the jobs of the optional marketing module (ADR-0021 D6). In the volume layout the
// worker runs them only when the module is on (ARGUS_MODULES=marketing, or the admin's switch in Settings -> General)
// and their sources are configured:
//
//	googlebot-watch     every minute: the crawler log buckets from Loki (a Grafana data source) into googlebot.db,
//	                    then the detector (notices to the notify room)
//	googlebot-sitemaps  once a day: the site's sitemaps into googlebot.db (ARGUS_GOOGLEBOT_SITEMAP)
//	seo-check           every minute: the SEO page's Googlebot table (seo_snapshots, seo_errors) from googlebot.db

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/marketing/googlebot"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/worker"
)

const marketingHelp = `usage: argus marketing googlebot collect|detect|sitemaps|seo [-file PATH]
       argus marketing googlebot backfill [-file PATH] FROM TO

The jobs of the marketing module (the worker runs them when the module is on):
collect   read the finished crawler log buckets of the last 2 hours from Loki (ARGUS_GOOGLEBOT_EDGES_FILE, through
          the Grafana data source ARGUS_GOOGLEBOT_GRAFANA) into ARGUS_GOOGLEBOT_DB, then run the detector
backfill  the same for the days FROM..TO (YYYY-MM-DD in ARGUS_TZ, inclusive), without the detector
detect    the detector alone: open and resolve alerts, send their notices
sitemaps  read the sitemap index ARGUS_GOOGLEBOT_SITEMAP and every sitemap it lists
seo       write the SEO page's Googlebot table (seo_snapshots, seo_errors in ARGUS_DB) for the newest window
`

func runMarketing(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || isHelp(args) || args[0] != "googlebot" {
		fmt.Fprint(stdout, marketingHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	cmd := args[1]
	fs := flag.NewFlagSet("marketing googlebot "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus marketing: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	ctx := context.Background()
	var err error
	switch {
	case cmd == "seo" && fs.NArg() == 0:
		var ts int64
		ts, err = seoCheck(c)
		if err == nil {
			fmt.Fprintln(stdout, map[bool]string{true: "seo: no new window", false: fmt.Sprintf("seo: window ending %d written", ts)}[ts == 0])
		}
	case (cmd == "collect" || cmd == "detect" || cmd == "sitemaps") && fs.NArg() == 0, cmd == "backfill" && fs.NArg() == 2:
		var o *googlebot.Options
		if o, err = googlebotOptions(c, stdout); err != nil {
			break
		}
		switch cmd {
		case "collect":
			var r googlebot.Result
			r, err = o.Collect(ctx)
			if err == nil && r.AllFailed(len(o.Groups)) {
				err = errors.New("every read failed (see gw_buckets)")
			}
		case "backfill":
			o.Notify, o.Pause = nil, 200*time.Millisecond
			_, err = o.Backfill(ctx, fs.Arg(0), fs.Arg(1))
		case "detect":
			var f []googlebot.Finding
			f, err = o.Detect(ctx)
			fmt.Fprintf(stdout, "detect: %d finding(s)\n", len(f))
		case "sitemaps":
			o.Pause = time.Second
			_, _, err = o.Sitemaps(ctx)
		}
	default:
		fmt.Fprint(stderr, marketingHelp)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "argus marketing:", sources.Mask(err.Error()))
		return 1
	}
	return 0
}

// marketingOn says whether the marketing module is on: the admin's switch in auth.db wins over ARGUS_MODULES.
func marketingOn(c *config.Config) bool {
	if c.AuthDB != "" {
		if _, err := os.Stat(c.AuthDB); err == nil {
			if db, err := sql.Open("sqlite", "file:"+c.AuthDB+"?mode=ro&_pragma=busy_timeout(5000)"); err == nil {
				var v string
				err := db.QueryRow("SELECT value FROM auth_settings WHERE key='module.marketing'").Scan(&v)
				db.Close()
				if err == nil && (v == "1" || v == "0") {
					return v == "1"
				}
			}
		}
	}
	for _, m := range list(c, "ARGUS_MODULES") {
		if m == "marketing" {
			return true
		}
	}
	return false
}

// googlebotGrafana is the Grafana data source of the crawler logs: the one ARGUS_GOOGLEBOT_GRAFANA names, else the first.
func googlebotGrafana(c *config.Config) *sources.Entry {
	if n := strings.TrimSpace(c.Values["ARGUS_GOOGLEBOT_GRAFANA"].Raw); n != "" {
		n = strings.TrimPrefix(n, "grafana/")
		so := sourcesOptions(c)
		for _, e := range so.Registry() {
			if e.Type == "grafana" && e.Name == n {
				return e
			}
		}
		return nil
	}
	return firstSource(c, "grafana")
}

// publicGet is a GET of a public address (Google's range lists, the site's sitemaps): HTTPS_PROXY applies.
func publicGet(ctx context.Context, u string) (io.ReadCloser, error) {
	ctx2, cancel := context.WithTimeout(ctx, 120*time.Second)
	req, err := http.NewRequestWithContext(ctx2, "GET", u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("User-Agent", "argus-marketing/1 (sitemap coverage)")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}).Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return cancelBody{resp.Body, cancel}, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel func()
}

func (b cancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

func intVal(c *config.Config, k string) int64 {
	var n int64
	fmt.Sscan(c.Values[k].Raw, &n)
	return n
}

// googlebotOptions are the crawler job's options of a configuration.
func googlebotOptions(c *config.Config, log io.Writer) (*googlebot.Options, error) {
	v := func(k string) string { return c.Values[k].Raw }
	groups, err := googlebot.LoadGroupsFile(v("ARGUS_GOOGLEBOT_EDGES_FILE"))
	if err != nil {
		return nil, fmt.Errorf("edge groups (%s): %w", v("ARGUS_GOOGLEBOT_EDGES_FILE"), err)
	}
	classes, err := googlebot.LoadClassesFile(v("ARGUS_GOOGLEBOT_PATHS_FILE"))
	if err != nil {
		return nil, fmt.Errorf("path classes (%s): %w", v("ARGUS_GOOGLEBOT_PATHS_FILE"), err)
	}
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		loc = time.UTC
	}
	hl := v("ARGUS_GOOGLEBOT_HOST_LABEL")
	if !googlebot.HostLabelRe.MatchString(hl) {
		return nil, fmt.Errorf("ARGUS_GOOGLEBOT_HOST_LABEL is not a label name")
	}
	var loki *googlebot.Loki
	if e := googlebotGrafana(c); e != nil { // the sitemap read needs no Grafana
		loki = &googlebot.Loki{Get: grafanaGetter(c, e), UID: v("ARGUS_GOOGLEBOT_LOKI_UID"), HostLabel: hl}
	}
	rng := &googlebot.Ranges{Path: c.StateDir + "/googlebot-ranges.json", URLs: list(c, "ARGUS_GOOGLE_RANGES_URLS"), Fetch: publicGet,
		Log: func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) }}
	sitemap := v("ARGUS_GOOGLEBOT_SITEMAP")
	o := &googlebot.Options{DB: v("ARGUS_GOOGLEBOT_DB"), Groups: groups, Classes: classes, Loki: loki, Ranges: rng.Load, Loc: loc,
		TZLabel: v("ARGUS_TZ_LABEL"), Width: intVal(c, "ARGUS_GOOGLEBOT_INTERVAL"), StoreLimit: intVal(c, "ARGUS_GOOGLEBOT_STORE_LIMIT_MB") << 20,
		Sitemap: sitemap, SiteHost: googlebot.SiteHost(sitemap), Fetch: publicGet, Log: log}
	o.Notify = func(ctx context.Context, text string) error {
		notify.SetAppName(c.DisplayName)
		err := senderFrom(c).Send(ctx, notify.Formatter{Base: c.PublicURL}.Text("googlebot", text, ""), "notify:googlebot")
		if errors.Is(err, notify.ErrNotSent) {
			return nil // no channel configured: the alert stays on the page
		}
		return err
	}
	return o, nil
}

// grafanaGetter is one guarded GET to a Grafana data source with its token.
func grafanaGetter(c *config.Config, e *sources.Entry) googlebot.Getter {
	so := sourcesOptions(c)
	cred := so.Credential(e)
	base := e.URL
	return func(ctx context.Context, path string) (int, []byte, error) {
		hdr := map[string]string{}
		if cred != "" {
			hdr["Authorization"] = "Bearer " + cred
		}
		st, b, err := sources.GetRaw(base, path, hdr, 256<<20, 90*time.Second)
		if err != nil {
			return 0, nil, errors.New(sources.NetError(err))
		}
		return st, b, nil
	}
}

// seoCheck writes the SEO page's Googlebot table from googlebot.db.
func seoCheck(c *config.Config) (int64, error) {
	path := c.Values["ARGUS_GOOGLEBOT_DB"].Raw
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("no crawler database yet (%s)", path)
	}
	g, err := googlebot.Open(context.Background(), path)
	if err != nil {
		return 0, err
	}
	defer g.Close()
	ops, err := openOpsDB(c)
	if err != nil {
		return 0, err
	}
	defer ops.Close()
	primary := ""
	if gs, err := googlebot.LoadGroupsFile(c.Values["ARGUS_GOOGLEBOT_EDGES_FILE"].Raw); err == nil {
		primary = gs[0].Label
	}
	sitemap := c.Values["ARGUS_GOOGLEBOT_SITEMAP"].Raw
	return googlebot.SEOSnapshot(g, ops, googlebot.SEOOptions{Primary: primary, Hosts: list(c, "ARGUS_SEO_HOSTS"),
		ServiceMap: c.Values["ARGUS_SEO_SERVICE_MAP"].Raw, SiteHost: googlebot.SiteHost(sitemap)}, time.Now().Unix())
}

// marketingJobs are the module's worker jobs.
func marketingJobs(c *config.Config, det needs.Detector, stderr io.Writer) []worker.Job {
	off := func(job string, extra func() string) func() (bool, string) {
		return func() (bool, string) {
			if !marketingOn(c) {
				return false, "the marketing module is off (Settings -> General, or ARGUS_MODULES=marketing)"
			}
			if miss := det.Now().Missing(needs.Jobs[job]); len(miss) > 0 {
				return false, needs.Reason(miss)
			}
			if extra != nil {
				if r := extra(); r != "" {
					return false, "not configured: " + r
				}
			}
			return true, ""
		}
	}
	edges := func() string {
		if _, err := googlebot.LoadGroupsFile(c.Values["ARGUS_GOOGLEBOT_EDGES_FILE"].Raw); err != nil {
			return "the edge groups file " + c.Values["ARGUS_GOOGLEBOT_EDGES_FILE"].Raw + " (" + strings.SplitN(err.Error(), ":", 2)[0] + ")"
		}
		return ""
	}
	run := func(fn func(context.Context, *googlebot.Options) error) func(context.Context) error {
		return func(ctx context.Context) error {
			o, err := googlebotOptions(c, stderr)
			if err != nil {
				return err
			}
			return fn(ctx, o)
		}
	}
	return []worker.Job{
		{Name: "googlebot-watch", Every: time.Minute, Sources: needs.Jobs["googlebot-watch"], Ready: off("googlebot-watch", edges),
			Run: run(func(ctx context.Context, o *googlebot.Options) error {
				r, err := o.Collect(ctx)
				if err == nil && r.AllFailed(len(o.Groups)) {
					return errors.New("googlebot: every read failed (see gw_buckets)")
				}
				return err
			})},
		{Name: "googlebot-sitemaps", Every: time.Hour, Ready: off("googlebot-sitemaps", func() string {
			if c.Values["ARGUS_GOOGLEBOT_SITEMAP"].Raw == "" {
				return "set ARGUS_GOOGLEBOT_SITEMAP (the sitemap index)"
			}
			return edges()
		}), Run: run(func(ctx context.Context, o *googlebot.Options) error {
			if !o.SitemapsDue(ctx) {
				return nil
			}
			o.Pause = time.Second
			_, _, err := o.Sitemaps(ctx)
			return err
		})},
		{Name: "seo-check", Every: time.Minute, Ready: off("seo-check", func() string {
			if _, err := os.Stat(c.Values["ARGUS_GOOGLEBOT_DB"].Raw); err != nil {
				return "no crawler database yet (googlebot-watch writes it)"
			}
			return ""
		}), Run: func(context.Context) error { _, err := seoCheck(c); return err }},
	}
}
