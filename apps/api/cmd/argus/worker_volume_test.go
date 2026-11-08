package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
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
