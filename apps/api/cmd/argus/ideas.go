package main

// ideas.go: argus ideas: apply applies the Ideas review's requests (explore, accept with the build prompt, reject,
// built) and rewrites ideas-status.json; status only rewrites the status file; run is one adviser's model run. In
// the volume layout the worker runs apply within seconds of a request, status every hour and run (auto) every hour.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/ideas"
	"github.com/sinaneysi/argus/internal/model"
)

const ideasHelp = `usage: argus ideas apply [-file PATH]
       argus ideas status [-file PATH]
       argus ideas run [-file PATH] [-adviser auto|KEY]

apply   apply the Ideas review's requests (ARGUS_SETTINGS_IN/idea-actions: explore, accept, reject, built); an
        accepted idea gets its build prompt (template ARGUS_ETC_DIR/ideas-build.md, else the built-in one); then
        rewrite ideas-status.json next to ARGUS_DB.
status  rewrite ideas-status.json only (the switch FEATURE_IDEAS, the advisers of ARGUS_ETC_DIR/ideas.json and
        IDEAS_SCHEDULE, their last runs, the IDEAS_* caps).
run     one adviser's model run: auto (the default) runs the adviser whose weekly slot is due, if any; a key
        (devops, qa, noc) is "Generate now" (once a day per adviser). The adviser reads a read-only workspace of
        facts (ideas-work next to ARGUS_DB) through the model runtime (ARGUS_MODEL_PROVIDER); the JSON answer is
        validated, deduplicated against earlier ideas and stored. Files in ARGUS_ETC_DIR (built-in defaults):
        ideas-run.md, ideas-agents/<agent>.md, ideas-features.md, ideas-job-model.md (optional).
`

func runIdeas(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stderr
		if isHelp(args) {
			w = stdout
		}
		fmt.Fprint(w, ideasHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("ideas "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	adviser := fs.String("adviser", "auto", "run: auto, or the adviser's key")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if (cmd != "apply" && cmd != "status" && cmd != "run") || fs.NArg() != 0 {
		fmt.Fprint(stderr, ideasHelp)
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus ideas: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	if cmd == "run" {
		out, err := ideasModelRun(context.Background(), c, *adviser)
		if err != nil {
			fmt.Fprintln(stderr, "argus ideas run:", err)
			return 1
		}
		fmt.Fprintln(stdout, out)
		return 0
	}
	out, err := ideasRun(c, cmd)
	if err != nil {
		fmt.Fprintln(stderr, "argus ideas "+cmd+":", err)
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

func ideasOptions(c *config.Config) *ideas.Options {
	loc, err := time.LoadLocation(c.Values["ARGUS_TZ"].Raw)
	if err != nil {
		loc = time.UTC
	}
	etc := c.Values["ARGUS_ETC_DIR"].Raw
	return &ideas.Options{StateDir: filepath.Dir(c.DB), ActionsDir: filepath.Join(c.SettingsIn, "idea-actions"),
		ConfigFile: filepath.Join(etc, "ideas.json"), Template: filepath.Join(etc, "ideas-build.md"),
		Env: func(k string) string { return c.Values[k].Raw }, Loc: loc, Log: collect.Logger.With("job", "ideas")}
}

func ideasRun(c *config.Config, cmd string) (string, error) {
	db, err := openOpsDB(c)
	if err != nil {
		return "", err
	}
	defer db.Close()
	o := ideasOptions(c)
	if cmd == "status" {
		return "ideas: status written", o.WriteStatus(db, o.LoadConfig(), "")
	}
	n, err := o.Apply(db)
	return fmt.Sprintf("ideas: %d request(s) applied", n), err
}

// ideasModelRun is one adviser run (target auto or an adviser's key).
func ideasModelRun(ctx context.Context, c *config.Config, target string) (string, error) {
	db, err := openOpsDB(c)
	if err != nil {
		return "", err
	}
	defer db.Close()
	o := ideasOptions(c)
	f := ideas.RunFiles{EtcDir: c.Values["ARGUS_ETC_DIR"].Raw, BoardDB: c.BoardDB}
	return o.Run(ctx, db, model.New(modelOptions(c, db)), f, target)
}
