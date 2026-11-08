package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/sources"
)

const sourcesHelp = `usage: argus sources list [type]
       argus sources routes <service>
       argus sources find "<service or question>" [--type t]... [--release] [--no-fanout] [--json] [--run id]
       argus sources unmapped [type]
       argus sources apply|check|record [-file PATH]
       argus sources discover [-force] [-only type/name,...] [-trigger timer|manual] [-file PATH]
       argus sources catalogue|actions [-file PATH]

list    the agent's catalogue (Settings -> Data sources): type, name, env, purpose, status, whether the agent may use
        it, last check, what reads it. Never a secret. Reads ARGUS_DB data_sources, else the status file.
apply   root, run by argus-sources.path (examples/systemd): processes the requests Settings -> Data sources writes into
        ARGUS_SETTINGS_IN/sources/ (save, test, remove, check-all): stores each credential in ARGUS_SOURCES_DIR/<type>/
        <name>.cred, checks the login read-only (https only, no redirect, never loopback or link-local), and mirrors
        the existing integrations (GitLab read-only token, GitLab issues, Grafana sites, Outline, Kubernetes clusters)
        without touching where they live. Status: ARGUS_SETTINGS_OUT/sources-status.json. Refused when DEMO_MODE=1.
routes  the agent's routing catalogue for one service (group/repo or its short name): confirmed routes first, then
        proposed ones with confidence and the reason; rejected ones are never listed.
find    which instance to read for a service or a question (ADR-0017 P3). Types: gitlab, k8s, loki, prometheus,
        sentry, es. Policy: one confirmed target -> use it; several -> the top 2-3 (say which one you used); none ->
        run it again with --type for a capped fan-out (ARGUS_SOURCES_FANOUT_*) and report "searched N of M instances"
        exactly as printed, never claim the others were checked. Release instances are searched only for a release
        question (or --release) and only when root verified them read-only. A fan-out hit is recorded as a proposed
        route (origin learned) for a person to review. One audit line per find in ARGUS_SOURCES_FIND_AUDIT_LOG (a hash
        of the question, never the text). Read tools that take the instance: argus tools kube <cluster>; GitLab,
        Grafana, Sentry and Elasticsearch have none in Argus yet (give the locator to a person).
unmapped what discovery found that no service claims (one type when given), with "ambiguous: ..." notes.
discover the agent's user, run by argus-sources-discover.timer: list what every source enabled for the agent holds
        (GitLab projects, Kubernetes objects, Loki/Prometheus names and dashboards, Sentry projects, index patterns),
        names only, within budgets, into ARGUS_DB source_inventory; then rebuild the catalogue. A capped run is
        partial and prunes nothing.
catalogue the agent's user: match the inventory to the services (ARGUS_SOURCES_SERVICES, else the CI/CD projects)
        -> source_routes (proposed / confirmed / rejected) and source_unmapped.
actions the agent's user, run by argus-sources-actions.path: the catalogue API's requests (confirm, reject, edit a
        route, Discover now) from ARGUS_SETTINGS_IN/source-actions/.
check   root, run by argus-sources-check.timer every 6 hours: migrate, then check every source again.
record  the agent's user, after apply and check (argus-sources-record.service): copy the status file into the
        data_sources table of ARGUS_DB (root never writes that database).
`

var sourceTypeRe = regexp.MustCompile(`^[a-z]{1,20}$`)

func sourcesOptions(c *config.Config) sources.Options {
	v := func(k string) string { return c.Values[k].Raw }
	gl := ""
	if x := c.Values["GITLAB_URL"]; x.Source != "default" {
		gl = x.Raw
	}
	graf := map[string]string{}
	for k, x := range c.Values {
		if len(k) > 12 && k[:8] == "GRAFANA_" && k[len(k)-4:] == "_URL" {
			graf[k] = x.Raw
		}
	}
	sources.AllowHTTP = v("ARGUS_SOURCES_ALLOW_HTTP") == "1"
	return sources.Options{ReqDir: filepath.Join(c.SettingsIn, "sources"), OutDir: c.SettingsOut, AuthDB: c.AuthDB,
		Dir: v("ARGUS_SOURCES_DIR"), AgentGroup: v("ARGUS_SOURCES_AGENT_GROUP"), AppUID: 65534,
		Legacy: sources.Legacy{EtcDir: c.EtcDir, SecretsDir: c.SecretsDir, EnvFile: v("ARGUS_SOURCES_LEGACY_ENV"),
			KubeDir: v("ARGUS_KUBE_DIR"), GitLabURL: gl, GrafanaURLs: graf}}
}

func runSources(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, sourcesHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	status := func(c *config.Config) string { return filepath.Join(c.SettingsOut, "sources-status.json") }
	if sub == "list" {
		typ := ""
		if len(args) > 2 {
			fmt.Fprintln(stderr, "usage: argus sources list [type]")
			return 2
		}
		if len(args) == 2 {
			typ = args[1]
			if !sourceTypeRe.MatchString(typ) {
				fmt.Fprintln(stderr, "argus sources: the type is one word, e.g. gitlab")
				return 2
			}
		}
		c, ok := loadQuiet("", stderr)
		if !ok {
			return 1
		}
		sources.List(typ, c.DB, status(c), stdout)
		return 0
	}
	if sub == "find" {
		return runSourcesFind(args[1:], stdout, stderr)
	}
	if sub == "routes" || sub == "unmapped" {
		return runSourcesRead(sub, args[1:], stdout, stderr)
	}
	if !In(sub, "apply", "check", "record", "discover", "catalogue", "actions") {
		fmt.Fprintf(stderr, "argus sources: unknown subcommand %q (list, routes, find, unmapped, apply, check, record, discover, catalogue, actions)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("sources "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	force := fs.Bool("force", false, "discover: ignore the minimum gap since the last run")
	only := fs.String("only", "", "discover: only these sources (type/name, comma-separated)")
	trigger := fs.String("trigger", "timer", "discover: timer or manual (recorded with the run)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintf(stderr, "argus sources %s: disabled in the demo (DEMO_MODE=1): nothing was changed\n", sub)
		return 1
	}
	if sub == "discover" || sub == "catalogue" || sub == "actions" {
		return runSourcesAgent(c, sub, *force, *only, *trigger, stdout, stderr)
	}
	if sub == "record" {
		n, err := sources.Record(status(c), c.DB)
		if err != nil {
			fmt.Fprintln(stderr, "argus sources record:", err)
			return 1
		}
		fmt.Fprintf(stdout, "data sources recorded: %d\n", n)
		return 0
	}
	o := sourcesOptions(c)
	o.Log = stdout
	run := sources.Apply
	if sub == "check" {
		run = sources.CheckAll
	}
	if err := run(o); err != nil {
		fmt.Fprintf(stderr, "argus sources %s: status not written: %v\n", sub, err)
		return 1
	}
	return 0
}
