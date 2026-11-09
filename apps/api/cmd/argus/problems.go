package main

// problems.go: argus problems (the problem records job): tick applies the Problems page's requests, closes the
// problems resolved long ago and writes problems.json; suggest is one pass of the deterministic link suggester. In
// the volume layout the worker runs both: the requests within seconds, the housekeeping and the suggester every
// 15 minutes. Nothing is applied while FEATURE_PROBLEMS is not 1.

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/problems"
)

const problemsHelp = `usage: argus problems tick [-file PATH]
       argus problems suggest [-file PATH]

tick     apply the Problems page's requests (ARGUS_SETTINGS_IN/problem-actions: create, link, unlink, accept,
         reject, primary, status, edit, merge, resolve), close the problems resolved for PROBLEMS_CLOSE_DAYS and
         rewrite problems.json next to ARGUS_DB. Off (FEATURE_PROBLEMS=0): the requests are removed unread.
suggest  one bounded pass of the link suggester: suggestions only, unless PROBLEMS_AUTO_ACCEPT=1 (then only at
         PROBLEMS_AUTO_ACCEPT_CONFIDENCE or more, for PROBLEMS_AUTO_ACCEPT_CLASSES). Off with PROBLEMS_SUGGEST=0.
Settings: FEATURE_PROBLEMS and the PROBLEMS_* keys; the admin overrides in ARGUS_ETC_DIR/problems.json.
`

func runProblems(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stderr
		if isHelp(args) {
			w = stdout
		}
		fmt.Fprint(w, problemsHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("problems "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if (cmd != "tick" && cmd != "suggest") || fs.NArg() != 0 {
		fmt.Fprint(stderr, problemsHelp)
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus problems: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	out, err := problemsRun(c, cmd)
	if err != nil {
		fmt.Fprintln(stderr, "argus problems "+cmd+":", err)
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

// problemsOptions: the state next to ops.db (where the app reads problems.json); host names, skipped hosts and the
// incident requests as the incident job has them.
func problemsOptions(c *config.Config) *problems.Options {
	io := incidentOptions(c)
	dir := filepath.Dir(c.DB)
	maps := []string{filepath.Join(dir, "service-map.json")}
	if c.StateDir != "" && filepath.Clean(c.StateDir) != filepath.Clean(dir) {
		maps = append(maps, filepath.Join(c.StateDir, "service-map.json"))
	}
	return &problems.Options{StateDir: dir, ActionsDir: filepath.Join(c.SettingsIn, "problem-actions"),
		AdminFile: filepath.Join(c.Values["ARGUS_ETC_DIR"].Raw, "problems.json"), ServiceMaps: maps,
		Env: func(k string) string { return c.Values[k].Raw }, HostPrefixes: io.Prefixes, SkipHosts: io.SkipHosts,
		Incidents: io, Log: collect.Logger.With("job", "problems")}
}

func problemsRun(c *config.Config, cmd string) (string, error) {
	db, err := openOpsDB(c)
	if err != nil {
		return "", err
	}
	defer db.Close()
	o := problemsOptions(c)
	if cmd == "tick" {
		log, err := o.Tick(db)
		return fmt.Sprintf("problems: %d request(s)", len(log)), err
	}
	res, on, err := o.Suggest(db)
	if !on {
		return "problem suggester: off (FEATURE_PROBLEMS / PROBLEMS_SUGGEST)", err
	}
	return fmt.Sprintf("problem suggester: %d problems, %d items, %d suggested, %d auto-accepted", res.Problems, res.Items, res.Suggested, res.AutoAccepted), err
}
