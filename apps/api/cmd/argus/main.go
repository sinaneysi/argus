// Command argus is the multi-call binary of Argus 2 (ADR-0016 D1): every job, check, ingest loop and dispatcher
// becomes a subcommand here, one Python job at a time. argus-api stays a separate binary.
//
//	argus version                 print the version
//	argus config check [-file F]  load and validate the configuration, print it with secrets masked
//	argus config show [-json]     the effective configuration and the source of each value
//	argus demo generate ...       write the public demo's sample data (see argus demo -h)
//	argus demo ready              exit 0 when the demo data exists (the data container's health check)
//	argus checks <name|all|list>  run collectors once (alerts, certs, backups), each behind ARGUS_COLLECT_<NAME>=1
//	argus worker [-once]          run the enabled collectors on their intervals
//	argus claude check|egress     the egress-country guard in front of every Claude run
//	argus settings apply          root: apply what Settings -> General saved (claude, qa: the same from the shell)
//	argus secrets apply-claude    root: replace or test the Claude token, egress check and guard settings
//	argus secrets apply-kube      root: store and check kubeconfigs from Settings -> Kubernetes (install-kubectl too)
//	argus tools kube ...          the agent's read-only Kubernetes tool (get, describe, logs, events ...)
//	argus sources list [type]     the data sources the agent may read (apply, check: root; record: the agent's DB copy)
//	argus sources routes|unmapped where each service lives (discover, catalogue, actions: the agent's user, ADR-0017 P2)
//	argus sources find ...        which instance to read for a service or a question (ADR-0017 P3)
//
// Exit codes: 0 success, 1 failure (for config check: invalid configuration), 2 usage error.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	_ "time/tzdata" // ARGUS_TZ is validated in a scratch image too
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int // nil: not implemented yet
}

// commands is the dispatch table, in help order.
var commands = []command{
	{"version", "print the version", runVersion},
	{"config", "check the configuration (argus config check)", runConfig},
	{"migrate", "apply the schema migrations to every database (-status: show versions only)", runMigrate},
	{"demo", "the public demo: generate sample data, readiness check", runDemo},
	{"checks", "run collectors once: alerts snapshot, certificates, backups", runChecks},
	{"worker", "run the enabled collectors on their intervals", runWorker},
	{"notify", "send a notice (Matrix, Telegram or a webhook) or print a formatted one", func(a []string, o, e io.Writer) int { return runNotify(a, os.Stdin, o, e) }},
	{"claude", "the egress-country guard in front of every Claude run (check, egress); one call through the model runtime (run)", runClaude},
	{"settings", "root: apply Settings -> General (apply), switch Claude usage (claude) or QA questions (qa)", runSettings},
	{"secrets", "root: apply Settings -> Claude and -> Kubernetes requests (apply-claude, apply-kube, install-kubectl)", runSecrets},
	{"tools", "the agent's read-only tools (kube)", runTools},
	{"board", "the task board sync with Jira and GitLab issues (sync)", runBoard},
	{"estate", "host estate from Prometheus: VM inventory, VM state, the 5-minute pulse", runEstate},
	{"outline", "documentation publisher: apply the Outline requests, publish the service pages under the parent document", runOutline},
	{"cicd", "CI/CD watcher: scan the GitLab estate, watch pipelines (failures, stuck pipelines, deploys)", runCicd},
	{"today", "the Today planner: build or refresh the day's plan (tick, plan), apply the page's requests (apply), show, status", runToday},
	{"incidents", "incident records: apply the incident page's requests and auto-resolve (tick); attach, backfill", runIncidents},
	{"import", "one-time import of an earlier installation into the one volume (names only; -dry-run first)", runImport},
	{"marketing", "the marketing module's jobs: crawler logs, sitemaps, the SEO page's Googlebot table (googlebot ...)", runMarketing},
	{"sources", "data sources: list, routes, find, unmapped (the agent); apply, check (root); record, discover, catalogue, actions", runSources},
	{"ingest", "read alerts and chat messages", nil},
	{"dispatch", "act on request files written by the web app", nil},
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: argus <command> [arguments]   (argus <command> -h for help)")
	fmt.Fprintln(w, "\ncommands:")
	for _, c := range commands {
		s := c.summary
		if c.run == nil {
			s += " (not implemented yet)"
		}
		fmt.Fprintf(w, "  %-10s %s\n", c.name, s)
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	}
	for _, c := range commands {
		if c.name != args[0] {
			continue
		}
		if c.run == nil {
			fmt.Fprintf(stderr, "argus %s: not implemented yet\n", c.name)
			return 2
		}
		return c.run(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "argus: unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

// isHelp reports whether args ask for help (-h anywhere before a "--").
func isHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	if isHelp(args) {
		fmt.Fprintln(stdout, "usage: argus version\n\nPrints the version, the source revision (when known) and the Go version.")
		return 0
	}
	rev := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				rev = " " + s.Value[:12]
			}
		}
	}
	fmt.Fprintf(stdout, "argus %s%s (%s %s/%s)\n", version, rev, strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)
	return 0
}
