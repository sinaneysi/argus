package main

// taskagent.go: the task agent in the volume worker. The board sync decides what runs (internal/board agent.go,
// with AgentOptions from here); the worker job task-agent runs the model jobs (internal/taskagent) through the shared
// model runtime and then lets the board sync read the result at once.

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/lessons"
	"github.com/sinaneysi/argus/internal/model"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/taskagent"
)

func cfgFloat(c *config.Config, k string, def float64) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(c.Values[k].Raw), 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}

func cfgMinutes(c *config.Config, k string, def int) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(c.Values[k].Raw))
	if err != nil || n <= 0 {
		n = def
	}
	return time.Duration(n) * time.Minute
}

func taskAgentDir(c *config.Config) string { return c.Values["ARGUS_TASK_AGENT_DIR"].Raw }

// environmentsText is what the planning prompt is told about the environments of this installation.
func environmentsText(c *config.Config) string {
	s := "develop: development / stage (changes allowed through the fixer); pre-release: the last step before production " +
		"(changes allowed through the fixer); release: production (proposals only, never executed); none: no environment."
	if l := strings.TrimSpace(c.Values["ENV_LABELS"].Raw); l != "" {
		s += " Names the team uses: " + l + "."
	}
	return s
}

// agentBoardOptions turn the task agent on in the board sync (the volume layout).
func agentBoardOptions(c *config.Config) *board.AgentOptions {
	dir := taskAgentDir(c)
	deb, err := strconv.Atoi(c.Values["ARGUS_TASK_PLAN_DEBOUNCE_SEC"].Raw)
	debounce := time.Duration(deb) * time.Second
	if err == nil && deb == 0 {
		debounce = time.Nanosecond // 0: no wait (the zero value means the default)
	}
	return &board.AgentOptions{JobsDir: filepath.Join(dir, "jobs"), OutDir: filepath.Join(dir, "out"),
		ClaudeStatus: filepath.Join(c.SettingsOut, "claude-token-status.json"),
		Guard: func() (bool, string) {
			r := model.New(modelOptions(c, nil))
			if ok, why := r.Configured(); !ok {
				return false, why
			}
			return r.Guard()
		},
		Environments: environmentsText(c), Debounce: debounce,
		TaskBudget: cfgFloat(c, "ARGUS_TASK_BUDGET_USD", 8),
		IntakeTO:   cfgMinutes(c, "ARGUS_TASK_INTAKE_MINUTES", 10) + 2*time.Minute,
		RunTO:      cfgMinutes(c, "ARGUS_TASK_RUN_MINUTES", 60) + 5*time.Minute,
		ChatReqDir: filepath.Join(c.SettingsIn, "task-chat"),
	}
}

// taskAgentJob runs the oldest pending model job of the task agent; after one ran, the board sync reads its result.
func taskAgentJob(c *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		dir := taskAgentDir(c)
		o := taskagent.Options{JobsDir: filepath.Join(dir, "jobs"), OutDir: filepath.Join(dir, "out"),
			Intake: taskagent.Limits{BudgetUSD: cfgFloat(c, "ARGUS_TASK_INTAKE_BUDGET_USD", 0.5), Timeout: cfgMinutes(c, "ARGUS_TASK_INTAKE_MINUTES", 10), MaxTurns: 40},
			Run:    taskagent.Limits{BudgetUSD: cfgFloat(c, "ARGUS_TASK_RUN_BUDGET_USD", 5), Timeout: cfgMinutes(c, "ARGUS_TASK_RUN_MINUTES", 60), MaxTurns: 200},
			Chat:   taskagent.Limits{BudgetUSD: cfgFloat(c, "ARGUS_TASK_CHAT_BUDGET_USD", 0.15), Timeout: cfgMinutes(c, "ARGUS_TASK_CHAT_MINUTES", 5), MaxTurns: 12},
			Log:    collect.Logger}
		lo := &lessons.Options{Allow: notify.LoadAllowlist(c.Values["ARGUS_SECRET_ALLOWLIST"].Raw), Log: collect.Logger}
		o.Lessons = func(ctx context.Context, job []byte) (string, []int64) { return lo.SelectFor(ctx, c.BoardDB, job) }
		if _, _, ok := o.Pending(); !ok {
			return nil
		}
		db, err := openOpsDB(c)
		if err != nil {
			db = nil
		} else {
			defer db.Close()
		}
		o.Model = model.New(modelOptions(c, db))
		ran, err := o.RunOnce(ctx)
		if ran {
			return boardSyncJob(context.Background(), c)
		}
		return err
	}
}
