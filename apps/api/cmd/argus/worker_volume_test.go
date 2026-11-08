package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/worker"

	_ "modernc.org/sqlite"
)

// A save of Settings -> General lands in the volume and the worker's job applies it: no root, no systemd.
func TestVolumeWorkerAppliesSettings(t *testing.T) {
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
	tasks := map[string]any{}
	for _, n := range []string{"chat-triage", "log-rca", "backup-check", "cert-check"} {
		tasks[n] = map[string]any{"schedule": "6h", "enabled": true}
	}
	save := map[string]any{"tasks": tasks,
		"thresholds": map[string]any{"kuma_min_down_minutes": 5, "kuma_flap_count": 3, "cert_warn_days": 30, "cert_fail_days": 14},
		"rca_auto":   map[string]any{"enabled": true, "open_hours": 4},
		"qa_ask":     map[string]any{"enabled": true, "dry_run": true, "read_images": false},
		"claude":     map[string]any{"enabled": false}, "updated_by": "admin", "updated_at": 1_800_000_000}
	b, _ := json.Marshal(save)
	if err := os.WriteFile(filepath.Join(c.SettingsIn, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	job := settingsApplyJob(c, io.Discard)
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	data, err := os.ReadFile(filepath.Join(c.SettingsOut, "applied.json"))
	if err != nil || json.Unmarshal(data, &applied) != nil || applied["ok"] != true {
		t.Fatalf("applied.json: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(c.EtcDir, "claude.json")); err != nil {
		t.Fatalf("claude.json not applied into the volume: %v", err)
	}
	// unchanged content: not applied again
	os.Remove(filepath.Join(c.SettingsOut, "applied.json"))
	job(context.Background())
	if _, err := os.Stat(filepath.Join(c.SettingsOut, "applied.json")); err == nil {
		t.Fatal("applied again without a change")
	}
	// the request folders exist for the API
	for _, d := range migrate.RequestDirs {
		if _, err := os.Stat(filepath.Join(c.SettingsIn, d)); err != nil {
			t.Fatal(err)
		}
	}
}

// A job whose data source is not connected does not run and says which source to connect (ADR-0021 D3, D5).
func TestVolumeJobNotConfigured(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\n"), 0o600)
	c, err := config.Load(config.Options{File: env, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range volumeJobs(c, map[string]time.Duration{}, io.Discard) {
		if j.Name != "collect-alerts" {
			continue
		}
		ok, why := j.Ready()
		if ok || !strings.Contains(why, "connect Alertmanager or Prometheus") || len(j.Sources) != 1 {
			t.Fatalf("collect-alerts without a source: %v %q %v", ok, why, j.Sources)
		}
		return
	}
	t.Fatal("no collect-alerts job")
}

// A Data sources save lands in the volume, the worker applies it, and the alerts collector reads through the entry
// (ADR-0021 D4, D5): no root unit, no ARGUS_ALERTS_SOURCES.
func TestVolumeWorkerAppliesSources(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\nARGUS_SOURCES_ALLOW_HTTP=1\n"), 0o600)
	c, err := config.Load(config.Options{File: env, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.EnsureDirs(c); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.All(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", c.AuthDB)
	if _, err := db.Exec("INSERT INTO users (username, hash, role, disabled) VALUES ('admin', 'x', 'admin', 0)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	req, _ := json.Marshal(map[string]any{"user": "admin", "kind": "save", "mode": "add", "type": "alertmanager", "name": "main",
		"env": "shared", "url": "http://am.example.invalid", "purpose": "alerts"})
	os.WriteFile(filepath.Join(c.SettingsIn, "sources", "admin-0123456789abcdef.json"), req, 0o600)
	var jobs = map[string]worker.Job{}
	for _, j := range volumeJobs(c, map[string]time.Duration{"alerts": time.Minute}, io.Discard) {
		jobs[j.Name] = j
	}
	if ok, _ := jobs["collect-alerts"].Ready(); ok {
		t.Fatal("ready before the source exists")
	}
	if err := jobs["sources-requests"].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(filepath.Join(c.SettingsIn, "sources")); len(left) != 0 {
		t.Fatal("request not consumed")
	}
	if ok, why := jobs["collect-alerts"].Ready(); !ok {
		t.Fatalf("not ready with the source saved: %s", why)
	}
	srcs := registryAlertSources(c, nil)
	if len(srcs) != 1 || srcs[0].URL != "http://am.example.invalid/api/v2/alerts" || srcs[0].Token != "" {
		t.Fatalf("alert sources: %+v", srcs)
	}
}

// the request kinds of the legacy root units have a worker job in the volume layout; discovery waits for a source
func TestVolumeWorkerConsumers(t *testing.T) {
	vol := t.TempDir()
	c, _ := volumeConfig(t, vol, "")
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]worker.Job{}
	for _, j := range volumeJobs(c, map[string]time.Duration{}, io.Discard) {
		jobs[j.Name] = j
	}
	for _, n := range []string{"settings-apply", "claude-requests", "sources-requests", "sources-check", "kube-requests",
		"source-actions", "sources-discover", "board-sync", "board-requests"} {
		if _, ok := jobs[n]; !ok {
			t.Errorf("no %s job", n)
		}
	}
	if ok, why := jobs["sources-discover"].Ready(); ok || !strings.Contains(why, "enabled for the agent") {
		t.Fatalf("discovery without a source: %v %q", ok, why)
	}
	// nothing waiting: the request jobs return at once
	for _, n := range []string{"kube-requests", "source-actions", "board-requests"} {
		if err := jobs[n].Run(context.Background()); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	// the board works locally without Jira: one sync run in the volume
	if err := jobs["board-sync"].Run(context.Background()); err != nil {
		t.Fatalf("board sync: %v", err)
	}
}
