package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/sources"
)

const checksHelp = `usage: argus checks <alerts|certs|backups|all> [-file PATH]
       argus checks list [-file PATH]

Runs collectors once. Each is off until its flag is 1 in the configuration:
  alerts   ARGUS_COLLECT_ALERTS=1   reads ARGUS_ALERTS_SOURCES, writes ARGUS_ALERTS_FILE (alerts-firing.json)
  certs    ARGUS_COLLECT_CERTS=1    probes ARGUS_CERT_TARGETS / reads ARGUS_CERT_FILES, records a cert-check run in ARGUS_DB
  backups  ARGUS_COLLECT_BACKUPS=1  checks ARGUS_BACKUP_SETS_FILE, records a backup-check run in ARGUS_DB
A disabled collector is skipped with a log line (exit 0). Collectors are read-only towards their targets.
Exit status 0: done (findings such as an expiring certificate are not failures); 1: a collector failed or the
configuration is invalid; 2: usage error.

flags:
`

const workerHelp = `usage: argus worker [-file PATH] [-once] [-alerts-every D] [-certs-every D] [-backups-every D]

Runs the enabled collectors in one long-lived process: each once at start, then on its own interval, until
SIGINT or SIGTERM. With -once it runs each enabled collector once and exits (status as for argus checks all).
The systemd units in examples/systemd run argus checks from timers instead; use one or the other.

flags:
`

// collector names in run order
var collectorNames = []string{"alerts", "certs", "backups"}

func loadForChecks(file string, stderr io.Writer) (*config.Config, bool) {
	c, err := config.Load(config.Options{File: file})
	for _, w := range c.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if errs, ok := err.(config.Errors); ok {
		for _, e := range errs {
			fmt.Fprintln(stderr, "CONFIG ERROR:", e)
		}
		return c, false
	} else if err != nil {
		fmt.Fprintln(stderr, "CONFIG ERROR:", err)
		return c, false
	}
	return c, true
}

// registryAlertSources are the Alertmanager / Prometheus entries of Settings -> Data sources, each with its own token
// (names that ARGUS_ALERTS_SOURCES already uses are skipped).
func registryAlertSources(c *config.Config, have []collect.AlertSource) []collect.AlertSource {
	used := map[string]bool{}
	for _, s := range have {
		used[s.Name] = true
	}
	o := sourcesOptions(c)
	var out []collect.AlertSource
	for _, e := range o.Registry() {
		if e.Type != "alertmanager" || used[e.Name] {
			continue
		}
		u, err := sources.CleanURL(e.URL)
		if err != nil {
			continue
		}
		out = append(out, collect.AlertSource{Name: e.Name, URL: u + sources.AlertsPath(e), Token: o.Credential(e)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// registryS3 are the S3 entries of Settings -> Data sources by name: the access key is the entry's user name, the
// secret key its credential.
func registryS3(c *config.Config) map[string]collect.S3Lister {
	o := sourcesOptions(c)
	out := map[string]collect.S3Lister{}
	for _, e := range o.Registry() {
		if e.Type != "s3" || e.Username == "" {
			continue
		}
		u, err := sources.CleanURL(e.URL)
		if err != nil {
			continue
		}
		region := c.Values["ARGUS_BACKUP_S3_REGION"].Raw
		if r, ok := e.Facts["region"].(string); ok && r != "" {
			region = r
		}
		out[e.Name] = &collect.S3{Endpoint: u, Region: region, AccessKey: e.Username, SecretKey: o.Credential(e)}
	}
	return out
}

func enabled(c *config.Config, name string) bool {
	if c.Values["ARGUS_LAYOUT"].Raw == "volume" && len(needs.Jobs["collect-"+name]) > 0 {
		return true // the volume layout runs a collector when its data source is connected (the worker's Ready)
	}
	if c.Values["ARGUS_LAYOUT"].Raw == "volume" && name == "backups" {
		if _, err := os.Stat(c.Values["ARGUS_BACKUP_SETS_FILE"].Raw); err == nil {
			return true // the volume layout checks the backups once a sets file is there
		}
	}
	return c.Values["ARGUS_COLLECT_"+strings.ToUpper(name)].Raw == "1"
}

func list(c *config.Config, key string) []string {
	var out []string
	for _, x := range strings.Split(c.Values[key].Raw, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func num(c *config.Config, key string) int {
	var n int
	fmt.Sscan(c.Values[key].Raw, &n)
	return n
}

// runCollector runs one collector once; it returns the error that makes the exit status 1.
func runCollector(ctx context.Context, c *config.Config, name string) error {
	log := collect.Logger.With("collector", name)
	if !enabled(c, name) {
		log.Info("disabled", "flag", "ARGUS_COLLECT_"+strings.ToUpper(name))
		return nil
	}
	start := time.Now()
	var res collect.Result
	var err error
	switch name {
	case "alerts":
		srcs, perrs := collect.ParseAlertSources(list(c, "ARGUS_ALERTS_SOURCES"))
		if len(perrs) > 0 {
			return fmt.Errorf("ARGUS_ALERTS_SOURCES: %s", strings.Join(perrs, "; "))
		}
		srcs = append(srcs, registryAlertSources(c, srcs)...)
		if len(srcs) == 0 {
			return fmt.Errorf("no alert source: connect Alertmanager or Prometheus in Settings -> Data sources, or set ARGUS_ALERTS_SOURCES")
		}
		timeout := time.Duration(num(c, "ARGUS_ALERTS_TIMEOUT")) * time.Second
		cctx, cancel := context.WithTimeout(ctx, timeout*time.Duration(len(srcs))+10*time.Second)
		defer cancel()
		res, err = collect.Alerts(cctx, collect.AlertsOptions{Sources: srcs, Out: c.AlertsFile,
			TokenFile: c.Values["ARGUS_ALERTS_TOKEN_FILE"].Raw, CAFile: c.Values["ARGUS_ALERTS_CA_FILE"].Raw,
			Verify: c.Values["ARGUS_ALERTS_TLS_VERIFY"].Raw != "0", Timeout: timeout})
	case "certs":
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		res, err = collect.Certs(cctx, collect.CertsOptions{Targets: list(c, "ARGUS_CERT_TARGETS"), Files: list(c, "ARGUS_CERT_FILES"),
			DB: c.DB, CAFile: c.Values["ARGUS_CERT_CA_FILE"].Raw, WarnDays: num(c, "ARGUS_CERT_WARN_DAYS"),
			FailDays: num(c, "ARGUS_CERT_FAIL_DAYS"), Timeout: time.Duration(num(c, "ARGUS_CERT_TIMEOUT")) * time.Second})
	case "backups":
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		res, err = collect.Backups(cctx, collect.BackupsOptions{SetsFile: c.Values["ARGUS_BACKUP_SETS_FILE"].Raw, DB: c.DB, S3: registryS3(c)})
	default:
		return fmt.Errorf("unknown collector %q", name)
	}
	ms := time.Since(start).Milliseconds()
	if err != nil {
		log.Error("failed", "status", res.Status, "err", err, "ms", ms)
		return err
	}
	log.Info("done", "status", res.Status, "items", res.Items, "summary", res.Summary, "ms", ms)
	return nil
}

func runChecks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stdout
		if len(args) == 0 {
			w = stderr
		}
		fs := flag.NewFlagSet("argus checks", flag.ContinueOnError)
		fs.String("file", "", "configuration file (default: $ARGUS_CONFIG or "+config.DefaultFile+")")
		fs.SetOutput(w)
		fmt.Fprint(w, checksHelp)
		fs.PrintDefaults()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	name := args[0]
	fs := flag.NewFlagSet("argus checks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	names := []string{name}
	switch name {
	case "all":
		names = collectorNames
	case "list":
	case "alerts", "certs", "backups":
	default:
		fmt.Fprintf(stderr, "argus checks: unknown collector %q (alerts, certs, backups, all, list)\n", name)
		return 2
	}
	c, ok := loadForChecks(*file, stderr)
	if !ok {
		return 1
	}
	if name == "list" {
		for _, n := range collectorNames {
			st := "off"
			if enabled(c, n) {
				st = "on"
			}
			fmt.Fprintf(stdout, "%-8s %s\n", n, st)
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := 0
	for _, n := range names {
		if err := runCollector(ctx, c, n); err != nil {
			code = 1
		}
	}
	return code
}

func runWorker(args []string, stdout, stderr io.Writer) int {
	if isHelp(args) {
		fmt.Fprint(stdout, workerHelp)
		return 0
	}
	fs := flag.NewFlagSet("argus worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file")
	once := fs.Bool("once", false, "run each enabled collector once and exit")
	every := map[string]*time.Duration{
		"alerts":  fs.Duration("alerts-every", time.Minute, "interval of the alerts snapshot"),
		"certs":   fs.Duration("certs-every", 6*time.Hour, "interval of the certificate check"),
		"backups": fs.Duration("backups-every", time.Hour, "interval of the backup check"),
	}
	fs.Usage = func() { fmt.Fprint(stderr, workerHelp); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	for n, d := range every {
		if *d < 30*time.Second {
			fmt.Fprintf(stderr, "argus worker: -%s-every must be at least 30s\n", n)
			return 2
		}
	}
	c, ok := loadForChecks(*file, stderr)
	if !ok {
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := migrate.OnStart(ctx, c, func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }); err != nil {
		fmt.Fprintln(stderr, "argus worker:", err)
		return 1
	}
	if c.Values["ARGUS_LAYOUT"].Raw == "volume" && !*once {
		ev := map[string]time.Duration{}
		for n, d := range every {
			ev[n] = *d
		}
		return runVolumeWorker(ctx, c, ev, stderr)
	}
	code := 0
	next := map[string]time.Time{}
	for _, n := range collectorNames {
		if err := runCollector(ctx, c, n); err != nil {
			code = 1
		}
		next[n] = time.Now().Add(*every[n])
	}
	if *once {
		return code
	}
	any := false
	for _, n := range collectorNames {
		any = any || enabled(c, n)
	}
	if !any {
		fmt.Fprintln(stderr, "argus worker: no collector is enabled (ARGUS_COLLECT_*=1); exiting")
		return 1
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			collect.Logger.Info("stopping", "component", "worker")
			return 0
		case now := <-tick.C:
			for _, n := range collectorNames {
				if enabled(c, n) && !now.Before(next[n]) {
					runCollector(ctx, c, n) // a failure is logged; the next interval tries again
					next[n] = time.Now().Add(*every[n])
				}
			}
		}
	}
}
