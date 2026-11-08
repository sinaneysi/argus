package main

// worker_volume.go: `argus worker` in the one-volume layout (ADR-0021 D3, D4). The worker is the scheduler: the
// collectors run on their intervals, and the saves of Settings that the legacy layout hands to root path units are
// applied here within seconds: Settings -> General (internal/settingsapply) and Settings -> Claude
// (internal/claudetoken). No host job, no systemd timer. The job states go to worker-status.json in the volume.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sinaneysi/argus/internal/claudetoken"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/settingsapply"
	"github.com/sinaneysi/argus/internal/worker"
)

// applyEvery is how often the worker looks for saved settings and Claude requests.
const applyEvery = 3 * time.Second

func volumeJobs(c *config.Config, every map[string]time.Duration, stderr io.Writer) []worker.Job {
	var jobs []worker.Job
	det := needs.FromConfig(func(k string) config.Value { return c.Values[k] }, false)
	det.Active = true // the worker runs in the volume layout only
	for _, n := range collectorNames {
		n := n
		jobs = append(jobs, worker.Job{Name: "collect-" + n, Every: every[n], Sources: needs.Jobs["collect-"+n],
			Ready: func() (bool, string) {
				if miss := det.Now().Missing(needs.Jobs["collect-"+n]); len(miss) > 0 {
					return false, needs.Reason(miss)
				}
				if !enabled(c, n) {
					return false, "ARGUS_COLLECT_" + upper(n) + "=1 is not set"
				}
				return true, ""
			},
			Run: func(ctx context.Context) error { return runCollector(ctx, c, n) }})
	}
	jobs = append(jobs, worker.Job{Name: "settings-apply", Every: applyEvery, Run: settingsApplyJob(c, stderr)})
	jobs = append(jobs, worker.Job{Name: "claude-requests", Every: applyEvery, Sources: []string{"claude"}, Run: claudeRequestsJob(c, stderr)})
	return jobs
}

func upper(s string) string {
	b := []byte(s)
	for i, x := range b {
		if x >= 'a' && x <= 'z' {
			b[i] = x - 32
		}
	}
	return string(b)
}

// settingsApplyJob applies Settings -> General when settings.json changed since the last apply (by content).
func settingsApplyJob(c *config.Config, stderr io.Writer) func(context.Context) error {
	var last [32]byte
	return func(context.Context) error {
		o := settingsOptions(c)
		o.TimerPrefix = "" // no systemd here: the schedules are validated and stored, the worker's intervals rule
		data, err := os.ReadFile(o.In)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if sum == last {
			return nil
		}
		last = sum
		o.Log, o.Err = stderr, stderr
		o.Notify = settingsNotice(c, stderr)
		if err := settingsapply.Apply(o); err != nil && !settingsapply.IsRejected(err) {
			return fmt.Errorf("settings apply: %w", err)
		}
		return nil
	}
}

// claudeRequestsJob runs the Settings -> Claude requests (replace, test, egress, egress-settings) when there are any.
// In the volume the API and the worker are the same user, the env file lives in the volume and there is no user
// switch for the token test.
func claudeRequestsJob(c *config.Config, stderr io.Writer) func(context.Context) error {
	return func(context.Context) error {
		o := claudeOptions(c)
		o.RunAs, o.AppUID, o.Log = "", os.Getuid(), stderr
		names, _ := os.ReadDir(o.ReqDir)
		pending := false
		for _, n := range names {
			if filepath.Ext(n.Name()) == ".json" {
				pending = true
			}
		}
		if !pending {
			return nil
		}
		if _, err := os.Stat(o.EnvFile); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(o.EnvFile, nil, 0o600); err != nil {
				return err
			}
		}
		return claudetoken.Apply(o)
	}
}

// runVolumeWorker is argus worker in ARGUS_LAYOUT=volume: it runs until ctx ends.
func runVolumeWorker(ctx context.Context, c *config.Config, every map[string]time.Duration, stderr io.Writer) int {
	s := &worker.Scheduler{Log: slog.New(slog.NewTextHandler(stderr, nil)).With("component", "worker")}
	for _, j := range volumeJobs(c, every, stderr) {
		s.Add(j)
	}
	statusPath := filepath.Join(c.StateDir, "worker-status.json")
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			writeWorkerStatus(statusPath, s.States())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	s.Run(ctx)
	writeWorkerStatus(statusPath, s.States())
	return 0
}

func writeWorkerStatus(path string, st []worker.State) {
	b, _ := json.MarshalIndent(map[string]any{"at": time.Now().Unix(), "jobs": st}, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		os.Rename(tmp, path)
	}
}
