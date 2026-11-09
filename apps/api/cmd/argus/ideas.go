package main

// ideas.go: argus ideas (the decision side of Ideas): apply applies the Ideas review's requests (explore, accept
// with the build prompt, reject, built) and rewrites ideas-status.json; status only rewrites the status file. In the
// volume layout the worker runs apply within seconds of a request and status every hour.

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/ideas"
)

const ideasHelp = `usage: argus ideas apply [-file PATH]
       argus ideas status [-file PATH]

apply   apply the Ideas review's requests (ARGUS_SETTINGS_IN/idea-actions: explore, accept, reject, built); an
        accepted idea gets its build prompt (template ARGUS_ETC_DIR/ideas-build.md, else the built-in one); then
        rewrite ideas-status.json next to ARGUS_DB.
status  rewrite ideas-status.json only (the switch FEATURE_IDEAS, the advisers of ARGUS_ETC_DIR/ideas.json and
        IDEAS_SCHEDULE, their last runs, the IDEAS_* caps).
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
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if (cmd != "apply" && cmd != "status") || fs.NArg() != 0 {
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
