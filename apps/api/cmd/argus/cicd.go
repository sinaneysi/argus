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

const cicdHelp = `usage: argus cicd scan|watch|audit|daily|seed|vars-meta [-budget N] [-file PATH]

scan    the estate: projects of CICD_GROUPS (all the token's projects when it is not set) with a .gitlab-ci.yml, or
        with an include of CICD_ENGINE_PROJECT when that is set; their develop/pre-release/release branches, the
        include ref and file, branch protection. Projects whose activity changed, or older than a day.
watch   the pipelines on the environment branches: failures are triaged by rules into findings (production ones
        also open an incident), stuck pipelines get the deploy-lock plan, successful deploys are recorded.
audit   the branch audit (missing, drift, stale, pushes without a pipeline, release deploys, default branch,
        include ref, protection) and the variable audit (full when ci-vars-meta wrote its metadata in 3 days).
daily   scan, then audit (the worker's cicd-daily).
seed    record the newest pipeline that ran on each env branch that has none recorded (history only).
vars-meta  CI variable metadata (keys, scopes, flags, production-marker classes; never a value) with the token
        of CICD_VARS_SOURCE, to ARGUS_STATE_DIR/ci-meta/meta.json.

GitLab: the Settings -> Data sources entry of type gitlab with purpose agent (read-only). Argus never writes to
GitLab: every fix is a proposal. Status: ARGUS_STATE_DIR/cicd-status.json.
`

var cicdBudgets = map[string]int{"scan": 600, "watch": 80, "audit": 400, "daily": 1000, "seed": 500, "vars-meta": 600}

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
	if rx := strings.TrimSpace(c.Values["CICD_DRIFT_ALLOW"].Raw); rx != "" {
		re, err := regexp.Compile(rx)
		if err != nil {
			return o, fmt.Errorf("CICD_DRIFT_ALLOW: %v", err)
		}
		o.DriftAllow = re
	}
	if rx := strings.TrimSpace(c.Values["CICD_VAR_SCOPES"].Raw); rx != "" {
		re, err := regexp.Compile(rx)
		if err != nil {
			return o, fmt.Errorf("CICD_VAR_SCOPES: %v", err)
		}
		o.VarScopes = re
	}
	o.TargetPrefixes = map[string][]string{}
	for _, kv := range list(c, "CICD_TARGET_PREFIXES") {
		env, ps, ok := strings.Cut(kv, "=")
		if !ok || cicd.EnvOf(strings.TrimSpace(env)) == "" {
			return o, fmt.Errorf("CICD_TARGET_PREFIXES: %q is not env=prefix|prefix", kv)
		}
		for _, p := range strings.Split(ps, "|") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				o.TargetPrefixes[strings.TrimSpace(env)] = append(o.TargetPrefixes[strings.TrimSpace(env)], p)
			}
		}
	}
	o.GroupSecretKeys = list(c, "CICD_GROUP_SECRET_KEYS")
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
	if task == "vars-meta" {
		return cicdVarsMeta(ctx, c, budget, stdout)
	}
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
	case "audit":
		err = w.Audit()
	case "daily":
		if _, err = w.Scan(false); err == nil || errors.Is(err, cicd.ErrBudget) {
			err = w.Audit()
		}
	case "seed":
		err = w.Seed()
	}
	if err == nil && (task == "audit" || task == "daily") {
		_, err = w.NotifyFindings(w.New, c.Values["CICD_NOTIFY_AUDIT"].Raw == "1")
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

// cicdVarsSource is the entry of CICD_VARS_SOURCE (nil when it is not set or not usable).
func cicdVarsSource(c *config.Config) *sources.Entry {
	k := strings.TrimSpace(c.Values["CICD_VARS_SOURCE"].Raw)
	if k == "" {
		return nil
	}
	so := sourcesOptions(c)
	e := so.Registry()[k]
	if e == nil || e.Type != "gitlab" || !e.Enabled || e.URL == "" {
		return nil
	}
	return e
}

func cicdMarkers(c *config.Config) ([]cicd.Marker, error) {
	var ms []cicd.Marker
	for _, kv := range list(c, "CICD_PROD_MARKERS") {
		name, rx, ok := strings.Cut(kv, "=")
		re, err := regexp.Compile(strings.TrimSpace(rx))
		if !ok || err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("CICD_PROD_MARKERS: %q is not name=regex", kv)
		}
		ms = append(ms, cicd.Marker{Name: strings.TrimSpace(name), Re: re})
	}
	return ms, nil
}

// cicdVarsMeta is ci-vars-meta: the CI variable metadata with the token of CICD_VARS_SOURCE. The values are read
// once in memory and dropped; the file holds keys, scopes, flags and marker classes only.
func cicdVarsMeta(ctx context.Context, c *config.Config, budget int, stdout io.Writer) error {
	e := cicdVarsSource(c)
	if e == nil {
		return errors.New("not configured: set CICD_VARS_SOURCE to a GitLab data source whose token reads CI variables")
	}
	so := sourcesOptions(c)
	tok := so.Credential(e)
	if tok == "" {
		return errors.New("the CI variables data source has no readable credential")
	}
	ms, err := cicdMarkers(c)
	if err != nil {
		return err
	}
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	gl := &cicd.GitLab{Base: e.URL, Token: tok, Budget: budget}
	w, err := cicd.New(ctx, cicd.Options{DB: db, GitLab: gl, Log: stdout})
	if err != nil {
		return err
	}
	defer w.Close()
	meta, err := w.CollectMeta(ms)
	if err != nil {
		return fmt.Errorf("%w; meta.json not replaced", err)
	}
	if err := cicd.WriteMeta(filepath.Join(c.StateDir, "ci-meta", "meta.json"), meta); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ci-vars-meta: %d projects, %d groups, %d calls, %d errors\n", len(meta.Projects), len(meta.Groups), gl.Calls, len(meta.Errors))
	return nil
}
