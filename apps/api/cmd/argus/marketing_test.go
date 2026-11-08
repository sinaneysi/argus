package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/needs"
)

// The marketing jobs wait for the module, then for Grafana and the edge groups file, and say which one is missing.
func TestMarketingJobsGate(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	write := func(extra string) *config.Config {
		os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\n"+extra), 0o600)
		c, err := config.Load(config.Options{File: env, Environ: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		migrate.EnsureDirs(c)
		return c
	}
	reasons := func(c *config.Config) map[string]string {
		det := needs.FromConfig(func(k string) config.Value { return c.Values[k] }, false)
		det.Active = true
		out := map[string]string{}
		for _, j := range marketingJobs(c, det, io.Discard) {
			_, r := j.Ready()
			out[j.Name] = r
		}
		return out
	}
	c := write("")
	for n, r := range reasons(c) {
		if !strings.Contains(r, "marketing module is off") {
			t.Errorf("%s: %q", n, r)
		}
	}
	c = write("ARGUS_MODULES=marketing\n")
	r := reasons(c)
	if !strings.Contains(r["googlebot-watch"], "Grafana") || !strings.Contains(r["googlebot-sitemaps"], "ARGUS_GOOGLEBOT_SITEMAP") ||
		!strings.Contains(r["seo-check"], "no crawler database") {
		t.Fatalf("%v", r)
	}
	os.WriteFile(filepath.Join(c.EtcDir, "googlebot-edges.tsv"), []byte("primary\tjson\t{job=\"nginx\"}\n"), 0o600)
	c = write("ARGUS_MODULES=marketing\nARGUS_GOOGLEBOT_SITEMAP=https://www.example.com/sitemap.xml\n")
	if r := reasons(c); r["googlebot-sitemaps"] != "" {
		t.Fatalf("%v", r)
	}
	// the admin's switch wins over ARGUS_MODULES
	if !marketingOn(c) {
		t.Fatal("on by ARGUS_MODULES")
	}
	if _, err := migrate.ApplyFile(context.Background(), c.AuthDB, migrate.Auth); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", "file:"+c.AuthDB)
	db.Exec("INSERT INTO auth_settings (key, value) VALUES ('module.marketing', '0')")
	db.Close()
	if marketingOn(c) {
		t.Fatal("the admin switched it off")
	}
}
