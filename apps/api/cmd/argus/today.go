package main

// today.go: argus today (internal/today), the Today planner. In the volume layout the worker runs it: the tick every
// 30 minutes (the day's plan once ARGUS_TODAY_PLAN_AT has passed, a refresh after that) and the page's requests
// (settings-in/today-actions) within seconds. Local data only: ops.db, board.db (read-only) and the state files.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/today"
	"github.com/sinaneysi/argus/internal/worker"
)

const todayHelp = `usage: argus today tick|plan|apply|show|status [-file PATH] [-force] [-replan]

tick    build the day's plan once ARGUS_TODAY_PLAN_AT has passed (-force: now, and also with FEATURE_TODAY=0);
        later ticks refresh it: closed sources done, page-level newcomers added, counts refreshed
plan    build today's plan now (-replan: rebuild it) and print it
apply   apply the Today page's requests (settings-in/today-actions): start, done, skip, defer, undo, today, replan,
        settings
show    print today's plan
status  rewrite today-status.json (next to ops.db; the page reads it)
`

func todayOptions(c *config.Config, stdout, stderr io.Writer) (*today.Options, func(), error) {
	v := func(k string) string { return c.Values[k].Raw }
	db, err := openOpsDB(c)
	if err != nil {
		return nil, nil, err
	}
	var board *sql.DB
	if _, err := os.Stat(c.BoardDB); err == nil {
		board, _ = sql.Open("sqlite", "file:"+c.BoardDB+"?mode=ro&_pragma=busy_timeout(10000)")
	}
	loc, err := time.LoadLocation(c.TZ)
	if err != nil || c.TZ == "" {
		loc = time.UTC
	}
	op := v("ARGUS_TODAY_OPERATOR")
	if op == "" && len(c.Admins) > 0 {
		op = c.Admins[0]
	}
	o := &today.Options{DB: db, ConfigFile: v("ARGUS_TODAY_CONFIG"), DecisionsFile: v("ARGUS_TODAY_DECISIONS"), StateDir: c.StateDir,
		SelfcheckFile: v("ARGUS_SELFCHECK_FILE"), CredFile: credExpiryFile(c), ActionsDir: filepath.Join(c.SettingsIn, "today-actions"),
		StatusFile: filepath.Join(filepath.Dir(c.DB), "today-status.json"), Enabled: v("FEATURE_TODAY") == "1", Operator: op,
		Statuses: v("ARGUS_TODAY_JIRA_STATUSES"), PlanAt: v("ARGUS_TODAY_PLAN_AT"), FocusMin: v("ARGUS_TODAY_FOCUS_MIN"),
		MaxItems: v("ARGUS_TODAY_MAX_ITEMS"), MaxRisky: v("ARGUS_TODAY_MAX_RISKY"), Loc: loc, App: c.DisplayName, Out: stdout, Log: stderr}
	if board != nil {
		o.Board = board
	}
	return o, func() {
		db.Close()
		if board != nil {
			board.Close()
		}
	}, nil
}

func todayRun(ctx context.Context, c *config.Config, cmd string, force, replan bool, stdout, stderr io.Writer) error {
	o, done, err := todayOptions(c, stdout, stderr)
	if err != nil {
		return err
	}
	defer done()
	cfg, err := today.LoadConfig(o.ConfigFile)
	if err != nil {
		return fmt.Errorf("today config: %w", err)
	}
	switch cmd {
	case "tick":
		return o.Tick(ctx, cfg, force)
	case "plan":
		return o.Plan(ctx, cfg, replan)
	case "apply":
		return o.Apply(ctx, cfg)
	case "status":
		_, err := o.WriteStatus(ctx, cfg)
		return err
	case "show":
		t, err := o.Text(ctx)
		fmt.Fprintln(stdout, t)
		return err
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func runToday(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		if isHelp(args) {
			fmt.Fprint(stdout, todayHelp)
			return 0
		}
		fmt.Fprint(stderr, todayHelp)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "tick", "plan", "apply", "show", "status":
	default:
		fmt.Fprint(stderr, todayHelp)
		return 2
	}
	fs := flag.NewFlagSet("today "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	force := fs.Bool("force", false, "tick: build the plan now")
	replan := fs.Bool("replan", false, "plan: rebuild today's plan")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode && cmd != "show" {
		fmt.Fprintln(stderr, "argus today: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	if err := todayRun(context.Background(), c, cmd, *force, *replan, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "argus today "+cmd+":", err)
		return 1
	}
	return 0
}

// todayJobs are the planner's worker jobs: the tick every 30 minutes, the page's requests within seconds.
func todayJobs(c *config.Config, stderr io.Writer) []worker.Job {
	return []worker.Job{
		{Name: "today", Every: 30 * time.Minute, Run: func(ctx context.Context) error {
			return todayRun(ctx, c, "tick", false, false, io.Discard, stderr)
		}},
		{Name: "today-actions", Every: applyEvery, Run: pendingJob(filepath.Join(c.SettingsIn, "today-actions"), func() error {
			return todayRun(context.Background(), c, "apply", false, false, io.Discard, stderr)
		})},
	}
}
