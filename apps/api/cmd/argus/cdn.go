package main

// cdn.go: the CDN log job of the marketing module (internal/marketing/cdn): every 5 minutes, the counts of the CDN's
// access log index (an Elasticsearch / OpenSearch data source, ARGUS_CDN_SOURCE) into ARGUS_DB.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/marketing/cdn"
	"github.com/sinaneysi/argus/internal/marketing/googlebot"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/worker"
)

// cdnSource is the Elasticsearch data source of the CDN logs: the one ARGUS_CDN_SOURCE names, else the first.
func cdnSource(c *config.Config) *sources.Entry {
	if n := strings.TrimPrefix(strings.TrimSpace(c.Values["ARGUS_CDN_SOURCE"].Raw), "elasticsearch/"); n != "" {
		so := sourcesOptions(c)
		for _, e := range so.Registry() {
			if e.Type == "elasticsearch" && e.Name == n {
				return e
			}
		}
		return nil
	}
	return firstSource(c, "elasticsearch")
}

func cdnRun(ctx context.Context, c *config.Config, out io.Writer) error {
	idx := c.Values["ARGUS_CDN_INDEX"].Raw
	if idx == "" {
		return errors.New("ARGUS_CDN_INDEX is not set")
	}
	e := cdnSource(c)
	if e == nil {
		return errors.New("no Elasticsearch data source for the CDN logs")
	}
	so := sourcesOptions(c)
	cred := so.Credential(e)
	hdr := map[string]string{}
	switch {
	case e.Username != "":
		hdr["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(e.Username+":"+cred))
	case cred != "":
		hdr["Authorization"] = "ApiKey " + cred
	}
	rng := &googlebot.Ranges{Path: c.StateDir + "/googlebot-ranges.json", URLs: list(c, "ARGUS_GOOGLE_RANGES_URLS"), Fetch: publicGet,
		Log: func(f string, a ...any) { fmt.Fprintf(out, f+"\n", a...) }}
	o := &cdn.Options{Index: idx, Ranges: rng.Load, Log: out,
		Search: func(_ context.Context, path string, body []byte) (int, []byte, error) {
			st, b, err := sources.PostRead(e.URL, path, hdr, body, 32<<20, 60*time.Second)
			if err != nil {
				return 0, nil, errors.New(sources.NetError(err))
			}
			return st, b, nil
		}}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = o.Collect(ctx, db)
	return err
}

// cdnJobs is the CDN log job.
func cdnJobs(c *config.Config, det needs.Detector, out io.Writer) []worker.Job {
	return []worker.Job{{Name: "cdn-watch", Every: 5 * time.Minute, Sources: needs.Jobs["cdn-watch"],
		Ready: func() (bool, string) {
			if !marketingOn(c) {
				return false, "the marketing module is off (Settings -> General, or ARGUS_MODULES=marketing)"
			}
			if miss := det.Now().Missing(needs.Jobs["cdn-watch"]); len(miss) > 0 {
				return false, needs.Reason(miss)
			}
			if c.Values["ARGUS_CDN_INDEX"].Raw == "" {
				return false, "not configured: set ARGUS_CDN_INDEX (the index pattern of the CDN logs)"
			}
			return true, ""
		},
		Run: func(ctx context.Context) error { return cdnRun(ctx, c, out) }}}
}
