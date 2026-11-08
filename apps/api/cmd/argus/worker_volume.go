package main

// worker_volume.go: `argus worker` in the one-volume layout (ADR-0021 D3, D4). The worker is the scheduler: the
// collectors run on their intervals, and the saves of Settings that the legacy layout hands to root path units are
// applied here within seconds: Settings -> General and Writing style (internal/settingsapply) and Settings -> Claude
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

	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/claudetoken"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/kube"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/settingsapply"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/sources/discover"
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
	jobs = append(jobs, worker.Job{Name: "writing-style-apply", Every: applyEvery, Run: styleApplyJob(c, stderr)})
	jobs = append(jobs, worker.Job{Name: "sources-requests", Every: applyEvery, Run: sourcesRequestsJob(c, stderr)})
	jobs = append(jobs, worker.Job{Name: "sources-check", Every: 6 * time.Hour, Run: func(context.Context) error {
		o := sourcesOptions(c)
		o.Log = stderr
		return sources.CheckAll(o)
	}})
	// the request kinds root's path units consume in the legacy layout
	jobs = append(jobs, worker.Job{Name: "kube-requests", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "kube"), func() error {
		o := kubeOptions(c)
		o.Log = stderr
		return kube.Apply(o)
	})})
	jobs = append(jobs, worker.Job{Name: "source-actions", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "source-actions"), func() error {
		return exitErr("source actions", runSourcesAgent(c, "actions", false, "", "manual", stderr, stderr))
	})})
	jobs = append(jobs, worker.Job{Name: "sources-discover", Every: time.Hour, Sources: []string{"gitlab", "kubernetes", "grafana", "sentry", "elasticsearch"},
		Ready: func() (bool, string) {
			if len(discover.Eligible(sources.LoadStatus(filepath.Join(c.SettingsOut, "sources-status.json")))) == 0 {
				return false, "not configured: no data source is enabled for the agent (purpose agent, read-only verified)"
			}
			return true, ""
		},
		Run: func(context.Context) error {
			return exitErr("discovery", runSourcesAgent(c, "discover", false, "", "timer", io.Discard, stderr))
		}})
	jobs = append(jobs, worker.Job{Name: "board-sync", Every: 2 * time.Minute, Run: func(ctx context.Context) error { return boardSyncJob(ctx, c) }})
	jobs = append(jobs, worker.Job{Name: "board-requests", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "board-requests"), func() error {
		return boardSyncJob(context.Background(), c)
	})})
	jobs = append(jobs, worker.Job{Name: "gitlab-issues-requests", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "gitlab-issues"), func() error {
		return boardSyncJob(context.Background(), c)
	})})
	// incidents: the incident page's requests within seconds (local data, always on); the auto-resolve against the
	// live alerts every 5 minutes, only once an alerts source is configured (without one every incident would
	// look quiet and resolve)
	jobs = append(jobs, worker.Job{Name: "incident-actions", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "incident-actions"), func() error {
		return incidentsTick(c, false)
	})})
	jobs = append(jobs, worker.Job{Name: "incidents", Every: 5 * time.Minute, Sources: needs.Jobs["incidents"],
		Ready: func() (bool, string) {
			if miss := det.Now().Missing(needs.Jobs["incidents"]); len(miss) > 0 {
				return false, needs.Reason(miss)
			}
			return true, ""
		},
		Run: func(context.Context) error { return incidentsTick(c, true) }})
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

// styleApplyJob applies Settings -> Writing style when its request file changed since the last apply (by content).
func styleApplyJob(c *config.Config, stderr io.Writer) func(context.Context) error {
	var last [32]byte
	return func(context.Context) error {
		o := settingsOptions(c)
		data, err := os.ReadFile(o.StyleRequestPath())
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
		if _, err := settingsapply.ApplyStyle(o); err != nil && !settingsapply.IsRejected(err) {
			return fmt.Errorf("writing style apply: %w", err)
		}
		return nil
	}
}

// pendingJob runs fn when dir holds a request file (*.json).
func pendingJob(dir string, fn func() error) func(context.Context) error {
	return func(context.Context) error {
		names, _ := os.ReadDir(dir)
		for _, n := range names {
			if filepath.Ext(n.Name()) == ".json" {
				return fn()
			}
		}
		return nil
	}
}

func exitErr(what string, rc int) error {
	if rc != 0 {
		return fmt.Errorf("%s: exit %d", what, rc)
	}
	return nil
}

// boardSyncJob is argus board sync inside the worker: the board's requests always; Jira when Settings -> Data
// sources has a Jira entry (its address and token, basic auth when it has a user name) or the Jira settings files
// are configured; GitLab issues with the connection of Settings -> Integrations, whose token may be the GitLab
// entry of Settings -> Data sources with purpose tracker.
func boardSyncJob(ctx context.Context, c *config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return board.Run(ctx, volumeBoardOptions(c))
}

// volumeBoardOptions are the board sync's options in the volume. A Jira data source supplies the address and the
// credential only: the Jira settings (project, JQL, status map, transitions, user map, mode, component, board id)
// still come from the volume's jira.env. JIRA_PROJECT of the configuration is used when it is set explicitly, or when
// jira.env names no project (then the compiled-in example is the last resort).
func volumeBoardOptions(c *config.Config) board.Options {
	o := boardOptions(c, false, false)
	so := sourcesOptions(c)
	if e := firstSource(c, "jira"); e != nil {
		o.Config = map[string]string{"JIRA_URL": e.URL}
		file, _ := board.LoadConfig(o.EnvFiles, o.OwnerUID)
		if p := c.Values["JIRA_PROJECT"]; p.Source != "default" || file["JIRA_PROJECT"] == "" {
			o.Config["JIRA_PROJECT"] = p.Raw
		}
		o.Token, o.JiraUser = so.Credential(e), e.Username
	}
	if e := trackerSource(c, "gitlab"); e != nil {
		o.GitLabURL, o.GitLabToken = e.URL, so.Credential(e)
	}
	return o
}

// incidentsTick is argus incidents tick inside the worker.
func incidentsTick(c *config.Config, resolve bool) error {
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = incidentOptions(c).Tick(db, resolve)
	return err
}

// sourcesRequestsJob applies the Settings -> Data sources requests (save, test, remove, check-all) when there are
// any: the same internal/sources code root's argus-sources.path runs in the legacy layout.
func sourcesRequestsJob(c *config.Config, stderr io.Writer) func(context.Context) error {
	return func(context.Context) error {
		o := sourcesOptions(c)
		o.Log = stderr
		names, _ := os.ReadDir(o.ReqDir)
		for _, n := range names {
			if filepath.Ext(n.Name()) == ".json" {
				return sources.Apply(o)
			}
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
