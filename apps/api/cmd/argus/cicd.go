package main

// cicd.go: argus cicd (the CI/CD watcher, internal/cicd): scan finds the GitLab projects with a CI file (or with an
// include of the CI engine project) and their environment branches; watch polls their pipelines, triages failures,
// watches stuck pipelines and deploys. GitLab is the GitLab entry of Settings -> Data sources with purpose agent
// (read-only). In the volume layout the worker runs watch every 5 minutes and scan once a day.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/cicd"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/sources"
)

const cicdHelp = `usage: argus cicd scan|watch [-budget N] [-file PATH]

scan    the estate: projects of CICD_GROUPS (all the token's projects when it is not set) with a .gitlab-ci.yml, or
        with an include of CICD_ENGINE_PROJECT when that is set; their develop/pre-release/release branches, the
        include ref and file, branch protection. Projects whose activity changed, or older than a day.
watch   the pipelines on the environment branches: failures are triaged by rules into findings (production ones
        also open an incident), stuck pipelines get the deploy-lock plan, successful deploys are recorded.

GitLab: the Settings -> Data sources entry of type gitlab with purpose agent (read-only). Argus never writes to
GitLab: every fix is a proposal. Status: ARGUS_STATE_DIR/cicd-status.json.
`

var cicdBudgets = map[string]int{"scan": 600, "watch": 80}

func runCicd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		if isHelp(args) {
			fmt.Fprint(stdout, cicdHelp)
			return 0
		}
		fmt.Fprint(stderr, cicdHelp)
		return 2
	}
	cmd := args[0]
	if _, ok := cicdBudgets[cmd]; !ok {
		fmt.Fprint(stderr, cicdHelp)
		return 2
	}
	fs := flag.NewFlagSet("cicd "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	budget := fs.Int("budget", cicdBudgets[cmd], "GitLab calls at most")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus cicd: disabled in the demo (DEMO_MODE=1)")
		return 1
	}
	if err := cicdRun(context.Background(), c, cmd, *budget, stdout); err != nil {
		fmt.Fprintln(stderr, "argus cicd "+cmd+":", err)
		return 1
	}
	return 0
}

// cicdSource is the GitLab entry the watcher reads with: type gitlab, purpose agent, enabled (by key).
func cicdSource(c *config.Config) *sources.Entry {
	so := sourcesOptions(c)
	reg := so.Registry()
	var names []string
	for k, e := range reg {
		if e != nil && e.Type == "gitlab" && e.Purpose == "agent" && e.Enabled && e.URL != "" {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return reg[names[0]]
}

func cicdOptions(c *config.Config, budget int, log io.Writer) (cicd.Options, error) {
	e := cicdSource(c)
	if e == nil {
		return cicd.Options{}, errors.New("not configured: connect GitLab in Settings -> Data sources (purpose agent, read-only)")
	}
	so := sourcesOptions(c)
	tok := so.Credential(e)
	if tok == "" {
		return cicd.Options{}, errors.New("the GitLab data source has no readable credential")
	}
	explicit := func(k string) string {
		if v := c.Values[k]; v.Source != "default" {
			return strings.TrimSpace(v.Raw)
		}
		return ""
	}
	o := cicd.Options{GitLab: &cicd.GitLab{Base: e.URL, Token: tok, Budget: budget},
		EnginePath: explicit("CICD_ENGINE_PROJECT"), DeployBridge: c.Values["CICD_DEPLOY_BRIDGE"].Raw,
		DeployJobNames: list(c, "CICD_DEPLOY_JOBS"), InventoryFile: c.Values["CICD_INVENTORY_FILE"].Raw,
		InventoryRef: c.Values["CICD_INVENTORY_REF"].Raw, StuckMinutes: num(c, "CICD_STUCK_MINUTES"),
		RoundRobin: num(c, "CICD_RR_PER_CYCLE"), StateDir: c.StateDir, PublicURL: c.PublicURL,
		MetaFile: filepath.Join(c.StateDir, "ci-meta", "meta.json"), Incidents: incidentOptions(c), Log: log}
	if explicit("CICD_GROUPS") != "" {
		o.Groups = list(c, "CICD_GROUPS")
	}
	if id := explicit("CICD_ENGINE_PROJECT_ID"); id != "" {
		o.EngineID, _ = strconv.ParseInt(id, 10, 64)
	}
	for _, kv := range list(c, "CICD_HOST_ENVS") {
		env, rx, ok := strings.Cut(kv, "=")
		re, err := regexp.Compile(strings.TrimSpace(rx))
		if !ok || err != nil || cicd.EnvOf(strings.TrimSpace(env)) == "" {
			return o, fmt.Errorf("CICD_HOST_ENVS: %q is not env=regex", kv)
		}
		o.HostEnvs = append(o.HostEnvs, cicd.HostEnv{Env: strings.TrimSpace(env), Re: re})
	}
	snd := senderFrom(c)
	notify.SetAppName(c.DisplayName)
	o.Notify = func(m notify.Message) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return snd.Send(ctx, m.Render(), "notify:cicd")
	}
	return o, nil
}

func cicdRun(ctx context.Context, c *config.Config, task string, budget int, stdout io.Writer) error {
	o, err := cicdOptions(c, budget, stdout)
	if err != nil {
		return err
	}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	o.DB = db
	w, err := cicd.New(ctx, o)
	if err != nil {
		return err
	}
	switch task {
	case "scan":
		_, err = w.Scan(false)
	case "watch":
		if err = w.Watch(0); err == nil {
			_, err = w.NotifyFindings(w.New, true)
		}
	}
	note, ok := "", err == nil
	if errors.Is(err, cicd.ErrBudget) {
		note, ok, err = err.Error(), true, nil
	} else if err != nil {
		note = err.Error()
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if serr := w.WriteStatus(task, ok, note); err == nil && serr != nil {
		err = serr
	}
	fmt.Fprintf(stdout, "cicd %s: %d/%d calls, %d errors, %v, %d new findings\n", task, o.GitLab.Calls, o.GitLab.Budget,
		o.GitLab.Errors, w.Counts, len(w.New))
	return err
}
