package main

// estate.go: argus estate (the host estate jobs, internal/estate): the VM inventory, the VM state and the 5-minute
// pulse, from Prometheus only. In the volume layout the worker runs them: inventory and VM state every 6 hours, the
// pulse every 5 minutes, once a Prometheus site is configured.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/estate"
	"github.com/sinaneysi/argus/internal/py"
	"github.com/sinaneysi/argus/internal/sources"
)

const estateHelp = `usage: argus estate inventory|vmstate|pulse|all|sites [-file PATH] [-vm ID]

inventory  every VM that a Prometheus site sees (node_exporter), merged by node name: ops.db vms and vm_changes,
           ARGUS_STATE_DIR/inventory-status.json
vmstate    every VM of the inventory (or -vm ID): 24-hour and 7-day series, mounts, unit states, verdicts and
           upstreams: ARGUS_STATE_DIR/vm/<id>.json, ops.db vm_state, vmstate-status.json
pulse      the 15-minute pressure and the failed units of every VM: ARGUS_STATE_DIR/vm-pulse.json
all        inventory, then vmstate
sites      list the Prometheus sites (no query)

Sites: the Grafana entries of Settings -> Data sources that are enabled for the agent (the first Prometheus data
source of each, through Grafana's proxy), the Prometheus entries of type Alertmanager / Prometheus, and
ARGUS_PROM_SOURCES (name=url). Only GET requests; no host shell access.
`

// estateSites are the Prometheus sites, in a stable order: ARGUS_PROM_SOURCES, then Grafana entries, then
// Prometheus entries, each by name. A name is used once.
func estateSites(c *config.Config) ([]*estate.Site, []string) {
	var out []*estate.Site
	var errs []string
	used := map[string]bool{}
	for _, e := range list(c, "ARGUS_PROM_SOURCES") {
		name, raw, ok := strings.Cut(e, "=")
		name = strings.TrimSpace(name)
		if !ok || !sources.NameRe.MatchString(name) || used[name] {
			errs = append(errs, fmt.Sprintf("ARGUS_PROM_SOURCES: %q: want name=url with a unique lower-case name", e))
			continue
		}
		u, err := sources.CleanURL(strings.TrimSpace(raw))
		if err != nil {
			errs = append(errs, fmt.Sprintf("ARGUS_PROM_SOURCES: %s: %v", name, err))
			continue
		}
		used[name] = true
		out = append(out, &estate.Site{Name: name, URL: u})
	}
	o := sourcesOptions(c)
	reg := o.Registry()
	keys := make([]string, 0, len(reg))
	for k := range reg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, pass := range []string{"grafana", "alertmanager"} {
		for _, k := range keys {
			e := reg[k]
			if e == nil || e.Type != pass || used[e.Name] {
				continue
			}
			if pass == "grafana" && (e.Purpose != "agent" || !e.Enabled) {
				continue // the same rule as the earlier observability queries: read-only verified, enabled
			}
			if pass == "alertmanager" && sources.AlertsPath(e) != "/api/v1/alerts" {
				continue // an Alertmanager has no query API
			}
			u, err := sources.CleanURL(e.URL)
			if err != nil {
				continue
			}
			used[e.Name] = true
			out = append(out, &estate.Site{Name: e.Name, URL: u, Token: o.Credential(e), Grafana: pass == "grafana"})
		}
	}
	return out, errs
}

// estateConfigured is the worker's Ready check (no network).
func estateConfigured(c *config.Config) (bool, string) {
	sites, _ := estateSites(c)
	if len(sites) == 0 {
		return false, "not configured: " + estate.ErrNoSite.Error()
	}
	return true, ""
}

func estateOptions(c *config.Config) (*estate.Options, error) {
	sites, errs := estateSites(c)
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	cl, err := estate.NewClient(c.Values["ARGUS_PROM_CA_FILE"].Raw, time.Duration(num(c, "ARGUS_PROM_TIMEOUT"))*time.Second)
	if err != nil {
		return nil, err
	}
	var pfx [][2]string
	for _, e := range list(c, "ARGUS_INVENTORY_ENV_PREFIXES") {
		if env, p, ok := strings.Cut(e, "="); ok && strings.TrimSpace(env) != "" && strings.TrimSpace(p) != "" {
			pfx = append(pfx, [2]string{strings.TrimSpace(env), strings.TrimSpace(p)})
		}
	}
	return &estate.Options{DB: c.DB, StateDir: c.StateDir, Sites: sites, Client: cl, ServiceMap: c.Values["ARGUS_SERVICE_MAP_FILE"].Raw,
		EnvPrefixes: pfx, AppUnits: list(c, "ARGUS_VM_APP_UNITS")}, nil
}

// estateRun runs one estate job; the result line goes to w.
func estateRun(ctx context.Context, c *config.Config, job string, vid int64, w io.Writer) error {
	o, err := estateOptions(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	switch job {
	case "inventory", "all":
		st, err := o.Inventory(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "inventory: %v VMs, %v\n", st["vms"], st["status"])
		if job == "inventory" {
			return nil
		}
		fallthrough
	case "vmstate":
		st, err := o.VMState(ctx, vid)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "vmstate:", py.Dumps(st, -1, true))
	case "pulse":
		st, err := o.Pulse(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "pulse:", py.Dumps(st, -1, true))
	default:
		return fmt.Errorf("unknown job %q", job)
	}
	return nil
}

func runEstate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stderr
		if isHelp(args) {
			w = stdout
		}
		fmt.Fprint(w, estateHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	job := args[0]
	fs := flag.NewFlagSet("estate "+job, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	vm := fs.String("vm", "", "vmstate: one VM id")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus estate: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	var vid int64
	if *vm != "" {
		n, err := strconv.ParseInt(*vm, 10, 64)
		if err != nil || n <= 0 || job != "vmstate" {
			fmt.Fprintln(stderr, "argus estate: -vm takes a VM id and only with vmstate")
			return 2
		}
		vid = n
	}
	switch job {
	case "sites":
		sites, errs := estateSites(c)
		for _, s := range sites {
			via := "prometheus"
			if s.Grafana {
				via = "grafana proxy"
			}
			fmt.Fprintf(stdout, "%s\t%s\n", s.Name, via)
		}
		for _, e := range errs {
			fmt.Fprintln(stderr, e)
		}
		if len(sites) == 0 {
			fmt.Fprintln(stderr, estate.ErrNoSite)
			return 1
		}
		return 0
	case "inventory", "vmstate", "pulse", "all":
	default:
		fmt.Fprint(stderr, estateHelp)
		return 2
	}
	if err := estateRun(context.Background(), c, job, vid, stdout); err != nil {
		fmt.Fprintln(stderr, "argus estate "+job+":", err)
		return 1
	}
	return 0
}
