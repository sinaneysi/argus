package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/selfcheck"
	"github.com/sinaneysi/argus/internal/worker"
)

// The worker's selfcheck job in an empty volume: it writes selfcheck.json with the jobs, the volume's disk, the
// databases and the master key, and nothing fails.
func TestVolumeSelfcheck(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\n"), 0o600)
	c, err := config.Load(config.Options{File: env, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.EnsureDirs(c); err != nil {
		t.Fatal(err)
	}
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s := &worker.Scheduler{}
	for _, j := range volumeJobs(c, map[string]time.Duration{}, io.Discard) {
		s.Add(j)
	}
	j := selfcheckJob(c, s, io.Discard)
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var doc selfcheck.Doc
	b, err := os.ReadFile(c.Values["ARGUS_SELFCHECK_FILE"].Raw)
	if err != nil || json.Unmarshal(b, &doc) != nil {
		t.Fatalf("selfcheck.json: %v", err)
	}
	got := map[string]selfcheck.Check{}
	for _, ck := range doc.Checks {
		got[ck.Name] = ck
	}
	if got["master key"].Status != "OK" || got["database ops.db"].Detail != "integrity ok" || got["disk "+vol].Name == "" ||
		got["incidents"].Group != "jobs" || doc.Status == "FAIL" || doc.Kuma != "not configured" {
		t.Errorf("selfcheck: %s", b)
	}
}
